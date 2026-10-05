package sentry

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"

	"github.com/olucurious/watchtower/internal/adapter"
	"github.com/olucurious/watchtower/internal/adapter/adaptertest"
	"github.com/olucurious/watchtower/internal/event"
)

// The fixtures are real envelopes captured from official SDKs (see
// testdata/README.md). The DSN key in them is a placeholder.
const fixtureKey = "0123456789abcdef0123456789abcdef"

var received = time.Date(2026, 10, 2, 14, 0, 0, 0, time.UTC)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name + ".envelope")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func eventsIn(t *testing.T, name string) []event.Event {
	t.Helper()
	env, err := ParseEnvelope(fixture(t, name))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var out []event.Event
	for _, it := range env.Items {
		if it.Type != "event" {
			continue
		}
		e, err := Normalize(it.Payload, 42, env.Header.EventID, received, func() string { return strings.Repeat("f", 32) })
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := e.Validate(); err != nil {
			t.Fatalf("%s: invalid event: %v", name, err)
		}
		out = append(out, e)
	}
	return out
}

func TestFixturesNormalize(t *testing.T) {
	t.Run("python exception chain", func(t *testing.T) {
		evs := eventsIn(t, "python-2.71.0-exception")
		if len(evs) != 1 {
			t.Fatalf("got %d events", len(evs))
		}
		e := evs[0]
		if e.ID != "345163604fe6438f807bd4b9ebbd503a" || e.Platform != "python" || e.Level != event.LevelError {
			t.Errorf("identity: %+v", e)
		}
		if got := e.Title(); got != "CatalogError: book could not be reserved" {
			t.Errorf("title %q", got)
		}
		if e.Exceptions[0].Type != "KeyError" || len(e.Exceptions) != 2 {
			t.Errorf("cause chain order: %+v", e.Exceptions)
		}
		if got := e.Culprit(); got != "__main__ in reserve_book" {
			t.Errorf("culprit %q", got)
		}
		if e.Release != "fixture-app@1.2.3" || e.Environment != "fixture" || e.Tags["service"] != "library" {
			t.Errorf("context: release=%q env=%q tags=%v", e.Release, e.Environment, e.Tags)
		}
		if e.Trace == nil || e.Trace.TraceID != "973e643617394f3482661b3e84f6d8fa" || e.User.ID != "user-1" {
			t.Errorf("trace/user: %+v %+v", e.Trace, e.User)
		}
		if !e.Timestamp.Equal(time.Date(2026, 10, 2, 13, 7, 5, 992710000, time.UTC)) {
			t.Errorf("timestamp %v", e.Timestamp)
		}
	})
	t.Run("python message with fingerprint", func(t *testing.T) {
		e := eventsIn(t, "python-2.71.0-message")[0]
		if e.Message != "search index out of date" || e.Level != event.LevelWarning {
			t.Errorf("%q %q", e.Message, e.Level)
		}
		if strings.Join(e.Fingerprint, ",") != "custom-group,{{ default }}" {
			t.Errorf("fingerprint %v", e.Fingerprint)
		}
	})
	t.Run("node chained cause and numeric timestamp", func(t *testing.T) {
		e := eventsIn(t, "node-11.2.0-exception")[0]
		if len(e.Exceptions) != 2 || e.Exceptions[0].Type != "TypeError" || e.Exceptions[1].Value != "reservation failed" {
			t.Errorf("chain: %+v", e.Exceptions)
		}
		if e.Timestamp.Unix() != 1790946426 {
			t.Errorf("timestamp %v", e.Timestamp)
		}
		last := e.Exceptions[0].Frames[len(e.Exceptions[0].Frames)-1]
		if last.Function != "loadMember" || !last.InApp || last.Lineno != 5 {
			t.Errorf("crashing frame %+v", last)
		}
	})
	t.Run("node message carries synthetic stack", func(t *testing.T) {
		e := eventsIn(t, "node-11.2.0-message")[0]
		if p := e.Primary(); p == nil || p.Mechanism == nil || !p.Mechanism.Synthetic {
			t.Fatalf("expected synthetic exception: %+v", e.Exceptions)
		}
		if e.Title() != "shelf capacity reached" {
			t.Errorf("title %q", e.Title())
		}
	})
	t.Run("node session has no events", func(t *testing.T) {
		if evs := eventsIn(t, "node-11.2.0-session"); len(evs) != 0 {
			t.Errorf("got %d events", len(evs))
		}
	})
	t.Run("elixir bare exception list", func(t *testing.T) {
		e := eventsIn(t, "elixir-11.0.4-exception")[0]
		if e.Primary() == nil || e.Primary().Type != "KeyError" || e.Platform != "elixir" {
			t.Fatalf("%+v", e.Exceptions)
		}
		if e.Culprit() != "Library.Catalog in fetch!/1" {
			t.Errorf("culprit %q", e.Culprit())
		}
	})
	t.Run("elixir erlang frames", func(t *testing.T) {
		frames := eventsIn(t, "elixir-11.0.4-exception")[0].Exceptions[0].Frames
		if f := frames[0]; f.Module != "elixir_compiler" || f.Function != "quoted/3" {
			t.Errorf("erlang frame %+v", f)
		}
	})
	t.Run("elixir message object and zoneless timestamp", func(t *testing.T) {
		e := eventsIn(t, "elixir-11.0.4-message")[0]
		if e.Message != "search index out of date" {
			t.Errorf("message %q", e.Message)
		}
		if e.Timestamp.Equal(received) || e.Timestamp.Location() != time.UTC {
			t.Errorf("timestamp not parsed: %v", e.Timestamp)
		}
	})
}

