package worker

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/olucurious/watchtower/internal/event"
	"github.com/olucurious/watchtower/internal/scrub"
	"github.com/olucurious/watchtower/internal/store"
	"github.com/olucurious/watchtower/internal/store/storetest"
	"github.com/olucurious/watchtower/internal/symbolicate"
)

func TestPreparedEventSanitizedBeforeStorage(t *testing.T) {
	ctx := context.Background()
	s := storetest.New(t)
	p, err := s.CreateProject(ctx, "mapped", "Mapped")
	if err != nil {
		t.Fatal(err)
	}
	content, err := json.Marshal(map[string]any{"version": 3, "sources": []string{"src/app\x00.js"}, "names": []string{}, "mappings": "AAAA", "sourcesContent": []string{"password=supersecret\x00\napi_key=othersecret"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveBundle(ctx, []int64{p.ID}, store.Bundle{Checksum: "map"}, []store.ArtifactFile{{Kind: "source_map", DebugID: "debug", URL: "~/app.js.map", Content: content}}, nil); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	e := event.Event{ID: strings.Repeat("a", 32), ProjectID: p.ID, Adapter: "sentry", Timestamp: now, ReceivedAt: now, Level: event.LevelError, Platform: "javascript", DebugImages: []event.DebugImage{{Type: "sourcemap", CodeFile: "app.js", DebugID: "debug"}}, Exceptions: []event.Exception{{Type: "Error", Value: "boom", Frames: []event.Frame{{Filename: "app.js", Lineno: 1, Colno: 1, InApp: true}}}}}
	scrub.Event(&e)
	if err := s.Accept(ctx, []event.Event{e}); err != nil {
		t.Fatal(err)
	}
	g := &Grouper{Symbolicator: symbolicate.New(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	out, err := s.ProcessQueue(ctx, 10, g.Group)
	if err != nil || out.Stored != 1 || out.Failed != 0 {
		t.Fatalf("outcome %+v: %v", out, err)
	}
	page, err := s.ListIssues(ctx, store.IssueFilter{Project: "mapped"})
	if err != nil || len(page.Issues) != 1 {
		t.Fatalf("issues %+v: %v", page, err)
	}
	detail, err := s.IssueEvent(ctx, page.Issues[0].ID, "latest")
	if err != nil {
		t.Fatal(err)
	}
	var prepared event.Event
	if err := json.Unmarshal(detail.Data, &prepared); err != nil {
		t.Fatal(err)
	}
	f := prepared.Exceptions[0].Frames[0]
	if f.Minified == nil || f.Filename != "src/app.js" {
		t.Fatalf("mapping not applied: %+v", f)
	}
	raw := string(detail.Data)
	if strings.Contains(raw, "supersecret") || strings.Contains(raw, "othersecret") || strings.Contains(raw, `\u0000`) {
		t.Fatalf("unsanitized mapped event: %s", raw)
	}
	if !strings.Contains(f.ContextLine, "[Filtered]") {
		t.Fatalf("missing scrubbed context: %+v", f)
	}
}
