package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/olucurious/watchtower/internal/event"
)

// seed stores events for two issues in project "web": KeyError (3 events,
// production and staging) and TypeError (1 event, production).
func seed(t *testing.T, s *Store) (keyErr, typeErr int64) {
	t.Helper()
	ctx := context.Background()
	p, err := s.CreateProject(ctx, "web", "Web")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	mk := func(n int, typ, env, release string, ago time.Duration, tags map[string]string) event.Event {
		e := testEvent(p.ID, eventID(n), now.Add(-ago))
		e.Exceptions[0].Type = typ
		e.Environment, e.Release, e.Tags = env, release, tags
		return e
	}
	events := []event.Event{
		mk(1, "KeyError", "production", "1.0", 3*time.Hour, map[string]string{"region": "lagos"}),
		mk(2, "KeyError", "staging", "1.1", 2*time.Hour, map[string]string{"region": "lagos"}),
		mk(3, "KeyError", "production", "1.1", 1*time.Hour, map[string]string{"region": "london"}),
		mk(4, "TypeError", "production", "1.1", 30*time.Minute, nil),
	}
	if err := s.Accept(ctx, events); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProcessQueue(ctx, 10, group); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListIssues(ctx, IssueFilter{Project: "web"})
	if err != nil || len(page.Issues) != 2 {
		t.Fatalf("seed: %+v %v", page, err)
	}
	for _, i := range page.Issues {
		if i.Title == "TypeError: boom" {
			typeErr = i.ID
		} else {
			keyErr = i.ID
		}
	}
	return keyErr, typeErr
}

