package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestFirstAdminSetup(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if _, err := s.CreateFirstAdmin(ctx, "ANYTHING", "ada@example.com", "Ada", "hash"); !errors.Is(err, ErrBadSetupCode) {
		t.Fatalf("before any code is issued: %v", err)
	}
	old, err := s.NewSetupCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	code, err := s.NewSetupCode(ctx) // a restart replaces the code
	if err != nil || len(code) != 14 || strings.Count(code, "-") != 2 || code == old {
		t.Fatalf("code %q (old %q): %v", code, old, err)
	}
	if _, err := s.CreateFirstAdmin(ctx, old, "ada@example.com", "Ada", "hash"); !errors.Is(err, ErrBadSetupCode) {
		t.Fatalf("replaced code: %v", err)
	}
	// Typed loosely: lower case, spaces instead of dashes.
	loose := strings.ToLower(strings.ReplaceAll(code, "-", " "))
	u, err := s.CreateFirstAdmin(ctx, loose, "ada@example.com", "Ada", "hash")
	if err != nil || !u.IsAdmin || u.Email != "ada@example.com" {
		t.Fatalf("%+v: %v", u, err)
	}
	if _, err := s.CreateFirstAdmin(ctx, code, "eve@example.com", "Eve", "hash"); !errors.Is(err, ErrSetupDone) {
		t.Fatalf("a code works only once: %v", err)
	}
	if _, err := s.NewSetupCode(ctx); !errors.Is(err, ErrSetupDone) {
		t.Fatalf("no new codes once an account exists: %v", err)
	}
}

// Racing attempts with the right code create exactly one administrator.
func TestFirstAdminSetupIsRaceFree(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	code, err := s.NewSetupCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 10)
	for i := range 10 {
		wg.Go(func() {
			_, err := s.CreateFirstAdmin(ctx, code, "user"+string(rune('a'+i))+"@example.com", "", "hash")
			results <- err
		})
	}
	wg.Wait()
	close(results)
	ok := 0
	for err := range results {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, ErrSetupDone):
			t.Errorf("unexpected error: %v", err)
		}
	}
	if n, _ := s.CountUsers(ctx); ok != 1 || n != 1 {
		t.Fatalf("%d attempts succeeded, %d users; want 1 and 1", ok, n)
	}
}
