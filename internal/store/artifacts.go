package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
)

// TokenPrefix marks Watchtower upload tokens. It deliberately differs from
// Sentry's "sntrys_", which sentry-cli would try to decode.
const TokenPrefix = "wtk_"

type APIToken struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Project    string     `json:"project"` // slug, or "" for every project
	CreatedBy  string     `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

// CreateToken issues a source-map upload token, returned once. An empty
// projectSlug allows uploads to every project.
func (s *Store) CreateToken(ctx context.Context, name, projectSlug string, createdBy int64) (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	token := TokenPrefix + hex.EncodeToString(b[:])
	tag, err := s.pool.Exec(ctx, `
		insert into api_tokens (name, token_sha256, scope, project_id, created_by)
		select $1, $2, 'sourcemaps', (select id from projects where slug = $3), nullif($4, 0)
		where $3 = '' or exists (select 1 from projects where slug = $3)`,
		name, hashKey(token), projectSlug, createdBy)
	if err == nil && tag.RowsAffected() == 0 {
		return "", ErrNotFound
	}
	return token, err
}

// Tokens lists upload tokens that can write to a project (including
// all-project tokens).
func (s *Store) Tokens(ctx context.Context, projectSlug string) ([]APIToken, error) {
	rows, err := s.pool.Query(ctx, `
		select t.id, t.name, coalesce(p.slug, ''), coalesce(u.email, ''), t.created_at, t.last_used_at, t.revoked_at
		from api_tokens t left join projects p on p.id = t.project_id left join users u on u.id = t.created_by
		where t.project_id is null or p.slug = $1
		order by t.revoked_at is not null, t.created_at desc`, projectSlug)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[APIToken])
}

func (s *Store) RevokeToken(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `update api_tokens set revoked_at = now() where id = $1 and revoked_at is null`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// TokenScope is what an upload token may write to.
type TokenScope struct {
	ID        int64
	ProjectID *int64 // nil: any project
}

// ResolveToken checks a bearer token and records its use.
func (s *Store) ResolveToken(ctx context.Context, token string) (TokenScope, error) {
	var t TokenScope
	err := s.pool.QueryRow(ctx, `
		update api_tokens set last_used_at = now()
		where token_sha256 = $1 and revoked_at is null returning id, project_id`, hashKey(token)).Scan(&t.ID, &t.ProjectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

// SaveChunk stores one upload chunk; re-uploads are no-ops.
func (s *Store) SaveChunk(ctx context.Context, checksum string, data []byte) error {
	_, err := s.pool.Exec(ctx, `insert into upload_chunks (checksum, data) values ($1, $2) on conflict do nothing`, checksum, data)
	return err
}

// MissingChunks returns the checksums not yet uploaded, in input order.
func (s *Store) MissingChunks(ctx context.Context, checksums []string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `select checksum from upload_chunks where checksum = any($1)`, checksums)
	if err != nil {
		return nil, err
	}
	have, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	present := map[string]bool{}
	for _, c := range have {
		present[c] = true
	}
	missing := []string{}
	for _, c := range checksums {
		if !present[c] {
			missing = append(missing, c)
		}
	}
	return missing, nil
}

// ChunkData concatenates chunks in order.
func (s *Store) ChunkData(ctx context.Context, checksums []string) ([]byte, error) {
	var buf bytes.Buffer
	for _, c := range checksums {
		var data []byte
		if err := s.pool.QueryRow(ctx, `select data from upload_chunks where checksum = $1`, c).Scan(&data); err != nil {
			return nil, fmt.Errorf("chunk %s: %w", c, err)
		}
		buf.Write(data)
	}
	return buf.Bytes(), nil
}

// ProjectIDs maps slugs to IDs, failing on any unknown slug.
func (s *Store) ProjectIDs(ctx context.Context, slugs []string) ([]int64, error) {
	ids := make([]int64, 0, len(slugs))
	for _, slug := range slugs {
		p, err := s.Project(ctx, slug)
		if err != nil {
			return nil, err
		}
		ids = append(ids, p.ID)
	}
	return ids, nil
}

// ArtifactFile is one file of an uploaded bundle.
type ArtifactFile struct {
	Kind, URL, DebugID, SourcemapRef string
	Content                          []byte // uncompressed
}

var artifactKinds = map[string]bool{"minified_source": true, "source_map": true, "source": true}

// Bundle is an uploaded artifact bundle's metadata.
type Bundle struct {
	Checksum, BundleID, Release, Dist string
}

// SaveBundle stores a bundle's files for each project and deletes the
// chunks it was assembled from. Saving the same checksum twice is a no-op.
func (s *Store) SaveBundle(ctx context.Context, projectIDs []int64, b Bundle, files []ArtifactFile, chunks []string) error {
	compressed := make([][]byte, len(files))
	var size int64
	for i, f := range files {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		_, _ = zw.Write(f.Content)
		_ = zw.Close()
		compressed[i] = buf.Bytes()
		size += int64(len(f.Content))
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		for _, pid := range projectIDs {
			var bundleID int64
			err := tx.QueryRow(ctx, `
				insert into artifact_bundles (project_id, checksum, bundle_id, release, dist, file_count, size_bytes)
				values ($1, $2, $3, $4, $5, $6, $7) on conflict (project_id, checksum) do nothing returning id`,
				pid, b.Checksum, b.BundleID, b.Release, b.Dist, len(files), size).Scan(&bundleID)
			if errors.Is(err, pgx.ErrNoRows) {
				continue // already stored
			}
			if err != nil {
				return err
			}
			for i, f := range files {
				if !artifactKinds[f.Kind] {
					continue // e.g. indexed RAM bundles; not supported
				}
				if _, err := tx.Exec(ctx, `
					insert into artifact_files (bundle_id, project_id, kind, url, debug_id, sourcemap_ref, content_gzip)
					values ($1, $2, $3, $4, $5, $6, $7)`,
					bundleID, pid, f.Kind, f.URL, f.DebugID, f.SourcemapRef, compressed[i]); err != nil {
					return err
				}
			}
		}
		_, err := tx.Exec(ctx, `delete from upload_chunks where checksum = any($1)`, chunks)
		return err
	})
}

// BundleExists reports whether every project already has the bundle.
func (s *Store) BundleExists(ctx context.Context, projectIDs []int64, checksum string) (bool, error) {
	var n int
	err := s.pool.QueryRow(ctx, `select count(*) from artifact_bundles where project_id = any($1) and checksum = $2`, projectIDs, checksum).Scan(&n)
	return n == len(projectIDs), err
}

type BundleRow struct {
	ID        int64     `json:"id"`
	BundleID  string    `json:"bundle_id"`
	Release   string    `json:"release"`
	Dist      string    `json:"dist"`
	FileCount int       `json:"file_count"`
	SizeBytes int64     `json:"size_bytes"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Store) Bundles(ctx context.Context, projectSlug string, limit int) ([]BundleRow, error) {
	rows, err := s.pool.Query(ctx, `
		select b.id, b.bundle_id, b.release, b.dist, b.file_count, b.size_bytes, b.created_at
		from artifact_bundles b join projects p on p.id = b.project_id
		where p.slug = $1 order by b.created_at desc limit $2`, projectSlug, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[BundleRow])
}

