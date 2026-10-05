package appsignal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/olucurious/watchtower/internal/event"
)

// The AppSignal agent's protobuf schema is not published. These field
// numbers were observed in payloads from the Elixir, Node.js, Python and
// Ruby integrations (agents 0.34.1–0.37.4); see testdata/ and
// docs/adapters/appsignal.md. Unknown fields are skipped, so additions in
// newer agents are harmless; renumbering would not be.
//
// Errors arrive in one of two layouts. Span-based integrations (Elixir,
// Node.js, Python) send samples in field 12. Ruby sends transaction
// aggregates in field 6, each holding its error samples.
const (
	payloadHostname    = 1
	payloadAPIKey      = 2 // the push API key; never read, never logged
	payloadEnvironment = 3
	payloadAppName     = 4
	payloadAggregates  = 6 // metrics and (Ruby) transaction aggregates
	payloadAgent       = 7
	payloadLanguage    = 8 // e.g. "elixir-2.17.4", "ruby-5.0.1", "python"
	payloadSample      = 12

	aggregatesTransaction = 4

	transactionAction    = 1
	transactionNamespace = 2
	transactionSample    = 7

	rubySampleID       = 1
	rubySampleTime     = 2 // seconds
	rubySampleRevision = 5
	rubySampleError    = 7 // {1 name, 2 message, 4 backtrace JSON}
	rubySampleData     = 12

	rubyErrorName      = 1
	rubyErrorMessage   = 2
	rubyErrorBacktrace = 4

	sampleID   = 1
	sampleSpan = 5

	spanTraceID    = 1
	spanID         = 2
	spanName       = 4 // action, e.g. "POST /books/:id/reserve"
	spanNamespace  = 5
	spanStart      = 6
	spanEnd        = 7
	spanAttribute  = 8
	spanSampleData = 9 // {1 key, 2 JSON}: tags, environment, params, session_data, custom_data
	spanError      = 10

	timeSeconds = 1
	timeNanos   = 2

	attrKey      = 1
	attrValue    = 2
	attrValueStr = 1

	sampleDataKey  = 1
	sampleDataJSON = 2

	errorName           = 2
	errorMessage        = 3
	errorBacktrace      = 4 // JSON array of lines (Elixir, Node.js)
	errorBacktraceLines = 5 // one line per field (Python)
)

var errMalformed = errors.New("malformed appsignal payload")

// Batch is what Watchtower extracts from one /2/collect payload.
type Batch struct {
	Hostname, Environment, AppName, Agent, Language string
	Errors                                          []ErrorSample
	SamplesWithoutErrors                            int
}

type ErrorSample struct {
	SampleID, TraceID, SpanID, Namespace, Revision, Action string
	Time                                                   time.Time
	Name, Message                                          string
	// Backtrace lines as the integration formats them; see parseBacktrace
	// for each language's order.
	Backtrace []string
	Causes    []Cause // Ruby: the exception's causes, direct cause first

	// Allowlisted sample data. "params", "session_data" and "custom_data"
	// carry request payloads and are deliberately never decoded.
	Tags    map[string]string
	Request *Request
}

// Cause is one entry of Ruby's error_causes sample data.
type Cause struct {
	Name      string `json:"name"`
	Message   string `json:"message"`
	FirstLine *struct {
		Path   string `json:"path"`
		Line   int    `json:"line"`
		Method string `json:"method"`
		Gem    any    `json:"gem"`
	} `json:"first_line"`
}

// Request is the allowlisted part of the "environment" sample data.
type Request struct {
	Method string
	Path   string
	Status int
}

// DecodeCollect parses a decompressed /2/collect body.
func DecodeCollect(b []byte) (*Batch, error) {
	batch := &Batch{}
	err := eachField(b, func(num protowire.Number, typ protowire.Type, v []byte) error {
		switch num {
		case payloadHostname:
			batch.Hostname = string(v)
		case payloadEnvironment:
			batch.Environment = string(v)
		case payloadAppName:
			batch.AppName = string(v)
		case payloadAgent:
			batch.Agent = string(v)
		case payloadLanguage:
			batch.Language = string(v)
		case payloadAggregates:
			if typ == protowire.BytesType {
				return decodeAggregates(v, batch)
			}
		case payloadSample:
			if typ != protowire.BytesType {
				return fmt.Errorf("%w: sample is not a message", errMalformed)
			}
			found, err := decodeSample(v, batch)
			if err != nil {
				return err
			}
			if !found {
				batch.SamplesWithoutErrors++
			}
		}
		return nil
	})
	return batch, err
}

