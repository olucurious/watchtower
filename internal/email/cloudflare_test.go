package email

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strings"
	"testing"
)

func TestCloudflareAPI(t *testing.T) {
	var got map[string]any
	var auth, path string
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, path = r.Header.Get("Authorization"), r.URL.Path
		json.NewDecoder(r.Body).Decode(&got)
		if fail {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`))
			return
		}
		w.Write([]byte(`{"success":true,"errors":[],"result":{}}`))
	}))
	defer srv.Close()
	c := &CloudflareAPI{AccountID: "0123456789abcdef0123456789abcdef", Token: "tok", From: mail.Address{Name: "Watchtower", Address: "alerts@example.com"}, BaseURL: srv.URL}
	m := Message{To: mail.Address{Name: "Ada", Address: "ada@example.com"}, Subject: "Hi\r\nBcc: x@example.com", Text: "t", HTML: "<p>h</p>"}
	if err := c.Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	from := got["from"].(map[string]any)
	if auth != "Bearer tok" || path != "/accounts/0123456789abcdef0123456789abcdef/email/sending/send" ||
		from["address"] != "alerts@example.com" || got["to"].([]any)[0] != "ada@example.com" || got["subject"] != "Hi  Bcc: x@example.com" ||
		got["text"] != "t" || got["html"] != "<p>h</p>" {
		t.Errorf("request %s %s %v", auth, path, got)
	}
	fail = true
	err := c.Send(context.Background(), m)
	if err == nil || !strings.Contains(err.Error(), "403: 10000 Authentication error") || strings.Contains(err.Error(), "tok") {
		t.Errorf("error %v", err)
	}
}
