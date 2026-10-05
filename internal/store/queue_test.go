package store

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/olucurious/watchtower/internal/event"
)

func manyID(n int) string { return fmt.Sprintf("%032x", n) }

// A batch writes each issue once, with the totals of all its events.
func TestBatchAggregatesPerIssue(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p, _ := s.CreateProject(ctx, "agg", "Agg")
	base := time.Now().UTC().Truncate(time.Second)
	ev := func(n int, typ, env, release string, at time.Time) event.Event {
		e := testEvent(p.ID, manyID(n), at)
		e.Exceptions[0].Type, e.Environment, e.Release = typ, env, release
		return e
	}
	// An event stored earlier, which the batch then repeats.
	ingest(t, s, ev(1, "KeyError", "production", "1.0", base))
	batch := []event.Event{
		ev(2, "KeyError", "staging", "1.1", base.Add(-time.Hour)), // earliest
		ev(3, "TypeError", "production", "", base.Add(time.Minute)),
		ev(1, "KeyError", "production", "1.0", base),               // an SDK retry
		ev(4, "KeyError", "production", "", base.Add(2*time.Hour)), // latest, no release
		ev(5, "KeyError", "canary", "1.2", base.Add(time.Hour)),
		ev(5, "KeyError", "canary", "1.2", base.Add(time.Hour)), // repeated within the batch
	}
	if err := s.Accept(ctx, batch); err != nil {
		t.Fatal(err)
	}
	out, err := s.ProcessQueue(ctx, 100, group)
	if err != nil {
		t.Fatal(err)
	}
	if out.Stored != 4 || out.Duplicates != 2 || out.NewIssues != 1 || out.Failed != 0 {
		t.Fatalf("outcome %+v", out)
	}
	var times int64
	var first, last time.Time
	var envs []string
	var release string
	if err := s.pool.QueryRow(ctx, `select times_seen, first_seen, last_seen, environments, last_release from issues where fingerprint = 'v1:KeyError'`).
		Scan(&times, &first, &last, &envs, &release); err != nil {
		t.Fatal(err)
	}
	if times != 4 || !first.Equal(base.Add(-time.Hour)) || !last.Equal(base.Add(2*time.Hour)) {
		t.Errorf("times_seen=%d first=%v last=%v", times, first, last)
	}
	if fmt.Sprint(envs) != "[production staging canary]" || release != "1.2" {
		t.Errorf("environments %v, last_release %q", envs, release)
	}
	var created, events int
	s.pool.QueryRow(ctx, `select count(*) from issue_activity where kind = 'first_seen'`).Scan(&created)
	s.pool.QueryRow(ctx, `select count(*) from events where issue_id is not null`).Scan(&events)
	if created != 2 || events != 5 {
		t.Errorf("first_seen activities %d, stored events %d", created, events)
	}
}

// One bad event fails on its own; the rest of its batch is stored.
func TestPoisonEventDoesNotBlockItsBatch(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p, _ := s.CreateProject(ctx, "poison", "Poison")
	var batch []event.Event
	for i := 1; i <= 4; i++ {
		e := testEvent(p.ID, manyID(i), time.Now())
		if i == 3 {
			e.Exceptions[0].Type = "Poison"
		}
		batch = append(batch, e)
	}
	if err := s.Accept(ctx, batch); err != nil {
		t.Fatal(err)
	}
	out, err := s.ProcessQueue(ctx, 100, func(ctx context.Context, q ArtifactQuerier, e *event.Event) Grouped {
		g := group(ctx, q, e)
		if e.Exceptions[0].Type == "Poison" {
			g.Title = "\x00" // Postgres rejects NUL in text
		}
		return g
	})
	if err != nil || out.Stored != 3 || out.Failed != 1 {
		t.Fatalf("outcome %+v: %v", out, err)
	}
	assertQueueDepth(t, s, 1)
}

