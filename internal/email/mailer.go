package email

import (
	"context"
	"log/slog"
	"net/mail"
	"time"

	"github.com/olucurious/watchtower/internal/store"
)

// MaxAttempts bounds retries of one email.
const MaxAttempts = 8

type Outbox interface {
	DeliverEmails(ctx context.Context, limit, maxAttempts int, send func(context.Context, *store.Email) error) (int, int, error)
	QueueDigests(ctx context.Context, now time.Time) (int64, error)
	Digest(ctx context.Context, since, until time.Time) (store.Digest, error)
}

type Counter interface {
	Add(name string, n int)
}

// Mailer schedules digests and drains the email outbox.
type Mailer struct {
	Outbox    Outbox
	Sender    Sender
	PublicURL string
	Log       *slog.Logger
	Metrics   Counter
	Idle      time.Duration
	// DigestCheck is how often due digests are looked for.
	DigestCheck time.Duration
}

func (m *Mailer) Run(ctx context.Context) {
	var lastCheck time.Time
	for {
		if time.Since(lastCheck) >= m.DigestCheck {
			lastCheck = time.Now()
			if n, err := m.Outbox.QueueDigests(ctx, time.Now()); err != nil && ctx.Err() == nil {
				m.Log.Error("scheduling digests", "err", err)
			} else if n > 0 {
				m.Log.Info("digests scheduled", "count", n)
			}
		}
		sent, failed, err := m.Outbox.DeliverEmails(ctx, 10, MaxAttempts, m.send)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			m.Log.Error("delivering emails", "err", err)
		}
		m.Metrics.Add("emails_sent", sent)
		m.Metrics.Add("emails_failed", failed)
		if sent+failed == 0 || err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(m.Idle):
			}
		}
	}
}

func (m *Mailer) send(ctx context.Context, e *store.Email) error {
	var digest *store.Digest
	if e.Kind == "digest" {
		since, err1 := time.Parse(time.RFC3339, e.Detail["since"])
		until, err2 := time.Parse(time.RFC3339, e.Detail["until"])
		if err1 != nil || err2 != nil {
			return store.ErrSkipEmail
		}
		d, err := m.Outbox.Digest(ctx, since, until)
		if err != nil {
			return err
		}
		if d.Empty() {
			return store.ErrSkipEmail
		}
		digest = &d
	}
	c, err := Compose(e, digest, m.PublicURL)
	if err != nil {
		return err
	}
	msg, err := Render(mail.Address{Name: e.To.Name, Address: e.To.Email}, c)
	if err != nil {
		return err
	}
	if err := m.Sender.Send(ctx, msg); err != nil {
		m.Log.Warn("email delivery failed", "kind", e.Kind, "attempt", e.Attempts+1, "err", err)
		return err
	}
	return nil
}