func (s *Store) DeleteBundle(ctx context.Context, projectSlug string, id int64) error {
	tag, err := s.pool.Exec(ctx, `delete from artifact_bundles b using projects p where p.id = b.project_id and p.slug = $1 and b.id = $2`, projectSlug, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ArtifactQuerier is the read access symbolication needs; both the pool
// and a transaction satisfy it.
type ArtifactQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// SourceMapByDebugID returns the newest source map uploaded for a debug
// ID, with its file ID for caching. ok is false when none exists.
func SourceMapByDebugID(ctx context.Context, q ArtifactQuerier, projectID int64, debugID string) (id int64, content []byte, ok bool, err error) {
	return loadArtifact(ctx, q, `
		select id, content_gzip from artifact_files
		where project_id = $1 and debug_id = $2 and kind = 'source_map' order by id desc limit 1`, projectID, debugID)
}

// SourceMapByURL finds the map for a minified file uploaded under a
// release, following its sourcemap reference. url is the "~/path" form.
func SourceMapByURL(ctx context.Context, q ArtifactQuerier, projectID int64, release, dist, url string) (int64, []byte, bool, error) {
	return loadArtifact(ctx, q, `
		with minified as (
			select f.sourcemap_ref, f.url, f.bundle_id from artifact_files f join artifact_bundles b on b.id = f.bundle_id
			where f.project_id = $1 and f.kind = 'minified_source' and f.url = $4 and b.release = $2 and b.dist = $3
			order by f.id desc limit 1
		)
		select f.id, f.content_gzip from artifact_files f, minified m
		where f.bundle_id = m.bundle_id and f.kind = 'source_map'
			and (f.url = m.sourcemap_ref or f.url = regexp_replace(m.url, '[^/]*$', '') || m.sourcemap_ref or f.url = m.url || '.map')
		order by f.id desc limit 1`, projectID, release, dist, url)
}

func loadArtifact(ctx context.Context, q ArtifactQuerier, sql string, args ...any) (int64, []byte, bool, error) {
	var id int64
	var gz []byte
	err := q.QueryRow(ctx, sql, args...).Scan(&id, &gz)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil, false, nil
	}
	if err != nil {
		return 0, nil, false, err
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return 0, nil, false, err
	}
	content, err := io.ReadAll(zr)
	return id, content, err == nil, err
}
