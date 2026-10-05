package api

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/olucurious/watchtower/internal/adapter/appsignal"
	"github.com/olucurious/watchtower/internal/auth"
	"github.com/olucurious/watchtower/internal/store"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

type adapterInfo struct {
	Name         string   `json:"name"`
	Summary      string   `json:"summary"`
	Routes       []string `json:"routes"`
	TestedSDK    []string `json:"tested_sdk"`
	Experimental bool     `json:"experimental"`
	Enabled      bool     `json:"enabled"`
}

func (a *API) listAdapters(w http.ResponseWriter, _ *http.Request) {
	out := make([]adapterInfo, 0, len(a.Adapters))
	for _, ad := range a.Adapters {
		d := ad.Describe()
		out = append(out, adapterInfo{ad.Name(), d.Summary, d.Routes, d.TestedSDK, d.Experimental, a.Enabled[ad.Name()]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"adapters": out})
}

func (a *API) listProjects(w http.ResponseWriter, r *http.Request) {
	ps, err := a.Store.Projects(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	if ps == nil {
		ps = []store.ProjectSummary{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": ps})
}

func (a *API) createProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	if err := decode(r, &req); err != nil || !slugPattern.MatchString(req.Slug) {
		writeError(w, http.StatusBadRequest, "slug must be lowercase letters, digits and dashes")
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		req.Name = req.Slug
	}
	p, err := a.Store.CreateProject(r.Context(), req.Slug, strings.TrimSpace(req.Name))
	if err != nil {
		a.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (a *API) listKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := a.Store.Keys(r.Context(), r.PathValue("slug"))
	if err != nil {
		a.internal(w, err)
		return
	}
	if keys == nil {
		keys = []store.Key{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys, "public_url": a.PublicURL})
}

// createKey returns the plaintext key once, with ready-to-paste settings.
func (a *API) createKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Adapter string `json:"adapter"`
		Label   string `json:"label"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	known := false
	for _, ad := range a.Adapters {
		known = known || ad.Name() == req.Adapter
	}
	if !known {
		writeError(w, http.StatusBadRequest, "unknown adapter")
		return
	}
	key, p, err := a.Store.CreateKey(r.Context(), r.PathValue("slug"), req.Adapter, strings.TrimSpace(req.Label), currentUser(r.Context()).ID)
	if err != nil {
		a.storeError(w, err)
		return
	}
	settings, err := SDKSettings(a.PublicURL, req.Adapter, key, p.ID)
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"key": key, "settings": settings})
}

// SDKSettings renders the environment variables an SDK needs for a key.
func SDKSettings(publicURL, adapterName, key string, projectID int64) (map[string]string, error) {
	switch adapterName {
	case "sentry":
		u, err := url.Parse(publicURL)
		if err != nil {
			return nil, err
		}
		u.User = url.User(key)
		u.Path = strings.TrimRight(u.Path, "/") + "/" + strconv.FormatInt(projectID, 10)
		return map[string]string{"SENTRY_DSN": u.String()}, nil
	case "appsignal":
		return map[string]string{"APPSIGNAL_PUSH_API_ENDPOINT": publicURL, "APPSIGNAL_PUSH_API_KEY": key}, nil
	case "appsignal-frontend":
		// Options for `new Appsignal({ key, uri })` in the browser.
		return map[string]string{"key": key, "uri": appsignal.FrontendURI(publicURL)}, nil
	}
	return map[string]string{}, nil
}

func (a *API) revokeKey(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "id")
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err := a.Store.RevokeKey(r.Context(), r.PathValue("slug"), id); err != nil {
		a.storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := a.Store.Users(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

func (a *API) createUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		Password string `json:"password"`
		IsAdmin  bool   `json:"is_admin"`
	}
	if err := decode(r, &req); err != nil || !strings.Contains(req.Email, "@") {
		writeError(w, http.StatusBadRequest, "a valid email is required")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if errors.Is(err, auth.ErrWeakPassword) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	} else if err != nil {
		a.internal(w, err)
		return
	}
	u, err := a.Store.CreateUser(r.Context(), strings.TrimSpace(req.Email), strings.TrimSpace(req.Name), hash, req.IsAdmin)
	if err != nil {
		a.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

func (a *API) updateUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "id")
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	var req struct {
		Disabled *bool `json:"disabled"`
	}
	if err := decode(r, &req); err != nil || req.Disabled == nil {
		writeError(w, http.StatusBadRequest, "expected {\"disabled\": true|false}")
		return
	}
	if id == currentUser(r.Context()).ID && *req.Disabled {
		writeError(w, http.StatusBadRequest, "you cannot disable your own account")
		return
	}
	if err := a.Store.SetUserDisabled(r.Context(), id, *req.Disabled); err != nil {
		a.storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