func TestParseEnvelopeRejectsBadLength(t *testing.T) {
	_, err := ParseEnvelope([]byte("{}\n{\"type\":\"event\",\"length\":999}\n{}\n"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseTimestampFallbacks(t *testing.T) {
	for _, raw := range []string{``, `"nonsense"`, `-5`, `9999999999999`} {
		if got := parseTimestamp(json.RawMessage(raw), received); !got.Equal(received) {
			t.Errorf("%s: got %v, want receive time", raw, got)
		}
	}
	if got := parseTimestamp(json.RawMessage(`"1790935690.5"`), received); got.Unix() != 1790935690 {
		t.Errorf("numeric string: %v", got)
	}
}

func TestTagsAsPairs(t *testing.T) {
	tags := parseTags(json.RawMessage(`[["a","1"],["b",2],["bad"]]`))
	if tags["a"] != "1" || tags["b"] != "2" || len(tags) != 2 {
		t.Errorf("%v", tags)
	}
}

func TestContextKeepsOnlyDiagnosticExtra(t *testing.T) {
	// The shapes sentry-elixir's logger handler and Oban integration send.
	ctx := parseContext(json.RawMessage(`{
		"logger_metadata": {"mfa": "DBConnection.Connection.connect/2", "application": "db_connection"},
		"logger_level": "error",
		"domain": ["otp"],
		"genserver_state": "%{token: \"private\"}",
		"last_message": "{:deliver, \"private\"}",
		"crash_reason": "private",
		"worker": "Paddy.Workers.Sync", "queue": "default", "attempt": 2, "max_attempts": 20,
		"args": {"message": "private"}, "meta": {"note": "private"}
	}`))
	want := map[string]string{
		"logger.mfa":         "DBConnection.Connection.connect/2",
		"logger.application": "db_connection",
		"logger.domain":      `["otp"]`,
		"job.worker":         "Paddy.Workers.Sync",
		"job.queue":          "default",
		"job.attempt":        "2",
		"job.max_attempts":   "20",
	}
	if !reflect.DeepEqual(ctx, want) {
		t.Errorf("context %v", ctx)
	}
	if parseContext(json.RawMessage(`{"genserver_state": "x"}`)) != nil {
		t.Error("extra without diagnostic keys must leave no context")
	}
}

// --- HTTP behaviour ---

type harness struct {
	srv     *httptest.Server
	sink    *adaptertest.Sink
	metrics *adaptertest.Metrics
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{sink: &adaptertest.Sink{}, metrics: &adaptertest.Metrics{}}
	mux := http.NewServeMux()
	st := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	st.Now = func() time.Time { return received }
	st.Register(mux, adapter.Deps{
		Keys:    adaptertest.Keys{"sentry/" + fixtureKey: {ID: 42, Slug: "fixture"}},
		Sink:    h.sink,
		Limits:  adaptertest.Limits,
		Metrics: h.metrics,
	})
	h.srv = httptest.NewServer(mux)
	t.Cleanup(h.srv.Close)
	return h
}

func (h *harness) post(t *testing.T, path string, body io.Reader, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.srv.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func authHeader(key string) map[string]string {
	return map[string]string{"X-Sentry-Auth": "Sentry sentry_key=" + key + ", sentry_version=7, sentry_client=sentry.python/2.71.0"}
}

func gz(b []byte) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write(b)
	w.Close()
	return buf.Bytes()
}

func TestEnvelopePythonGzipHeaderAuth(t *testing.T) {
	h := newHarness(t)
	hdr := authHeader(fixtureKey)
	hdr["Content-Encoding"] = "gzip"
	resp := h.post(t, "/api/42/envelope/", bytes.NewReader(gz(fixture(t, "python-2.71.0-exception"))), hdr)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	if body["id"] != "345163604fe6438f807bd4b9ebbd503a" || len(h.sink.Events) != 1 {
		t.Errorf("id %q, %d events", body["id"], len(h.sink.Events))
	}
}

func TestEnvelopeNodeChunkedQueryAuth(t *testing.T) {
	h := newHarness(t)
	// io.MultiReader hides the length, so the client sends chunked, like the Node SDK.
	body := io.MultiReader(bytes.NewReader(fixture(t, "node-11.2.0-exception")))
	resp := h.post(t, "/api/42/envelope/?sentry_key="+fixtureKey+"&sentry_version=7", body, nil)
	if resp.StatusCode != http.StatusOK || len(h.sink.Events) != 1 {
		t.Fatalf("status %d, %d events", resp.StatusCode, len(h.sink.Events))
	}
}

func TestEnvelopeOtherEncodings(t *testing.T) {
	raw := fixture(t, "elixir-11.0.4-exception")
	var br, zs bytes.Buffer
	bw := brotli.NewWriter(&br)
	bw.Write(raw)
	bw.Close()
	zw, _ := zstd.NewWriter(&zs)
	zw.Write(raw)
	zw.Close()
	for enc, body := range map[string][]byte{"br": br.Bytes(), "zstd": zs.Bytes(), "identity": raw} {
		h := newHarness(t)
		hdr := authHeader(fixtureKey)
		hdr["Content-Encoding"] = enc
		if resp := h.post(t, "/api/42/envelope/", bytes.NewReader(body), hdr); resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status %d", enc, resp.StatusCode)
		}
	}
}

func TestEnvelopeSessionOnlyIsAcknowledged(t *testing.T) {
	h := newHarness(t)
	resp := h.post(t, "/api/42/envelope/", bytes.NewReader(fixture(t, "node-11.2.0-session")), authHeader(fixtureKey))
	if resp.StatusCode != http.StatusOK || len(h.sink.Events) != 0 {
		t.Fatalf("status %d, %d events", resp.StatusCode, len(h.sink.Events))
	}
	if h.metrics.Get("sentry/ignored_session") != 1 {
		t.Error("session not counted")
	}
}

func TestStoreEndpoint(t *testing.T) {
	h := newHarness(t)
	env, _ := ParseEnvelope(fixture(t, "python-2.71.0-message"))
	resp := h.post(t, "/api/42/store/", bytes.NewReader(env.Items[0].Payload), authHeader(fixtureKey))
	if resp.StatusCode != http.StatusOK || len(h.sink.Events) != 1 {
		t.Fatalf("status %d, %d events", resp.StatusCode, len(h.sink.Events))
	}
}

func TestAuthFailures(t *testing.T) {
	h := newHarness(t)
	body := fixture(t, "python-2.71.0-message")
	cases := []struct {
		name, path string
		hdr        map[string]string
		want       int
	}{
		{"no key", "/api/42/envelope/", nil, http.StatusUnauthorized},
		{"unknown key", "/api/42/envelope/", authHeader(strings.Repeat("a", 32)), http.StatusUnauthorized},
		{"other project", "/api/7/envelope/", authHeader(fixtureKey), http.StatusForbidden},
	}
	for _, c := range cases {
		if resp := h.post(t, c.path, bytes.NewReader(body), c.hdr); resp.StatusCode != c.want {
			t.Errorf("%s: status %d, want %d", c.name, resp.StatusCode, c.want)
		}
	}
	if len(h.sink.Events) != 0 {
		t.Error("rejected requests stored events")
	}
}

func TestOverloadTellsSDKToBackOff(t *testing.T) {
	h := newHarness(t)
	h.sink.Err = adapter.ErrOverloaded
	resp := h.post(t, "/api/42/envelope/", bytes.NewReader(fixture(t, "python-2.71.0-message")), authHeader(fixtureKey))
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") != "60" || resp.Header.Get("X-Sentry-Rate-Limits") != "60::organization" {
		t.Errorf("headers %v", resp.Header)
	}
}

func TestSizeLimits(t *testing.T) {
	h := newHarness(t)
	big := bytes.Repeat([]byte("a"), int(adaptertest.Limits.MaxBodyBytes)+10)
	if resp := h.post(t, "/api/42/envelope/", bytes.NewReader(big), authHeader(fixtureKey)); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("wire limit: status %d", resp.StatusCode)
	}
	// Small on the wire, huge once decompressed.
	bomb := gz(bytes.Repeat([]byte{'\n'}, int(adaptertest.Limits.MaxDecompressedBytes)+10))
	hdr := authHeader(fixtureKey)
	hdr["Content-Encoding"] = "gzip"
	if resp := h.post(t, "/api/42/envelope/", bytes.NewReader(bomb), hdr); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("decompression limit: status %d", resp.StatusCode)
	}
}