func TestListIssuesFilters(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	keyErr, typeErr := seed(t, s)

	page, err := s.ListIssues(ctx, IssueFilter{Project: "web", Period: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if page.Issues[0].ID != typeErr {
		t.Error("default sort is most recently seen first")
	}
	if page.Trend.Bucket != "hour" || len(page.Issues[0].Trend) != 24 {
		t.Errorf("trend spec %+v len %d", page.Trend, len(page.Issues[0].Trend))
	}
	var sum int64
	for _, n := range page.Issues[1].Trend {
		sum += n
	}
	if sum != 3 {
		t.Errorf("KeyError trend sums to %d, want 3", sum)
	}

	if p, _ := s.ListIssues(ctx, IssueFilter{Project: "web", Environment: "staging"}); len(p.Issues) != 1 || p.Issues[0].ID != keyErr {
		t.Errorf("environment filter: %+v", p.Issues)
	}
	if p, _ := s.ListIssues(ctx, IssueFilter{Query: "typeerr"}); len(p.Issues) != 1 || p.Issues[0].ID != typeErr {
		t.Errorf("query filter: %+v", p.Issues)
	}
	if p, _ := s.ListIssues(ctx, IssueFilter{Query: "100%_"}); len(p.Issues) != 0 {
		t.Error("LIKE wildcards in the query must be literal")
	}
	if p, _ := s.ListIssues(ctx, IssueFilter{Sort: "times_seen"}); p.Issues[0].ID != keyErr {
		t.Error("times_seen sort")
	}

	if n, err := s.SetIssuesStatus(ctx, []int64{keyErr, typeErr}, "muted", "ada"); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	if n, _ := s.SetIssuesStatus(ctx, []int64{keyErr}, "muted", "ada"); n != 0 {
		t.Error("unchanged issues must not be counted or logged again")
	}
	p, _ := s.ListIssues(ctx, IssueFilter{Project: "web", Status: "unresolved"})
	if len(p.Issues) != 0 || p.Counts["muted"] != 2 || p.Total != 0 {
		t.Errorf("status filter and counts: %+v", p)
	}
	if envs, _ := s.Environments(ctx, "web"); len(envs) != 2 || envs[0] != "production" {
		t.Errorf("environments %v", envs)
	}
}

func TestIssueDetailAndEventNavigation(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	keyErr, _ := seed(t, s)

	d, err := s.Issue(ctx, keyErr)
	if err != nil {
		t.Fatal(err)
	}
	if d.Events24h != 3 || d.Events30d != 3 || len(d.Hourly.Counts) != 24 || len(d.Daily.Counts) != 30 {
		t.Errorf("stats: 24h=%d 30d=%d hourly=%d daily=%d", d.Events24h, d.Events30d, len(d.Hourly.Counts), len(d.Daily.Counts))
	}
	tags := map[string]TagSummary{}
	for _, tg := range d.Tags {
		tags[tg.Key] = tg
	}
	if r := tags["region"]; r.Total != 3 || r.Values[0].Value != "lagos" || r.Values[0].Count != 2 {
		t.Errorf("region tag %+v", r)
	}
	if e := tags["environment"]; e.Total != 3 || len(e.Values) != 2 {
		t.Errorf("environment pseudo-tag %+v", e)
	}
	if d.Tags[0].Key != "environment" {
		t.Error("environment is listed first")
	}
	if len(d.Activity) != 1 || d.Activity[0].Kind != "first_seen" {
		t.Errorf("activity %+v", d.Activity)
	}

	latest, err := s.IssueEvent(ctx, keyErr, "latest")
	if err != nil || latest.EventID != eventID(3) || latest.Newer != "" || latest.Older != eventID(2) || latest.Oldest != eventID(1) {
		t.Fatalf("latest %+v %v", latest, err)
	}
	mid, _ := s.IssueEvent(ctx, keyErr, eventID(2))
	if mid.Older != eventID(1) || mid.Newer != eventID(3) {
		t.Errorf("middle event neighbours %+v", mid)
	}
	if _, err := s.IssueEvent(ctx, keyErr, eventID(4)); !errors.Is(err, ErrNotFound) {
		t.Error("an event from another issue must not be served")
	}
	if evs, _ := s.IssueEvents(ctx, keyErr, 2, 0); len(evs) != 2 || evs[0].EventID != eventID(3) || evs[0].Environment != "production" {
		t.Errorf("event list %+v", evs)
	}
	if _, err := s.Issue(ctx, 999999); !errors.Is(err, ErrNotFound) {
		t.Error("missing issue")
	}
}

func TestSessions(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	u, err := s.CreateUser(ctx, "Ada@Example.com", "Ada", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, "ada@example.com", "", "hash", false); !errors.Is(err, ErrExists) {
		t.Error("emails are unique regardless of case")
	}
	if got, _, err := s.UserForLogin(ctx, "ADA@example.com"); err != nil || got.ID != u.ID {
		t.Errorf("case-insensitive login lookup: %v", err)
	}
	digest := []byte("01234567890123456789012345678901")
	if err := s.CreateSession(ctx, digest, u.ID, time.Hour); err != nil {
		t.Fatal(err)
	}
	if got, err := s.SessionUser(ctx, digest); err != nil || got.ID != u.ID {
		t.Fatalf("session: %v", err)
	}
	if err := s.SetUserDisabled(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionUser(ctx, digest); !errors.Is(err, ErrNotFound) {
		t.Error("disabling a user must end their sessions")
	}
	if _, _, err := s.UserForLogin(ctx, "ada@example.com"); !errors.Is(err, ErrNotFound) {
		t.Error("disabled users cannot sign in")
	}
	expired := []byte("expired-session-digest-000000000")
	s.SetUserDisabled(ctx, u.ID, false)
	s.CreateSession(ctx, expired, u.ID, -time.Minute)
	if _, err := s.SessionUser(ctx, expired); !errors.Is(err, ErrNotFound) {
		t.Error("expired session accepted")
	}
}

func TestPrune(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	keyErr, typeErr := seed(t, s)
	// Age the KeyError's oldest event and the TypeError issue entirely.
	old := time.Now().Add(-100 * 24 * time.Hour)
	s.pool.Exec(ctx, `update events set received_at = $1 where event_id = $2`, old, eventID(1))
	s.pool.Exec(ctx, `update issues set last_seen = $1 where id = $2`, old, typeErr)
	if _, err := s.pool.Exec(ctx, `update events set received_at = $1 where issue_id = $2`, old, typeErr); err != nil {
		t.Fatal(err)
	}

	p, err := s.Prune(ctx, time.Now().Add(-90*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if p.Issues != 1 || p.Events != 1 {
		t.Errorf("pruned %+v, want 1 issue (with its event) and 1 old event", p)
	}
	if _, err := s.Issue(ctx, typeErr); !errors.Is(err, ErrNotFound) {
		t.Error("stale issue kept")
	}
	evs, _ := s.IssueEvents(ctx, keyErr, 10, 0)
	if len(evs) != 2 {
		t.Errorf("active issue keeps its recent events: got %d", len(evs))
	}
}

func TestPruneKeepsRecentlyReceivedDelayedEvent(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p, err := s.CreateProject(ctx, "delayed", "Delayed")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	e := testEvent(p.ID, eventID(1), now.Add(-100*24*time.Hour))
	e.ReceivedAt = now
	ingest(t, s, e)
	cutoff := now.Add(-90 * 24 * time.Hour)
	if p, err := s.Prune(ctx, cutoff); err != nil || p.Issues != 0 || p.Events != 0 {
		t.Fatalf("prune %+v: %v", p, err)
	}
	page, err := s.ListIssues(ctx, IssueFilter{Project: "delayed"})
	if err != nil || len(page.Issues) != 1 {
		t.Fatalf("issues %+v: %v", page, err)
	}
	if _, err := s.IssueEvent(ctx, page.Issues[0].ID, "latest"); err != nil {
		t.Fatal(err)
	}
	if p, err := s.Prune(ctx, now.Add(time.Hour)); err != nil || p.Issues != 1 {
		t.Fatalf("expired prune %+v: %v", p, err)
	}
}

func TestPruneSkipsIssueBeingUpdated(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p, err := s.CreateProject(ctx, "retention-race", "Retention")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-100 * 24 * time.Hour)
	ingest(t, s, testEvent(p.ID, eventID(1), old))
	page, err := s.ListIssues(ctx, IssueFilter{Project: p.Slug})
	if err != nil {
		t.Fatal(err)
	}
	id := page.Issues[0].ID
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `select id from issues where id=$1 for update`, id); err != nil {
		t.Fatal(err)
	}
	// In-flight worker writes a recent receipt while retaining old occurrence time.
	data, err := json.Marshal(testEvent(p.ID, eventID(2), old))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `insert into events(project_id,event_id,issue_id,occurred_at,received_at,data) values($1,$2,$3,$4,now(),$5)`, p.ID, eventID(2), id, old, data); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().Add(-90 * 24 * time.Hour)
	if n, err := s.pruneIssues(ctx, cutoff); err != nil || n != 0 {
		t.Fatalf("locked issue pruned: %d %v", n, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Prune(ctx, cutoff); err != nil {
		t.Fatal(err)
	}
	if _, err := s.IssueEvent(ctx, id, eventID(2)); err != nil {
		t.Fatalf("recent event lost: %v", err)
	}
}
