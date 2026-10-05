package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/olucurious/watchtower/internal/store"
)

func (a *API) listAccessTokens(w http.ResponseWriter, r *http.Request) {
	ts, err := a.Store.AccessTokens(r.Context(), currentUser(r.Context()).ID)
	if err != nil {
		a.internal(w, err)
		return
	}
	if ts == nil {
		ts = []store.AccessToken{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": ts, "mcp_url": a.PublicURL + "/mcp"})
}

// createAccessToken issues a personal access token for the signed-in user;
// the plaintext is returned once.
func (a *API) createAccessToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name          string `json:"name"`
		Scope         string `json:"scope"`
		ExpiresInDays int    `json:"expires_in_days"` // 0 means no expiry
	}
	if err := decode(r, &req); err != nil || (req.Scope != "read" && req.Scope != "write") || req.ExpiresInDays < 0 || req.ExpiresInDays > 366 {
		writeError(w, http.StatusBadRequest, "expected a name, a scope of read or write, and expires_in_days from 0 (never) to 366")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 80 {
		writeError(w, http.StatusBadRequest, "name must be 1 to 80 characters")
		return
	}
	var expires *time.Time
	if req.ExpiresInDays > 0 {
		t := time.Now().Add(time.Duration(req.ExpiresInDays) * 24 * time.Hour)
		expires = &t
	}
	token, t, err := a.Store.CreateAccessToken(r.Context(), currentUser(r.Context()).ID, name, req.Scope, expires)
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "access_token": t, "mcp_url": a.PublicURL + "/mcp"})
}

func (a *API) revokeAccessToken(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "id")
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err := a.Store.RevokeAccessToken(r.Context(), currentUser(r.Context()).ID, id); err != nil {
		a.storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
