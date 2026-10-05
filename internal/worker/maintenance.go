package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/olucurious/watchtower/internal/store"
)

type Maintainer interface {
	Prune(ctx context.Context, cutoff time.Time) (store.Pruned, error)
	DeleteExpiredSessions(ctx context.Context) error
}

// Maintenance runs housekeeping on an interval: retention and expired
// sessions. Running it on several replicas at once is safe, only wasteful.
type Maintenance struct {
	Store     Maintainer
	Log       *slog.Logger
	Metrics   Counter
	Retention time.Duration
	Every     time.Duration
}

func (m *Maintenance) Run(ctx context.Context) {
	for {
		m.once(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(m.Every):
		}
	}
}

func (m *Maintenance) once(ctx context.Context) {
	p, err := m.Store.Prune(ctx, time.Now().Add(-m.Retention))
	if err != nil && ctx.Err() == nil {
		m.Log.Error("retention prune failed", "err", err)
	}
	m.Metrics.Add("retention_issues_deleted", int(p.Issues))
	m.Metrics.Add("retention_events_deleted", int(p.Events))
	if p.Issues+p.Events+p.DeadLetters > 0 {
		m.Log.Info("retention pruned", "issues", p.Issues, "events", p.Events, "dead_letters", p.DeadLetters)
	}
	if err := m.Store.DeleteExpiredSessions(ctx); err != nil && ctx.Err() == nil {
		m.Log.Warn("deleting expired sessions", "err", err)
	}
}
