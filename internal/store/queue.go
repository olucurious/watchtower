package store

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/olucurious/watchtower/internal/adapter"
	"github.com/olucurious/watchtower/internal/event"
)

// Accept implements adapter.Sink: events are committed to the ingest queue
// before the SDK is acknowledged.
func (s *Store) Accept(ctx context.Context, events []event.Event) error {
	if len(events) == 0 {
		return nil
	}
	rows := make([][]any, 0, len(events))
	for i := range events {
		events[i].StripNUL()
		if err := events[i].Validate(); err != nil {
			return fmt.Errorf("event %d: %w", i, err)
		}
		data, err := json.Marshal(&events[i])
		if err != nil {
			return err
		}
		rows = append(rows, []any{events[i].ProjectID, data})
	}
	depth, err := s.depth.get(ctx, s.pool, s.MaxQueueDepth)
	if err != nil {
		return err
	}
	if depth+int64(len(events)) > int64(s.MaxQueueDepth) {
		return adapter.ErrOverloaded
	}
	// One COPY is atomic on its own, so no explicit transaction is needed.
	if _, err := s.pool.CopyFrom(ctx, pgx.Identifier{"ingest_queue"}, []string{"project_id", "event"}, pgx.CopyFromRows(rows)); err != nil {
		return err
	}
	s.depth.add(int64(len(events)))
	return nil
}

// depthGauge estimates the ingest queue's length for load shedding.
//
// An exact, shared counter would serialize every ingest commit across all
// replicas on one row. Instead each process counts the queue at most once
// per ttl, and adds what it accepts in between. Shedding is therefore
// approximate: replicas together can overshoot the limit by roughly one
// ttl of their combined traffic, which is fine for a back-pressure bound.
type depthGauge struct {
	ttl time.Duration

	mu         sync.Mutex
	value      int64
	counted    time.Time
	refreshing bool
}

func (g *depthGauge) get(ctx context.Context, db *pgxpool.Pool, limit int) (int64, error) {
	g.mu.Lock()
	// While one request recounts, the others use the previous estimate.
	if !g.counted.IsZero() && (g.refreshing || time.Since(g.counted) < g.ttl) {
		defer g.mu.Unlock()
		return g.value, nil
	}
	g.refreshing = true
	g.mu.Unlock()

	// Counting stops past the limit, so a large backlog costs a bounded scan.
	var n int64
	err := db.QueryRow(ctx, `select count(*) from (select from ingest_queue limit $1) q`, limit+1).Scan(&n)

	g.mu.Lock()
	defer g.mu.Unlock()
	g.refreshing = false
	if err != nil {
		return 0, fmt.Errorf("counting ingest queue: %w", err)
	}
	g.value, g.counted = n, time.Now()
	return n, nil
}

func (g *depthGauge) add(n int64) {
	g.mu.Lock()
	g.value += n
	g.mu.Unlock()
}

// Grouped is the worker's decision about where an event belongs.
type Grouped struct {
	Fingerprint, Title, Culprit string
}

// GroupFunc prepares an event (e.g. applies source maps, which may change
// it) and assigns it to an issue. q reads uploaded artifacts within the
// worker's transaction. It must be deterministic.
type GroupFunc func(ctx context.Context, q ArtifactQuerier, e *event.Event) Grouped

// Outcome reports what one ProcessQueue call did.
type Outcome struct {
	Stored, CountedOnly, Duplicates, NewIssues, Regressions, Failed, DeadLettered int
}

// Processed is how many claimed events the call handled; zero means the
// queue had nothing ready.
func (o Outcome) Processed() int { return o.Stored + o.CountedOnly + o.Duplicates + o.Failed }

const (
	maxAttempts  = 5
	retryBackoff = 30 * time.Second
)

