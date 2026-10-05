package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/olucurious/watchtower/internal/store/storetest"
)

func TestSetupCreatesFirstAdminAndSignsIn(t *testing.T) {
	ctx := context.Background()
	s := storetest.New(t)
	mux := http.NewServeMux()
	(&API{Store: s, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), PublicURL: "https://errors.example.com", Version: "test"}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	h := &harness{srv: srv, store: s}
	code, err := s.NewSetupCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := h.client(t)
	setup := func(code, password string) *http.Response {
		resp, _ := h.do(t, c, "POST", "/api/v1/setup", `{"code":"`+code+`","name":"Ada","email":"ada@example.com","password":"`+password+`"}`)
		return resp
	}
	if resp := setup("WRONG-CODE-XXXX", "a-long-password"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong code: %d", resp.StatusCode)
	}
	if resp := setup(code, "short"); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("weak password: %d", resp.StatusCode)
	}
	if resp := setup(code, "a-long-password"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d", resp.StatusCode)
	}
	// Signed in straight away, as an administrator.
	if resp, me := h.do(t, c, "GET", "/api/v1/auth/me", ""); resp.StatusCode != 200 || me["is_admin"] != true {
		t.Fatalf("me after setup: %d %v", resp.StatusCode, me)
	}
	if resp, meta := h.do(t, h.client(t), "GET", "/api/v1/meta", ""); resp.StatusCode != 200 || meta["has_users"] != true {
		t.Fatalf("meta after setup: %v", meta)
	}
	if resp := setup(code, "a-long-password"); resp.StatusCode != http.StatusConflict {
		t.Fatalf("second setup: %d", resp.StatusCode)
	}
	// Cross-site requests are refused like every other mutation.
	resp, _ := h.do(t, h.client(t), "POST", "/api/v1/setup", `{"code":"x","email":"e@example.com","password":"a-long-password"}`, "Origin", "https://evil.example")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin setup: %d", resp.StatusCode)
	}
}
