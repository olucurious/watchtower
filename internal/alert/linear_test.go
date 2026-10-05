package alert

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olucurious/watchtower/internal/event"
	"github.com/olucurious/watchtower/internal/store"
)

const linearKey = "lin_api_0123456789abcdefWXYZ"

// fakeLinear implements the slice of Linear's GraphQL API Watchtower uses.
type fakeLinear struct {
	mu        sync.Mutex
	issues    map[string]*LinearIssue
	comments  map[string][]string
	created   []map[string]any
	ratelimit bool
	badURL    bool
}

func newFakeLinear(t *testing.T) (*fakeLinear, *Linear) {
	f := &fakeLinear{issues: map[string]*LinearIssue{}, comments: map[string][]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != linearKey {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"errors":[{"message":"Authentication required, not authenticated","extensions":{"code":"AUTHENTICATION_ERROR"}}]}`)
			return
		}
		var req struct {
			Query     string
			Variables map[string]any
		}
		json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.ratelimit {
			w.Header().Set("Retry-After", "42")
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"errors":[{"message":"Rate limit exceeded","extensions":{"code":"RATELIMITED"}}]}`)
			return
		}
		reply := func(v any) { json.NewEncoder(w).Encode(map[string]any{"data": v}) }
		switch {
		case strings.Contains(req.Query, "teams("):
			reply(map[string]any{"teams": map[string]any{"nodes": []map[string]string{{"id": "team-1", "name": "Reader", "key": "LIB"}}}})
		case strings.Contains(req.Query, "issueCreate"):
			in := req.Variables["input"].(map[string]any)
			id := in["id"].(string)
			if _, dup := f.issues[id]; dup {
				io.WriteString(w, `{"errors":[{"message":"Entity with this id already exists"}]}`)
				return
			}
			f.created = append(f.created, in)
			is := &LinearIssue{ID: id, Identifier: fmt.Sprintf("LIB-%d", len(f.created)), URL: "https://linear.app/acme/issue/LIB-" + fmt.Sprint(len(f.created))}
			if f.badURL {
				is.URL = "javascript:alert(1)"
			}
			is.State.Type = "unstarted"
			f.issues[id] = is
			reply(map[string]any{"issueCreate": map[string]any{"success": true, "issue": is}})
		case strings.Contains(req.Query, "issue(id"):
			reply(map[string]any{"issue": f.issues[req.Variables["id"].(string)]})
		case strings.Contains(req.Query, "commentCreate"):
			in := req.Variables["input"].(map[string]any)
			f.comments[in["issueId"].(string)] = append(f.comments[in["issueId"].(string)], in["body"].(string))
			reply(map[string]any{"commentCreate": map[string]any{"success": true}})
		case strings.Contains(req.Query, "issues("):
			var nodes []*LinearIssue
			for _, id := range req.Variables["ids"].([]any) {
				if is := f.issues[id.(string)]; is != nil {
					nodes = append(nodes, is)
				}
			}
			reply(map[string]any{"issues": map[string]any{"nodes": nodes}})
		}
	}))
	t.Cleanup(srv.Close)
	return f, &Linear{API: srv.URL, Client: srv.Client()}
}

func TestLinearClient(t *testing.T) {
	ctx := context.Background()
	f, l := newFakeLinear(t)
	teams, err := l.Teams(ctx, linearKey)
	if err != nil || len(teams) != 1 || teams[0].Key != "LIB" {
		t.Fatalf("teams %v %v", teams, err)
	}
	if _, err := l.Teams(ctx, "lin_api_wrongwrongwrongwrong"); err == nil || strings.Contains(err.Error(), "wrong") {
		t.Errorf("bad key: %v", err)
	}
	a, err := l.CreateIssue(ctx, linearKey, "6f1c…", "team-1", "Error: boom", "body")
	if err != nil || a.Identifier != "LIB-1" {
		t.Fatalf("create %+v %v", a, err)
	}
	// A retry with the same ID returns the first issue rather than a second.
	b, err := l.CreateIssue(ctx, linearKey, "6f1c…", "team-1", "Error: boom", "body")
	if err != nil || b.Identifier != "LIB-1" || len(f.created) != 1 {
		t.Errorf("retried create %+v %v (created %d)", b, err, len(f.created))
	}
	f.ratelimit = true
	var rl *RateLimited
	if err := l.Comment(ctx, linearKey, a.ID, "x"); !errors.As(err, &rl) || rl.After != 42*time.Second {
		t.Errorf("rate limit: %v", err)
	}
	if hint, err := ValidateLinearKey(linearKey); err != nil || hint != "lin_api_…WXYZ" {
		t.Errorf("hint %q %v", hint, err)
	}
	if _, err := ValidateLinearKey("xoxb-123"); err == nil {
		t.Error("non-Linear key accepted")
	}
}

