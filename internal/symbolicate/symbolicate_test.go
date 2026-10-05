package symbolicate

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-sourcemap/sourcemap"
	"github.com/jackc/pgx/v5"

	"github.com/olucurious/watchtower/internal/adapter/sentry"
	"github.com/olucurious/watchtower/internal/event"
	"github.com/olucurious/watchtower/internal/sourcemaps"
)

// fakeArtifacts answers source map lookups with one map, and records the
// debug IDs it was asked for and how often the map's content was loaded.
type fakeArtifacts struct {
	gz     []byte
	asked  []any
	misses bool
	loads  int
}

type row struct {
	f   *fakeArtifacts
	err error
}

func (r row) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	switch d := dest[0].(type) {
	case *int64: // looking up the map's ID
		*d = 7
	case *[]byte: // loading its content
		r.f.loads++
		*d = r.f.gz
	}
	return nil
}

func (f *fakeArtifacts) QueryRow(_ context.Context, _ string, args ...any) pgx.Row {
	if len(args) == 1 { // content by ID
		return row{f: f}
	}
	f.asked = append(f.asked, args[1])
	// Like the real store: only the uploaded debug ID has a map.
	if f.misses || args[1] != "c15ec507-acfd-5e0f-8e7d-28eddc80f480" {
		return row{err: pgx.ErrNoRows}
	}
	return row{f: f}
}

func fixtures(t *testing.T) (*fakeArtifacts, event.Event) {
	t.Helper()
	zipData, err := os.ReadFile("testdata/sentry-cli-3.8.0-bundle.zip")
	if err != nil {
		t.Fatal(err)
	}
	_, files, err := sourcemaps.ParseBundle(zipData)
	if err != nil {
		t.Fatal(err)
	}
	var mapContent []byte
	for _, f := range files {
		if f.Kind == "source_map" {
			mapContent = f.Content
			if f.DebugID != "c15ec507-acfd-5e0f-8e7d-28eddc80f480" {
				t.Fatalf("debug id %q", f.DebugID)
			}
		}
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(mapContent)
	zw.Close()

	raw, _ := os.ReadFile("testdata/node-event.json")
	e, err := sentry.Normalize(raw, 1, "", time.Now(), func() string { return strings.Repeat("a", 32) })
	if err != nil {
		t.Fatal(err)
	}
	return &fakeArtifacts{gz: buf.Bytes()}, e
}

func TestParseBundle(t *testing.T) {
	data, _ := os.ReadFile("testdata/sentry-cli-3.8.0-bundle.zip")
	b, files, err := sourcemaps.ParseBundle(data)
	if err != nil || len(files) != 2 || b.Release != "library-web@2.4.0" {
		t.Fatalf("bundle %+v, %d files, %v", b, len(files), err)
	}
	for _, f := range files {
		if f.Kind == "minified_source" && (f.URL != "~/main.mjs" || f.SourcemapRef != "main.mjs.map") {
			t.Errorf("minified file %+v", f)
		}
	}
	if _, _, err := sourcemaps.ParseBundle([]byte("not a zip")); err == nil {
		t.Error("garbage accepted")
	}
}

func TestSymbolicateNodeEvent(t *testing.T) {
	art, e := fixtures(t)
	if len(e.DebugImages) != 1 {
		t.Fatalf("debug images %+v", e.DebugImages)
	}
	n, err := New().Event(context.Background(), art, &e)
	if err != nil || n == 0 {
		t.Fatalf("mapped %d frames, err %v", n, err)
	}
	frames := e.Exceptions[0].Frames
	crash := frames[len(frames)-1]
	if crash.Filename != "src/catalog.js" || crash.Function != "reserveBook" || crash.Lineno != 4 {
		t.Errorf("crashing frame %+v", crash)
	}
	if !strings.Contains(crash.ContextLine, "throw new TypeError") || len(crash.PreContext) == 0 {
		t.Errorf("context %q / %v", crash.ContextLine, crash.PreContext)
	}
	if crash.Minified == nil || len(crash.Minified.Function) > 2 || !strings.HasSuffix(crash.Minified.Filename, "dist/main.mjs") {
		t.Errorf("minified location %+v", crash.Minified)
	}
	caller := frames[len(frames)-2]
	if caller.Filename != "src/catalog.js" || caller.Function != "reserve" || caller.Lineno != 11 {
		t.Errorf("caller frame %+v", caller)
	}
	for _, f := range frames {
		if f.Minified != nil && !strings.HasSuffix(f.Minified.Filename, "dist/main.mjs") {
			t.Errorf("mapped a frame outside the bundle: %+v", f)
		}
	}
	// Mapping is idempotent: already-mapped frames are left alone.
	before, _ := json.Marshal(e)
	New().Event(context.Background(), art, &e)
	after, _ := json.Marshal(e)
	if !bytes.Equal(before, after) {
		t.Error("second pass changed the event")
	}
}

func TestNoSourceMapLeavesFramesAlone(t *testing.T) {
	art, e := fixtures(t)
	art.misses = true
	before, _ := json.Marshal(e)
	if n, err := New().Event(context.Background(), art, &e); n != 0 || err != nil {
		t.Fatalf("%d %v", n, err)
	}
	after, _ := json.Marshal(e)
	if !bytes.Equal(before, after) {
		t.Error("frames changed without a source map")
	}
}

func TestCleanSource(t *testing.T) {
	for in, want := range map[string]string{
		"../src/catalog.js":           "src/catalog.js",
		"webpack://my-app/./src/a.js": "src/a.js",
		"webpack:///./src/b.ts":       "src/b.ts",
		"src/c.js":                    "src/c.js",
	} {
		if got := cleanSource(in); got != want {
			t.Errorf("cleanSource(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReleaseURLs(t *testing.T) {
	if got := releaseURLs("https://shop.example.com/static/js/main.abc.js"); got[0] != "~/static/js/main.abc.js" {
		t.Errorf("browser URL: %v", got)
	}
	if got := releaseURLs("/app/dist/main.mjs"); got[0] != "~/app/dist/main.mjs" || got[1] != "~/main.mjs" {
		t.Errorf("file path: %v", got)
	}
}

// A cached map is never loaded from the store again: only its ID is looked
// up for each frame.
func TestCachedMapIsLoadedOnce(t *testing.T) {
	art, e := fixtures(t)
	s := New()
	for range 5 {
		ev := e
		ev.Exceptions = []event.Exception{e.Exceptions[0]}
		ev.Exceptions[0].Frames = append([]event.Frame(nil), e.Exceptions[0].Frames...)
		if n, err := s.Event(context.Background(), art, &ev); err != nil || n == 0 {
			t.Fatalf("mapped %d frames: %v", n, err)
		}
	}
	if art.loads != 1 {
		t.Fatalf("map content loaded %d times for 5 events, want 1", art.loads)
	}
}

func TestMapCacheStaysWithinBudget(t *testing.T) {
	c := newMapCache(100)
	m := &sourcemap.Consumer{}
	c.put(1, m, 40)
	c.put(2, m, 40)
	c.get(1) // 1 is now more recent than 2
	c.put(3, m, 40)
	if _, ok := c.get(2); ok {
		t.Error("the least recently used map should have been evicted")
	}
	for _, id := range []int64{1, 3} {
		if _, ok := c.get(id); !ok {
			t.Errorf("map %d evicted too early", id)
		}
	}
	if c.used > c.budget {
		t.Errorf("used %d of a %d budget", c.used, c.budget)
	}
	c.put(4, m, 101)
	if _, ok := c.get(4); ok || c.used != 80 {
		t.Errorf("a map over the budget must not be cached (used %d)", c.used)
	}
}