// Channel filters apply to each event, even when a batch holds several.
func TestAlertFiltersApplyPerEventInABatch(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p, _ := s.CreateProject(ctx, "web", "Web")
	two := 2
	if _, err := s.CreateAlertChannel(ctx, "web", "slack", AlertTarget{Sealed: []byte("sealed"), Hint: "hint"},
		AlertRules{Name: "prod", OnNewIssue: true, FrequencyThreshold: &two, MinLevel: "error", Environment: "production"}, 0); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	staging, prod := testEvent(p.ID, manyID(1), now), testEvent(p.ID, manyID(2), now)
	staging.Environment, prod.Environment = "staging", "production"
	// The issue's first event is from staging, so there is no new-issue
	// alert, but the production event behind it reaches the threshold.
	ingest(t, s, staging, prod)
	if k := pendingKinds(t, s); len(k) != 1 || k[0] != "frequency" {
		t.Fatalf("alerts %v, want [frequency]", k)
	}
}

// Workers lock issues in one order, so several can drain the same queue,
// with the same hot issues, without deadlocking or blaming events.
func TestConcurrentWorkersDoNotDeadlock(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p, _ := s.CreateProject(ctx, "busy", "Busy")
	const n, issues = 600, 12
	var batch []event.Event
	for i := range n {
		e := testEvent(p.ID, manyID(i+1), time.Now())
		e.Exceptions[0].Type = fmt.Sprintf("Error%02d", rand.IntN(issues))
		batch = append(batch, e)
	}
	if err := s.Accept(ctx, batch); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Go(func() {
			for {
				out, err := s.ProcessQueue(ctx, 25, group)
				if err != nil {
					errs <- err
					return
				}
				if out.Stored+out.Duplicates+out.Failed == 0 {
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("worker: %v", err)
	}
	var queued, retried, total int64
	s.pool.QueryRow(ctx, `select count(*), count(*) filter (where attempts > 0) from ingest_queue`).Scan(&queued, &retried)
	s.pool.QueryRow(ctx, `select coalesce(sum(times_seen), 0) from issues`).Scan(&total)
	if queued != 0 || retried != 0 || total != n {
		t.Errorf("queued %d (retried %d), times_seen total %d, want 0, 0, %d", queued, retried, total, n)
	}
}

// The hourly rollup behind charts, counts and digests agrees with the
// stored events, including events spread over hours, days and batches.
func TestHourlyRollupMatchesEvents(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p, _ := s.CreateProject(ctx, "roll", "Roll")
	now := time.Now().UTC()
	n := 0
	for batch := range 3 {
		var events []event.Event
		for i := range 40 {
			n++
			// Spread over 40 days, so some fall outside each window.
			at := now.Add(-time.Duration((n*37+batch*11+i)%(40*24)) * time.Hour).Add(-time.Duration(i) * time.Minute)
			events = append(events, testEvent(p.ID, manyID(n), at))
		}
		ingest(t, s, events...)
	}
	page, err := s.ListIssues(ctx, IssueFilter{Project: "roll"})
	if err != nil || len(page.Issues) != 1 {
		t.Fatalf("%+v %v", page, err)
	}
	d, err := s.Issue(ctx, page.Issues[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	count := func(from time.Time) (n int64) {
		s.pool.QueryRow(ctx, `select count(*) from events where occurred_at >= $1`, from).Scan(&n)
		return n
	}
	if want := count(d.Hourly.Start); d.Events24h != want {
		t.Errorf("Events24h %d, events since %v: %d", d.Events24h, d.Hourly.Start, want)
	}
	if want := count(d.Daily.Start); d.Events30d != want {
		t.Errorf("Events30d %d, events since %v: %d", d.Events30d, d.Daily.Start, want)
	}
	var trend int64
	for _, v := range page.Issues[0].Trend {
		trend += v
	}
	if want := count(page.Trend.Start); trend != want {
		t.Errorf("sparkline total %d, events since %v: %d", trend, page.Trend.Start, want)
	}
	since := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -7)
	until := since.AddDate(0, 0, 7)
	dg, err := s.Digest(ctx, since, until)
	if err != nil {
		t.Fatal(err)
	}
	var want int64
	s.pool.QueryRow(ctx, `select count(*) from events where occurred_at >= $1 and occurred_at < $2`, since, until).Scan(&want)
	if dg.Events != want || len(dg.Busiest) != 1 || dg.Busiest[0].TimesSeen != want {
		t.Errorf("digest events %d, busiest %+v, want %d", dg.Events, dg.Busiest, want)
	}
}
