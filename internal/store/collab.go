package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// MaxCommentLength bounds a comment; comments are short triage notes.
const MaxCommentLength = 2000

// ErrInvalidComment: a comment is empty, too long or not plain text.
var ErrInvalidComment = fmt.Errorf("a comment must be 1 to %d characters of plain text", MaxCommentLength)

// CleanComment trims a comment and checks it is one that can be stored.
func CleanComment(body string) (string, error) {
	body = strings.TrimSpace(body)
	if body == "" || utf8.RuneCountInString(body) > MaxCommentLength || !utf8.ValidString(body) || strings.ContainsRune(body, 0) {
		return "", ErrInvalidComment
	}
	return body, nil
}

// SetProjectRepo sets the repository URL used to link releases to commits.
func (s *Store) SetProjectRepo(ctx context.Context, slug, repoURL string) error {
	tag, err := s.pool.Exec(ctx, `update projects set repo_url = $2 where slug = $1`, slug, repoURL)
	if err == nil && tag.RowsAffected() == 0 {
		return fmt.Errorf("project %q: %w", slug, ErrNotFound)
	}
	return err
}

// Member is the public view of an enabled user, for assignment.
type Member struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Members lists enabled users by name.
func (s *Store) Members(ctx context.Context) ([]Member, error) {
	rows, err := s.pool.Query(ctx, `select id, name, email from users where disabled_at is null order by lower(coalesce(nullif(name, ''), email))`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Member])
}

// SetAssignee assigns an issue to an enabled user, or unassigns it when
// assigneeID is nil, and records the change. Assigning the current
// assignee again is a no-op.
func (s *Store) SetAssignee(ctx context.Context, issueID int64, assigneeID *int64, actor string, actorID int64) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var current *int64
		err := tx.QueryRow(ctx, `select assignee_id from issues where id = $1 for update`, issueID).Scan(&current)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if (current == nil && assigneeID == nil) || (current != nil && assigneeID != nil && *current == *assigneeID) {
			return nil
		}
		kind, detail := "unassigned", []byte(`{}`)
		if assigneeID != nil {
			var name string
			err := tx.QueryRow(ctx, `select coalesce(nullif(name, ''), email) from users where id = $1 and disabled_at is null`, *assigneeID).Scan(&name)
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("assignee: %w", ErrNotFound)
			}
			if err != nil {
				return err
			}
			kind = "assigned"
			detail, _ = json.Marshal(map[string]string{"assignee": name})
		}
		if _, err := tx.Exec(ctx, `update issues set assignee_id = $2 where id = $1`, issueID, assigneeID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into issue_activity (issue_id, kind, actor, user_id, detail) values ($1, $2, $3, $4, $5)`,
			issueID, kind, actor, actorID, detail); err != nil {
			return err
		}
		// Tell the new owner, unless they took the issue themselves.
		if assigneeID != nil && *assigneeID != actorID {
			return s.queueEmail(ctx, tx, *assigneeID, "assigned", issueID, map[string]string{"actor": actor})
		}
		return nil
	})
}

// AddComment records a comment on an issue's activity log and tells the
// issue's owner, unless they wrote it.
func (s *Store) AddComment(ctx context.Context, issueID int64, body, actor string, actorID int64) (int64, error) {
	detail, _ := json.Marshal(map[string]string{"body": body})
	var id int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var owner *int64
		err := tx.QueryRow(ctx, `
			with c as (
				insert into issue_activity (issue_id, kind, actor, user_id, detail)
				select id, 'comment', $2, $3, $4 from issues where id = $1 returning id
			) select c.id, i.assignee_id from c, issues i where i.id = $1`, issueID, actor, actorID, detail).Scan(&id, &owner)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if owner != nil && *owner != actorID {
			return s.queueEmail(ctx, tx, *owner, "commented", issueID, map[string]string{"actor": actor, "body": body})
		}
		return nil
	})
	return id, err
}

// DeleteComment removes a comment written by userID, or any comment when
// asAdmin is set.
func (s *Store) DeleteComment(ctx context.Context, issueID, commentID, userID int64, asAdmin bool) error {
	tag, err := s.pool.Exec(ctx, `
		delete from issue_activity where id = $2 and issue_id = $1 and kind = 'comment' and ($4 or user_id = $3)`,
		issueID, commentID, userID, asAdmin)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}
