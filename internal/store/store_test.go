package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/olucurious/watchtower/internal/adapter"
	"github.com/olucurious/watchtower/internal/event"
)

// newTestStore creates a throwaway database on the server named by
// WATCHTOWER_TEST_DATABASE_URL and drops it afterwards.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	admin := os.Getenv("WATCHTOWER_TEST_DATABASE_URL")
	if admin == "" {
		t.Skip("WATCHTOWER_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	var b [6]byte
	rand.Read(b[:])
	name := "watchtower_test_" + hex.EncodeToString(b[:])
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "create database "+name); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(admin)
	u.Path = "/" + name
	s, err := Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Close()
		conn.Exec(ctx, "drop database "+name+" with (force)")
		conn.Close(ctx)
	})
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil { // idempotent
		t.Fatalf("second migrate: %v", err)
	}
	return s
}

func testEvent(project int64, id string, at time.Time) event.Event {
	return event.Event{
		ID: id, ProjectID: project, Adapter: "sentry", Timestamp: at, ReceivedAt: at,
		Level: event.LevelError, Release: "1.0.0",
		Exceptions: []event.Exception{{Type: "KeyError", Value: "boom"}},
	}
}

func group(_ context.Context, _ ArtifactQuerier, e *event.Event) Grouped {
	return Grouped{Fingerprint: "v1:" + e.Exceptions[0].Type, Title: e.Title(), Culprit: e.Culprit()}
}

func eventID(n int) string {
	var b [16]byte
	b[15] = byte(n)
	return hex.EncodeToString(b[:])
}

func TestKeysAndIngestLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	p, err := s.CreateProject(ctx, "library", "Library")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateProject(ctx, "library", "again"); !errors.Is(err, ErrExists) {
		t.Errorf("duplicate project: %v", err)
	}
	key, _, err := s.CreateKey(ctx, "library", "sentry", "test", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.ResolveKey(ctx, "sentry", key); err != nil || got.ID != p.ID {
		t.Fatalf("resolve: %+v %v", got, err)
	}
	if _, err := s.ResolveKey(ctx, "appsignal", key); !errors.Is(err, adapter.ErrUnknownKey) {
		t.Errorf("key must be scoped to its adapter: %v", err)
	}
	var stored []byte
	s.pool.QueryRow(ctx, `select key_sha256 from project_keys`).Scan(&stored)
	if string(stored) == key {
		t.Error("plaintext key stored")
	}

	t0 := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	// Two occurrences plus an SDK retry of the first one.
	batch := []event.Event{testEvent(p.ID, eventID(1), t0), testEvent(p.ID, eventID(2), t0.Add(time.Minute))}
	if err := s.Accept(ctx, batch); err != nil {
		t.Fatal(err)
	}
	if err := s.Accept(ctx, batch[:1]); err != nil {
		t.Fatal(err)
	}
	out, err := s.ProcessQueue(ctx, 10, group)
	if err != nil {
		t.Fatal(err)
	}
	if out.Stored != 2 || out.Duplicates != 1 || out.NewIssues != 1 {
		t.Fatalf("outcome %+v", out)
	}
	issues, err := s.Issues(ctx, "library", "", 10)
	if err != nil || len(issues) != 1 {
		t.Fatalf("issues %+v %v", issues, err)
	}
	if issues[0].TimesSeen != 2 || !issues[0].LastSeen.Equal(t0.Add(time.Minute)) || issues[0].Title != "KeyError: boom" {
		t.Errorf("issue %+v", issues[0])
	}

	// Resolve, then the error comes back in a new release: it reopens.
	if n, err := s.SetIssuesStatus(ctx, []int64{issues[0].ID}, "resolved", "ada"); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	back := testEvent(p.ID, eventID(3), t0.Add(time.Hour))
	back.Release = "1.0.1"
	s.Accept(ctx, []event.Event{back})
	out, err = s.ProcessQueue(ctx, 10, group)
	if err != nil || out.Regressions != 1 {
		t.Fatalf("regression outcome %+v %v", out, err)
	}
	issues, _ = s.Issues(ctx, "library", "unresolved", 10)
	if len(issues) != 1 || issues[0].RegressedAt == nil || issues[0].LastRelease != "1.0.1" || issues[0].TimesSeen != 3 {
		t.Fatalf("regressed issue %+v", issues)
	}
	var kinds []string
	rows, _ := s.pool.Query(ctx, `select kind || ':' || actor from issue_activity where issue_id = $1 order by id`, issues[0].ID)
	kinds, _ = pgx.CollectRows(rows, pgx.RowTo[string])
	if want := "first_seen:system,resolved:ada,regressed:system"; join(kinds) != want {
		t.Errorf("activity %v, want %s", kinds, want)
	}
}

