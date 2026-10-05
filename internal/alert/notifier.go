package alert

import (
	"context"
	"log/slog"
	"time"

	"github.com/olucurious/watchtower/internal/store"
)

// MaxAttempts is how often a notification is tried before it is left
// undelivered (it stays visible as the channel's last error).
const MaxAttempts = 8

type Outbox interface {
	DeliverNotifications(ctx context.Context, limit, maxAttempts int,
		send func(context.Context, *store.Notification) (time.Duration, error)) (int, int, error)
}

type Counter interface {
	Add(name string, n int)
}

// Notifier drains the notification outbox.
type Notifier struct {
	Outbox  Outbox
	Sealer  *Sealer
	Slack   *Slack
	Tracker *Tracker // Linear channels
	Log     *slog.Logger
	Metrics Counter
	Idle    time.Duration
}

func (n *Notifier) Run(ctx context.Context) {
	for {
		sent, failed, err := n.Outbox.DeliverNotifications(ctx, 10, MaxAttempts, n.send)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			n.Log.Error("delivering notifications", "err", err)
		}
		n.Metrics.Add("alerts_sent", sent)
		n.Metrics.Add("alerts_failed", failed)
		if sent+failed == 0 || err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(n.Idle):
			}
		}
	}
}

func (n *Notifier) send(ctx context.Context, msg *store.Notification) (time.Duration, error) {
	if msg.Channel.Kind == "linear" {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		after, err := n.Tracker.Notify(ctx, msg)
		if err != nil {
			n.Log.Warn("alert delivery failed", "channel", msg.Channel.Name, "kind", msg.Kind, "attempt", msg.Attempts+1, "err", err)
		}
		return after, err
	}
	target, err := n.Sealer.Open(msg.Channel.Sealed)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var after time.Duration
	if msg.Channel.Kind == "slack_bot" {
		after, err = n.Slack.SendBot(ctx, target, msg.Channel.Target, msg)
	} else {
		after, err = n.Slack.Send(ctx, target, msg)
	}
	if err != nil {
		n.Log.Warn("alert delivery failed", "channel", msg.Channel.Name, "kind", msg.Kind, "attempt", msg.Attempts+1, "err", err)
	}
	return after, err
}
