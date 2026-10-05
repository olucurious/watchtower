package appsignal

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/olucurious/watchtower/internal/adapter"
	"github.com/olucurious/watchtower/internal/event"
)

// FrontendName is the adapter for @appsignal/javascript, AppSignal's
// browser SDK. It has its own keys: front-end keys are embedded in public
// web pages, so they must never be server push keys.
const FrontendName = "appsignal-frontend"

const maxFrontendBody = 256 << 10

type Frontend struct {
	Log *slog.Logger
	Now func() time.Time
}

func NewFrontend(log *slog.Logger) *Frontend { return &Frontend{Log: log, Now: time.Now} }

func (f *Frontend) Name() string { return FrontendName }

func (f *Frontend) Describe() adapter.Description {
	return adapter.Description{
		Experimental: true,
		Summary:      "AppSignal front-end JavaScript SDK (POST /collect); set the SDK's uri to Watchtower.",
		Routes:       []string{"POST /collect"},
		TestedSDK:    []string{"@appsignal/javascript 1.6.1 (Chromium)"},
	}
}

func (f *Frontend) Register(mux *http.ServeMux, deps adapter.Deps) {
	h := &frontendHandler{f: f, deps: deps}
	mux.HandleFunc("POST /collect", h.collect)
	mux.HandleFunc("OPTIONS /collect", h.preflight)
}

type frontendHandler struct {
	f    *Frontend
	deps adapter.Deps
}

// frontendPayload is the JSON the browser SDK posts. Params, breadcrumbs
// and environment details are not decoded.
type frontendPayload struct {
	Timestamp int64  `json:"timestamp"`
	Namespace string `json:"namespace"`
	Action    string `json:"action"`
	Revision  string `json:"revision"`
	Error     struct {
		Name      string   `json:"name"`
		Message   string   `json:"message"`
		Backtrace []string `json:"backtrace"`
	} `json:"error"`
	Tags map[string]any `json:"tags"`
}

// The SDK posts text/plain to avoid a CORS preflight; answer one anyway
// for proxies or SDK versions that send it.
func (h *frontendHandler) preflight(w http.ResponseWriter, _ *http.Request) {
	cors(w)
	w.Header().Set("Access-Control-Allow-Methods", "POST")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.WriteHeader(http.StatusNoContent)
}

func cors(w http.ResponseWriter) { w.Header().Set("Access-Control-Allow-Origin", "*") }

func (h *frontendHandler) collect(w http.ResponseWriter, r *http.Request) {
	cors(w)
	key := r.URL.Query().Get("api_key")
	if key == "" {
		h.reject(w, http.StatusUnauthorized, "missing_key")
		return
	}
	project, err := h.deps.Keys.ResolveKey(r.Context(), FrontendName, key)
	if errors.Is(err, adapter.ErrUnknownKey) {
		h.reject(w, http.StatusUnauthorized, "unknown_key")
		return
	}
	if err != nil {
		h.f.Log.Error("resolving appsignal front-end key", "err", err)
		h.reject(w, http.StatusServiceUnavailable, "key_lookup_failed")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxFrontendBody+1))
	if err != nil || len(body) > maxFrontendBody {
		h.reject(w, http.StatusRequestEntityTooLarge, "body_too_large")
		return
	}
	var p frontendPayload
	if json.Unmarshal(body, &p) != nil || p.Error.Name == "" {
		h.reject(w, http.StatusBadRequest, "invalid_payload")
		return
	}
	received := h.f.Now().UTC()
	e := frontendEvent(&p, project.ID, r.URL.Query().Get("version"), received)
	err = h.deps.Sink.Accept(r.Context(), []event.Event{e})
	switch {
	case errors.Is(err, adapter.ErrOverloaded):
		h.reject(w, http.StatusTooManyRequests, "overloaded")
		return
	case err != nil:
		h.f.Log.Error("accepting appsignal front-end event", "err", err)
		h.reject(w, http.StatusServiceUnavailable, "sink_unavailable")
		return
	}
	h.count("accepted_events", 1)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte("{}"))
}

func frontendEvent(p *frontendPayload, projectID int64, sdkVersion string, received time.Time) event.Event {
	ts := time.Unix(p.Timestamp, 0).UTC()
	if p.Timestamp <= 0 || ts.After(received.Add(time.Hour)) {
		ts = received
	}
	var id [16]byte
	_, _ = rand.Read(id[:])
	e := event.Event{
		ID:          hex.EncodeToString(id[:]),
		ProjectID:   projectID,
		Adapter:     FrontendName,
		Timestamp:   ts,
		ReceivedAt:  received,
		Platform:    "javascript",
		Level:       event.LevelError,
		Release:     p.Revision,
		Transaction: p.Action,
		SDK:         event.SDK{Name: "appsignal-javascript", Version: sdkVersion},
		Exceptions: []event.Exception{{
			Type:   p.Error.Name,
			Value:  p.Error.Message,
			Frames: parseBacktrace("javascript", p.Error.Backtrace),
		}},
	}
	tags := map[string]string{}
	for k, v := range p.Tags {
		if s, ok := v.(string); ok && len(tags) < 50 {
			tags[k] = s
		}
	}
	if p.Namespace != "" {
		tags["namespace"] = p.Namespace
	}
	if len(tags) > 0 {
		e.Tags = tags
	}
	return e
}

func (h *frontendHandler) reject(w http.ResponseWriter, status int, reason string) {
	h.count("rejected_"+reason, 1)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": reason})
}

func (h *frontendHandler) count(outcome string, n int) {
	if h.deps.Metrics != nil {
		h.deps.Metrics.Count(FrontendName, outcome, n)
	}
}

// FrontendURI is the value for the browser SDK's `uri` option: the full
// collect URL, not just the host.
func FrontendURI(publicURL string) string { return strings.TrimRight(publicURL, "/") + "/collect" }