// memTracker is an in-memory TrackerStore.
type memTracker struct {
	sealed   map[int64][]byte // per-issue key override
	links    map[int64]*store.IssueLink
	activity []string
	resolved map[int64]bool
	event    event.Event
}

func (m *memTracker) IssueLink(_ context.Context, id int64, _ string) (*store.IssueLink, error) {
	return m.links[id], nil
}
func (m *memTracker) SaveIssueLink(_ context.Context, l store.IssueLink, actor string, _ int64) (store.IssueLink, error) {
	if m.links[l.IssueID] == nil {
		m.links[l.IssueID] = &l
		m.activity = append(m.activity, actor+" linked "+l.Identifier)
	}
	return *m.links[l.IssueID], nil
}
func (m *memTracker) IssueEvent(context.Context, int64, string) (store.EventDetail, error) {
	b, _ := json.Marshal(m.event)
	return store.EventDetail{Data: b}, nil
}
func (m *memTracker) LinksToCheck(context.Context, string, int) ([]store.LinkToCheck, error) {
	var out []store.LinkToCheck
	for _, l := range m.links {
		if !m.resolved[l.IssueID] {
			k := sealedKey
			if m.sealed[l.IssueID] != nil {
				k = m.sealed[l.IssueID]
			}
			out = append(out, store.LinkToCheck{IssueLink: *l, Sealed: k})
		}
	}
	return out, nil
}
func (m *memTracker) LinkChecked(_ context.Context, id int64, _, state string, done bool) (bool, error) {
	m.links[id].State = state
	if done && !m.resolved[id] {
		m.resolved[id] = true
		return true, nil
	}
	return false, nil
}

var sealedKey []byte

func TestTrackerLifecycle(t *testing.T) {
	ctx := context.Background()
	f, l := newFakeLinear(t)
	sealer, _ := NewSealer(strings.Repeat("ab", 32))
	sealedKey, _ = sealer.Seal(linearKey)
	mem := &memTracker{links: map[int64]*store.IssueLink{}, resolved: map[int64]bool{},
		event: event.Event{Exceptions: []event.Exception{{Type: "Error", Value: "reservation failed\nmore", Frames: []event.Frame{
			{Function: "loadMember", Filename: "/app/node_modules/x.js", Lineno: 3},
			{Function: "reserve", Filename: "/app/node_app.js", Lineno: 6, InApp: true}}}}}}
	tr := &Tracker{Store: mem, Linear: l, Sealer: sealer, PublicURL: "https://w.example", Log: slog.New(slog.DiscardHandler)}

	n := &store.Notification{Kind: "new_issue", Detail: map[string]any{"release": "b240901873c07c4dd3e0c6b8986259d12ec770a6", "environment": "production"}}
	n.Channel.ID, n.Channel.Name, n.Channel.Sealed, n.Channel.Target, n.Channel.Kind = 7, "Reader triage", sealedKey, "team-1", "linear"
	n.Project.Name = "Storefront"
	n.Issue = &store.IssueRow{ID: 91, Title: "Error: reservation failed", Culprit: "node_app in reserve", TimesSeen: 61,
		FirstSeen: time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC), LastSeen: time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC),
		LastRelease: "b240901873c07c4dd3e0c6b8986259d12ec770a6", Environments: []string{"production"}}
	if _, err := tr.Notify(ctx, n); err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 1 || mem.links[91] == nil || mem.links[91].Identifier != "LIB-1" || mem.activity[0] != "Alert: Reader triage linked LIB-1" {
		t.Fatalf("created %v links %v activity %v", f.created, mem.links, mem.activity)
	}
	desc := f.created[0]["description"].(string)
	for _, want := range []string{"`node_app in reserve`", "**Storefront** · 61 events", "release `b240901`", "Error: reservation failed\n  at reserve (/app/node_app.js:6)", "[Open WT-91 in Watchtower](https://w.example/issues/91)"} {
		if !strings.Contains(desc, want) {
			t.Errorf("description lacks %q:\n%s", want, desc)
		}
	}
	if strings.Contains(desc, "node_modules") {
		t.Error("library frames should give way to app frames")
	}
	if id := f.created[0]["id"].(string); !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(id) {
		t.Errorf("issue id %q is not a v4 UUID", id)
	}

	// A regression of a linked issue becomes a comment, not a second issue.
	n.Kind = "regression"
	if _, err := tr.Notify(ctx, n); err != nil {
		t.Fatal(err)
	}
	ext := mem.links[91].ExternalID
	if len(f.created) != 1 || len(f.comments[ext]) != 1 || !strings.Contains(f.comments[ext][0], "**Regressed** in Watchtower in release `b240901` (production)") {
		t.Errorf("comments %v", f.comments)
	}

	// Completing the Linear issue resolves the Watchtower issue.
	if n, _ := tr.Sync(ctx); n != 0 {
		t.Error("resolved before completion")
	}
	f.issues[ext].State.Type = "completed"
	if n, err := tr.Sync(ctx); err != nil || n != 1 || !mem.resolved[91] {
		t.Errorf("sync resolved %d %v", n, err)
	}

	// A test notification checks the team without filing anything.
	n.Issue, n.Kind = nil, "test"
	if _, err := tr.Notify(ctx, n); err != nil || len(f.created) != 1 {
		t.Errorf("test: %v", err)
	}
	n.Channel.Target = "team-gone"
	if _, err := tr.Notify(ctx, n); err == nil {
		t.Error("test should fail for a team the key cannot see")
	}
}

