package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// EmailPrefs are a user's personal notification settings.
type EmailPrefs struct {
	Assigned  bool   `json:"assigned"`  // an issue was assigned to me
	Regressed bool   `json:"regressed"` // an issue I own came back
	Comments  bool   `json:"comments"`  // someone commented on an issue I own
	Digest    string `json:"digest"`    // off, daily or weekly
}

func (s *Store) EmailPrefs(ctx context.Context, userID int64) (EmailPrefs, error) {
	var p EmailPrefs
	err := s.pool.QueryRow(ctx, `select email_assigned, email_regressed, email_comments, email_digest from users where id = $1`, userID).
		Scan(&p.Assigned, &p.Regressed, &p.Comments, &p.Digest)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

func (s *Store) SetEmailPrefs(ctx context.Context, userID int64, p EmailPrefs) error {
	_, err := s.pool.Exec(ctx, `update users set email_assigned = $2, email_regressed = $3, email_comments = $4, email_digest = $5 where id = $1`,
		userID, p.Assigned, p.Regressed, p.Comments, p.Digest)
	return err
}

// emailPref maps an email kind to the preference that allows it. The
// column names are fixed here, never taken from input.
var emailPref = map[string]string{
	"assigned":  "email_assigned",
	"regressed": "email_regressed",
	"commented": "email_comments",
}

// queueEmail writes a personal email inside tx, provided email is enabled,
// the recipient is active and has not turned this kind off.
func (s *Store) queueEmail(ctx context.Context, tx pgx.Tx, userID int64, kind string, issueID int64, detail map[string]string) error {
	pref, ok := emailPref[kind]
	if !ok {
		return fmt.Errorf("unknown email kind %q", kind)
	}
	if !s.EmailEnabled {
		return nil
	}
	d, _ := json.Marshal(detail)
	_, err := tx.Exec(ctx, `
		insert into emails (user_id, kind, issue_id, detail)
		select id, $2, $3, $4 from users where id = $1 and disabled_at is null and `+pref, userID, kind, issueID, d)
	return err
}

// DigestHour is the UTC hour after which a day's digest is due; weekly
// digests go out on Mondays.
const DigestHour = 8

// QueueDigests schedules due digests. Each user gets at most one per
// period, however many workers run this.
func (s *Store) QueueDigests(ctx context.Context, now time.Time) (int64, error) {
	if !s.EmailEnabled {
		return 0, nil
	}
	now = now.UTC()
	if now.Hour() < DigestHour {
		return 0, nil
	}
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	var n int64
	queue := func(period string, since time.Time, key string) error {
		d, _ := json.Marshal(map[string]string{"period": period, "since": since.Format(time.RFC3339), "until": day.Format(time.RFC3339)})
		tag, err := s.pool.Exec(ctx, `
			insert into emails (user_id, kind, detail, dedupe_key)
			select id, 'digest', $2, 'digest:' || $1 || ':' || id || ':' || $3 from users
			where disabled_at is null and email_digest = $1
			on conflict (dedupe_key) do nothing`, period, d, key)
		n += tag.RowsAffected()
		return err
	}
	if err := queue("daily", day.AddDate(0, 0, -1), day.Format("2006-01-02")); err != nil {
		return n, err
	}
	if now.Weekday() == time.Monday {
		y, w := now.ISOWeek()
		if err := queue("weekly", day.AddDate(0, 0, -7), fmt.Sprintf("%d-W%02d", y, w)); err != nil {
			return n, err
		}
	}
	return n, nil
}

// Digest summarizes what happened across all projects in a period.
type Digest struct {
	NewIssues   []IssueRow `json:"new_issues"`
	Regressions []IssueRow `json:"regressions"`
	Busiest     []IssueRow `json:"busiest"` // TimesSeen holds events within the period
	NewTotal    int64      `json:"new_total"`
	Events      int64      `json:"events"`
}

func (d Digest) Empty() bool { return d.NewTotal == 0 && len(d.Regressions) == 0 && d.Events == 0 }

const digestListSize = 8

func (s *Store) Digest(ctx context.Context, since, until time.Time) (Digest, error) {
	var d Digest
	collect := func(q string, args ...any) ([]IssueRow, error) {
		rows, err := s.pool.Query(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, func(r pgx.CollectableRow) (IssueRow, error) { return scanIssueRow(r) })
	}
	var err error
	if d.NewIssues, err = collect(`select `+issueRowColumns+` from issues i join projects p on p.id = i.project_id
		where i.first_seen >= $1 and i.first_seen < $2 and i.status <> 'muted'
		order by i.times_seen desc, i.id limit $3`, since, until, digestListSize); err != nil {
		return d, err
	}
	if d.Regressions, err = collect(`select `+issueRowColumns+` from issues i join projects p on p.id = i.project_id
		where exists (select 1 from issue_activity a where a.issue_id = i.id and a.kind = 'regressed' and a.at >= $1 and a.at < $2)
		and i.status = 'unresolved' order by i.last_seen desc limit $3`, since, until, digestListSize); err != nil {
		return d, err
	}
	if d.Busiest, err = collect(`select `+issueRowColumns+` from issues i join projects p on p.id = i.project_id
		join (select issue_id, sum(events) n from issue_hourly where hour >= $1 and hour < $2 group by issue_id) c on c.issue_id = i.id
		where i.status = 'unresolved' order by c.n desc, i.id limit 5`, since, until); err != nil {
		return d, err
	}
	// Report events within the period, not all time, for the busiest list.
	for i := range d.Busiest {
		if err := s.pool.QueryRow(ctx, `select coalesce(sum(events), 0)::bigint from issue_hourly where issue_id = $1 and hour >= $2 and hour < $3`,
			d.Busiest[i].ID, since, until).Scan(&d.Busiest[i].TimesSeen); err != nil {
			return d, err
		}
	}
	// Digest periods start at UTC midnight, so whole hours cover them exactly.
	err = s.pool.QueryRow(ctx, `select
		(select count(*) from issues where first_seen >= $1 and first_seen < $2 and status <> 'muted'),
		(select coalesce(sum(events), 0)::bigint from issue_hourly where hour >= $1 and hour < $2)`, since, until).Scan(&d.NewTotal, &d.Events)
	return d, err
}

// Email is a claimed outbox row with what is needed to render it.
type Email struct {
	ID       int64
	Attempts int
	Kind     string
	Detail   map[string]string
	To       struct{ Email, Name string }
	Issue    *IssueRow // nil for digests
	Project  string    // the issue's project name
}

// ErrSkipEmail tells DeliverEmails the email was deliberately not sent,
// such as an empty digest; it is recorded as done.
var ErrSkipEmail = errors.New("nothing to send")

// DeliverEmails claims due emails one at a time and calls send for each,
// keeping the row locked until its outcome is committed. A send error is
// retried with backoff until maxAttempts.
func (s *Store) DeliverEmails(ctx context.Context, limit, maxAttempts int, send func(context.Context, *Email) error) (sent, failed int, err error) {
	for i := 0; i < limit; i++ {
		if err = ctx.Err(); err != nil {
			return sent, failed, err
		}
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
		claimed := false
		err = pgx.BeginFunc(finishCtx, s.pool, func(tx pgx.Tx) error {
			var e Email
			var issueID *int64
			var detail []byte
			err := tx.QueryRow(finishCtx, `
				select m.id, m.attempts, m.kind, m.detail, m.issue_id, u.email, u.name
				from emails m join users u on u.id = m.user_id
				where m.sent_at is null and m.available_at <= now() and m.attempts < $1 and u.disabled_at is null
				order by m.available_at, m.id limit 1 for update of m skip locked`, maxAttempts).
				Scan(&e.ID, &e.Attempts, &e.Kind, &detail, &issueID, &e.To.Email, &e.To.Name)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			claimed = true
			_ = json.Unmarshal(detail, &e.Detail)
			if issueID != nil {
				row, err := scanIssueRow(tx.QueryRow(finishCtx, `select `+issueRowColumns+` from issues i join projects p on p.id = i.project_id where i.id = $1`, *issueID))
				if err != nil {
					return err
				}
				e.Issue = &row
				if err := tx.QueryRow(finishCtx, `select name from projects where slug = $1`, row.ProjectSlug).Scan(&e.Project); err != nil {
					return err
				}
			}
			deadline, _ := finishCtx.Deadline()
			sendCtx, stop := context.WithDeadline(ctx, deadline.Add(-5*time.Second))
			sendErr := send(sendCtx, &e)
			stop()
			if sendErr == nil || errors.Is(sendErr, ErrSkipEmail) {
				sent++
				var note *string
				if sendErr != nil {
					msg := "skipped: " + sendErr.Error()
					note = &msg
				}
				_, err := tx.Exec(finishCtx, `update emails set sent_at = now(), attempts = attempts + 1, last_error = $2 where id = $1`, e.ID, note)
				return err
			}
			failed++
			backoff := min(time.Duration(60<<e.Attempts)*time.Second, 6*time.Hour)
			_, err = tx.Exec(finishCtx, `update emails set attempts = attempts + 1, last_error = $2, available_at = now() + $3::interval where id = $1`,
				e.ID, sendErr.Error(), fmt.Sprintf("%d seconds", int(backoff.Seconds())))
			return err
		})
		cancel()
		if err != nil || !claimed {
			return sent, failed, err
		}
	}
	return sent, failed, nil
}