// ProcessQueue claims up to limit queued events, groups them into issues
// and stores them.
//
// The batch is stored as a unit: each issue it touches is written once,
// however many of its events the batch holds, which keeps error storms
// cheap. Issues are locked in a fixed order, so concurrent workers never
// deadlock. If the batch fails, its events are stored one at a time
// instead, each in its own savepoint, so one bad event is retried (and
// eventually dead-lettered) without blocking the rest.
func (s *Store) ProcessQueue(ctx context.Context, limit int, group GroupFunc) (Outcome, error) {
	var out Outcome
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		out = Outcome{}
		items, err := claim(ctx, tx, limit)
		if err != nil || len(items) == 0 {
			return err
		}
		ready, failed := prepareAll(ctx, tx, items, group)
		var removed []int64
		var batch Outcome
		err = pgx.BeginFunc(ctx, tx, func(sp pgx.Tx) error { return s.storeBatch(ctx, sp, ready, &batch) })
		switch {
		case err == nil:
			out = batch
			for _, p := range ready {
				removed = append(removed, p.item.id)
			}
		case ctx.Err() != nil:
			return ctx.Err()
		case retryable(err):
			// Contention, not a bad event: retry the batch later.
			return err
		default:
			for _, p := range ready {
				var one Outcome
				err := pgx.BeginFunc(ctx, tx, func(sp pgx.Tx) error { return s.storeBatch(ctx, sp, []*prepared{p}, &one) })
				switch {
				case err == nil:
					out.add(one)
					removed = append(removed, p.item.id)
				case ctx.Err() != nil:
					return ctx.Err()
				case retryable(err):
					return err
				default:
					p.err = err
					failed = append(failed, p)
				}
			}
		}
		for _, p := range failed {
			out.Failed++
			dead, err := retryLater(ctx, tx, p)
			if err != nil {
				return err
			}
			if dead {
				out.DeadLettered++
				removed = append(removed, p.item.id)
			}
		}
		if len(removed) > 0 {
			if _, err := tx.Exec(ctx, `delete from ingest_queue where id = any($1)`, removed); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Outcome{}, err
	}
	return out, nil
}

func (o *Outcome) add(b Outcome) {
	o.Stored += b.Stored
	o.CountedOnly += b.CountedOnly
	o.Duplicates += b.Duplicates
	o.NewIssues += b.NewIssues
	o.Regressions += b.Regressions
}

// retryable reports whether err comes from contention between
// transactions (a deadlock or serialization failure) rather than from the
// events themselves.
func retryable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "40P01" || pgErr.Code == "40001")
}

type queued struct {
	id       int64
	data     []byte
	attempts int
}

func claim(ctx context.Context, tx pgx.Tx, limit int) ([]queued, error) {
	rows, err := tx.Query(ctx, `
		select id, event, attempts from ingest_queue
		where available_at <= now() order by id limit $1 for update skip locked`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (queued, error) {
		var q queued
		err := r.Scan(&q.id, &q.data, &q.attempts)
		return q, err
	})
}

// prepared is a queued event decoded, prepared and grouped, ready to store.
type prepared struct {
	item queued
	e    event.Event
	g    Grouped
	data []byte // the prepared event as stored: symbolication may rewrite frames
	err  error  // why it could not be stored

	duplicate bool // already stored (an SDK retry); set by insertEvents
	countOnly bool // counted towards its issue but not stored; set by limitStorage
}

// prepareAll decodes and groups the claimed events. The ones that can be
// stored come back sorted by issue, which is the order issues are locked
// in; the rest come back as failed.
func prepareAll(ctx context.Context, tx pgx.Tx, items []queued, group GroupFunc) (ready, failed []*prepared) {
	for _, it := range items {
		p := &prepared{item: it}
		if err := json.Unmarshal(it.data, &p.e); err != nil {
			p.err = fmt.Errorf("decoding queued event: %w", err)
			failed = append(failed, p)
			continue
		}
		p.g = group(ctx, tx, &p.e)
		if p.data, p.err = json.Marshal(&p.e); p.err != nil {
			failed = append(failed, p)
			continue
		}
		ready = append(ready, p)
	}
	// Stable, so each issue's events keep their queue order.
	slices.SortStableFunc(ready, func(a, b *prepared) int {
		return cmp.Or(cmp.Compare(a.e.ProjectID, b.e.ProjectID), strings.Compare(a.g.Fingerprint, b.g.Fingerprint))
	})
	return ready, failed
}

