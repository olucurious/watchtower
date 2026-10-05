// Package api serves the JSON API behind the web UI under /api/v1/.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/olucurious/watchtower/internal/adapter"
	"github.com/olucurious/watchtower/internal/alert"
	"github.com/olucurious/watchtower/internal/email"
	"github.com/olucurious/watchtower/internal/store"
)

type API struct {
	Store         *store.Store
	Log           *slog.Logger
	Adapters      []adapter.Adapter // every known adapter
	Enabled       map[string]bool   // adapters enabled for ingestion
	PublicURL     string
	Version       string
	RetentionDays int
	Sealer        *alert.Sealer // nil when WATCHTOWER_SECRET_KEY is unset
	Slack         *alert.Slack
	Email         email.Sender // nil when no mail server is configured
	Linear        *alert.Linear
	Tracker       *alert.Tracker

	limiter   loginLimiter
	testMails testMailLimiter
}

func (a *API) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/meta", a.meta)
	mux.HandleFunc("POST /api/v1/auth/login", a.mutation(a.login))
	mux.HandleFunc("POST /api/v1/setup", a.mutation(a.setup))
	mux.HandleFunc("POST /api/v1/auth/logout", a.mutation(a.logout))
	mux.HandleFunc("GET /api/v1/auth/me", a.user(a.me))
	mux.HandleFunc("POST /api/v1/account/password", a.mutation(a.user(a.changePassword)))
	mux.HandleFunc("GET /api/v1/account/notifications", a.user(a.getEmailPrefs))
	mux.HandleFunc("PUT /api/v1/account/notifications", a.mutation(a.user(a.setEmailPrefs)))
	mux.HandleFunc("POST /api/v1/account/test-email", a.mutation(a.user(a.sendTestEmail)))
	mux.HandleFunc("GET /api/v1/account/tokens", a.user(a.listAccessTokens))
	mux.HandleFunc("POST /api/v1/account/tokens", a.mutation(a.user(a.createAccessToken)))
	mux.HandleFunc("DELETE /api/v1/account/tokens/{id}", a.mutation(a.user(a.revokeAccessToken)))

	mux.HandleFunc("GET /api/v1/adapters", a.user(a.listAdapters))
	mux.HandleFunc("GET /api/v1/projects", a.user(a.listProjects))
	mux.HandleFunc("POST /api/v1/projects", a.mutation(a.admin(a.createProject)))
	mux.HandleFunc("PATCH /api/v1/projects/{slug}", a.mutation(a.admin(a.updateProject)))
	mux.HandleFunc("GET /api/v1/projects/{slug}/keys", a.user(a.listKeys))
	mux.HandleFunc("POST /api/v1/projects/{slug}/keys", a.mutation(a.admin(a.createKey)))
	mux.HandleFunc("DELETE /api/v1/projects/{slug}/keys/{id}", a.mutation(a.admin(a.revokeKey)))
	mux.HandleFunc("GET /api/v1/environments", a.user(a.listEnvironments))
	mux.HandleFunc("GET /api/v1/projects/{slug}/alerts", a.admin(a.listAlerts))
	mux.HandleFunc("POST /api/v1/projects/{slug}/alerts", a.mutation(a.admin(a.createAlert)))
	mux.HandleFunc("PUT /api/v1/projects/{slug}/alerts/{id}", a.mutation(a.admin(a.updateAlert)))
	mux.HandleFunc("DELETE /api/v1/projects/{slug}/alerts/{id}", a.mutation(a.admin(a.deleteAlert)))
	mux.HandleFunc("POST /api/v1/projects/{slug}/alerts/{id}/test", a.mutation(a.admin(a.testAlert)))
	mux.HandleFunc("POST /api/v1/projects/{slug}/linear/teams", a.mutation(a.admin(a.linearTeams)))
	mux.HandleFunc("POST /api/v1/issues/{id}/linear", a.mutation(a.user(a.createLinearIssue)))
	mux.HandleFunc("GET /api/v1/projects/{slug}/sourcemaps", a.admin(a.listSourceMaps))
	mux.HandleFunc("POST /api/v1/projects/{slug}/upload-tokens", a.mutation(a.admin(a.createUploadToken)))
	mux.HandleFunc("DELETE /api/v1/projects/{slug}/upload-tokens/{id}", a.mutation(a.admin(a.revokeUploadToken)))
	mux.HandleFunc("DELETE /api/v1/projects/{slug}/sourcemaps/{id}", a.mutation(a.admin(a.deleteBundle)))

	mux.HandleFunc("GET /api/v1/issues", a.user(a.listIssues))
	mux.HandleFunc("POST /api/v1/issues/status", a.mutation(a.user(a.setIssuesStatus)))
	mux.HandleFunc("GET /api/v1/issues/{id}", a.user(a.getIssue))
	mux.HandleFunc("GET /api/v1/issues/{id}/events", a.user(a.listIssueEvents))
	mux.HandleFunc("GET /api/v1/issues/{id}/events/{which}", a.user(a.getIssueEvent))
	mux.HandleFunc("PUT /api/v1/issues/{id}/assignee", a.mutation(a.user(a.setAssignee)))
	mux.HandleFunc("POST /api/v1/issues/{id}/comments", a.mutation(a.user(a.addComment)))
	mux.HandleFunc("DELETE /api/v1/issues/{id}/comments/{comment}", a.mutation(a.user(a.deleteComment)))
	mux.HandleFunc("GET /api/v1/members", a.user(a.listMembers))

	mux.HandleFunc("GET /api/v1/users", a.admin(a.listUsers))
	mux.HandleFunc("POST /api/v1/users", a.mutation(a.admin(a.createUser)))
	mux.HandleFunc("PATCH /api/v1/users/{id}", a.mutation(a.admin(a.updateUser)))
	// Unknown API paths get a JSON 404 rather than the UI's index page.
	mux.HandleFunc("GET /api/v1/{rest...}", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
}

