package appsignal

import (
	"bytes"
	"compress/zlib"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/olucurious/watchtower/internal/adapter"
	"github.com/olucurious/watchtower/internal/adapter/adaptertest"
	"github.com/olucurious/watchtower/internal/event"
)

// The fixtures in testdata/sdks come from the harness next to each one,
// run against the unmodified Ruby, Node.js and Python integrations and the
// browser SDK (see testdata/README.md).

func sdkEvents(t *testing.T, file string) (*Batch, map[string]event.Event, int) {
	t.Helper()
	raw, err := os.ReadFile("testdata/sdks/" + file)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := DecodeCollect(inflate(t, raw))
	if err != nil {
		t.Fatal(err)
	}
	byTitle := map[string]event.Event{}
	burst := 0
	for _, es := range batch.Errors {
		e := ToEvent(batch, es, 1, received)
		if err := e.Validate(); err != nil {
			t.Fatalf("invalid event: %v", err)
		}
		if strings.Contains(e.Title(), "deep failure ") {
			burst++
			continue
		}
		byTitle[e.Title()] = e
	}
	all, _ := json.Marshal(batch.Errors)
	for _, secret := range []string{"hunter22", "tok_live_123"} {
		if strings.Contains(string(all), secret) {
			t.Errorf("request params leaked into decoded samples: %q", secret)
		}
	}
	return batch, byTitle, burst
}

func mustEvent(t *testing.T, byTitle map[string]event.Event, title string) event.Event {
	t.Helper()
	e, ok := byTitle[title]
	if !ok {
		keys := make([]string, 0, len(byTitle))
		for k := range byTitle {
			keys = append(keys, k)
		}
		t.Fatalf("no event %q; have %q", title, keys)
	}
	return e
}

func crashFrame(e event.Event) event.Frame {
	frames := e.Primary().Frames
	return frames[len(frames)-1]
}

func TestRubyIntegration(t *testing.T) {
	batch, events, burst := sdkEvents(t, "ruby-5.0.1.deflate")
	if batch.Language != "ruby-5.0.1" || len(batch.Errors) != 29 || burst != 25 {
		t.Fatalf("language %q, %d errors, burst %d", batch.Language, len(batch.Errors), burst)
	}
	e := mustEvent(t, events, `KeyError: key not found: "missing"`)
	if e.Platform != "ruby" || e.SDK.Version != "5.0.1" || e.Tags["region"] != "eu" || e.Tags["namespace"] != "background" {
		t.Errorf("send_error context: %+v", e)
	}
	if f := crashFrame(e); f.Filename != "harness.rb" || f.Function != "fetch" || f.Lineno != 6 || !f.InApp {
		t.Errorf("crash frame %+v", f)
	}
	monitored := mustEvent(t, events, "ArgumentError: cannot reserve 5 copies; the limit is 3")
	if monitored.Transaction != "library.reserve" || monitored.Tags["namespace"] != "background_job" {
		t.Errorf("monitor: transaction %q tags %v", monitored.Transaction, monitored.Tags)
	}
	for _, f := range monitored.Primary().Frames {
		if strings.Contains(f.Filename, "/gems/") && f.InApp {
			t.Errorf("gem frame marked in-app: %+v", f)
		}
	}
	// Ruby sends exception causes; they become the start of the chain.
	caused := mustEvent(t, events, "CatalogError: book could not be reserved")
	if len(caused.Exceptions) != 2 || caused.Exceptions[0].Type != "KeyError" || caused.Exceptions[0].Frames[0].Lineno != 6 {
		t.Errorf("cause chain %+v", caused.Exceptions)
	}
}

func TestNodeIntegration(t *testing.T) {
	batch, events, burst := sdkEvents(t, "nodejs-3.9.1.deflate")
	if batch.Language != "nodejs-3.9.1" || len(batch.Errors) != 28 || burst != 25 {
		t.Fatalf("language %q, %d errors, burst %d", batch.Language, len(batch.Errors), burst)
	}
	e := mustEvent(t, events, "Error: key not found: missing")
	if e.Platform != "node" || e.Transaction != "" || e.Tags["region"] != "eu" || e.Tags["request_id"] != "req-123" {
		t.Errorf("sendError context: platform %q transaction %q tags %v", e.Platform, e.Transaction, e.Tags)
	}
	if f := crashFrame(e); f.Function != "Object.fetch" || f.Filename != "/w/harness.js" || f.Lineno != 11 || f.Colno == 0 || !f.InApp {
		t.Errorf("crash frame %+v", f)
	}
	for _, f := range e.Primary().Frames {
		if strings.HasPrefix(f.Filename, "node:") && f.InApp {
			t.Errorf("node internal frame marked in-app: %+v", f)
		}
	}
	if e.Trace == nil || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(e.Trace.TraceID) {
		t.Errorf("Node.js trace IDs are W3C ids: %+v", e.Trace)
	}
	span := mustEvent(t, events, "RangeError: cannot reserve 5 copies; the limit is 3")
	if span.Transaction != "library.reserve" || span.Tags["namespace"] != "background_job" {
		t.Errorf("span error: transaction %q tags %v", span.Transaction, span.Tags)
	}
}