func TestDeriveUUID(t *testing.T) {
	a, _ := NewSealer(strings.Repeat("ab", 32))
	b, _ := NewSealer(strings.Repeat("cd", 32))
	x1, _ := a.DeriveUUID("linear-issue:91:team-1")
	x2, _ := a.DeriveUUID("linear-issue:91:team-1")
	y, _ := b.DeriveUUID("linear-issue:91:team-1")
	z, _ := a.DeriveUUID("linear-issue:92:team-1")
	if x1 != x2 || x1 == y || x1 == z || x1[14] != '4' {
		t.Errorf("%s %s %s %s", x1, x2, y, z)
	}
	var nilSealer *Sealer
	if _, err := nilSealer.DeriveUUID("x"); !errors.Is(err, ErrNoSecretKey) {
		t.Error("nil sealer")
	}
}

func TestTrackerConvergesAndSyncsPastFailures(t *testing.T) {
	ctx := context.Background()
	f, l := newFakeLinear(t)
	sealer, _ := NewSealer(strings.Repeat("ab", 32))
	sealedKey, _ = sealer.Seal(linearKey)
	revoked, _ := sealer.Seal("lin_api_revokedrevokedrevoked")
	mem := &memTracker{links: map[int64]*store.IssueLink{}, resolved: map[int64]bool{}, sealed: map[int64][]byte{}}
	tr := &Tracker{Store: mem, Linear: l, Sealer: sealer, PublicURL: "https://w.example", Log: slog.New(slog.DiscardHandler)}
	issue := &store.IssueRow{ID: 7, Title: "Error: boom"}

	// Two destinations (different teams) filing the same issue end up with
	// one Linear issue: the second creation is refused and the first reused.
	a, _, err := tr.Link(ctx, LinearChannel{ID: 1, Sealed: sealedKey, TeamID: "team-1"}, issue, "Web", "Ada", 1)
	if err != nil {
		t.Fatal(err)
	}
	delete(mem.links, 7) // as if the second filing raced the first one's save
	b, _, err := tr.Link(ctx, LinearChannel{ID: 2, Sealed: sealedKey, TeamID: "team-2"}, issue, "Web", "Ada", 1)
	if err != nil || b.ExternalID != a.ExternalID || len(f.created) != 1 {
		t.Fatalf("second destination: %+v vs %+v, created %d, %v", b, a, len(f.created), err)
	}

	// A link whose key was revoked does not stop the others from syncing.
	mem.links[8] = &store.IssueLink{IssueID: 8, Provider: "linear", ExternalID: "gone"}
	mem.sealed[8] = revoked
	f.issues[a.ExternalID].State.Type = "completed"
	n, err := tr.Sync(ctx)
	if n != 1 || !mem.resolved[7] || err == nil || !strings.Contains(err.Error(), "rejected the API key") {
		t.Errorf("sync resolved %d, err %v", n, err)
	}
}

func TestTrackerRefusesNonHTTPSLinks(t *testing.T) {
	ctx := context.Background()
	f, l := newFakeLinear(t)
	sealer, _ := NewSealer(strings.Repeat("ab", 32))
	sealedKey, _ = sealer.Seal(linearKey)
	mem := &memTracker{links: map[int64]*store.IssueLink{}, resolved: map[int64]bool{}}
	tr := &Tracker{Store: mem, Linear: l, Sealer: sealer, PublicURL: "https://w.example", Log: slog.New(slog.DiscardHandler)}
	f.badURL = true
	if _, _, err := tr.Link(ctx, LinearChannel{ID: 1, Sealed: sealedKey, TeamID: "team-1"}, &store.IssueRow{ID: 9, Title: "x"}, "Web", "Ada", 1); err == nil || mem.links[9] != nil {
		t.Errorf("javascript: URL accepted: %v", err)
	}
}
