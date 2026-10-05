// Package storetest creates throwaway Postgres databases for tests in other
// packages. Tests skip unless WATCHTOWER_TEST_DATABASE_URL names a server.
package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/olucurious/watchtower/internal/store"
)

func New(t *testing.T) *store.Store {
	t.Helper()
	admin := os.Getenv("WATCHTOWER_TEST_DATABASE_URL")
	if admin == "" {
		t.Skip("WATCHTOWER_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	var b [6]byte
	_, _ = rand.Read(b[:])
	name := "watchtower_test_" + hex.EncodeToString(b[:])
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "create database "+name); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(admin)
	u.Path = "/" + name
	s, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Close()
		_, _ = conn.Exec(ctx, "drop database "+name+" with (force)")
		_ = conn.Close(ctx)
	})
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}