func TestPythonIntegration(t *testing.T) {
	batch, events, burst := sdkEvents(t, "python-1.9.0.deflate")
	if batch.Language != "python" || len(batch.Errors) != 28 || burst != 25 {
		t.Fatalf("language %q, %d errors, burst %d", batch.Language, len(batch.Errors), burst)
	}
	e := mustEvent(t, events, "KeyError: 'missing'")
	if e.Platform != "python" || e.Tags["region"] != "eu" || e.Tags["namespace"] != "background" {
		t.Errorf("set_error context: %+v", e)
	}
	// Python sends one traceback line per entry, oldest call first.
	if f := crashFrame(e); f.Filename != "/w/harness.py" || f.Function != "fetch" || f.Lineno != 15 || !f.InApp {
		t.Errorf("crash frame %+v", f)
	}
	caught := mustEvent(t, events, "CatalogError: book could not be reserved")
	if caught.Primary().Module != "__main__" || caught.Transaction != "" {
		t.Errorf("qualified class name: module %q transaction %q", caught.Primary().Module, caught.Transaction)
	}
	span := mustEvent(t, events, "ValueError: cannot reserve 5 copies; the limit is 3")
	if span.Transaction != "library.reserve" || span.Tags["namespace"] != "background_job" {
		t.Errorf("span error: transaction %q tags %v", span.Transaction, span.Tags)
	}
}

func TestBacktraceParsers(t *testing.T) {
	cases := []struct {
		lang, line, file, fn, module string
		lineno                       int
		inApp                        bool
	}{
		{"ruby", "app/models/book.rb:12:in `reserve'", "app/models/book.rb", "reserve", "", 12, true},
		{"ruby", "app/models/book.rb:12:in 'Library::Catalog#reserve'", "app/models/book.rb", "reserve", "Library::Catalog", 12, true},
		{"ruby", "/usr/local/bundle/gems/rack-3.1.0/lib/rack/builder.rb:9:in `call'", "/usr/local/bundle/gems/rack-3.1.0/lib/rack/builder.rb", "call", "", 9, false},
		{"nodejs", "at Object.fetch (/srv/app/catalog.js:11:67)", "/srv/app/catalog.js", "Object.fetch", "", 11, true},
		{"nodejs", "at /srv/app/catalog.js:39:66", "/srv/app/catalog.js", "?", "", 39, true},
		{"nodejs", "at async Promise.all (/srv/app/node_modules/pg/lib/client.js:5:2)", "/srv/app/node_modules/pg/lib/client.js", "Promise.all", "", 5, false},
		{"javascript", "reserve@https://shop.example.com/app.js:983:13", "https://shop.example.com/app.js", "reserve", "", 983, true},
		{"python", `File "/srv/app/catalog.py", line 15, in fetch`, "/srv/app/catalog.py", "fetch", "", 15, true},
		{"python", `File "/usr/lib/python3.12/site-packages/django/core/handlers.py", line 3, in inner`, "/usr/lib/python3.12/site-packages/django/core/handlers.py", "inner", "", 3, false},
	}
	for _, c := range cases {
		frames := parseBacktrace(c.lang, []string{c.line})
		if len(frames) != 1 {
			t.Errorf("%s: %d frames", c.line, len(frames))
			continue
		}
		f := frames[0]
		if f.Filename != c.file || f.Function != c.fn || f.Module != c.module || f.Lineno != c.lineno || f.InApp != c.inApp {
			t.Errorf("%s %s\n got %+v", c.lang, c.line, f)
		}
	}
	if frames := parseBacktrace("nodejs", []string{"TypeError: member id missing", "at reserve (/a.js:1:2)"}); len(frames) != 1 {
		t.Errorf("V8 header line not skipped: %+v", frames)
	}
}

func frontendServer(t *testing.T) (*httptest.Server, *adaptertest.Sink) {
	t.Helper()
	sink := &adaptertest.Sink{}
	f := NewFrontend(slog.New(slog.NewTextHandler(io.Discard, nil)))
	f.Now = func() time.Time { return time.Date(2026, 10, 2, 14, 0, 0, 0, time.UTC) }
	mux := http.NewServeMux()
	f.Register(mux, adapter.Deps{
		Keys:    adaptertest.Keys{FrontendName + "/browser-key": {ID: 4, Slug: "web"}},
		Sink:    sink,
		Metrics: &adaptertest.Metrics{},
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, sink
}

func TestFrontendSDK(t *testing.T) {
	srv, sink := frontendServer(t)
	post := func(key, file string) *http.Response {
		body, err := os.ReadFile("testdata/sdks/" + file)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.Post(srv.URL+"/collect?api_key="+key+"&version=1.6.1", "text/plain;charset=UTF-8", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	if resp := post("browser-key", "frontend-1.6.1-with-metadata.json"); resp.StatusCode != 200 || resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("status %d, CORS %q", resp.StatusCode, resp.Header.Get("Access-Control-Allow-Origin"))
	}
	post("browser-key", "frontend-1.6.1-plain.json")
	if len(sink.Events) != 2 {
		t.Fatalf("%d events", len(sink.Events))
	}
	e := sink.Events[0]
	if e.Title() != "Error: reservation failed" || e.Platform != "javascript" || e.Transaction != "BookPage#reserve" ||
		e.Release != "library@synthetic-frontend" || e.Tags["region"] != "eu" || e.Tags["namespace"] != "frontend" || e.SDK.Version != "1.6.1" {
		t.Errorf("event %+v", e)
	}
	if f := crashFrame(e); f.Function != "reserve" || !strings.HasSuffix(f.Filename, "/app.js") || f.Lineno == 0 || f.Colno == 0 {
		t.Errorf("crash frame %+v", f)
	}
	if b, _ := json.Marshal(e); strings.Contains(string(b), "hunter22") {
		t.Error("params stored")
	}
	if resp := post("push-key-of-a-server", "frontend-1.6.1-plain.json"); resp.StatusCode != 401 {
		t.Errorf("unknown key: %d", resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodOptions, srv.URL+"/collect", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Errorf("preflight: %v %v", resp, err)
	}
}

func inflate(t *testing.T, raw []byte) []byte {
	t.Helper()
	zr, err := zlib.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