// retryLater schedules a failed event for another attempt with backoff,
// or moves it to the dead-letter table after its last attempt.
func retryLater(ctx context.Context, tx pgx.Tx, p *prepared) (dead bool, err error) {
	attempts := p.item.attempts + 1
	if attempts >= maxAttempts {
		_, err := tx.Exec(ctx, `
			insert into ingest_dead_letter (id, project_id, event, enqueued_at, attempts, last_error)
			select id, project_id, event, enqueued_at, $2, $3 from ingest_queue where id = $1`,
			p.item.id, attempts, p.err.Error())
		return true, err
	}
	_, err = tx.Exec(ctx, `
		update ingest_queue set attempts = $2, last_error = $3, available_at = now() + $4::interval where id = $1`,
		p.item.id, attempts, p.err.Error(), fmt.Sprintf("%d seconds", int(retryBackoff.Seconds())*attempts))
	return false, err
}

// pendingIssue is one issue a batch touches, with the batch's events for it.
type pendingIssue struct {
	id      int64
	status  string // before this batch
	created bool
	events  []*prepared
}

// storeBatch stores prepared events, sorted by issue, in tx.
func (s *Store) storeBatch(ctx context.Context, tx pgx.Tx, events []*prepared, out *Outcome) error {
	var issues []*pendingIssue
	for _, p := range events {
		if n := len(issues); n > 0 {
			if last := issues[n-1].events[0]; last.e.ProjectID == p.e.ProjectID && last.g.Fingerprint == p.g.Fingerprint {
				issues[n-1].events = append(issues[n-1].events, p)
				continue
			}
		}
		issues = append(issues, &pendingIssue{events: []*prepared{p}})
	}
	if err := lockIssues(ctx, tx, issues); err != nil {
		return err
	}
	if err := s.limitStorage(ctx, tx, issues); err != nil {
		return err
	}
	if err := insertEvents(ctx, tx, issues); err != nil {
		return err
	}

	// One write per issue, pipelined.
	b := &pgx.Batch{}
	type alertCheck struct {
		issue   *pendingIssue
		events  []*event.Event // counted, in queue order
		stored  map[*event.Event]bool
		trigger string
	}
	var alerts []alertCheck
	for _, is := range issues {
		var kept []*prepared
		storedEvents := map[*event.Event]bool{}
		for _, p := range is.events {
			switch {
			case p.duplicate:
				out.Duplicates++
			case p.countOnly:
				out.CountedOnly++
				kept = append(kept, p)
			default:
				out.Stored++
				storedEvents[&p.e] = true
				kept = append(kept, p)
			}
		}
		if len(kept) == 0 {
			if is.created { // only possible if every event was a duplicate
				b.Queue(`delete from issues where id = $1`, is.id)
			}
			continue
		}
		first, last := &kept[0].e, &kept[len(kept)-1].e
		firstSeen, lastSeen, release := first.Timestamp, first.Timestamp, ""
		var envs []string
		stored := make([]*event.Event, len(kept))
		occurred := make([]time.Time, len(kept))
		for i, p := range kept {
			stored[i], occurred[i] = &p.e, p.e.Timestamp
			firstSeen, lastSeen = minTime(firstSeen, p.e.Timestamp), maxTime(lastSeen, p.e.Timestamp)
			if p.e.Release != "" {
				release = p.e.Release
			}
			if p.e.Environment != "" && !slices.Contains(envs, p.e.Environment) {
				envs = append(envs, p.e.Environment)
			}
		}
		g := kept[len(kept)-1].g
		b.Queue(`
			update issues set
				times_seen = times_seen + $2,
				first_seen = least(first_seen, $3),
				last_seen = greatest(last_seen, $4),
				title = $5, culprit = $6, level = $7,
				last_release = case when $8 = '' then last_release else $8 end,
				environments = environments || array(select e from unnest($9::text[]) e where e <> all(environments))
			where id = $1`,
			is.id, len(kept), firstSeen, lastSeen, g.Title, g.Culprit, string(last.Level), release, envs)
		b.Queue(`
			insert into issue_hourly (issue_id, hour, events)
			select $1, date_trunc('hour', t, 'UTC'), count(*) from unnest($2::timestamptz[]) t group by 2
			on conflict (issue_id, hour) do update set events = issue_hourly.events + excluded.events`,
			is.id, occurred)
		trigger := ""
		switch {
		case is.created:
			out.NewIssues++
			trigger = "new_issue"
			b.Queue(`insert into issue_activity (issue_id, kind, at) values ($1, 'first_seen', $2)`, is.id, first.Timestamp)
		case is.status == "resolved":
			// A resolved issue happening again is a regression: reopen it.
			out.Regressions++
			trigger = "regression"
			detail, _ := json.Marshal(map[string]string{"release": first.Release, "event_id": first.ID})
			b.Queue(`update issues set status = 'unresolved', regressed_at = now() where id = $1`, is.id)
			b.Queue(`insert into issue_activity (issue_id, kind, detail) values ($1, 'regressed', $2)`, is.id, detail)
		}
		alerts = append(alerts, alertCheck{is, stored, storedEvents, trigger})
	}
	if err := tx.SendBatch(ctx, b).Close(); err != nil {
		return err
	}

	channels := map[int64][]alertChannelRule{}
	for _, a := range alerts {
		if a.trigger == "regression" {
			if err := s.emailOwnerOfRegression(ctx, tx, a.issue.id, a.events[0]); err != nil {
				return err
			}
		}
		project := a.events[0].ProjectID
		rules, ok := channels[project]
		if !ok {
			var err error
			if rules, err = alertRules(ctx, tx, project); err != nil {
				return fmt.Errorf("queueing alerts: %w", err)
			}
			channels[project] = rules
		}
		status := a.issue.status
		if a.issue.created {
			status = "new"
		}
		if err := queueAlerts(ctx, tx, rules, a.events, a.stored, a.issue.id, status, a.trigger); err != nil {
			return fmt.Errorf("queueing alerts: %w", err)
		}
	}
	return nil
}

