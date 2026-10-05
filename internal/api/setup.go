package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/olucurious/watchtower/internal/auth"
	"github.com/olucurious/watchtower/internal/store"
)

// setup creates the first administrator from the browser. It needs the
// one-time code the server prints at startup, so whoever reaches a fresh
// install first cannot claim it without access to its logs.
func (a *API) setup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code     string `json:"code"`
		Email    string `json:"email"`
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if err := decode(r, &req); err != nil || !strings.Contains(req.Email, "@") {
		writeError(w, http.StatusBadRequest, "a valid email is required")
		return
	}
	limitKey := "setup|" + clientIP(r)
	if !a.limiter.allow(limitKey) {
		writeError(w, http.StatusTooManyRequests, "too many wrong setup codes; try again later")
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
	u, err := a.Store.CreateFirstAdmin(r.Context(), req.Code, strings.TrimSpace(req.Email), strings.TrimSpace(req.Name), hash)
	switch {
	case errors.Is(err, store.ErrSetupDone):
		writeError(w, http.StatusConflict, "Watchtower is already set up; sign in instead")
		return
	case errors.Is(err, store.ErrBadSetupCode):
		a.limiter.fail(limitKey)
		writeError(w, http.StatusForbidden, "that setup code is not valid; the server logs show the current one")
		return
	case err != nil:
		a.internal(w, err)
		return
	}
	a.Log.Info("first administrator created", "user_id", u.ID)
	token, digest, err := auth.NewToken()
	if err == nil {
		err = a.Store.CreateSession(r.Context(), digest, u.ID, sessionTTL)
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	a.setSessionCookie(w, token, sessionTTL)
	writeJSON(w, http.StatusCreated, u)
}
