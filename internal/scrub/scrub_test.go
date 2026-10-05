package scrub

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/olucurious/watchtower/internal/event"
)

func TestText(t *testing.T) {
	cases := map[string]string{
		"got 4111 1111 1111 1111 in a form field":        "got [Filtered:card] in a form field",
		"got 4111-1111-1111-1111 in a form field":        "got [Filtered:card] in a form field",
		"build 1234567890123 failed":                     "build 1234567890123 failed", // not Luhn-valid
		"Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.abc": "Authorization: [Filtered]",
		"retrying with Bearer eyJhbGciOiJIUzI1NiJ9.abc":  "retrying with Bearer [Filtered]",
		"login failed password=hunter22 user=ada":        "login failed password=[Filtered] user=ada",
		`config {"api_key": "sk_live_123"}`:              `config {"api_key": "[Filtered]"}`,
		"nothing to see here":                            "nothing to see here",
	}
	for in, want := range cases {
		if got := Text(in); got != want {
			t.Errorf("Text(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestURL(t *testing.T) {
	got := URL("https://ada:pw@api.example.com/search?page=10&token=abc&card=4111111111111111")
	for _, leaked := range []string{"pw", "abc", "4111111111111111"} {
		if strings.Contains(got, leaked) {
			t.Errorf("%q leaked in %q", leaked, got)
		}
	}
	if !strings.Contains(got, "page=10") {
		t.Errorf("non-sensitive parameter lost: %q", got)
	}
}

func TestEventTags(t *testing.T) {
	e := &event.Event{Tags: map[string]string{"span_id": "abc123", "session_token": "s3cr3t", "company": "acme", "pin": "1234"}}
	Event(e)
	if e.Tags["span_id"] != "abc123" || e.Tags["company"] != "acme" {
		t.Errorf("benign tags filtered: %v", e.Tags)
	}
	if e.Tags["session_token"] != filtered || e.Tags["pin"] != filtered {
		t.Errorf("sensitive tags kept: %v", e.Tags)
	}
}

func TestRetainedTextAndLocations(t *testing.T) {
	secret := "password=supersecret"
	location := "https://example.com/app.js?access_token=supersecret"
	e := event.Event{Platform: secret, Message: secret, Logger: secret, Transaction: secret, Release: secret, Environment: secret, ServerName: secret,
		Tags: map[string]string{secret: secret}, SDK: event.SDK{Name: secret, Version: secret}, Fingerprint: []string{secret}, User: &event.User{ID: secret}, Trace: &event.Trace{TraceID: secret, SpanID: secret},
		DebugImages: []event.DebugImage{{Type: secret, DebugID: secret, CodeFile: location}}, Request: &event.Request{Method: secret, URL: location},
		Exceptions: []event.Exception{{Type: secret, Value: secret, Module: secret, Mechanism: &event.Mechanism{Type: secret}, Frames: []event.Frame{{Function: secret, Module: secret, Filename: location, AbsPath: location, ContextLine: secret, PreContext: []string{secret}, PostContext: []string{secret}, Minified: &event.MinifiedLocation{Filename: location, Function: secret}}}}}}
	Event(&e)
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "supersecret") {
		t.Fatalf("secret retained: %s", raw)
	}
	e.Message = "pass\x00word=supersecret"
	e.Exceptions[0].Frames[0].Minified.Filename = "file\x00.js"
	Event(&e)
	if strings.Contains(e.Message, "supersecret") || strings.ContainsRune(e.Exceptions[0].Frames[0].Minified.Filename, 0) {
		t.Fatal("NUL bypassed sanitization")
	}
}

func TestContextScrubbed(t *testing.T) {
	e := event.Event{Context: map[string]string{"logger.session_id": "abc", "logger.mfa": "password=hunter22"}}
	Event(&e)
	if e.Context["logger.session_id"] != filtered || e.Context["logger.mfa"] != "password="+filtered {
		t.Errorf("%v", e.Context)
	}
}
