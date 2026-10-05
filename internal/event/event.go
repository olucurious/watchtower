// Package event defines the canonical error event every adapter produces.
//
// The shape follows Sentry's event schema because it is the richest of the
// supported protocols; other adapters map into it. Only allowlisted fields
// exist here: request bodies, headers, cookies, breadcrumbs and free-form
// "extra" data are never carried, so they can never be stored.
package event

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

type Level string

const (
	LevelDebug   Level = "debug"
	LevelInfo    Level = "info"
	LevelWarning Level = "warning"
	LevelError   Level = "error"
	LevelFatal   Level = "fatal"
)

// ParseLevel maps SDK level spellings onto Level, defaulting to error.
func ParseLevel(s string) Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug", "trace":
		return LevelDebug
	case "info", "log", "notice":
		return LevelInfo
	case "warning", "warn":
		return LevelWarning
	case "fatal", "critical", "emergency", "alert":
		return LevelFatal
	default:
		return LevelError
	}
}

type Event struct {
	ID         string    `json:"id"` // 32 lowercase hex characters
	ProjectID  int64     `json:"project_id"`
	Adapter    string    `json:"adapter"` // which ingestion adapter produced it
	Timestamp  time.Time `json:"timestamp"`
	ReceivedAt time.Time `json:"received_at"`

	Platform    string `json:"platform,omitempty"`
	Level       Level  `json:"level"`
	Message     string `json:"message,omitempty"`
	Logger      string `json:"logger,omitempty"`
	Transaction string `json:"transaction,omitempty"`

	// Exceptions is the cause chain: innermost cause first, the exception
	// that was actually raised last (Sentry's ordering).
	Exceptions []Exception `json:"exceptions,omitempty"`

	Release     string            `json:"release,omitempty"`
	Environment string            `json:"environment,omitempty"`
	ServerName  string            `json:"server_name,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
	// Context holds allowlisted diagnostic values such as the logger
	// metadata an app chose to send ("logger.mfa") and a failed job's
	// worker and queue ("job.worker"). Free-form SDK data is never kept.
	Context     map[string]string `json:"context,omitempty"`
	Fingerprint []string          `json:"fingerprint,omitempty"`

	Trace   *Trace   `json:"trace,omitempty"`
	User    *User    `json:"user,omitempty"`
	Request *Request `json:"request,omitempty"`
	SDK     SDK      `json:"sdk"`

	// DebugImages link minified files to uploaded source maps by debug ID.
	DebugImages []DebugImage `json:"debug_images,omitempty"`
}

type DebugImage struct {
	Type     string `json:"type"` // "sourcemap"
	CodeFile string `json:"code_file"`
	DebugID  string `json:"debug_id"`
}

type Exception struct {
	Type      string     `json:"type,omitempty"`
	Value     string     `json:"value,omitempty"`
	Module    string     `json:"module,omitempty"`
	Mechanism *Mechanism `json:"mechanism,omitempty"`
	// Frames are ordered oldest call first, crashing frame last.
	Frames []Frame `json:"frames,omitempty"`
}

type Mechanism struct {
	Type    string `json:"type,omitempty"`
	Handled *bool  `json:"handled,omitempty"`
	// Synthetic exceptions are created by the SDK only to carry a stack
	// (e.g. captureMessage); their type and value say nothing about the bug.
	Synthetic bool `json:"synthetic,omitempty"`
}

type Frame struct {
	Function    string `json:"function,omitempty"`
	Module      string `json:"module,omitempty"`
	Filename    string `json:"filename,omitempty"`
	AbsPath     string `json:"abs_path,omitempty"`
	Lineno      int    `json:"lineno,omitempty"`
	Colno       int    `json:"colno,omitempty"`
	InApp       bool   `json:"in_app"`
	ContextLine string `json:"context_line,omitempty"`

	PreContext  []string `json:"pre_context,omitempty"`
	PostContext []string `json:"post_context,omitempty"`
	// Minified is the frame's location before source maps were applied.
	Minified *MinifiedLocation `json:"minified,omitempty"`
}

type MinifiedLocation struct {
	Filename string `json:"filename,omitempty"`
	Function string `json:"function,omitempty"`
	Lineno   int    `json:"lineno,omitempty"`
	Colno    int    `json:"colno,omitempty"`
}

type Trace struct {
	TraceID string `json:"trace_id,omitempty"`
	SpanID  string `json:"span_id,omitempty"`
}

// User carries only an opaque identifier; emails, names and IP addresses
// are deliberately not part of the model.
type User struct {
	ID string `json:"id,omitempty"`
}

type Request struct {
	Method string `json:"method,omitempty"`
	URL    string `json:"url,omitempty"`
}

type SDK struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

var idPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// NormalizeID accepts UUIDs with or without dashes and returns the
// 32-character lowercase form, or "" when s is not an event ID.
func NormalizeID(s string) string {
	id := strings.ToLower(strings.ReplaceAll(s, "-", ""))
	if !idPattern.MatchString(id) {
		return ""
	}
	return id
}

// Validate reports whether e is complete enough to store.
func (e *Event) Validate() error {
	var errs []error
	if !idPattern.MatchString(e.ID) {
		errs = append(errs, fmt.Errorf("invalid event id %q", e.ID))
	}
	if e.ProjectID <= 0 {
		errs = append(errs, errors.New("missing project"))
	}
	if e.Adapter == "" {
		errs = append(errs, errors.New("missing adapter"))
	}
	if e.Timestamp.IsZero() || e.ReceivedAt.IsZero() {
		errs = append(errs, errors.New("missing timestamps"))
	}
	if len(e.Exceptions) == 0 && e.Message == "" {
		errs = append(errs, errors.New("event has neither exception nor message"))
	}
	return errors.Join(errs...)
}

// StripNUL removes NUL characters from every string, because Postgres
// text and jsonb reject them and SDK payloads occasionally contain them.
func (e *Event) StripNUL() {
	for _, p := range []*string{&e.Platform, &e.Message, &e.Logger, &e.Transaction, &e.Release,
		&e.Environment, &e.ServerName, &e.SDK.Name, &e.SDK.Version} {
		*p = noNUL(*p)
	}
	if len(e.Tags) > 0 {
		tags := make(map[string]string, len(e.Tags))
		for k, v := range e.Tags {
			tags[noNUL(k)] = noNUL(v)
		}
		e.Tags = tags
	}
	if len(e.Context) > 0 {
		ctx := make(map[string]string, len(e.Context))
		for k, v := range e.Context {
			ctx[noNUL(k)] = noNUL(v)
		}
		e.Context = ctx
	}
	for i := range e.Fingerprint {
		e.Fingerprint[i] = noNUL(e.Fingerprint[i])
	}
	for i := range e.Exceptions {
		ex := &e.Exceptions[i]
		ex.Type, ex.Value, ex.Module = noNUL(ex.Type), noNUL(ex.Value), noNUL(ex.Module)
		if ex.Mechanism != nil {
			ex.Mechanism.Type = noNUL(ex.Mechanism.Type)
		}
		for j := range ex.Frames {
			f := &ex.Frames[j]
			for _, p := range []*string{&f.Function, &f.Module, &f.Filename, &f.AbsPath, &f.ContextLine} {
				*p = noNUL(*p)
			}
			if f.Minified != nil {
				f.Minified.Filename = noNUL(f.Minified.Filename)
				f.Minified.Function = noNUL(f.Minified.Function)
			}
			for _, lines := range [][]string{f.PreContext, f.PostContext} {
				for k := range lines {
					lines[k] = noNUL(lines[k])
				}
			}
		}
	}
	if e.Trace != nil {
		e.Trace.TraceID, e.Trace.SpanID = noNUL(e.Trace.TraceID), noNUL(e.Trace.SpanID)
	}
	if e.User != nil {
		e.User.ID = noNUL(e.User.ID)
	}
	if e.Request != nil {
		e.Request.Method, e.Request.URL = noNUL(e.Request.Method), noNUL(e.Request.URL)
	}
	for i := range e.DebugImages {
		d := &e.DebugImages[i]
		d.Type, d.CodeFile, d.DebugID = noNUL(d.Type), noNUL(d.CodeFile), noNUL(d.DebugID)
	}
}

func noNUL(s string) string {
	if strings.IndexByte(s, 0) < 0 {
		return s
	}
	return strings.ReplaceAll(s, "\x00", "")
}

// Primary returns the exception that was actually raised, or nil.
func (e *Event) Primary() *Exception {
	if len(e.Exceptions) == 0 {
		return nil
	}
	return &e.Exceptions[len(e.Exceptions)-1]
}

// Title is the one-line summary shown for the event's issue.
func (e *Event) Title() string {
	if ex := e.Primary(); ex != nil && (ex.Mechanism == nil || !ex.Mechanism.Synthetic) {
		switch {
		case ex.Type != "" && ex.Value != "":
			return truncate(ex.Type+": "+firstLine(ex.Value), 200)
		case ex.Type != "":
			return ex.Type
		}
	}
	if e.Message != "" {
		return truncate(firstLine(e.Message), 200)
	}
	return "<untitled>"
}

// Culprit names the innermost in-app frame of the raised exception.
func (e *Event) Culprit() string {
	ex := e.Primary()
	if ex == nil {
		return e.Transaction
	}
	for i := len(ex.Frames) - 1; i >= 0; i-- {
		if f := ex.Frames[i]; f.InApp {
			return frameLabel(f)
		}
	}
	if n := len(ex.Frames); n > 0 {
		return frameLabel(ex.Frames[n-1])
	}
	return e.Transaction
}

func frameLabel(f Frame) string {
	where := f.Module
	if where == "" {
		where = f.Filename
	}
	switch {
	case where != "" && f.Function != "":
		return where + " in " + f.Function
	case f.Function != "":
		return f.Function
	default:
		return where
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && (s[cut]&0xC0) == 0x80 { // stay on a UTF-8 boundary
		cut--
	}
	return s[:cut] + "…"
}

var commitSHA = regexp.MustCompile(`^[0-9a-f]{12,40}$`)

// ShortRelease abbreviates a release that is a full commit SHA to its
// first seven characters, as git does; other release names are unchanged.
func ShortRelease(r string) string {
	if commitSHA.MatchString(r) {
		return r[:7]
	}
	return r
}
