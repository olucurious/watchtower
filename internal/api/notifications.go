package api

import (
	"context"
	"net/http"
	"net/mail"
	"sync"
	"time"

	"github.com/olucurious/watchtower/internal/email"
	"github.com/olucurious/watchtower/internal/store"
)

func (a *API) getEmailPrefs(w http.ResponseWriter, r *http.Request) {
	p, err := a.Store.EmailPrefs(r.Context(), currentUser(r.Context()).ID)
	if err != nil {
		a.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (a *API) setEmailPrefs(w http.ResponseWriter, r *http.Request) {
	var p store.EmailPrefs
	if err := decode(r, &p); err != nil || (p.Digest != "off" && p.Digest != "daily" && p.Digest != "weekly") {
		writeError(w, http.StatusBadRequest, "expected assigned, regressed and comments booleans and a digest of off, daily or weekly")
		return
	}
	if err := a.Store.SetEmailPrefs(r.Context(), currentUser(r.Context()).ID, p); err != nil {
		a.internal(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// testMailInterval stops the test button from being used to flood a mailbox.
const testMailInterval = 30 * time.Second

type testMailLimiter struct {
	mu   sync.Mutex
	last map[int64]time.Time
}

func (l *testMailLimiter) allow(userID int64, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last == nil {
		l.last = map[int64]time.Time{}
	}
	if now.Sub(l.last[userID]) < testMailInterval {
		return false
	}
	l.last[userID] = now
	return true
}

// sendTestEmail sends to the signed-in user straight away, so a mistake in
// the SMTP settings shows up here rather than as a silently failing outbox.
func (a *API) sendTestEmail(w http.ResponseWriter, r *http.Request) {
	if a.Email == nil {
		writeError(w, http.StatusConflict, "email is not configured on this server; see WATCHTOWER_EMAIL_PROVIDER in the README")
		return
	}
	u := currentUser(r.Context())
	if !a.testMails.allow(u.ID, time.Now()) {
		writeError(w, http.StatusTooManyRequests, "a test email was just sent; try again in a moment")
		return
	}
	msg, err := email.Render(mail.Address{Name: u.Name, Address: u.Email}, email.TestContent(a.PublicURL))
	if err != nil {
		a.internal(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	if err := a.Email.Send(ctx, msg); err != nil {
		a.Log.Warn("test email failed", "err", err)
		writeError(w, http.StatusBadGateway, "the email provider did not accept the message: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"sent_to": u.Email})
}
