package sentry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/olucurious/watchtower/internal/event"
)

const (
	maxTags        = 50
	maxTagKeyLen   = 64
	maxTagValueLen = 200
	maxFrames      = 250
	maxFutureSkew  = time.Hour
)

// wireEvent is the subset of Sentry's event payload Watchtower reads.
// Polymorphic fields stay raw and are decoded by the helpers below.
type wireEvent struct {
	EventID     string          `json:"event_id"`
	Timestamp   json.RawMessage `json:"timestamp"`
	Platform    string          `json:"platform"`
	Level       string          `json:"level"`
	Logger      string          `json:"logger"`
	Transaction string          `json:"transaction"`
	Release     string          `json:"release"`
	Environment string          `json:"environment"`
	ServerName  string          `json:"server_name"`
	Message     json.RawMessage `json:"message"`
	LogEntry    *wireMessage    `json:"logentry"`
	Exception   json.RawMessage `json:"exception"`
	Tags        json.RawMessage `json:"tags"`
	Extra       json.RawMessage `json:"extra"`
	Fingerprint []any           `json:"fingerprint"`
	Contexts    struct {
		Trace struct {
			TraceID string `json:"trace_id"`
			SpanID  string `json:"span_id"`
		} `json:"trace"`
	} `json:"contexts"`
	User *struct {
		ID json.RawMessage `json:"id"`
	} `json:"user"`
	Request *struct {
		Method string `json:"method"`
		URL    string `json:"url"`
	} `json:"request"`
	SDK struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"sdk"`
	DebugMeta struct {
		Images []struct {
			Type     string `json:"type"`
			CodeFile string `json:"code_file"`
			DebugID  string `json:"debug_id"`
		} `json:"images"`
	} `json:"debug_meta"`
}

const maxDebugImages = 200

type wireMessage struct {
	Formatted string `json:"formatted"`
	Message   string `json:"message"`
}

type wireException struct {
	Type      string `json:"type"`
	Value     any    `json:"value"`
	Module    string `json:"module"`
	Mechanism *struct {
		Type      string `json:"type"`
		Handled   *bool  `json:"handled"`
		Synthetic bool   `json:"synthetic"`
	} `json:"mechanism"`
	Stacktrace *struct {
		Frames []wireFrame `json:"frames"`
	} `json:"stacktrace"`
}

type wireFrame struct {
	Function    string   `json:"function"`
	Module      string   `json:"module"`
	Filename    string   `json:"filename"`
	AbsPath     string   `json:"abs_path"`
	Lineno      int      `json:"lineno"`
	Colno       int      `json:"colno"`
	InApp       bool     `json:"in_app"`
	ContextLine string   `json:"context_line"`
	PreContext  []string `json:"pre_context"`
	PostContext []string `json:"post_context"`
}

// Normalize converts one Sentry event payload into a canonical event.
// fallbackID is the envelope header's event_id; newID supplies an ID when
// neither carries one.
func Normalize(payload []byte, projectID int64, fallbackID string, received time.Time, newID func() string) (event.Event, error) {
	var w wireEvent
	if err := json.Unmarshal(payload, &w); err != nil {
		return event.Event{}, fmt.Errorf("event payload: %w", err)
	}
	e := event.Event{
		ID:          firstNonEmpty(event.NormalizeID(w.EventID), event.NormalizeID(fallbackID)),
		ProjectID:   projectID,
		Adapter:     Name,
		Timestamp:   parseTimestamp(w.Timestamp, received),
		ReceivedAt:  received,
		Platform:    w.Platform,
		Level:       event.ParseLevel(w.Level),
		Logger:      w.Logger,
		Transaction: w.Transaction,
		Release:     w.Release,
		Environment: w.Environment,
		ServerName:  w.ServerName,
		Message:     messageText(w.Message, w.LogEntry),
		Tags:        parseTags(w.Tags),
		Context:     parseContext(w.Extra),
		Fingerprint: stringify(w.Fingerprint),
		SDK:         event.SDK{Name: w.SDK.Name, Version: w.SDK.Version},
	}
	if e.ID == "" {
		e.ID = newID()
	}
	if w.Contexts.Trace.TraceID != "" {
		e.Trace = &event.Trace{TraceID: w.Contexts.Trace.TraceID, SpanID: w.Contexts.Trace.SpanID}
	}
	if w.User != nil {
		if id := rawScalar(w.User.ID); id != "" {
			e.User = &event.User{ID: id}
		}
	}
	if w.Request != nil && (w.Request.URL != "" || w.Request.Method != "") {
		e.Request = &event.Request{Method: w.Request.Method, URL: w.Request.URL}
	}
	for _, img := range w.DebugMeta.Images {
		if img.Type == "sourcemap" && img.DebugID != "" && len(e.DebugImages) < maxDebugImages {
			e.DebugImages = append(e.DebugImages, event.DebugImage{Type: img.Type, CodeFile: img.CodeFile, DebugID: img.DebugID})
		}
	}
	exceptions, err := parseExceptions(w.Exception)
	if err != nil {
		return event.Event{}, err
	}
	e.Exceptions = exceptions
	if e.Platform == "elixir" {
		normalizeElixirFrames(e.Exceptions)
	}
	e.StripNUL()
	return e, e.Validate()
}

// normalizeElixirFrames rewrites the Elixir SDK's frames, e.g. module
// "Elixir.Library.Catalog" with function "Library.Catalog.fetch!/1", into
// "Library.Catalog" and "fetch!/1", matching how Elixir prints stacktraces.
func normalizeElixirFrames(exceptions []event.Exception) {
	for i := range exceptions {
		for j := range exceptions[i].Frames {
			f := &exceptions[i].Frames[j]
			f.Module = strings.TrimPrefix(f.Module, "Elixir.")
			for _, prefix := range []string{f.Module + ".", ":" + f.Module + "."} {
				if f.Module != "" && strings.HasPrefix(f.Function, prefix) {
					f.Function = strings.TrimPrefix(f.Function, prefix)
					break
				}
			}
		}
	}
}

// parseExceptions accepts both {"values": [...]} and a bare list (the
// Elixir SDK's form).
func parseExceptions(raw json.RawMessage) ([]event.Exception, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var values []wireException
	if raw[0] == '[' {
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, fmt.Errorf("exception list: %w", err)
		}
	} else {
		var wrapped struct {
			Values []wireException `json:"values"`
		}
		if err := json.Unmarshal(raw, &wrapped); err != nil {
			return nil, fmt.Errorf("exception: %w", err)
		}
		values = wrapped.Values
	}
	out := make([]event.Exception, 0, len(values))
	for _, v := range values {
		ex := event.Exception{Type: v.Type, Value: scalarString(v.Value), Module: v.Module}
		if m := v.Mechanism; m != nil {
			ex.Mechanism = &event.Mechanism{Type: m.Type, Handled: m.Handled, Synthetic: m.Synthetic}
		}
		if v.Stacktrace != nil {
			frames := v.Stacktrace.Frames
			if len(frames) > maxFrames { // keep the crashing end of the stack
				frames = frames[len(frames)-maxFrames:]
			}
			for _, f := range frames {
				ex.Frames = append(ex.Frames, event.Frame{
					Function: f.Function, Module: f.Module, Filename: f.Filename, AbsPath: f.AbsPath,
					Lineno: f.Lineno, Colno: f.Colno, InApp: f.InApp, ContextLine: f.ContextLine,
					PreContext: f.PreContext, PostContext: f.PostContext,
				})
			}
		}
		out = append(out, ex)
	}
	return out, nil
}

func messageText(raw json.RawMessage, logEntry *wireMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil && s != "" {
			return s
		}
	}
	if len(raw) > 0 && raw[0] == '{' {
		var m wireMessage
		if json.Unmarshal(raw, &m) == nil {
			if s := firstNonEmpty(m.Formatted, m.Message); s != "" {
				return s
			}
		}
	}
	if logEntry != nil {
		return firstNonEmpty(logEntry.Formatted, logEntry.Message)
	}
	return ""
}

// parseTimestamp accepts epoch seconds (number or numeric string) and
// RFC 3339 strings, with or without a zone (zoneless means UTC). Missing,
// invalid or far-future timestamps fall back to the receive time.
func parseTimestamp(raw json.RawMessage, received time.Time) time.Time {
	raw = bytes.TrimSpace(raw)
	var t time.Time
	if len(raw) > 0 && raw[0] == '"' {
		var s string
		_ = json.Unmarshal(raw, &s)
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			t = fromEpoch(f)
		} else {
			for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999"} {
				if p, err := time.Parse(layout, s); err == nil {
					t = p.UTC()
					break
				}
			}
		}
	} else if f, err := strconv.ParseFloat(string(raw), 64); err == nil {
		t = fromEpoch(f)
	}
	if t.IsZero() || t.After(received.Add(maxFutureSkew)) {
		return received
	}
	return t
}

func fromEpoch(f float64) time.Time {
	if f <= 0 || math.IsInf(f, 0) || math.IsNaN(f) {
		return time.Time{}
	}
	sec, frac := math.Modf(f)
	return time.Unix(int64(sec), int64(frac*1e9)).UTC()
}

// jobExtra are the keys Sentry's Oban integration sets on a failed job that
// identify it without its arguments.
var jobExtra = []string{"worker", "queue", "attempt", "max_attempts"}

// parseContext keeps the diagnostic parts of Sentry's free-form "extra":
// the logger metadata an app explicitly allowed (Elixir's logger handler
// sends it as "logger_metadata", plus the log "domain") and a failed job's
// identity. Everything else in "extra", such as process state, last
// messages or job arguments, is dropped unread.
func parseContext(raw json.RawMessage) map[string]string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return nil
	}
	var extra map[string]json.RawMessage
	if json.Unmarshal(raw, &extra) != nil {
		return nil
	}
	ctx := map[string]string{}
	add := func(k string, v any) {
		s := scalarString(v)
		if s == "" || len(ctx) >= maxTags {
			return
		}
		if len(s) > maxTagValueLen {
			s = s[:maxTagValueLen]
		}
		ctx[k] = s
	}
	var meta map[string]any
	if json.Unmarshal(extra["logger_metadata"], &meta) == nil {
		keys := make([]string, 0, len(meta))
		for k := range meta {
			if k != "" && len(k) <= maxTagKeyLen {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			add("logger."+k, meta[k])
		}
	}
	var domain any
	if json.Unmarshal(extra["domain"], &domain) == nil {
		add("logger.domain", domain)
	}
	if _, ok := extra["worker"]; ok {
		for _, k := range jobExtra {
			var v any
			if json.Unmarshal(extra[k], &v) == nil {
				add("job."+k, v)
			}
		}
	}
	if len(ctx) == 0 {
		return nil
	}
	return ctx
}

// parseTags accepts an object or a list of [key, value] pairs.
func parseTags(raw json.RawMessage) map[string]string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil
	}
	tags := map[string]string{}
	add := func(k string, v any) {
		k = strings.TrimSpace(k)
		if k == "" || len(k) > maxTagKeyLen || len(tags) >= maxTags {
			return
		}
		s := scalarString(v)
		if len(s) > maxTagValueLen {
			s = s[:maxTagValueLen]
		}
		tags[k] = s
	}
	switch raw[0] {
	case '{':
		var m map[string]any
		if json.Unmarshal(raw, &m) == nil {
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			sort.Strings(keys) // deterministic truncation at maxTags
			for _, k := range keys {
				add(k, m[k])
			}
		}
	case '[':
		var pairs [][]any
		if json.Unmarshal(raw, &pairs) == nil {
			for _, p := range pairs {
				if len(p) == 2 {
					if k, ok := p[0].(string); ok {
						add(k, p[1])
					}
				}
			}
		}
	}
	if len(tags) == 0 {
		return nil
	}
	return tags
}

func rawScalar(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	return scalarString(v)
}

func scalarString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

func stringify(vs []any) []string {
	var out []string
	for _, v := range vs {
		if s := scalarString(v); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
