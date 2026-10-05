package alert

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/olucurious/watchtower/internal/store"
)

const testKey = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

func TestSealer(t *testing.T) {
	s, err := NewSealer(testKey)
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := s.Seal("https://hooks.slack.com/services/T/B/secret")
	if strings.Contains(string(sealed), "secret") {
		t.Fatal("plaintext visible in sealed value")
	}
	if got, err := s.Open(sealed); err != nil || got != "https://hooks.slack.com/services/T/B/secret" {
		t.Fatalf("round trip: %q %v", got, err)
	}
	other, _ := NewSealer(strings.Repeat("ab", 32))
	if _, err := other.Open(sealed); err == nil {
		t.Error("a different key must not open the value")
	}
	var none *Sealer
	if _, err := none.Seal("x"); !errors.Is(err, ErrNoSecretKey) {
		t.Errorf("unset key: %v", err)
	}
	if _, err := NewSealer("too-short"); err == nil {
		t.Error("short key accepted")
	}
}

func TestValidateWebhook(t *testing.T) {
	s := NewSlack("https://errors.example.com", nil)
	hint, err := s.ValidateWebhook("https://hooks.slack.com/services/T000/B000/abcdWXYZ")
	if err != nil || hint != "hooks.slack.com/…WXYZ" {
		t.Errorf("valid webhook: %q %v", hint, err)
	}
	for _, bad := range []string{"http://hooks.slack.com/services/x", "https://169.254.169.254/latest", "https://hooks.slack.com.evil.io/x", "not a url"} {
		if _, err := s.ValidateWebhook(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func issueNotification(kind string) *store.Notification {
	n := &store.Notification{Kind: kind, Detail: map[string]any{"environment": "production", "release": "library@1.4.0", "count": float64(120)}}
	n.Project.Name = "Library <core>"
	n.Issue = &store.IssueRow{ID: 42, Title: "KeyError: key :shelf not found", Culprit: "Library.Catalog in fetch!/1", Level: "error", TimesSeen: 7}
	return n
}

func TestMessages(t *testing.T) {
	s := NewSlack("https://errors.example.com/", nil)
	for kind, want := range map[string]string{
		"new_issue":  "New issue in Library <core>: KeyError",
		"regression": "Regression in Library <core>: KeyError",
		"frequency":  "120+ events in the last hour in Library <core>: KeyError",
	} {
		msg := s.Message(issueNotification(kind))
		if !strings.HasPrefix(msg["text"].(string), want) {
			t.Errorf("%s fallback text %q", kind, msg["text"])
		}
		b, _ := json.Marshal(msg)
		if !strings.Contains(string(b), "https://errors.example.com/issues/42|") {
			t.Errorf("%s: missing issue link: %s", kind, b)
		}
		if strings.Contains(string(b), "Library <core>") && strings.Contains(string(b), `"type":"section"`) && !strings.Contains(string(b), "Library &lt;core&gt;") {
			t.Errorf("%s: mrkdwn not escaped", kind)
		}
	}
	if !strings.Contains(mustJSON(s.Message(issueNotification("regression"))), "came back in `library@1.4.0`") {
		t.Error("regression message should name the release")
	}
	n := issueNotification("regression")
	n.Detail["event_id"] = "4C2B33B7-2AE8-4760-9F79-6AA65E96BD9D"
	n.Detail["release"] = "b240901873c07c4dd3e0c6b8986259d12ec770a6"
	if b := mustJSON(s.Message(n)); !strings.Contains(b, "https://errors.example.com/issues/42/events/4c2b33b72ae847609f796aa65e96bd9d|") || !strings.Contains(b, "came back in `b240901`") {
		t.Errorf("alerts should open the triggering event and abbreviate SHAs: %s", b)
	}
	test := &store.Notification{Kind: "test"}
	test.Project.Name, test.Channel.Name = "Library", "#alerts"
	if !strings.Contains(mustJSON(s.Message(test)), "Test alert") {
		t.Error("test message")
	}
}

// slackServer stands in for Slack, behind TLS like the real thing.
func slackServer(t *testing.T, status int, retryAfter string) (*Slack, string, *[]map[string]any) {
	var got []map[string]any
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		got = append(got, body)
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		w.WriteHeader(status)
		io.WriteString(w, "invalid_token")
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	s := NewSlack("https://errors.example.com", []string{u.Hostname()})
	s.Client = srv.Client()
	return s, srv.URL + "/services/T/B/very-secret-token", &got
}

func TestSend(t *testing.T) {
	s, hook, got := slackServer(t, http.StatusOK, "")
	if _, err := s.Send(context.Background(), hook, issueNotification("new_issue")); err != nil || len(*got) != 1 {
		t.Fatalf("send: %v (%d requests)", err, len(*got))
	}

	s, hook, _ = slackServer(t, http.StatusTooManyRequests, "30")
	after, err := s.Send(context.Background(), hook, issueNotification("new_issue"))
	if err == nil || after != 30*time.Second {
		t.Errorf("429: after=%v err=%v", after, err)
	}

	s, hook, _ = slackServer(t, http.StatusForbidden, "")
	_, err = s.Send(context.Background(), hook, issueNotification("new_issue"))
	if err == nil || strings.Contains(err.Error(), "very-secret-token") {
		t.Errorf("errors must not include the webhook URL: %v", err)
	}
	// Unreachable host: the url.Error would normally quote the URL.
	s.Client.Timeout = time.Second
	_, err = s.Send(context.Background(), strings.Replace(hook, "https://", "https://127.0.0.1:1@", 1), issueNotification("new_issue"))
	if err != nil && strings.Contains(err.Error(), "very-secret-token") {
		t.Errorf("transport error leaked the URL: %v", err)
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

// botServer stands in for Slack's Web API.
func botServer(t *testing.T, reply string, status int) (*Slack, *[]map[string]any, *[]string) {
	var bodies []map[string]any
	var auths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat.postMessage" {
			t.Errorf("path %s", r.URL.Path)
		}
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		auths = append(auths, r.Header.Get("Authorization"))
		if status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "7")
		}
		w.WriteHeader(status)
		io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	s := NewSlack("https://errors.example.com", nil)
	s.BotAPI = srv.URL
	return s, &bodies, &auths
}

const testBotToken = "xoxb-1111-2222-secretbottoken"

func TestSendBot(t *testing.T) {
	s, bodies, auths := botServer(t, `{"ok":true}`, 200)
	if _, err := s.SendBot(context.Background(), testBotToken, "C0123ABCD", issueNotification("new_issue")); err != nil {
		t.Fatal(err)
	}
	b := (*bodies)[0]
	if b["channel"] != "C0123ABCD" || !strings.HasPrefix(b["text"].(string), "New issue in Library <core>") || (*auths)[0] != "Bearer "+testBotToken {
		t.Errorf("request %v auth %q", b, (*auths)[0])
	}

	s, _, _ = botServer(t, `{"ok":false,"error":"not_in_channel"}`, 200)
	_, err := s.SendBot(context.Background(), testBotToken, "C0123ABCD", issueNotification("new_issue"))
	if err == nil || !strings.Contains(err.Error(), "not_in_channel") || strings.Contains(err.Error(), "secretbottoken") {
		t.Errorf("slack error must be reported without the token: %v", err)
	}

	s, _, _ = botServer(t, `{"ok":false,"error":"ratelimited"}`, http.StatusTooManyRequests)
	if after, err := s.SendBot(context.Background(), testBotToken, "C0123ABCD", issueNotification("new_issue")); err == nil || after != 7*time.Second {
		t.Errorf("429: after=%v err=%v", after, err)
	}
}

func TestValidateBot(t *testing.T) {
	if hint, err := ValidateBot(testBotToken, "C0123ABCD"); err != nil || hint != "xoxb-…oken" {
		t.Errorf("valid: %q %v", hint, err)
	}
	for _, c := range []struct{ token, channel string }{
		{"xoxp-user-token-not-a-bot-0000", "C0123ABCD"},
		{testBotToken, "#errors"},
		{testBotToken, ""},
		{"xoxb-short", "C0123ABCD"},
	} {
		if _, err := ValidateBot(c.token, c.channel); err == nil {
			t.Errorf("%q/%q accepted", c.token, c.channel)
		}
	}
}
