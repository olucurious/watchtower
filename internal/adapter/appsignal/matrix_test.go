package appsignal

import (
	"bytes"
	"compress/zlib"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olucurious/watchtower/internal/event"
)

// The matrix fixtures come from testdata/harness.exs run against each
// appsignal-elixir release from 2.9.2 (scripts/appsignal-check.sh adds new
// ones).
// Each batch holds the same synthetic scenarios.
func TestVersionMatrix(t *testing.T) {
	files, _ := filepath.Glob("testdata/matrix/elixir-*.deflate")
	if len(files) < 13 {
		t.Fatalf("expected 13 version fixtures, found %d", len(files))
	}
	for _, f := range files {
		version := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "elixir-"), ".deflate")
		t.Run(version, func(t *testing.T) {
			events := decodeFixtureEvents(t, f)
			checkScenarios(t, version, events)
		})
	}
}

func decodeFixtureEvents(t *testing.T, path string) []event.Event {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zlib.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := DecodeCollect(body)
	if err != nil {
		t.Fatal(err)
	}
	if batch.AppName != "library" || batch.Environment != "prod" || batch.Hostname != "matrix-host" {
		t.Errorf("batch metadata: app=%q env=%q host=%q", batch.AppName, batch.Environment, batch.Hostname)
	}
	var out []event.Event
	for _, es := range batch.Errors {
		e := ToEvent(batch, es, 1, received)
		if err := e.Validate(); err != nil {
			t.Fatalf("invalid event: %v", err)
		}
		out = append(out, e)
	}
	return out
}

func checkScenarios(t *testing.T, version string, events []event.Event) {
	t.Helper()
	// 5 single scenarios + a burst of 25 distinct errors. Up to 2.15.x the
	// SDK also reports the GenServer crash; from 2.16.0 its default
	// configuration no longer does.
	want, reportsCrashes := 30, versionBefore(version, "2.16.0")
	if reportsCrashes {
		want = 31
	}
	if len(events) != want {
		t.Fatalf("got %d events, want %d", len(events), want)
	}
	ids := map[string]bool{}
	byTitle := map[string]event.Event{}
	burst := 0
	for _, e := range events {
		if ids[e.ID] {
			t.Errorf("duplicate event id %s", e.ID)
		}
		ids[e.ID] = true
		if e.Release != "library@synthetic-"+version {
			t.Errorf("%s: release %q", e.Title(), e.Release)
		}
		if strings.HasPrefix(e.Title(), "RuntimeError: deep failure ") {
			burst++
			continue
		}
		byTitle[e.Title()] = e
	}
	if burst != 25 {
		t.Errorf("burst: got %d events, want 25", burst)
	}

	if e, ok := byTitle[`KeyError: key "missing" not found in: %{"978-0" => 1}`]; !ok {
		t.Error("send_error scenario missing")
	} else {
		if e.Tags["region"] != "eu" || e.Tags["request_id"] != "req-123" || e.Tags["namespace"] != "background" {
			t.Errorf("send_error tags: %v", e.Tags)
		}
		frames := e.Exceptions[0].Frames
		var crash *event.Frame
		for i := len(frames) - 1; i >= 0; i-- {
			if frames[i].InApp {
				crash = &frames[i]
				break
			}
		}
		if crash == nil || crash.Module != "Library.Catalog" || crash.Function != "fetch!/1" {
			t.Errorf("innermost in-app frame: %+v", crash)
		}
		if f := frames[len(frames)-1]; f.Module != ":erlang" || f.Function != "map_get" || f.InApp {
			t.Errorf("erlang BIF frame: %+v", f)
		}
	}

	if e, ok := byTitle[`KeyError: key "missing-42" not found in: %{"978-0" => 1}`]; !ok {
		t.Error("plug request scenario missing")
	} else {
		if e.Transaction != "POST /books/:id/reserve" || e.Tags["namespace"] != "http_request" || e.Tags["http.status_code"] != "500" {
			t.Errorf("plug context: transaction=%q tags=%v", e.Transaction, e.Tags)
		}
		if e.Request == nil || e.Request.Method != "POST" || e.Request.URL != "/books/42/reserve" {
			t.Errorf("plug request: %+v", e.Request)
		}
		for _, f := range e.Exceptions[0].Frames {
			if strings.Contains(f.Filename, "/deps/") && f.InApp {
				t.Errorf("dependency frame marked in-app: %+v", f)
			}
		}
	}

	if e, ok := byTitle["ArgumentError: cannot reserve 5 copies; the limit is 3"]; !ok {
		t.Error("instrumented span scenario missing")
	} else if e.Transaction != "library.reserve" {
		t.Errorf("instrumented transaction %q", e.Transaction)
	}
	if e, ok := byTitle["KeyError: key :shelf not found in: %{}"]; ok != reportsCrashes {
		t.Errorf("GenServer crash reported=%v, want %v", ok, reportsCrashes)
	} else if ok && e.Tags["namespace"] != "background_job" {
		t.Errorf("GenServer crash namespace %q", e.Tags["namespace"])
	}
	if _, ok := byTitle["exit: :index_timeout"]; !ok {
		t.Error("exit scenario missing")
	}
	if _, ok := byTitle["throw: :shelf_full"]; !ok {
		t.Error("throw scenario missing")
	}

	// Params and session data must never be decoded into events.
	all, _ := json.Marshal(events)
	for _, secret := range []string{"hunter22", "secret-token", "tok_live_123"} {
		if strings.Contains(string(all), secret) {
			t.Errorf("request payload value %q leaked into events", secret)
		}
	}
}

// versionBefore compares dotted numeric versions.
func versionBefore(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		var x, y int
		fmt.Sscan(pa[i], &x)
		fmt.Sscan(pb[i], &y)
		if x != y {
			return x < y
		}
	}
	return len(pa) < len(pb)
}