// lockIssues finds or creates each issue and locks it until the
// transaction ends. Every worker locks issues in (project, fingerprint)
// order, so concurrent batches wait for each other but never deadlock.
func lockIssues(ctx context.Context, tx pgx.Tx, issues []*pendingIssue) error {
	b := &pgx.Batch{}
	for _, is := range issues {
		e, g := &is.events[0].e, is.events[0].g
		b.Queue(`
			insert into issues (project_id, fingerprint, title, culprit, level, first_seen, last_seen,
				times_seen, first_release, last_release)
			values ($1, $2, $3, $4, $5, $6, $6, 0, $7, $7)
			on conflict (project_id, fingerprint) do nothing returning id`,
			e.ProjectID, g.Fingerprint, g.Title, g.Culprit, string(e.Level), e.Timestamp, e.Release)
		b.Queue(`select id, status from issues where project_id = $1 and fingerprint = $2 for update`, e.ProjectID, g.Fingerprint)
	}
	br := tx.SendBatch(ctx, b)
	for _, is := range issues {
		var id int64
		switch err := br.QueryRow().Scan(&id); {
		case err == nil:
			is.created = true
		case !errors.Is(err, pgx.ErrNoRows):
			br.Close()
			return err
		}
		if err := br.QueryRow().Scan(&is.id, &is.status); err != nil {
			br.Close()
			return err
		}
	}
	return br.Close()
}

// sampleEvery keeps one in this many events past an issue's hourly
// storage limit, so later events (a new release, say) still have examples.
const sampleEvery = 1000

