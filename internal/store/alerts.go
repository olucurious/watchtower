package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/olucurious/watchtower/internal/event"
)

// AlertChannel is a destination plus the rules that send to it. The
// sealed target never leaves the store except to the notifier.
type AlertChannel struct {
	ID                 int64      `json:"id"`
	Kind               string     `json:"kind"` // "slack" (webhook), "slack_bot" (bot token) or "linear"
	Name               string     `json:"name"`
	TargetHint         string     `json:"target_hint"`
	TargetChannel      string     `json:"target_channel"` // Slack channel ID, or Linear team ID
	TargetLabel        string     `json:"target_label"`   // Linear team name
	OnNewIssue         bool       `json:"on_new_issue"`
	OnRegression       bool       `json:"on_regression"`
	FrequencyThreshold *int       `json:"frequency_threshold"`
	MinLevel           string     `json:"min_level"`
	Environment        string     `json:"environment"`
	CreatedBy          string     `json:"created_by"`
	CreatedAt          time.Time  `json:"created_at"`
	LastDeliveryAt     *time.Time `json:"last_delivery_at"`
	LastError          *string    `json:"last_error"`
}

// AlertRules are the editable settings of a channel.
type AlertRules struct {
	Name               string
	OnNewIssue         bool
	OnRegression       bool
	FrequencyThreshold *int
	MinLevel           string
	Environment        string
}

func (s *Store) AlertChannels(ctx context.Context, projectSlug string) ([]AlertChannel, error) {
	rows, err := s.pool.Query(ctx, `
		select c.id, c.kind, c.name, c.target_hint, c.target_channel, c.target_label, c.on_new_issue, c.on_regression, c.frequency_threshold,
			c.min_level, c.environment, coalesce(u.email, ''), c.created_at, c.last_delivery_at, c.last_error
		from alert_channels c join projects p on p.id = c.project_id left join users u on u.id = c.created_by
		where p.slug = $1 order by c.created_at`, projectSlug)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[AlertChannel])
}

// AlertTarget is where a channel delivers: a sealed webhook URL or bot
// token, its display hint, and (for bot tokens) the Slack channel ID.
type AlertTarget struct {
	Sealed  []byte
	Hint    string
	Channel string // Slack channel ID or Linear team ID
	Label   string // Linear team name
}

