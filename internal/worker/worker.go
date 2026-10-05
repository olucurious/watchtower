// Package worker drains the ingest queue into issues and events.
package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/olucurious/watchtower/internal/event"
	"github.com/olucurious/watchtower/internal/grouping"
	"github.com/olucurious/watchtower/internal/scrub"
	"github.com/olucurious/watchtower/internal/store"
	"github.com/olucurious/watchtower/internal/symbolicate"
)

type Queue interface {
	ProcessQueue(ctx context.Context, limit int, group store.GroupFunc) (store.Outcome, error)
}

type Counter interface {
	Add(name string, n int)
}

type Worker struct {
	Queue     Queue
	Grouper   *Grouper
	Log       *slog.Logger
	Metrics   Counter
	BatchSize int
	Idle      time.Duration // pause when the queue is empty
}

// Grouper prepares and groups events: it applies source maps first, so
// minified JavaScript groups by its original code location.
type Grouper struct {
	Symbolicator *symbolicate.Symbolicator
	Log          *slog.Logger
}

func (g *Grouper) Group(ctx context.Context, q store.ArtifactQuerier, e *event.Event) store.Grouped {
	if g.Symbolicator != nil {
		if _, err := g.Symbolicator.Event(ctx, q, e); err != nil {
			g.Log.Warn("source map lookup failed; keeping minified frames", "event_id", e.ID, "err", err)
		}
	}
	scrub.Event(e)
	return Group(e)
}

// Group is the grouping decision for one prepared event.
func Group(e *event.Event) store.Grouped {
	return store.Grouped{
		Fingerprint: grouping.Fingerprint(e),
		Title:       e.Title(),
		Culprit:     e.Culprit(),
	}
}

// Run processes batches until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	backoff := w.Idle
	for {
		out, err := w.Queue.ProcessQueue(ctx, w.BatchSize, w.Grouper.Group)
		if ctx.Err() != nil {
			return
		}
		wait := time.Duration(0)
		switch {
		case err != nil:
			w.Log.Error("processing ingest queue", "err", err)
			wait, backoff = backoff, min(backoff*2, 30*time.Second)
		case out.Stored+out.CountedOnly+out.Duplicates+out.Failed == 0:
			wait, backoff = w.Idle, w.Idle
		default:
			backoff = w.Idle
		}
		w.record(out)
		if wait > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
	}
}

func (w *Worker) record(o store.Outcome) {
	w.Metrics.Add("worker_events_stored", o.Stored)
	w.Metrics.Add("worker_events_counted_only", o.CountedOnly)
	w.Metrics.Add("worker_events_duplicate", o.Duplicates)
	w.Metrics.Add("worker_issues_created", o.NewIssues)
	w.Metrics.Add("worker_issues_regressed", o.Regressions)
	w.Metrics.Add("worker_events_failed", o.Failed)
	w.Metrics.Add("worker_events_dead_lettered", o.DeadLettered)
	if o.DeadLettered > 0 {
		w.Log.Warn("events moved to dead letter", "count", o.DeadLettered)
	}
}
