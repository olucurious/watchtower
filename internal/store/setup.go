package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

var (
	// ErrSetupDone: an account exists, so first-run setup is closed.
	ErrSetupDone = errors.New("watchtower is already set up")
	// ErrBadSetupCode: the code is wrong, or none has been issued.
	ErrBadSetupCode = errors.New("that setup code is not valid")
)

// setupAlphabet leaves out letters and digits that look alike (0/O, 1/I/L).
const setupAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// NewSetupCode issues the one-time code that creates the first
// administrator in the browser, replacing any earlier one. It returns
// ErrSetupDone once an account exists.
func (s *Store) NewSetupCode(ctx context.Context) (string, error) {
	if n, err := s.CountUsers(ctx); err != nil {
		return "", err
	} else if n > 0 {
		return "", ErrSetupDone
	}
	// 12 characters of 31 (about 59 bits), drawn without modulo bias.
	var code strings.Builder
	limit := byte(256 / len(setupAlphabet) * len(setupAlphabet))
	for n := 0; n < 12; {
		var b [1]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		if b[0] >= limit {
			continue
		}
		if n > 0 && n%4 == 0 {
			code.WriteByte('-')
		}
		code.WriteByte(setupAlphabet[int(b[0])%len(setupAlphabet)])
		n++
	}
	_, err := s.pool.Exec(ctx, `
		insert into setup_code (code_sha256) values ($1)
		on conflict (singleton) do update set code_sha256 = excluded.code_sha256, created_at = now()`,
		setupDigest(code.String()))
	return code.String(), err
}

// setupDigest ignores case, spaces and dashes, so a code typed loosely
// still matches.
func setupDigest(code string) []byte {
	code = strings.Map(func(r rune) rune {
		if r == '-' || r == ' ' {
			return -1
		}
		return r
	}, strings.ToUpper(strings.TrimSpace(code)))
	h := sha256.Sum256([]byte(code))
	return h[:]
}

// CreateFirstAdmin creates the first administrator if code is the current
// setup code, then retires the code. Concurrent attempts are serialized,
// so at most one succeeds.
func (s *Store) CreateFirstAdmin(ctx context.Context, code, email, name, passwordHash string) (User, error) {
	var u User
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var want []byte
		err := tx.QueryRow(ctx, `select code_sha256 from setup_code where singleton for update`).Scan(&want)
		var users int
		if err := tx.QueryRow(ctx, `select count(*) from users`).Scan(&users); err != nil {
			return err
		}
		switch {
		case users > 0:
			return ErrSetupDone
		case errors.Is(err, pgx.ErrNoRows):
			return ErrBadSetupCode
		case err != nil:
			return err
		case subtle.ConstantTimeCompare(want, setupDigest(code)) != 1:
			return ErrBadSetupCode
		}
		if u, err = scanUser(tx.QueryRow(ctx, `
			insert into users (email, name, password_hash, is_admin) values ($1, $2, $3, true)
			returning `+userColumns, email, name, passwordHash)); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `delete from setup_code`)
		return err
	})
	return u, err
}
