// Package sentry implements the Sentry ingestion protocol, so official
// Sentry SDKs work by pointing their DSN at Watchtower.
package sentry

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/olucurious/watchtower/internal/adapter"
	"github.com/olucurious/watchtower/internal/event"
	"github.com/olucurious/watchtower/internal/httpbody"
)

const Name = "sentry"

// retryAfterSeconds is what SDKs are told to wait while Watchtower sheds load.
const retryAfterSeconds = 60

type Adapter struct {
	Log *slog.Logger
	Now func() time.Time
}

func New(log *slog.Logger) *Adapter {
	return &Adapter{Log: log, Now: time.Now}
}

func (s *Adapter) Name() string { return Name }

func (s *Adapter) Describe() adapter.Description {
	return adapter.Description{
		Summary: "Sentry envelope and legacy store endpoints; configure SDKs with a Watchtower DSN.",
		Routes:  []string{"POST /api/{project_id}/envelope/", "POST /api/{project_id}/store/"},
		TestedSDK: []string{
			"sentry.python 2.71.0", "sentry.javascript.node 11.2.0", "sentry-elixir 11.0.4",
		},
	}
}

func (s *Adapter) Register(mux *http.ServeMux, deps adapter.Deps) {
	h := &handler{s: s, deps: deps}
	mux.HandleFunc("POST /api/{project_id}/envelope/", h.envelope)
	mux.HandleFunc("POST /api/{project_id}/store/", h.store)
}

type handler struct {
	s    *Adapter
	deps adapter.Deps
}

func (h *handler) envelope(w http.ResponseWriter, r *http.Request) {
	body, ok := h.readBody(w, r)
	if !ok {
		return
	}
	env, err := ParseEnvelope(body)
	if err != nil {
		h.reject(w, http.StatusBadRequest, "malformed_envelope", err)
		return
	}
	project, ok := h.authenticate(w, r, env.Header.DSN)
	if !ok {
		return
	}
	received := h.s.Now().UTC()
	var events []event.Event
	for _, item := range env.Items {
		switch item.Type {
		case "event":
			if int64(len(item.Payload)) > h.deps.Limits.MaxEventBytes {
				h.reject(w, http.StatusRequestEntityTooLarge, "event_too_large", nil)
				return
			}
			e, err := Normalize(item.Payload, project.ID, env.Header.EventID, received, newEventID)
			if err != nil {
				h.reject(w, http.StatusBadRequest, "invalid_event", nil)
				return
			}
			events = append(events, e)
		default:
			// Transactions, sessions, client reports, attachments, profiles,
			// replays, check-ins and logs are outside Watchtower's scope:
			// acknowledge them so SDKs don't retry, and count them.
			h.count("ignored_"+itemTypeLabel(item.Type), 1)
		}
	}
	if !h.accept(w, r, events) {
		return
	}
	id := env.Header.EventID
	if len(events) > 0 {
		id = events[0].ID
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

// store handles the legacy endpoint: the body is a single event.
func (h *handler) store(w http.ResponseWriter, r *http.Request) {
	body, ok := h.readBody(w, r)
	if !ok {
		return
	}
	if int64(len(body)) > h.deps.Limits.MaxEventBytes {
		h.reject(w, http.StatusRequestEntityTooLarge, "event_too_large", nil)
		return
	}
	project, ok := h.authenticate(w, r, "")
	if !ok {
		return
	}
	e, err := Normalize(body, project.ID, "", h.s.Now().UTC(), newEventID)
	if err != nil {
		h.reject(w, http.StatusBadRequest, "invalid_event", nil)
		return
	}
	if !h.accept(w, r, []event.Event{e}) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": e.ID})
}

func (h *handler) readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := httpbody.Read(r, h.deps.Limits.MaxBodyBytes, h.deps.Limits.MaxDecompressedBytes)
	switch {
	case errors.Is(err, httpbody.ErrTooLarge):
		h.reject(w, http.StatusRequestEntityTooLarge, "body_too_large", nil)
	case errors.Is(err, httpbody.ErrUnsupportedEncoding):
		h.reject(w, http.StatusUnsupportedMediaType, "unsupported_encoding", err)
	case err != nil:
		h.reject(w, http.StatusBadRequest, "unreadable_body", err)
	default:
		return body, true
	}
	return nil, false
}

// authenticate resolves the DSN key and checks it belongs to the project
// in the URL, so one project's key cannot write into another.
func (h *handler) authenticate(w http.ResponseWriter, r *http.Request, envelopeDSN string) (adapter.Project, bool) {
	key := publicKey(r, envelopeDSN)
	if key == "" {
		h.reject(w, http.StatusUnauthorized, "missing_key", nil)
		return adapter.Project{}, false
	}
	project, err := h.deps.Keys.ResolveKey(r.Context(), Name, key)
	if errors.Is(err, adapter.ErrUnknownKey) {
		h.reject(w, http.StatusUnauthorized, "unknown_key", nil)
		return adapter.Project{}, false
	}
	if err != nil {
		h.s.Log.Error("resolving sentry key", "err", err)
		h.reject(w, http.StatusServiceUnavailable, "key_lookup_failed", nil)
		return adapter.Project{}, false
	}
	if r.PathValue("project_id") != strconv.FormatInt(project.ID, 10) {
		h.reject(w, http.StatusForbidden, "project_mismatch", nil)
		return adapter.Project{}, false
	}
	return project, true
}

func (h *handler) accept(w http.ResponseWriter, r *http.Request, events []event.Event) bool {
	if len(events) == 0 {
		return true
	}
	err := h.deps.Sink.Accept(r.Context(), events)
	switch {
	case errors.Is(err, adapter.ErrOverloaded):
		// Format: retry_after:categories:scope; empty categories = all.
		w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds))
		w.Header().Set("X-Sentry-Rate-Limits", strconv.Itoa(retryAfterSeconds)+"::organization")
		h.reject(w, http.StatusTooManyRequests, "overloaded", nil)
		h.count("shed_events", len(events))
		return false
	case err != nil:
		h.s.Log.Error("accepting sentry events", "err", err, "events", len(events))
		h.reject(w, http.StatusServiceUnavailable, "sink_unavailable", nil)
		return false
	}
	h.count("accepted_events", len(events))
	return true
}

// reject answers with Sentry's {"detail": ...} error shape. err is logged
// for diagnosis but never echoed, as it can quote payload fragments.
func (h *handler) reject(w http.ResponseWriter, status int, reason string, err error) {
	h.count("rejected_"+reason, 1)
	if err != nil {
		h.s.Log.Debug("sentry request rejected", "reason", reason, "err", err)
	}
	writeJSON(w, status, map[string]string{"detail": reason})
}

func (h *handler) count(outcome string, n int) {
	if h.deps.Metrics != nil {
		h.deps.Metrics.Count(Name, outcome, n)
	}
}

// itemTypeLabel bounds metric label cardinality to known item types.
func itemTypeLabel(t string) string {
	switch t {
	case "transaction", "session", "sessions", "client_report", "attachment",
		"profile", "profile_chunk", "replay_event", "replay_recording", "replay_video",
		"check_in", "span", "log", "statsd", "metric_buckets", "user_report", "feedback", "otel_log":
		return t
	}
	return "other"
}

func newEventID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
