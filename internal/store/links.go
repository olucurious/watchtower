package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// IssueLink is an issue's counterpart in an external tracker.
type IssueLink struct {
	IssueID    int64     `json:"-"`
	Provider   string    `json:"provider"`
	ChannelID  *int64    `json:"channel_id"`
	ExternalID string    `json:"-"`
	Identifier string    `json:"identifier"` // e.g. "LIB-12"
	URL        string    `json:"url"`
	State      string    `json:"state"` // the tracker's state type, e.g. "started"
	CreatedAt  time.Time `json:"created_at"`
}

const linkColumns = `issue_id, provider, channel_id, external_id, identifier, url, state, created_at`

func scanLink(r pgx.Row) (IssueLink, error) {
	var l IssueLink
	err := r.Scan(&l.IssueID, &l.Provider, &l.ChannelID, &l.ExternalID, &l.Identifier, &l.URL, &l.State, &l.CreatedAt)
	return l, err
}

// IssueLink returns the issue's link for a provider, or nil.
func (s *Store) IssueLink(ctx context.Context, issueID int64, provider string) (*IssueLink, error) {
	l, err := scanLink(s.pool.QueryRow(ctx, `select `+linkColumns+` from issue_links where issue_id = $1 and provider = $2`, issueID, provider))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &l, nil
}

func (s *Store) issueLinks(ctx context.Context, issueID int64) ([]IssueLink, error) {
	rows, err := s.pool.Query(ctx, `select `+linkColumns+` from issue_links where issue_id = $1 order by provider`, issueID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (IssueLink, error) { return scanLink(r) })
}

// SaveIssueLink records a newly created tracker issue and logs it on the
// activity timeline. If the issue was linked meanwhile, the existing link
// wins and is returned.
func (s *Store) SaveIssueLink(ctx context.Context, l IssueLink, actor string, actorID int64) (IssueLink, error) {
	var saved IssueLink
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			insert into issue_links (issue_id, provider, channel_id, external_id, identifier, url, state, checked_at)
			values ($1, $2, $3, $4, $5, $6, $7, now()) on conflict do nothing`,
			l.IssueID, l.Provider, l.ChannelID, l.ExternalID, l.Identifier, l.URL, l.State)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 1 {
			d, _ := json.Marshal(map[string]string{"provider": l.Provider, "identifier": l.Identifier, "url": l.URL})
			if _, err := tx.Exec(ctx, `insert into issue_activity (issue_id, kind, actor, user_id, detail) values ($1, 'linked', $2, nullif($3, 0), $4)`,
				l.IssueID, actor, actorID, d); err != nil {
				return err
			}
		}
		saved, err = scanLink(tx.QueryRow(ctx, `select `+linkColumns+` from issue_links where issue_id = $1 and provider = $2`, l.IssueID, l.Provider))
		return err
	})
	return saved, err
}

// ChannelTarget is what is needed to call a channel's destination.
type ChannelTarget struct {
	ID      int64
	Kind    string
	Name    string
	Sealed  []byte
	Channel string // Slack channel or Linear team ID
	Project string // slug
}

// ChannelTarget loads one channel's sealed target, scoped to a project.
func (s *Store) ChannelTarget(ctx context.Context, projectSlug string, id int64) (ChannelTarget, error) {
	var t ChannelTarget
	err := s.pool.QueryRow(ctx, `
		select c.id, c.kind, c.name, c.target_sealed, c.target_channel, p.slug
		from alert_channels c join projects p on p.id = c.project_id where p.slug = $1 and c.id = $2`, projectSlug, id).
		Scan(&t.ID, &t.Kind, &t.Name, &t.Sealed, &t.Channel, &t.Project)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

// TrackerChannel names a channel that can hold linked issues.
type TrackerChannel struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Team string `json:"team"`
	Kind string `json:"kind"`
}

func (s *Store) trackerChannels(ctx context.Context, projectID int64) ([]TrackerChannel, error) {
	rows, err := s.pool.Query(ctx, `select id, name, target_label, kind from alert_channels where project_id = $1 and kind = 'linear' order by created_at`, projectID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[TrackerChannel])
}

// LinkToCheck is an open issue whose tracker counterpart may have closed.
type LinkToCheck struct {
	IssueLink
	Sealed []byte
}

// LinksToCheck returns links of unresolved issues, least recently checked
// first, with their channel's sealed key.
func (s *Store) LinksToCheck(ctx context.Context, provider string, limit int) ([]LinkToCheck, error) {
	rows, err := s.pool.Query(ctx, `
		select l.issue_id, l.provider, l.channel_id, l.external_id, l.identifier, l.url, l.state, l.created_at, c.target_sealed
		from issue_links l join issues i on i.id = l.issue_id join alert_channels c on c.id = l.channel_id
		where l.provider = $1 and i.status = 'unresolved'
		order by l.checked_at nulls first limit $2`, provider, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (LinkToCheck, error) {
		var l LinkToCheck
		err := r.Scan(&l.IssueID, &l.Provider, &l.ChannelID, &l.ExternalID, &l.Identifier, &l.URL, &l.State, &l.CreatedAt, &l.Sealed)
		return l, err
	})
}

// LinkChecked records a link's current tracker state. When that state is
// done, an unresolved issue is resolved on the tracker's behalf.
func (s *Store) LinkChecked(ctx context.Context, issueID int64, provider, state string, done bool) (resolved bool, err error) {
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var identifier, url string
		err := tx.QueryRow(ctx, `update issue_links set state = $3, checked_at = now() where issue_id = $1 and provider = $2 returning identifier, url`,
			issueID, provider, state).Scan(&identifier, &url)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil || !done {
			return err
		}
		tag, err := tx.Exec(ctx, `update issues set status = 'resolved', resolved_at = now() where id = $1 and status = 'unresolved'`, issueID)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		resolved = true
		d, _ := json.Marshal(map[string]string{"provider": provider, "identifier": identifier, "url": url})
		_, err = tx.Exec(ctx, `insert into issue_activity (issue_id, kind, actor, detail) values ($1, 'resolved', 'Linear', $2)`, issueID, d)
		return err
	})
	return resolved, err
}
