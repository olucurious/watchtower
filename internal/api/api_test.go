package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olucurious/watchtower/internal/adapter"
	"github.com/olucurious/watchtower/internal/adapter/appsignal"
	"github.com/olucurious/watchtower/internal/adapter/sentry"
	"github.com/olucurious/watchtower/internal/auth"
	"github.com/olucurious/watchtower/internal/store"
	"github.com/olucurious/watchtower/internal/store/storetest"
)

const (
	adminPassword  = "admin-password-123"
	memberPassword = "member-password-123"
)

type harness struct {
	srv   *httptest.Server
	store *store.Store
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	s := storetest.New(t)
	ctx := context.Background()
	for _, u := range []struct {
		email, pw string
		admin     bool
	}{{"admin@example.com", adminPassword, true}, {"member@example.com", memberPassword, false}} {
		hash, err := auth.HashPassword(u.pw)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateUser(ctx, u.email, "", hash, u.admin); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.CreateProject(ctx, "library", "Library"); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a := &API{Store: s, Log: log, Adapters: []adapter.Adapter{sentry.New(log), appsignal.New(log)},
		Enabled: map[string]bool{"sentry": true}, PublicURL: "https://errors.example.com", Version: "test"}
	mux := http.NewServeMux()
	a.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &harness{srv: srv, store: s}
}

// client returns an HTTP client with its own cookie jar.
func (h *harness) client(t *testing.T) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

func (h *harness) do(t *testing.T, c *http.Client, method, path, body string, headers ...string) (*http.Response, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, h.srv.URL+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func (h *harness) login(t *testing.T, email, password string) *http.Client {
	t.Helper()
	c := h.client(t)
	resp, _ := h.do(t, c, "POST", "/api/v1/auth/login", `{"email":"`+email+`","password":"`+password+`"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("login %s: %d", email, resp.StatusCode)
	}
	return c
}

func TestLoginSessionAndLogout(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	if resp, _ := h.do(t, c, "GET", "/api/v1/auth/me", ""); resp.StatusCode != 401 {
		t.Fatalf("anonymous me: %d", resp.StatusCode)
	}
	resp, body := h.do(t, c, "POST", "/api/v1/auth/login", `{"email":"ADMIN@example.com","password":"`+adminPassword+`"}`)
	if resp.StatusCode != 200 || body["is_admin"] != true {
		t.Fatalf("login: %d %v", resp.StatusCode, body)
	}
	cookie := resp.Header.Get("Set-Cookie")
	for _, want := range []string{"HttpOnly", "SameSite=Lax", "Secure"} {
		if !strings.Contains(cookie, want) {
			t.Errorf("session cookie lacks %s: %s", want, cookie)
		}
	}
	if resp, _ := h.do(t, c, "GET", "/api/v1/auth/me", ""); resp.StatusCode != 200 {
		t.Error("session not accepted")
	}
	h.do(t, c, "POST", "/api/v1/auth/logout", "{}")
	if resp, _ := h.do(t, c, "GET", "/api/v1/auth/me", ""); resp.StatusCode != 401 {
		t.Error("session survived logout")
	}
}

func TestLoginFailuresAreUniformAndLimited(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	_, unknown := h.do(t, c, "POST", "/api/v1/auth/login", `{"email":"nobody@example.com","password":"whatever-123"}`)
	_, wrong := h.do(t, c, "POST", "/api/v1/auth/login", `{"email":"admin@example.com","password":"wrong-password"}`)
	if unknown["error"] != wrong["error"] {
		t.Errorf("unknown email and wrong password must look the same: %v vs %v", unknown, wrong)
	}
	for i := 0; i < maxLoginFailures; i++ {
		h.do(t, c, "POST", "/api/v1/auth/login", `{"email":"member@example.com","password":"wrong-password"}`)
	}
	resp, _ := h.do(t, c, "POST", "/api/v1/auth/login", `{"email":"member@example.com","password":"`+memberPassword+`"}`)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("after %d failures, correct password got %d, want 429", maxLoginFailures, resp.StatusCode)
	}
}

func TestMutationsRequireJSONAndSameOrigin(t *testing.T) {
	h := newHarness(t)
	c := h.login(t, "admin@example.com", adminPassword)
	req, _ := http.NewRequest("POST", h.srv.URL+"/api/v1/projects", strings.NewReader(`slug=x`))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, _ := c.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("form post: %d", resp.StatusCode)
	}
	if resp, _ := h.do(t, c, "POST", "/api/v1/projects", `{"slug":"x"}`, "Origin", "https://evil.example"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin post: %d", resp.StatusCode)
	}
	if resp, _ := h.do(t, c, "POST", "/api/v1/projects", `{"slug":"search"}`, "Origin", "https://errors.example.com"); resp.StatusCode != http.StatusCreated {
		t.Errorf("same-origin post via public URL: %d", resp.StatusCode)
	}
}

func TestAdminOnlyActions(t *testing.T) {
	h := newHarness(t)
	member := h.login(t, "member@example.com", memberPassword)
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/users", ""},
		{"POST", "/api/v1/projects", `{"slug":"x"}`},
		{"POST", "/api/v1/projects/library/keys", `{"adapter":"sentry"}`},
		{"POST", "/api/v1/users", `{"email":"a@b.c","password":"long-enough-pass"}`},
	} {
		if resp, _ := h.do(t, member, c.method, c.path, c.body); resp.StatusCode != http.StatusForbidden {
			t.Errorf("member %s %s: %d", c.method, c.path, resp.StatusCode)
		}
	}
	if resp, _ := h.do(t, member, "GET", "/api/v1/issues", ""); resp.StatusCode != 200 {
		t.Errorf("members can read issues: %d", resp.StatusCode)
	}
}

func TestKeyLifecycle(t *testing.T) {
	h := newHarness(t)
	admin := h.login(t, "admin@example.com", adminPassword)
	resp, body := h.do(t, admin, "POST", "/api/v1/projects/library/keys", `{"adapter":"appsignal","label":"prod"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create key: %d %v", resp.StatusCode, body)
	}
	key := body["key"].(string)
	settings := body["settings"].(map[string]any)
	if settings["APPSIGNAL_PUSH_API_ENDPOINT"] != "https://errors.example.com" || settings["APPSIGNAL_PUSH_API_KEY"] != key {
		t.Errorf("settings %v", settings)
	}
	ctx := context.Background()
	if _, err := h.store.ResolveKey(ctx, "appsignal", key); err != nil {
		t.Fatal(err)
	}
	_, list := h.do(t, admin, "GET", "/api/v1/projects/library/keys", "")
	keys := list["keys"].([]any)
	k := keys[0].(map[string]any)
	if k["created_by"] != "admin@example.com" || k["label"] != "prod" {
		t.Errorf("key listing %v", k)
	}
	if strings.Contains(mustJSON(list), key) {
		t.Error("key listing must never include the key itself")
	}
	id := int(k["id"].(float64))
	if resp, _ := h.do(t, admin, "DELETE", "/api/v1/projects/library/keys/"+itoa(id), ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke: %d", resp.StatusCode)
	}
	if _, err := h.store.ResolveKey(ctx, "appsignal", key); !errors.Is(err, adapter.ErrUnknownKey) {
		t.Errorf("revoked key still resolves (cache not flushed): %v", err)
	}
	if resp, _ := h.do(t, admin, "POST", "/api/v1/projects/library/keys", `{"adapter":"bugsnag"}`); resp.StatusCode != http.StatusBadRequest {
		t.Error("unknown adapter accepted")
	}
}

func TestPasswordChangeEndsSessions(t *testing.T) {
	h := newHarness(t)
	first := h.login(t, "member@example.com", memberPassword)
	second := h.login(t, "member@example.com", memberPassword)
	if resp, body := h.do(t, first, "POST", "/api/v1/account/password", `{"current_password":"nope","new_password":"another-long-password"}`); resp.StatusCode != 403 {
		t.Errorf("wrong current password: %d %v", resp.StatusCode, body)
	}
	if resp, _ := h.do(t, first, "POST", "/api/v1/account/password", `{"current_password":"`+memberPassword+`","new_password":"short"}`); resp.StatusCode != 400 {
		t.Error("weak password accepted")
	}
	if resp, _ := h.do(t, first, "POST", "/api/v1/account/password", `{"current_password":"`+memberPassword+`","new_password":"another-long-password"}`); resp.StatusCode != 204 {
		t.Fatalf("change: %d", resp.StatusCode)
	}
	if resp, _ := h.do(t, second, "GET", "/api/v1/auth/me", ""); resp.StatusCode != 401 {
		t.Error("other sessions survived a password change")
	}
	h.login(t, "member@example.com", "another-long-password")
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
func itoa(n int) string     { b, _ := json.Marshal(n); return string(b) }
