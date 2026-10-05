// Package store persists projects, keys, the ingest queue, issues and
// events in Postgres.
package store

import (
	"context"
	"crypto/sha256"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/olucurious/watchtower/internal/adapter"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	pool *pgxpool.Pool

	// MaxQueueDepth makes Accept shed load once this many events are waiting.
	MaxQueueDepth int

	// StoredPerIssueHour limits how many of an issue's events are stored in
	// full each hour; past it, events are counted and only a sample is
	// stored. Zero stores every event.
	StoredPerIssueHour int

	// EmailEnabled queues personal emails; without a mail server nothing
	// is written, so no backlog builds up.
	EmailEnabled bool

	keys  keyCache
	depth depthGauge
}

// defaultMaxConns sizes the pool when the URL doesn't set pool_max_conns.
// pgx defaults to the CPU count, which suits CPU-bound work; ingestion
// mostly waits for commits to reach disk, so it needs more connections.
const defaultMaxConns = 25

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("connecting to postgres: %w", err)
	}
	if !strings.Contains(databaseURL, "pool_max_conns") {
		cfg.MaxConns = defaultMaxConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connecting to postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connecting to postgres: %w", err)
	}
	return &Store{pool: pool, MaxQueueDepth: 100_000, StoredPerIssueHour: 20, depth: depthGauge{ttl: time.Second}}, nil
}

func (s *Store) Close()                         { s.pool.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// migrationLock serializes concurrent Migrate calls across replicas.
const migrationLock = 7_202_610_01

// Migrate applies embedded migrations that have not run yet.
func (s *Store) Migrate(ctx context.Context) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `select pg_advisory_lock($1)`, migrationLock); err != nil {
		return err
	}
	defer func() {
		ctx := context.WithoutCancel(ctx)
		if _, err := conn.Exec(ctx, `select pg_advisory_unlock($1)`, migrationLock); err != nil {
			// The lock belongs to the session: close it rather than return
			// a connection to the pool that would block other replicas.
			_ = conn.Conn().Close(ctx)
		}
	}()

	if _, err := conn.Exec(ctx, `create table if not exists schema_migrations (
		name text primary key, applied_at timestamptz not null default now())`); err != nil {
		return err
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		base := strings.TrimPrefix(name, "migrations/")
		var exists bool
		if err := conn.QueryRow(ctx, `select exists(select 1 from schema_migrations where name = $1)`, base).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		sql, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, string(sql)); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `insert into schema_migrations (name) values ($1)`, base)
			return err
		})
		if err != nil {
			return fmt.Errorf("migration %s: %w", base, err)
		}
	}
	return nil
}

func hashKey(key string) []byte {
	h := sha256.Sum256([]byte(key))
	return h[:]
}

// ResolveKey implements adapter.KeyResolver. Results, including misses, are
// cached briefly so a burst of events costs one lookup.
func (s *Store) ResolveKey(ctx context.Context, adapterName, key string) (adapter.Project, error) {
	cacheKey := adapterName + "\x00" + string(hashKey(key))
	if p, err, ok := s.keys.get(cacheKey); ok {
		return p, err
	}
	var p adapter.Project
	err := s.pool.QueryRow(ctx, `
		select p.id, p.slug from project_keys k join projects p on p.id = k.project_id
		where k.adapter = $1 and k.key_sha256 = $2 and k.revoked_at is null`,
		adapterName, hashKey(key)).Scan(&p.ID, &p.Slug)
	if errors.Is(err, pgx.ErrNoRows) {
		s.keys.put(cacheKey, adapter.Project{}, adapter.ErrUnknownKey, negativeKeyTTL)
		return adapter.Project{}, adapter.ErrUnknownKey
	}
	if err != nil {
		return adapter.Project{}, err
	}
	s.keys.put(cacheKey, p, nil, keyTTL)
	return p, nil
}

const (
	keyTTL         = 30 * time.Second
	negativeKeyTTL = 5 * time.Second
)

type keyCache struct {
	mu sync.Mutex
	m  map[string]keyEntry
}

type keyEntry struct {
	p       adapter.Project
	err     error
	expires time.Time
}

func (c *keyCache) get(k string) (adapter.Project, error, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	if !ok || time.Now().After(e.expires) {
		return adapter.Project{}, nil, false
	}
	return e.p, e.err, true
}

func (c *keyCache) put(k string, p adapter.Project, err error, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) > 10_000 {
		c.m = map[string]keyEntry{}
	}
	c.m[k] = keyEntry{p: p, err: err, expires: time.Now().Add(ttl)}
}

func (c *keyCache) flush() {
	c.mu.Lock()
	c.m = nil
	c.mu.Unlock()
}
