package appsignal

import (
	"bytes"
	"compress/zlib"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/olucurious/watchtower/internal/adapter"
	"github.com/olucurious/watchtower/internal/adapter/adaptertest"
)

// A batch from appsignal-elixir 2.17.4 (agent 0.36.12) running the synthetic
// harness (see testdata/README.md). The embedded push key is all zeros.
const fixtureFile = "testdata/matrix/elixir-2.17.4.deflate"

var received = time.Date(2026, 10, 2, 11, 0, 0, 0, time.UTC)

func fixtureBody(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(fixtureFile)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func decodedFixture(t *testing.T) []byte {
	t.Helper()
	r, err := zlib.NewReader(bytes.NewReader(fixtureBody(t)))
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDecodeCollectFixture(t *testing.T) {
	batch, err := DecodeCollect(decodedFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if batch.AppName != "library" || batch.Environment != "prod" ||
		batch.Hostname != "matrix-host" || batch.Agent != "0.36.12" || batch.Language != "elixir-2.17.4" {
		t.Errorf("batch metadata: %+v", batch)
	}
	// The agent repeats each error record within a span; 30 distinct errors remain.
	if len(batch.Errors) != 30 {
		t.Fatalf("got %d error samples, want 30", len(batch.Errors))
	}
	var es *ErrorSample
	for i := range batch.Errors {
		if batch.Errors[i].Name == "ArgumentError" {
			es = &batch.Errors[i]
		}
	}
	if es == nil || es.Revision != "library@synthetic-2.17.4" || es.Namespace != "background_job" || es.Action != "library.reserve" {
		t.Fatalf("error sample: %+v", es)
	}

	e := ToEvent(batch, *es, 9, received)
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := e.Title(); got != "ArgumentError: cannot reserve 5 copies; the limit is 3" {
		t.Errorf("title %q", got)
	}
	if e.Release != "library@synthetic-2.17.4" || e.Platform != "elixir" || e.SDK.Version != "2.17.4" || e.Transaction != "library.reserve" {
		t.Errorf("context: %+v", e)
	}
	frames := e.Exceptions[0].Frames
	crash := frames[len(frames)-1]
	if crash.Module != "Library.Catalog" || crash.Function != "reserve!/1" ||
		crash.Filename != "harness.exs" || crash.Lineno == 0 || !crash.InApp {
		t.Errorf("crashing frame %+v", crash)
	}
	for _, f := range frames {
		if strings.HasPrefix(f.Filename, "src/elixir") && f.InApp {
			t.Errorf("elixir stdlib frame marked in-app: %+v", f)
		}
	}
	if again := ToEvent(batch, *es, 9, received.Add(time.Minute)); again.ID != e.ID {
		t.Error("event ID must be stable across agent retries")
	}
}

func TestParseElixirLine(t *testing.T) {
	cases := map[string]struct {
		module, function, file string
		line                   int
		inApp                  bool
	}{
		"(library 0.4.1) lib/library/catalog.ex:42: Library.Catalog.fetch!/1":        {"Library.Catalog", "fetch!/1", "lib/library/catalog.ex", 42, true},
		"(elixir 1.19.5) lib/map.ex:300: Map.fetch!/2":                               {"Map", "fetch!/2", "lib/map.ex", 300, false},
		"(stdlib 6.0) gen_server.erl:1123: :gen_server.try_dispatch/4":               {":gen_server", "try_dispatch/4", "gen_server.erl", 1123, false},
		"(mix 1.18.4) lib/mix/utils.ex:576: anonymous fn/2 in Mix.Utils.read_path/2": {"Mix.Utils", "anonymous fn/2 in Mix.Utils.read_path/2", "lib/mix/utils.ex", 576, false},
		"probe.exs:98: (file)": {"", "(file)", "probe.exs", 98, true},
	}
	for line, want := range cases {
		f := parseElixirLine(line)
		if f.Module != want.module || f.Function != want.function || f.Filename != want.file || f.Lineno != want.line || f.InApp != want.inApp {
			t.Errorf("%s\n got %+v", line, f)
		}
	}
	if f := parseElixirLine("something unexpected"); f.Function != "something unexpected" {
		t.Errorf("unparseable line not preserved: %+v", f)
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	if _, err := DecodeCollect([]byte{0x62, 0xff}); err == nil { // field 12, truncated length
		t.Error("expected error")
	}
}

func newServer(t *testing.T, logs io.Writer) (*httptest.Server, *adaptertest.Sink) {
	t.Helper()
	sink := &adaptertest.Sink{}
	st := New(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	st.Now = func() time.Time { return received }
	mux := http.NewServeMux()
	st.Register(mux, adapter.Deps{
		Keys:    adaptertest.Keys{"appsignal/good-key": {ID: 9, Slug: "library"}},
		Sink:    sink,
		Limits:  adaptertest.Limits,
		Metrics: &adaptertest.Metrics{},
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, sink
}

func TestAuthEndpoint(t *testing.T) {
	srv, _ := newServer(t, io.Discard)
	for key, want := range map[string]int{"good-key": 200, "bad-key": 401} {
		resp, err := http.Get(srv.URL + "/1/auth?api_key=" + key + "&name=library&environment=prod")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s: status %d, want %d", key, resp.StatusCode, want)
		}
	}
}

func TestCollectEndpoint(t *testing.T) {
	var logs bytes.Buffer
	srv, sink := newServer(t, &logs)
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2/collect?api_key=good-key&app_name=library&environment=prod&hostname=h", bytes.NewReader(fixtureBody(t)))
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Content-Encoding", "deflate")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(sink.Events) != 30 || sink.Events[0].ProjectID != 9 {
		t.Fatalf("status %d, events %+v", resp.StatusCode, sink.Events)
	}

	// A malformed body is rejected without its bytes reaching the logs.
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/2/collect?api_key=good-key",
		bytes.NewReader([]byte{0x12, 0x05, 's', 'e', 'c', 'r', 'e', 0x62, 0xff}))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("garbage: status %d", resp.StatusCode)
	}
	if strings.Contains(logs.String(), "secre") || strings.Contains(logs.String(), "good-key") {
		t.Error("credential material reached the logs")
	}
}
