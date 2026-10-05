package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type User struct {
	ID        int64     `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	IsAdmin   bool      `json:"is_admin"`
	CreatedAt time.Time `json:"created_at"`
	Disabled  bool      `json:"disabled"`
}

const userColumns = `id, email, name, is_admin, created_at, disabled_at is not null`

func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.Name, &u.IsAdmin, &u.CreatedAt, &u.Disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

func (s *Store) CreateUser(ctx context.Context, email, name, passwordHash string, admin bool) (User, error) {
	u, err := scanUser(s.pool.QueryRow(ctx, `
		insert into users (email, name, password_hash, is_admin) values ($1, $2, $3, $4)
		returning `+userColumns, email, name, passwordHash, admin))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return u, fmt.Errorf("user %q: %w", email, ErrExists)
	}
	return u, err
}

// UserForLogin returns an enabled user and their password hash.
func (s *Store) UserForLogin(ctx context.Context, email string) (User, string, error) {
	var hash string
	var u User
	err := s.pool.QueryRow(ctx, `
		select `+userColumns+`, password_hash from users
		where lower(email) = lower($1) and disabled_at is null`, email).
		Scan(&u.ID, &u.Email, &u.Name, &u.IsAdmin, &u.CreatedAt, &u.Disabled, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, "", ErrNotFound
	}
	return u, hash, err
}

func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.pool.Query(ctx, `select `+userColumns+` from users order by lower(email)`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (User, error) { return scanUser(r) })
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `select count(*) from users`).Scan(&n)
	return n, err
}

// SetUserDisabled disables or re-enables an account; disabling also ends
// its sessions.
func (s *Store) SetUserDisabled(ctx context.Context, id int64, disabled bool) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update users set disabled_at = case when $2 then now() else null end where id = $1`, id, disabled)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if disabled {
			_, err = tx.Exec(ctx, `delete from sessions where user_id = $1`, id)
		}
		return err
	})
}

func (s *Store) SetPassword(ctx context.Context, id int64, passwordHash string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update users set password_hash = $2 where id = $1`, id, passwordHash)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		_, err = tx.Exec(ctx, `delete from sessions where user_id = $1`, id)
		return err
	})
}

func (s *Store) UserByEmail(ctx context.Context, email string) (User, error) {
	return scanUser(s.pool.QueryRow(ctx, `select `+userColumns+` from users where lower(email) = lower($1)`, email))
}

func (s *Store) CreateSession(ctx context.Context, digest []byte, userID int64, ttl time.Duration) error {
	_, err := s.pool.Exec(ctx, `insert into sessions (token_sha256, user_id, expires_at) values ($1, $2, now() + $3::interval)`,
		digest, userID, fmt.Sprintf("%d seconds", int(ttl.Seconds())))
	return err
}

// SessionUser returns the enabled user owning an unexpired session and
// records activity at most once a minute.
func (s *Store) SessionUser(ctx context.Context, digest []byte) (User, error) {
	u, err := scanUser(s.pool.QueryRow(ctx, `
		with touched as (
			update sessions set last_seen_at = now()
			where token_sha256 = $1 and expires_at > now() and last_seen_at < now() - interval '1 minute'
		)
		select u.id, u.email, u.name, u.is_admin, u.created_at, u.disabled_at is not null
		from sessions s join users u on u.id = s.user_id
		where s.token_sha256 = $1 and s.expires_at > now() and u.disabled_at is null`, digest))
	return u, err
}

func (s *Store) DeleteSession(ctx context.Context, digest []byte) error {
	_, err := s.pool.Exec(ctx, `delete from sessions where token_sha256 = $1`, digest)
	return err
}

// DeleteExpiredSessions is called periodically by the server.
func (s *Store) DeleteExpiredSessions(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `delete from sessions where expires_at <= now()`)
	return err
}
