package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/olucurious/watchtower/internal/adapter"
)

var (
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("already exists")
)

func (s *Store) CreateProject(ctx context.Context, slug, name string) (adapter.Project, error) {
	p := adapter.Project{Slug: slug}
	err := s.pool.QueryRow(ctx, `insert into projects (slug, name) values ($1, $2) returning id`, slug, name).Scan(&p.ID)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return p, fmt.Errorf("project %q: %w", slug, ErrExists)
	}
	return p, err
}

func (s *Store) Project(ctx context.Context, slug string) (adapter.Project, error) {
	p := adapter.Project{Slug: slug}
	err := s.pool.QueryRow(ctx, `select id from projects where slug = $1`, slug).Scan(&p.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, fmt.Errorf("project %q: %w", slug, ErrNotFound)
	}
	return p, err
}

// CreateKey issues a new credential for an adapter. The plaintext key is
// returned once and only its digest is stored. createdBy is a user ID, or
// 0 when issued from the CLI.
func (s *Store) CreateKey(ctx context.Context, projectSlug, adapterName, label string, createdBy int64) (string, adapter.Project, error) {
	p, err := s.Project(ctx, projectSlug)
	if err != nil {
		return "", p, err
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", p, err
	}
	key := hex.EncodeToString(b[:]) // 32 hex chars: valid as a Sentry DSN public key
	_, err = s.pool.Exec(ctx, `insert into project_keys (project_id, adapter, key_sha256, label, created_by)
		values ($1, $2, $3, $4, nullif($5, 0))`, p.ID, adapterName, hashKey(key), label, createdBy)
	return key, p, err
}

type Issue struct {
	ID          int64
	Title       string
	Culprit     string
	Level       string
	Status      string
	TimesSeen   int64
	FirstSeen   time.Time
	LastSeen    time.Time
	LastRelease string
	RegressedAt *time.Time
}

// Issues lists a project's issues, most recently seen first. An empty
// status lists every status.
func (s *Store) Issues(ctx context.Context, projectSlug, status string, limit int) ([]Issue, error) {
	rows, err := s.pool.Query(ctx, `
		select i.id, i.title, i.culprit, i.level, i.status, i.times_seen, i.first_seen, i.last_seen,
			i.last_release, i.regressed_at
		from issues i join projects p on p.id = i.project_id
		where p.slug = $1 and ($2 = '' or i.status = $2)
		order by i.last_seen desc limit $3`, projectSlug, status, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Issue, error) {
		var i Issue
		err := r.Scan(&i.ID, &i.Title, &i.Culprit, &i.Level, &i.Status, &i.TimesSeen, &i.FirstSeen,
			&i.LastSeen, &i.LastRelease, &i.RegressedAt)
		return i, err
	})
}
