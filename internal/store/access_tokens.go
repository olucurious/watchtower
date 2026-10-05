package store

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// AccessTokenPrefix marks Watchtower personal access tokens, so they are
// recognisable in config files and by secret scanners.
const AccessTokenPrefix = "wtp_"

// AccessToken describes a personal access token; the secret itself is
// never stored.
type AccessToken struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Scope      string     `json:"scope"` // read or write
	Hint       string     `json:"hint"`  // the first characters, for recognition
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

// CreateAccessToken issues a token for userID and returns its plaintext once.
func (s *Store) CreateAccessToken(ctx context.Context, userID int64, name, scope string, expiresAt *time.Time) (string, AccessToken, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", AccessToken{}, err
	}
	token := AccessTokenPrefix + base64.RawURLEncoding.EncodeToString(b[:])
	t := AccessToken{Name: name, Scope: scope, Hint: token[:len(AccessTokenPrefix)+4] + "…", ExpiresAt: expiresAt}
	err := s.pool.QueryRow(ctx, `
		insert into access_tokens (user_id, name, hint, token_sha256, scope, expires_at) values ($1, $2, $3, $4, $5, $6)
		returning id, created_at`, userID, name, t.Hint, hashKey(token), scope, expiresAt).Scan(&t.ID, &t.CreatedAt)
	return token, t, err
}

// AccessTokens lists a user's tokens, newest first.
func (s *Store) AccessTokens(ctx context.Context, userID int64) ([]AccessToken, error) {
	rows, err := s.pool.Query(ctx, `
		select id, name, scope, hint, created_at, expires_at, last_used_at, revoked_at
		from access_tokens where user_id = $1 order by created_at desc`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (AccessToken, error) {
		var t AccessToken
		err := r.Scan(&t.ID, &t.Name, &t.Scope, &t.Hint, &t.CreatedAt, &t.ExpiresAt, &t.LastUsedAt, &t.RevokedAt)
		return t, err
	})
}

// RevokeAccessToken revokes one of userID's tokens.
func (s *Store) RevokeAccessToken(ctx context.Context, userID, id int64) error {
	tag, err := s.pool.Exec(ctx, `update access_tokens set revoked_at = now() where id = $1 and user_id = $2 and revoked_at is null`, id, userID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ErrInvalidToken means a token is unknown, revoked, expired, or belongs
// to a disabled user.
var ErrInvalidToken = errors.New("invalid access token")

// AccessTokenUser resolves a token to its enabled owner, its scope and its
// expiry, and records use at most once a minute.
func (s *Store) AccessTokenUser(ctx context.Context, token string) (User, string, *time.Time, error) {
	var u User
	var scope string
	var expires *time.Time
	err := s.pool.QueryRow(ctx, `
		with touched as (
			update access_tokens set last_used_at = now()
			where token_sha256 = $1 and (last_used_at is null or last_used_at < now() - interval '1 minute')
		)
		select u.id, u.email, u.name, u.is_admin, u.created_at, u.disabled_at is not null, t.scope, t.expires_at
		from access_tokens t join users u on u.id = t.user_id
		where t.token_sha256 = $1 and t.revoked_at is null and (t.expires_at is null or t.expires_at > now())
			and u.disabled_at is null`, hashKey(token)).
		Scan(&u.ID, &u.Email, &u.Name, &u.IsAdmin, &u.CreatedAt, &u.Disabled, &scope, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, "", nil, ErrInvalidToken
	}
	return u, scope, expires, err
}