// limitStorage decides which events are stored in full. Each issue stores
// its first StoredPerIssueHour events of every hour, then one in
// sampleEvery; the rest are only counted, so an error storm costs counts,
// not storage. An issue's first event and the event that reopens it are
// always stored, since alerts and emails link to them. Issues are locked,
// so the hourly counts read here are current.
func (s *Store) limitStorage(ctx context.Context, tx pgx.Tx, issues []*pendingIssue) error {
	if s.StoredPerIssueHour <= 0 {
		return nil
	}
	type bucket struct {
		issue int64
		hour  time.Time
	}
	var ids []int64
	var hours []time.Time
	for _, is := range issues {
		ids = append(ids, is.id)
		for _, p := range is.events {
			hours = append(hours, p.e.Timestamp.UTC().Truncate(time.Hour))
		}
	}
	rows, err := tx.Query(ctx, `select issue_id, hour, events from issue_hourly where issue_id = any($1) and hour = any($2)`, ids, hours)
	if err != nil {
		return err
	}
	seen := map[bucket]int64{}
	var k bucket
	var n int64
	if _, err := pgx.ForEachRow(rows, []any{&k.issue, &k.hour, &n}, func() error { seen[bucket{k.issue, k.hour.UTC()}] = n; return nil }); err != nil {
		return err
	}
	for _, is := range issues {
		for i, p := range is.events {
			k := bucket{is.id, p.e.Timestamp.UTC().Truncate(time.Hour)}
			n := seen[k]
			seen[k]++
			changesState := i == 0 && (is.created || is.status == "resolved")
			p.countOnly = !changesState && n >= int64(s.StoredPerIssueHour) && n%sampleEvery != 0
		}
	}
	return nil
}

// insertEvents writes the batch's stored events in one statement. Events
// already stored (an SDK retry), and repeats within the batch, are marked
// as duplicates so they don't count towards their issue. Events that are
// only counted have no row to check, so only repeats within the batch are
// caught for them.
func insertEvents(ctx context.Context, tx pgx.Tx, issues []*pendingIssue) error {
	var projects, issueIDs []int64
	var ids, data []string
	var occurred, received []time.Time
	for _, is := range issues {
		for _, p := range is.events {
			if p.countOnly {
				continue
			}
			projects, issueIDs = append(projects, p.e.ProjectID), append(issueIDs, is.id)
			ids, data = append(ids, p.e.ID), append(data, string(p.data))
			occurred, received = append(occurred, p.e.Timestamp), append(received, p.e.ReceivedAt)
		}
	}
	type key struct {
		project int64
		id      string
	}
	inserted := map[key]bool{}
	if len(ids) > 0 {
		rows, err := tx.Query(ctx, `
			insert into events (project_id, event_id, issue_id, occurred_at, received_at, data)
			select * from unnest($1::bigint[], $2::text[], $3::bigint[], $4::timestamptz[], $5::timestamptz[], $6::text[]::jsonb[])
			on conflict do nothing returning project_id, event_id`,
			projects, ids, issueIDs, occurred, received, data)
		if err != nil {
			return err
		}
		var k key
		if _, err := pgx.ForEachRow(rows, []any{&k.project, &k.id}, func() error { inserted[k] = true; return nil }); err != nil {
			return err
		}
	}
	seen := map[key]bool{}
	for _, is := range issues {
		for _, p := range is.events {
			k := key{p.e.ProjectID, p.e.ID}
			p.duplicate = seen[k] || (!p.countOnly && !inserted[k])
			seen[k] = true
		}
	}
	return nil
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// emailOwnerOfRegression tells an issue's owner, if any, that it regressed.
func (s *Store) emailOwnerOfRegression(ctx context.Context, tx pgx.Tx, issueID int64, e *event.Event) error {
	var owner *int64
	if err := tx.QueryRow(ctx, `select assignee_id from issues where id = $1`, issueID).Scan(&owner); err != nil || owner == nil {
		return err
	}
	return s.queueEmail(ctx, tx, *owner, "regressed", issueID, map[string]string{"release": e.Release, "event_id": e.ID, "environment": e.Environment})
}
