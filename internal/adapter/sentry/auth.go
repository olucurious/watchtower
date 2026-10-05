package sentry

import (
	"net/http"
	"net/url"
	"strings"
)

// publicKey finds the DSN public key the SDK authenticated with. SDKs send
// it in X-Sentry-Auth (Python, Elixir), in the query string (JavaScript), or
// only in the envelope header's dsn (tunnelled browser events).
func publicKey(r *http.Request, envelopeDSN string) string {
	for _, h := range []string{"X-Sentry-Auth", "Authorization"} {
		if k := authHeaderKey(r.Header.Get(h)); k != "" {
			return k
		}
	}
	if k := r.URL.Query().Get("sentry_key"); k != "" {
		return k
	}
	if envelopeDSN != "" {
		if u, err := url.Parse(envelopeDSN); err == nil && u.User != nil {
			return u.User.Username()
		}
	}
	return ""
}

// authHeaderKey parses "Sentry sentry_key=abc, sentry_version=7, ...".
func authHeaderKey(h string) string {
	rest, ok := strings.CutPrefix(strings.TrimSpace(h), "Sentry ")
	if !ok {
		return ""
	}
	for _, part := range strings.Split(rest, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		if k == "sentry_key" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