func TestPoisonEventIsRetriedThenDeadLettered(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p, _ := s.CreateProject(ctx, "api", "API")
	s.MaxQueueDepth = 1
	s.Accept(ctx, []event.Event{testEvent(p.ID, eventID(1), time.Now())})
	explode := func(ctx context.Context, q ArtifactQuerier, e *event.Event) Grouped {
		g := group(ctx, q, e)
		g.Title = "\x00" // Postgres rejects NUL in text: a deterministic failure
		return g
	}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		s.pool.Exec(ctx, `update ingest_queue set available_at = now()`)
		out, err := s.ProcessQueue(ctx, 10, explode)
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		wantDepth := int64(1)
		if attempt == maxAttempts {
			wantDepth = 0
		}
		assertQueueDepth(t, s, wantDepth)
		if out.Failed != 1 {
			t.Fatalf("attempt %d: outcome %+v", attempt, out)
		}
	}
	var queued, dead int
	s.pool.QueryRow(ctx, `select count(*) from ingest_queue`).Scan(&queued)
	s.pool.QueryRow(ctx, `select count(*) from ingest_dead_letter`).Scan(&dead)
	if queued != 0 || dead != 1 {
		t.Errorf("queued %d, dead %d", queued, dead)
	}
	var events int
	s.pool.QueryRow(ctx, `select count(*) from events`).Scan(&events)
	if events != 0 {
		t.Error("failed event left a partial row")
	}
}