func decodeSample(b []byte, batch *Batch) (bool, error) {
	var id string
	var spans [][]byte
	err := eachField(b, func(num protowire.Number, typ protowire.Type, v []byte) error {
		switch {
		case num == sampleID && typ == protowire.BytesType:
			id = string(v)
		case num == sampleSpan && typ == protowire.BytesType:
			spans = append(spans, v)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	found := false
	for _, s := range spans {
		errs, err := decodeSpan(s, id)
		if err != nil {
			return false, err
		}
		found = found || len(errs) > 0
		batch.Errors = append(batch.Errors, errs...)
	}
	return found, nil
}

func decodeSpan(b []byte, sample string) ([]ErrorSample, error) {
	base := ErrorSample{SampleID: sample}
	var start, end time.Time
	var raw [][]byte
	err := eachField(b, func(num protowire.Number, typ protowire.Type, v []byte) error {
		if typ != protowire.BytesType {
			return nil
		}
		var err error
		switch num {
		case spanTraceID:
			base.TraceID = string(v)
		case spanID:
			base.SpanID = string(v)
		case spanName:
			base.Action = string(v)
		case spanNamespace:
			base.Namespace = string(v)
		case spanStart:
			start, err = decodeTime(v)
		case spanEnd:
			end, err = decodeTime(v)
		case spanAttribute:
			var k, val string
			k, val, err = decodeStringAttribute(v)
			switch {
			case k == "revision":
				base.Revision = val
			case k == "" || val == "" || strings.HasPrefix(k, "appsignal"):
				// internal attributes such as appsignal:category
			default:
				// Node.js and Python send tags as span attributes.
				if base.Tags == nil {
					base.Tags = map[string]string{}
				}
				base.Tags[k] = val
			}
		case spanSampleData:
			err = decodeSampleData(v, &base)
		case spanError:
			raw = append(raw, v)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	base.Time = end
	if base.Time.IsZero() {
		base.Time = start
	}
	// The agent repeats an identical error record within one span; keep
	// one occurrence per distinct error.
	seen := map[string]bool{}
	var out []ErrorSample
	for _, r := range raw {
		es := base
		var backtrace string
		var lines []string
		err := eachField(r, func(num protowire.Number, typ protowire.Type, v []byte) error {
			if typ != protowire.BytesType {
				return nil
			}
			switch num {
			case errorName:
				es.Name = string(v)
			case errorMessage:
				es.Message = string(v)
			case errorBacktrace:
				backtrace = string(v)
			case errorBacktraceLines:
				lines = append(lines, string(v))
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		key := es.Name + "\x00" + es.Message + "\x00" + backtrace + "\x00" + strings.Join(lines, "\n")
		if seen[key] {
			continue
		}
		seen[key] = true
		es.Backtrace = backtraceLines(backtrace, lines)
		out = append(out, es)
	}
	return out, nil
}

// decodeSampleData keeps only tags and the request line. A value that is
// not the expected JSON is ignored rather than failing the batch.
func decodeSampleData(b []byte, es *ErrorSample) error {
	var key string
	var data []byte
	err := eachField(b, func(num protowire.Number, typ protowire.Type, v []byte) error {
		if typ != protowire.BytesType {
			return nil
		}
		switch num {
		case sampleDataKey:
			key = string(v)
		case sampleDataJSON:
			data = v
		}
		return nil
	})
	if err != nil {
		return err
	}
	switch key {
	case "tags":
		var tags map[string]any
		if json.Unmarshal(data, &tags) == nil && len(tags) > 0 {
			es.Tags = map[string]string{}
			for k, v := range tags {
				switch x := v.(type) {
				case string:
					es.Tags[k] = x
				case float64, bool:
					es.Tags[k] = fmt.Sprint(x)
				}
			}
		}
	case "environment":
		// Elixir: method/request_path/status; Ruby: Rack's REQUEST_METHOD/PATH_INFO.
		var env map[string]any
		if json.Unmarshal(data, &env) == nil {
			r := Request{Method: str(env, "method", "REQUEST_METHOD"), Path: str(env, "request_path", "PATH_INFO")}
			if status, ok := env["status"].(float64); ok {
				r.Status = int(status)
			}
			if r.Method != "" || r.Path != "" {
				es.Request = &r
			}
		}
	case "error_causes":
		_ = json.Unmarshal(data, &es.Causes)
	}
	return nil
}

func str(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func backtraceLines(jsonArray string, lines []string) []string {
	if len(lines) > 0 {
		return lines
	}
	if jsonArray == "" {
		return nil
	}
	var out []string
	if json.Unmarshal([]byte(jsonArray), &out) != nil {
		return []string{jsonArray}
	}
	return out
}

// decodeAggregates reads Ruby's transaction aggregates, which carry error
// samples; metric entries in the same message are skipped.
func decodeAggregates(b []byte, batch *Batch) error {
	return eachField(b, func(num protowire.Number, typ protowire.Type, v []byte) error {
		if num != aggregatesTransaction || typ != protowire.BytesType {
			return nil
		}
		var action, namespace string
		var samples [][]byte
		err := eachField(v, func(n protowire.Number, t protowire.Type, inner []byte) error {
			if t != protowire.BytesType {
				return nil
			}
			switch n {
			case transactionAction:
				action = string(inner)
			case transactionNamespace:
				namespace = string(inner)
			case transactionSample:
				samples = append(samples, inner)
			}
			return nil
		})
		if err != nil {
			return err
		}
		for _, sample := range samples {
			es := ErrorSample{Action: action, Namespace: namespace}
			hasError := false
			err := eachField(sample, func(n protowire.Number, t protowire.Type, inner []byte) error {
				switch {
				case n == rubySampleID && t == protowire.BytesType:
					es.SampleID = string(inner)
				case n == rubySampleTime && t == protowire.VarintType:
					if sec, _ := protowire.ConsumeVarint(inner); sec > 0 {
						es.Time = time.Unix(int64(sec), 0).UTC()
					}
				case n == rubySampleRevision && t == protowire.BytesType:
					es.Revision = string(inner)
				case n == rubySampleData && t == protowire.BytesType:
					return decodeSampleData(inner, &es)
				case n == rubySampleError && t == protowire.BytesType:
					hasError = true
					return eachField(inner, func(en protowire.Number, et protowire.Type, ev []byte) error {
						if et != protowire.BytesType {
							return nil
						}
						switch en {
						case rubyErrorName:
							es.Name = string(ev)
						case rubyErrorMessage:
							es.Message = string(ev)
						case rubyErrorBacktrace:
							es.Backtrace = backtraceLines(string(ev), nil)
						}
						return nil
					})
				}
				return nil
			})
			if err != nil {
				return err
			}
			if hasError && es.Name != "" {
				batch.Errors = append(batch.Errors, es)
			} else {
				batch.SamplesWithoutErrors++
			}
		}
		return nil
	})
}

func decodeTime(b []byte) (time.Time, error) {
	var sec, nsec uint64
	err := eachField(b, func(num protowire.Number, typ protowire.Type, v []byte) error {
		if typ != protowire.VarintType {
			return nil
		}
		n, _ := protowire.ConsumeVarint(v)
		switch num {
		case timeSeconds:
			sec = n
		case timeNanos:
			nsec = n
		}
		return nil
	})
	if err != nil || sec == 0 {
		return time.Time{}, err
	}
	return time.Unix(int64(sec), int64(nsec%1e9)).UTC(), nil
}

func decodeStringAttribute(b []byte) (key, value string, err error) {
	err = eachField(b, func(num protowire.Number, typ protowire.Type, v []byte) error {
		if typ != protowire.BytesType {
			return nil
		}
		switch num {
		case attrKey:
			key = string(v)
		case attrValue:
			return eachField(v, func(n protowire.Number, t protowire.Type, inner []byte) error {
				if n == attrValueStr && t == protowire.BytesType {
					value = string(inner)
				}
				return nil
			})
		}
		return nil
	})
	return key, value, err
}

// eachField walks the top-level fields of one protobuf message. Nesting is
// bounded by the fixed schema above, not by recursion on input. For
// varints, v holds the encoded varint; for length-delimited fields, the
// contents.
func eachField(b []byte, fn func(protowire.Number, protowire.Type, []byte) error) error {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return fmt.Errorf("%w: %w", errMalformed, protowire.ParseError(n))
		}
		b = b[n:]
		var v []byte
		switch typ {
		case protowire.BytesType:
			v, n = protowire.ConsumeBytes(b)
		default:
			n = protowire.ConsumeFieldValue(num, typ, b)
			if n >= 0 {
				v = b[:n]
			}
		}
		if n < 0 {
			return fmt.Errorf("%w: field %d: %w", errMalformed, num, protowire.ParseError(n))
		}
		b = b[n:]
		if err := fn(num, typ, v); err != nil {
			return err
		}
	}
	return nil
}

// ToEvent converts one error sample into a canonical event. The event ID
// is derived from the sample, so an agent retrying the same payload
// produces the same ID and is deduplicated downstream.
func ToEvent(batch *Batch, es ErrorSample, projectID int64, received time.Time) event.Event {
	h := sha256.Sum256([]byte(strings.Join([]string{
		batch.AppName, batch.Environment, es.SampleID, es.SpanID, es.Name, es.Message,
	}, "\x00")))
	ts := es.Time
	if ts.IsZero() || ts.After(received.Add(time.Hour)) {
		ts = received
	}
	lang, sdkVersion, _ := strings.Cut(batch.Language, "-")
	name := strings.TrimPrefix(es.Name, ":") // ":exit" and ":throw" are kinds, not modules
	var module string
	if lang == "python" {
		// "__main__.CatalogError" -> type CatalogError, module __main__
		if i := strings.LastIndex(name, "."); i > 0 {
			module, name = name[:i], name[i+1:]
		}
	}
	action := es.Action
	if action == name || action == es.Name {
		action = "" // Node.js and Python name standalone error spans after the error
	}
	exceptions := causeExceptions(lang, es.Causes)
	exceptions = append(exceptions, event.Exception{
		Type:   name,
		Value:  stripElixirPrefix(es.Message),
		Module: module,
		Frames: parseBacktrace(lang, es.Backtrace),
	})
	e := event.Event{
		ID:          hex.EncodeToString(h[:16]),
		ProjectID:   projectID,
		Adapter:     Name,
		Timestamp:   ts,
		ReceivedAt:  received,
		Platform:    eventPlatform(lang),
		Level:       event.LevelError,
		Release:     es.Revision,
		Transaction: action,
		Environment: batch.Environment,
		ServerName:  batch.Hostname,
		SDK:         event.SDK{Name: "appsignal-" + lang, Version: sdkVersion},
		Exceptions:  exceptions,
	}
	tags := map[string]string{}
	for k, v := range es.Tags {
		tags[k] = v
	}
	if es.Namespace != "" {
		tags["namespace"] = es.Namespace
	}
	if r := es.Request; r != nil {
		e.Request = &event.Request{Method: r.Method, URL: r.Path}
		if r.Status > 0 {
			tags["http.status_code"] = strconv.Itoa(r.Status)
		}
	}
	if len(tags) > 0 {
		e.Tags = tags
	}
	if es.TraceID != "" {
		e.Trace = &event.Trace{TraceID: es.TraceID, SpanID: es.SpanID}
	}
	return e
}

// eventPlatform maps AppSignal language names onto Sentry's platform names,
// which source map lookups and the UI use.
func eventPlatform(lang string) string {
	if lang == "nodejs" {
		return "node"
	}
	return lang
}

// causeExceptions turns Ruby's error_causes (direct cause first) into the
// start of the exception chain (innermost cause first).
func causeExceptions(lang string, causes []Cause) []event.Exception {
	var out []event.Exception
	for i := len(causes) - 1; i >= 0; i-- {
		c := causes[i]
		ex := event.Exception{Type: c.Name, Value: c.Message}
		if f := c.FirstLine; f != nil {
			ex.Frames = []event.Frame{{Filename: f.Path, Function: f.Method, Lineno: f.Line, InApp: f.Gem == nil}}
		}
		out = append(out, ex)
	}
	return out
}

var elixirMessagePrefix = regexp.MustCompile(`^\*\* \([^)]*\) `)

// stripElixirPrefix turns "** (ArgumentError) boom" or
// "** (exit) :timeout" into "boom" or ":timeout".
func stripElixirPrefix(msg string) string {
	return elixirMessagePrefix.ReplaceAllString(msg, "")
}
