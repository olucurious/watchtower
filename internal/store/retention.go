package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Pruned reports what one retention pass removed.
type Pruned struct {
	Issues, Events, DeadLetters, Notifications, Emails, Bundles, Chunks int64
}

// pruneBatch bounds each delete so a large backlog is removed in short
// transactions instead of one long lock.
const pruneBatch = 5000

// Prune deletes data older than cutoff: issues not seen since then (with
// their events and activity), provided none of their events was received
// recently. It also removes older events of issues that are still
// active, and old dead letters.
func (s *Store) Prune(ctx context.Context, cutoff time.Time) (Pruned, error) {
	var p Pruned
	var hourly int64 // not reported: counts, not data anyone reads alone
	for {
		n, err := s.pruneIssues(ctx, cutoff)
		if err != nil {
			return p, err
		}
		p.Issues += n
		if n < pruneBatch {
			break
		}
	}
	steps := []struct {
		n   *int64
		sql string
	}{
		{&p.Events, `delete from events where (project_id, event_id) in
			(select project_id, event_id from events where received_at < $1 limit $2)`},
		{&hourly, `delete from issue_hourly where (issue_id, hour) in
			(select issue_id, hour from issue_hourly where hour < $1 limit $2)`},
		{&p.Notifications, `delete from notifications where id in (select id from notifications where created_at < $1 limit $2)`},
		{&p.Emails, `delete from emails where id in (select id from emails where created_at < $1 limit $2)`},
		{&p.Bundles, `delete from artifact_bundles where id in (select id from artifact_bundles where created_at < $1 limit $2)`},
		{&p.DeadLetters, `delete from ingest_dead_letter where id in (select id from ingest_dead_letter where failed_at < $1 limit $2)`},
	}
	for _, st := range steps {
		for {
			tag, err := s.pool.Exec(ctx, st.sql, cutoff, pruneBatch)
			if err != nil {
				return p, err
			}
			*st.n += tag.RowsAffected()
			if tag.RowsAffected() < pruneBatch {
				break
			}
		}
	}
	// Chunks belong to uploads in progress; a day is long enough.
	tag, err := s.pool.Exec(ctx, `delete from upload_chunks where created_at < now() - interval '1 day'`)
	if err != nil {
		return p, err
	}
	p.Chunks = tag.RowsAffected()
	return p, nil
}

// Lock candidates first, then recheck receipt times in a fresh snapshot.
// A worker that completed between selection and locking must not have its
// newly stored event cascaded away by a stale retention decision.
func (s *Store) pruneIssues(ctx context.Context, cutoff time.Time) (int64, error) {
	var n int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select i.id from issues i where i.last_seen < $1
			and not exists (select 1 from events e where e.issue_id = i.id and e.received_at >= $1)
			limit $2 for update of i skip locked`, cutoff, pruneBatch)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `delete from issues i where i.id = any($1) and i.last_seen < $2
			and not exists (select 1 from events e where e.issue_id = i.id and e.received_at >= $2)`, ids, cutoff)
		n = tag.RowsAffected()
		return err
	})
	return n, err
}
