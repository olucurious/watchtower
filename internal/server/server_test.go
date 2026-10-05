package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olucurious/watchtower/internal/adapter"
	"github.com/olucurious/watchtower/internal/adapter/adaptertest"
	"github.com/olucurious/watchtower/internal/adapter/appsignal"
	"github.com/olucurious/watchtower/internal/adapter/sentry"
	"github.com/olucurious/watchtower/internal/api"
	"github.com/olucurious/watchtower/internal/sourcemaps"
	"github.com/olucurious/watchtower/internal/ui"
)

type okPinger struct{}

func (okPinger) Ping(context.Context) error { return nil }

// TestFullRouteTable mounts every adapter, the API and the UI together:
// ServeMux panics on conflicting patterns, so this catches clashes
// between vendor protocol routes and Watchtower's own.
func TestFullRouteTable(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	adapters := []adapter.Adapter{sentry.New(log), appsignal.New(log), appsignal.NewFrontend(log)}
	deps := adapter.Deps{Keys: adaptertest.Keys{}, Sink: &adaptertest.Sink{}, Limits: adaptertest.Limits, Metrics: &adaptertest.Metrics{}}
	a := &api.API{Log: log, Adapters: adapters}
	up := &sourcemaps.Uploads{Log: log}
	h := Handler(log, adapters, deps, okPinger{}, http.NotFoundHandler(), a.Register, up.Register, func(mux *http.ServeMux) {
		mux.Handle("GET /", ui.Handler())
	})

	cases := []struct {
		method, path string
		want         int
		body         string
	}{
		{"POST", "/api/1/envelope/", http.StatusUnauthorized, ""}, // Sentry adapter, no key
		{"POST", "/2/collect", http.StatusUnauthorized, ""},       // AppSignal adapter, no key
		{"GET", "/api/v1/nope", http.StatusNotFound, `"error"`},   // API 404 is JSON
		{"GET", "/healthz", http.StatusOK, ""},
		{"POST", "/api/0/organizations/x/chunk-upload/", http.StatusUnauthorized, "Invalid token"}, // source map uploads
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, strings.NewReader("")))
		if rec.Code != c.want || !strings.Contains(rec.Body.String(), c.body) {
			t.Errorf("%s %s: %d %q", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
}