func TestAcceptStripsNUL(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p, _ := s.CreateProject(ctx, "nul", "NUL")
	e := testEvent(p.ID, eventID(1), time.Now())
	e.Exceptions[0].Value = "bad\x00byte"
	e.Tags = map[string]string{"k\x00": "v\x00"}
	if err := s.Accept(ctx, []event.Event{e}); err != nil {
		t.Fatalf("NUL in an SDK payload must not block ingestion: %v", err)
	}
	if out, err := s.ProcessQueue(ctx, 10, group); err != nil || out.Stored != 1 {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestAcceptShedsLoad(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	s.MaxQueueDepth = 1
	p, _ := s.CreateProject(ctx, "web", "Web")
	err := s.Accept(ctx, []event.Event{testEvent(p.ID, eventID(1), time.Now()), testEvent(p.ID, eventID(2), time.Now())})
	if !errors.Is(err, adapter.ErrOverloaded) {
		t.Errorf("got %v, want ErrOverloaded", err)
	}
}

func join(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}

// The queue depth used for load shedding is an estimate: each store
// counts the queue at most once per ttl and adds what it accepts itself.
func TestQueueDepthEstimate(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p, err := s.CreateProject(ctx, "capacity", "Capacity")
	if err != nil {
		t.Fatal(err)
	}
	other, err := Open(ctx, s.pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	s.MaxQueueDepth, other.MaxQueueDepth = 2, 2
	s.depth.ttl, other.depth.ttl = time.Hour, time.Hour
	for i := 1; i <= 2; i++ {
		if err := s.Accept(ctx, []event.Event{testEvent(p.ID, eventID(i), time.Now())}); err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
	}
	// The store counts what it accepted itself, without recounting.
	if err := s.Accept(ctx, []event.Event{testEvent(p.ID, eventID(3), time.Now())}); !errors.Is(err, adapter.ErrOverloaded) {
		t.Fatalf("full queue: %v", err)
	}
	// Another store counts the queue on first use.
	if err := other.Accept(ctx, []event.Event{testEvent(p.ID, eventID(4), time.Now())}); !errors.Is(err, adapter.ErrOverloaded) {
		t.Fatalf("full queue seen by another store: %v", err)
	}
	if _, err := s.ProcessQueue(ctx, 100, group); err != nil {
		t.Fatal(err)
	}
	assertQueueDepth(t, s, 0)
	// Freed capacity is noticed at the next count.
	other.depth.ttl = 0
	if err := other.Accept(ctx, []event.Event{testEvent(p.ID, eventID(5), time.Now())}); err != nil {
		t.Fatalf("capacity not freed: %v", err)
	}
}

func assertQueueDepth(t *testing.T, s *Store, want int64) {
	t.Helper()
	var n int64
	if err := s.pool.QueryRow(context.Background(), `select count(*) from ingest_queue`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != want {
		t.Fatalf("queued=%d want=%d", n, want)
	}
}

func TestConcurrentQueueAdmissionWithAvailableCapacity(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p, err := s.CreateProject(ctx, "parallel", "Parallel")
	if err != nil {
		t.Fatal(err)
	}
	other, err := Open(ctx, s.pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	s.MaxQueueDepth, other.MaxQueueDepth = 20, 20
	results := make(chan error, 20)
	start := make(chan struct{})
	for i := 0; i < 20; i++ {
		go func(i int) {
			<-start
			target := s
			if i%2 == 0 {
				target = other
			}
			results <- target.Accept(ctx, []event.Event{testEvent(p.ID, eventID(i+1), time.Now())})
		}(i)
	}
	close(start)
	for i := 0; i < 20; i++ {
		if err := <-results; err != nil {
			t.Fatalf("capacity available: %v", err)
		}
	}
	assertQueueDepth(t, s, 20)
	if _, err := s.ProcessQueue(ctx, 100, group); err != nil {
		t.Fatal(err)
	}
	assertQueueDepth(t, s, 0)
}

func TestWorkerOutcomeExcludesRolledBackWork(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p, err := s.CreateProject(ctx, "outcome", "Outcome")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Accept(ctx, []event.Event{testEvent(p.ID, eventID(1), time.Now())}); err != nil {
		t.Fatal(err)
	}
	out, err := s.ProcessQueue(ctx, 10, func(ctx context.Context, q ArtifactQuerier, e *event.Event) Grouped {
		g := group(ctx, q, e)
		e.Environment = "\x00" // Fail after creating the issue and its activity.
		return g
	})
	if err != nil || out.Failed != 1 || out.NewIssues != 0 || out.Stored != 0 {
		t.Fatalf("outcome %+v: %v", out, err)
	}
	assertQueueDepth(t, s, 1)
	if _, err := s.pool.Exec(ctx, `update ingest_queue set available_at=now()`); err != nil {
		t.Fatal(err)
	}
	if err := s.Accept(ctx, []event.Event{testEvent(p.ID, eventID(2), time.Now())}); err != nil {
		t.Fatal(err)
	}
	processCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	calls := 0
	out, err = s.ProcessQueue(processCtx, 10, func(ctx context.Context, q ArtifactQuerier, e *event.Event) Grouped {
		calls++
		if calls == 2 {
			cancel()
		}
		return group(ctx, q, e)
	})
	if !errors.Is(err, context.Canceled) || out != (Outcome{}) {
		t.Fatalf("rollback outcome %+v: %v", out, err)
	}
	assertQueueDepth(t, s, 2)
}