func TestOversizedEnvelopeEventRejectedAtomically(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		h := newHarness(t)
		body := "{}\n"
		if mixed {
			body += "{\"type\":\"event\"}\n{\"message\":\"valid\"}\n"
		}
		payload := `{"message":"` + strings.Repeat("x", int(adaptertest.Limits.MaxEventBytes)) + `"}`
		body += "{\"type\":\"event\"}\n" + payload + "\n"
		resp := h.post(t, "/api/42/envelope/", strings.NewReader(body), authHeader(fixtureKey))
		if resp.StatusCode != http.StatusRequestEntityTooLarge || len(h.sink.Events) != 0 {
			t.Fatalf("mixed=%v status=%d stored=%d", mixed, resp.StatusCode, len(h.sink.Events))
		}
	}
}

func TestMalformedEnvelopeEventRejectedAtomically(t *testing.T) {
	for _, payload := range []string{`{"exception":"invalid"}`, `{}`, `{"message":"\u0000"}`} {
		for _, mixed := range []bool{false, true} {
			h := newHarness(t)
			body := "{}\n"
			if mixed {
				body += "{\"type\":\"event\"}\n{\"message\":\"valid\"}\n"
			}
			body += "{\"type\":\"event\"}\n" + payload + "\n"
			resp := h.post(t, "/api/42/envelope/", strings.NewReader(body), authHeader(fixtureKey))
			if resp.StatusCode != http.StatusBadRequest || len(h.sink.Events) != 0 {
				t.Fatalf("mixed=%v status=%d stored=%d", mixed, resp.StatusCode, len(h.sink.Events))
			}
		}
	}
}
