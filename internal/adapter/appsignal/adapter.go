// Package appsignal implements the AppSignal agent's push protocol, so
// existing AppSignal integrations report to Watchtower after setting
// APPSIGNAL_PUSH_API_ENDPOINT and APPSIGNAL_PUSH_API_KEY.
//
// Only error samples are extracted. Performance samples, metrics and host
// data in the same batches are acknowledged and counted, not stored.
package appsignal

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/olucurious/watchtower/internal/adapter"
	"github.com/olucurious/watchtower/internal/event"
	"github.com/olucurious/watchtower/internal/httpbody"
)

const Name = "appsignal"

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
		Experimental: true,
		Summary:      "AppSignal push API (/1/auth, /2/collect) used by the Elixir, Ruby, Node.js and Python integrations; error samples only. Reverse-engineered, verified on every listed version.",
		Routes:       []string{"GET|POST /1/auth", "POST /2/collect"},
		TestedSDK: []string{
			"appsignal-elixir 2.9.2–2.18.0 (17 versions, agents 0.34.1–0.37.4)",
			"appsignal (Ruby) 5.0.1", "@appsignal/nodejs 3.9.1", "appsignal (Python) 1.9.0",
		},
	}
}

func (s *Adapter) Register(mux *http.ServeMux, deps adapter.Deps) {
	h := &handler{s: s, deps: deps}
	mux.HandleFunc("GET /1/auth", h.auth)
	mux.HandleFunc("POST /1/auth", h.auth)
	mux.HandleFunc("POST /2/collect", h.collect)
}

type handler struct {
	s    *Adapter
	deps adapter.Deps
}

// auth backs the SDK's push-key validation: 200 valid, 401 invalid.
func (h *handler) auth(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authenticate(w, r); ok {
		w.WriteHeader(http.StatusOK)
	}
}

func (h *handler) collect(w http.ResponseWriter, r *http.Request) {
	project, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	body, err := httpbody.Read(r, h.deps.Limits.MaxBodyBytes, h.deps.Limits.MaxDecompressedBytes)
	switch {
	case errors.Is(err, httpbody.ErrTooLarge):
		h.reject(w, http.StatusRequestEntityTooLarge, "body_too_large", nil)
		return
	case err != nil:
		h.reject(w, http.StatusBadRequest, "unreadable_body", err)
		return
	}
	batch, err := DecodeCollect(body)
	if err != nil {
		// Never log the body: it embeds the push API key.
		h.reject(w, http.StatusBadRequest, "undecodable_payload", err)
		return
	}
	received := h.s.Now().UTC()
	events := make([]event.Event, 0, len(batch.Errors))
	for _, es := range batch.Errors {
		events = append(events, ToEvent(batch, es, project.ID, received))
	}
	h.count("ignored_samples", batch.SamplesWithoutErrors)
	if len(events) > 0 {
		err := h.deps.Sink.Accept(r.Context(), events)
		switch {
		case errors.Is(err, adapter.ErrOverloaded):
			h.count("shed_events", len(events))
			h.reject(w, http.StatusTooManyRequests, "overloaded", nil)
			return
		case err != nil:
			h.s.Log.Error("accepting appsignal events", "err", err, "events", len(events))
			h.reject(w, http.StatusServiceUnavailable, "sink_unavailable", nil)
			return
		}
		h.count("accepted_events", len(events))
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte("{}"))
}

func (h *handler) authenticate(w http.ResponseWriter, r *http.Request) (adapter.Project, bool) {
	key := r.URL.Query().Get("api_key")
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
		h.s.Log.Error("resolving appsignal key", "err", err)
		h.reject(w, http.StatusServiceUnavailable, "key_lookup_failed", nil)
		return adapter.Project{}, false
	}
	return project, true
}

func (h *handler) reject(w http.ResponseWriter, status int, reason string, err error) {
	h.count("rejected_"+reason, 1)
	if err != nil {
		h.s.Log.Debug("appsignal request rejected", "reason", reason, "err", err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": reason})
}

func (h *handler) count(outcome string, n int) {
	if h.deps.Metrics != nil && n > 0 {
		h.deps.Metrics.Count(Name, outcome, n)
	}
}
