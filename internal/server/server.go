// Package server assembles the HTTP surface: enabled adapters, health
// checks and metrics.
package server

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/olucurious/watchtower/internal/adapter"
	"github.com/olucurious/watchtower/internal/event"
	"github.com/olucurious/watchtower/internal/scrub"
)

type Pinger interface {
	Ping(ctx context.Context) error
}

// Handler mounts each adapter and the operational endpoints.
// mounts registers further routes, such as the UI API and static files.
func Handler(log *slog.Logger, adapters []adapter.Adapter, deps adapter.Deps, db Pinger, metrics http.Handler, mounts ...func(*http.ServeMux)) http.Handler {
	deps.Sink = scrubbingSink{next: deps.Sink}
	mux := http.NewServeMux()
	for _, s := range adapters {
		s.Register(mux, deps)
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := db.Ping(ctx); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("GET /metrics", metrics)
	for _, m := range mounts {
		m(mux)
	}
	return accessLog(log, mux)
}

// scrubbingSink redacts events before anything durable sees them, so the
// queue, events table and dead letters never hold unscrubbed values.
type scrubbingSink struct{ next adapter.Sink }

func (s scrubbingSink) Accept(ctx context.Context, events []event.Event) error {
	for i := range events {
		scrub.Event(&events[i])
	}
	return s.next.Accept(ctx, events)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// accessLog records method, path, status and duration. The query string
// is deliberately omitted: SDKs put credentials there.
func accessLog(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" || r.URL.Path == "/metrics" ||
			(r.Method == http.MethodGet && !strings.HasPrefix(r.URL.Path, "/api/")) {
			return
		}
		log.Info("request", "method", r.Method, "path", r.URL.Path, "status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(), "user_agent", r.UserAgent())
	})
}