func (s *Store) CreateAlertChannel(ctx context.Context, projectSlug, kind string, t AlertTarget, r AlertRules, createdBy int64) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		insert into alert_channels (project_id, kind, name, target_sealed, target_hint, target_channel, target_label, on_new_issue,
			on_regression, frequency_threshold, min_level, environment, created_by)
		select p.id, $2, $3, $4, $5, $6, $13, $7, $8, $9, $10, $11, nullif($12, 0) from projects p where p.slug = $1
		returning id`,
		projectSlug, kind, r.Name, t.Sealed, t.Hint, t.Channel, r.OnNewIssue, r.OnRegression, r.FrequencyThreshold,
		r.MinLevel, r.Environment, createdBy, t.Label).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

// UpdateAlertChannel replaces a channel's rules, its sealed target when
// t.Sealed is non-nil, and its Slack channel ID when t.Channel is set.
func (s *Store) UpdateAlertChannel(ctx context.Context, projectSlug string, id int64, r AlertRules, t AlertTarget) error {
	tag, err := s.pool.Exec(ctx, `
		update alert_channels c set name = $3, on_new_issue = $4, on_regression = $5, frequency_threshold = $6,
			min_level = $7, environment = $8,
			target_sealed = coalesce($9, c.target_sealed),
			target_hint = case when $9::bytea is null then c.target_hint else $10 end,
			target_channel = coalesce(nullif($11, ''), c.target_channel),
			target_label = case when $11 = '' then c.target_label else $12 end
		from projects p where p.id = c.project_id and p.slug = $1 and c.id = $2`,
		projectSlug, id, r.Name, r.OnNewIssue, r.OnRegression, r.FrequencyThreshold, r.MinLevel, r.Environment, t.Sealed, t.Hint, t.Channel, t.Label)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) DeleteAlertChannel(ctx context.Context, projectSlug string, id int64) error {
	tag, err := s.pool.Exec(ctx, `
		delete from alert_channels c using projects p where p.id = c.project_id and p.slug = $1 and c.id = $2`, projectSlug, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// QueueTestNotification schedules a test message for one channel.
func (s *Store) QueueTestNotification(ctx context.Context, projectSlug string, id int64, actor string) error {
	detail, _ := json.Marshal(map[string]string{"actor": actor})
	tag, err := s.pool.Exec(ctx, `
		insert into notifications (channel_id, kind, detail)
		select c.id, 'test', $3 from alert_channels c join projects p on p.id = c.project_id
		where p.slug = $1 and c.id = $2`, projectSlug, id, detail)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

var levelRank = map[event.Level]int{event.LevelDebug: 0, event.LevelInfo: 1, event.LevelWarning: 2, event.LevelError: 3, event.LevelFatal: 4}

// frequencyCooldown is the minimum gap between frequency alerts for the
// same issue on the same channel.
const frequencyCooldown = time.Hour

// alertChannelRule is what the worker needs to decide whether a channel
// wants an alert.
type alertChannelRule struct {
	id                    int64
	onNew, onRegression   bool
	threshold             *int
	minLevel, environment string
}

// alertRules loads a project's channels, once per batch.
func alertRules(ctx context.Context, tx pgx.Tx, projectID int64) ([]alertChannelRule, error) {
	rows, err := tx.Query(ctx, `
		select id, on_new_issue, on_regression, frequency_threshold, min_level, environment
		from alert_channels where project_id = $1`, projectID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (alertChannelRule, error) {
		var c alertChannelRule
		err := r.Scan(&c.id, &c.onNew, &c.onRegression, &c.threshold, &c.minLevel, &c.environment)
		return c, err
	})
}

// queueAlerts writes notifications for an issue that just received
// events (in queue order), inside the worker's transaction. trigger is
// "new_issue" or "regression" when the first event changed the issue's
// state, otherwise "". Channel filters apply event by event, as if each
// event were processed alone.
func queueAlerts(ctx context.Context, tx pgx.Tx, channels []alertChannelRule, events []*event.Event, issueID int64, issueStatus, trigger string) error {
	if issueStatus == "muted" || len(channels) == 0 {
		return nil
	}
	var hourCount *int64
	for _, c := range channels {
		matches := func(e *event.Event) bool {
			return levelRank[e.Level] >= levelRank[event.Level(c.minLevel)] && (c.environment == "" || c.environment == e.Environment)
		}
		candidates := events
		if first := events[0]; trigger != "" && matches(first) {
			wanted := (trigger == "new_issue" && c.onNew) || (trigger == "regression" && c.onRegression)
			if wanted {
				detail, _ := json.Marshal(map[string]string{"event_id": first.ID, "release": first.Release, "environment": first.Environment})
				if _, err := tx.Exec(ctx, `insert into notifications (channel_id, issue_id, kind, detail) values ($1, $2, $3, $4)`, c.id, issueID, trigger, detail); err != nil {
					return err
				}
				candidates = events[1:] // the first event's alert was the state change
			}
		}
		if c.threshold == nil {
			continue
		}
		var e *event.Event // the latest event this channel would hear about
		for _, cand := range candidates {
			if matches(cand) {
				e = cand
			}
		}
		if e == nil {
			continue
		}
		// The cooldown check is an index lookup, so it goes first.
		var recent bool
		if err := tx.QueryRow(ctx, `
			select exists(select 1 from notifications where channel_id = $1 and issue_id = $2 and kind = 'frequency'
				and created_at > now() - $3::interval)`, c.id, issueID, fmt.Sprintf("%d seconds", int(frequencyCooldown.Seconds()))).Scan(&recent); err != nil {
			return err
		}
		if recent {
			continue
		}
		if hourCount == nil {
			// Counting stops at the largest threshold, so a busy issue
			// doesn't cost a count of every event in the hour.
			limit := 0
			for _, c := range channels {
				if c.threshold != nil {
					limit = max(limit, *c.threshold)
				}
			}
			var n int64
			if err := tx.QueryRow(ctx, `select count(*) from (select from events where issue_id = $1 and occurred_at > now() - interval '1 hour' limit $2) q`,
				issueID, limit).Scan(&n); err != nil {
				return err
			}
			hourCount = &n
		}
		if *hourCount < int64(*c.threshold) {
			continue
		}
		d, _ := json.Marshal(map[string]any{"event_id": e.ID, "release": e.Release, "environment": e.Environment, "count": *hourCount, "threshold": *c.threshold})
		if _, err := tx.Exec(ctx, `insert into notifications (channel_id, issue_id, kind, detail) values ($1, $2, 'frequency', $3)`, c.id, issueID, d); err != nil {
			return err
		}
	}
	return nil
}

// Notification is a claimed outbox row with everything needed to send it.
type Notification struct {
	ID       int64
	Attempts int
	Kind     string
	Detail   map[string]any
	Channel  struct {
		ID     int64
		Kind   string
		Name   string
		Sealed []byte
		Target string // Slack channel ID for "slack_bot" channels
	}
	Project struct{ Slug, Name string }
	Issue   *IssueRow // nil for test messages
}

// DeliverNotifications claims due notifications and calls send for each,
// recording the outcome. A send error schedules a retry with backoff until
// maxAttempts; retryAfter > 0 overrides the backoff (e.g. Slack's 429).
func (s *Store) DeliverNotifications(ctx context.Context, limit, maxAttempts int,
	send func(context.Context, *Notification) (retryAfter time.Duration, err error)) (sent, failed int, err error) {
	deliveryCtx := ctx
	// Slack webhooks are delivered at least once: a crash after Slack accepts
	// a message but before its outcome commits can still cause a retry.
	// Commit each outcome separately so later failures cannot replay a batch.
	for i := 0; i < limit; i++ {
		if err = ctx.Err(); err != nil {
			return sent, failed, err
		}
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		var didSend, didFail bool
		err = pgx.BeginFunc(finishCtx, s.pool, func(tx pgx.Tx) error {
			// Keep the row locked through delivery and its committed outcome.
			// finishCtx lets a completed send be recorded during shutdown.
			ctx := finishCtx
			rows, err := tx.Query(ctx, `
			select n.id, n.attempts, n.kind, n.detail, c.id, c.kind, c.name, c.target_sealed, c.target_channel, p.slug, p.name, n.issue_id
			from notifications n join alert_channels c on c.id = n.channel_id join projects p on p.id = c.project_id
			where n.sent_at is null and n.available_at <= now() and n.attempts < $2
			order by n.available_at, n.id limit $1 for update of n skip locked`, 1, maxAttempts)
			if err != nil {
				return err
			}
			type claimed struct {
				n       Notification
				issueID *int64
			}
			var items []claimed
			for rows.Next() {
				var c claimed
				var detail []byte
				if err := rows.Scan(&c.n.ID, &c.n.Attempts, &c.n.Kind, &detail, &c.n.Channel.ID, &c.n.Channel.Kind, &c.n.Channel.Name,
					&c.n.Channel.Sealed, &c.n.Channel.Target, &c.n.Project.Slug, &c.n.Project.Name, &c.issueID); err != nil {
					return err
				}
				_ = json.Unmarshal(detail, &c.n.Detail)
				items = append(items, c)
			}
			if err := rows.Err(); err != nil {
				return err
			}
			for _, it := range items {
				n := it.n
				if it.issueID != nil {
					row, err := scanIssueRow(tx.QueryRow(ctx, `select `+issueRowColumns+` from issues i join projects p on p.id = i.project_id where i.id = $1`, *it.issueID))
					if err != nil {
						return err
					}
					n.Issue = &row
				}
				// Bound delivery by the transaction's remaining lifetime,
				// reserving five seconds to persist the outcome. Database
				// lookup delays must not let a send outlive its row lock.
				deadline, _ := finishCtx.Deadline()
				sendCtx, stopSend := context.WithDeadline(deliveryCtx, deadline.Add(-5*time.Second))
				retryAfter, sendErr := send(sendCtx, &n)
				stopSend()
				if sendErr == nil {
					didSend = true
					if _, err := tx.Exec(ctx, `update notifications set sent_at = now(), attempts = attempts + 1, last_error = null where id = $1`, n.ID); err != nil {
						return err
					}
					if _, err := tx.Exec(ctx, `update alert_channels set last_delivery_at = now(), last_error = null where id = $1`, n.Channel.ID); err != nil {
						return err
					}
					if n.Issue != nil {
						d, _ := json.Marshal(map[string]string{"channel": n.Channel.Name, "alert": n.Kind})
						if _, err := tx.Exec(ctx, `insert into issue_activity (issue_id, kind, detail) values ($1, 'alerted', $2)`, n.Issue.ID, d); err != nil {
							return err
						}
					}
					continue
				}
				didFail = true
				if retryAfter <= 0 {
					retryAfter = min(time.Duration(30<<n.Attempts)*time.Second, time.Hour)
				}
				msg := sendErr.Error()
				if _, err := tx.Exec(ctx, `
				update notifications set attempts = attempts + 1, last_error = $2, available_at = now() + $3::interval where id = $1`,
					n.ID, msg, fmt.Sprintf("%d seconds", int(retryAfter.Seconds()))); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `update alert_channels set last_error = $2 where id = $1`, n.Channel.ID, msg); err != nil {
					return err
				}
			}
			return nil
		})
		cancel()
		if err != nil {
			return sent, failed, err
		}
		if didSend {
			sent++
		}
		if didFail {
			failed++
		}
		if !didSend && !didFail {
			break
		}
	}
	return sent, failed, nil
}