func (a *API) meta(w http.ResponseWriter, r *http.Request) {
	n, err := a.Store.CountUsers(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": a.Version, "has_users": n > 0, "retention_days": a.RetentionDays, "email_enabled": a.Email != nil})
}

type ctxKey struct{}

func currentUser(ctx context.Context) store.User {
	u, _ := ctx.Value(ctxKey{}).(store.User)
	return u
}

// user requires a valid session.
func (a *API) user(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := a.sessionUser(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "sign in required")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)))
	}
}

// admin requires an administrator session.
func (a *API) admin(next http.HandlerFunc) http.HandlerFunc {
	return a.user(func(w http.ResponseWriter, r *http.Request) {
		if !currentUser(r.Context()).IsAdmin {
			writeError(w, http.StatusForbidden, "administrator access required")
			return
		}
		next(w, r)
	})
}

// mutation guards state-changing requests against CSRF: the body must be
// JSON (which a cross-site form cannot send without a CORS preflight) and
// any Origin header must match this server.
func (a *API) mutation(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			writeError(w, http.StatusUnsupportedMediaType, "expected application/json")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !a.sameOrigin(r, origin) {
			writeError(w, http.StatusForbidden, "cross-origin request refused")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		next(w, r)
	}
}

func (a *API) sameOrigin(r *http.Request, origin string) bool {
	o, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if o.Host == r.Host {
		return true
	}
	pub, err := url.Parse(a.PublicURL)
	return err == nil && o.Scheme == pub.Scheme && o.Host == pub.Host
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func pathInt(r *http.Request, name string) (int64, bool) {
	n, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	return n, err == nil && n > 0
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (a *API) internal(w http.ResponseWriter, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	a.Log.Error("api request failed", "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

// storeError maps store errors to responses.
func (a *API) storeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, store.ErrExists):
		writeError(w, http.StatusConflict, "already exists")
	default:
		a.internal(w, err)
	}
}
