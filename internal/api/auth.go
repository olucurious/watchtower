package api

import (
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/olucurious/watchtower/internal/auth"
	"github.com/olucurious/watchtower/internal/store"
)

const (
	sessionCookie = "watchtower_session"
	sessionTTL    = 14 * 24 * time.Hour
)

func (a *API) sessionUser(r *http.Request) (store.User, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return store.User{}, store.ErrNotFound
	}
	return a.Store.SessionUser(r.Context(), auth.Digest(c.Value))
}

func (a *API) secureCookies() bool { return strings.HasPrefix(a.PublicURL, "https://") }

func (a *API) setSessionCookie(w http.ResponseWriter, token string, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(maxAge.Seconds()),
		HttpOnly: true,
		Secure:   a.secureCookies(),
		SameSite: http.SameSiteLaxMode,
	})
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	email := strings.TrimSpace(req.Email)
	limitKey := clientIP(r) + "|" + strings.ToLower(email)
	if !a.limiter.allow(limitKey) {
		writeError(w, http.StatusTooManyRequests, "too many failed attempts; try again later")
		return
	}
	u, hash, err := a.Store.UserForLogin(r.Context(), email)
	switch {
	case errors.Is(err, store.ErrNotFound):
		auth.CheckDummy(req.Password)
	case err != nil:
		a.internal(w, err)
		return
	default:
		if ok, err := auth.CheckPassword(hash, req.Password); err != nil {
			a.internal(w, err)
			return
		} else if ok {
			a.limiter.reset(limitKey)
			token, digest, err := auth.NewToken()
			if err == nil {
				err = a.Store.CreateSession(r.Context(), digest, u.ID, sessionTTL)
			}
			if err != nil {
				a.internal(w, err)
				return
			}
			a.setSessionCookie(w, token, sessionTTL)
			writeJSON(w, http.StatusOK, u)
			return
		}
	}
	a.limiter.fail(limitKey)
	writeError(w, http.StatusUnauthorized, "incorrect email or password")
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		if err := a.Store.DeleteSession(r.Context(), auth.Digest(c.Value)); err != nil {
			a.internal(w, err)
			return
		}
	}
	a.setSessionCookie(w, "", -time.Second)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, currentUser(r.Context()))
}

func (a *API) changePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	u := currentUser(r.Context())
	_, hash, err := a.Store.UserForLogin(r.Context(), u.Email)
	if err != nil {
		a.storeError(w, err)
		return
	}
	if ok, err := auth.CheckPassword(hash, req.Current); err != nil || !ok {
		writeError(w, http.StatusForbidden, "current password is incorrect")
		return
	}
	newHash, err := auth.HashPassword(req.New)
	if errors.Is(err, auth.ErrWeakPassword) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err == nil {
		err = a.Store.SetPassword(r.Context(), u.ID, newHash) // also ends every session
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	a.setSessionCookie(w, "", -time.Second)
	w.WriteHeader(http.StatusNoContent)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// loginLimiter blocks a client+email pair after repeated failures.
type loginLimiter struct {
	mu   sync.Mutex
	hits map[string]*attempts
}

type attempts struct {
	n     int
	reset time.Time
}

const (
	maxLoginFailures = 10
	loginWindow      = 15 * time.Minute
)

func (l *loginLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.hits[key]
	return a == nil || time.Now().After(a.reset) || a.n < maxLoginFailures
}

func (l *loginLimiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.hits == nil || len(l.hits) > 50_000 {
		l.hits = map[string]*attempts{}
	}
	a := l.hits[key]
	if a == nil || time.Now().After(a.reset) {
		a = &attempts{reset: time.Now().Add(loginWindow)}
		l.hits[key] = a
	}
	a.n++
}

func (l *loginLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.hits, key)
}
