// Package sourcemaps implements the Sentry artifact-bundle upload protocol
// used by sentry-cli and Sentry's bundler plugins: chunk upload, then
// assemble. Point them at Watchtower with SENTRY_URL and an upload token.
package sourcemaps

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/olucurious/watchtower/internal/store"
)

const (
	chunkSize       = 8 << 20
	maxRequestSize  = 32 << 20
	chunksPerReq    = 64
	maxBundleSize   = 256 << 20
	maxFileInBundle = 100 << 20
	maxBundleFiles  = 10_000
)

type Store interface {
	ResolveToken(ctx context.Context, token string) (store.TokenScope, error)
	SaveChunk(ctx context.Context, checksum string, data []byte) error
	MissingChunks(ctx context.Context, checksums []string) ([]string, error)
	ChunkData(ctx context.Context, checksums []string) ([]byte, error)
	ProjectIDs(ctx context.Context, slugs []string) ([]int64, error)
	BundleExists(ctx context.Context, projectIDs []int64, checksum string) (bool, error)
	SaveBundle(ctx context.Context, projectIDs []int64, b store.Bundle, files []store.ArtifactFile, chunks []string) error
}

type Uploads struct {
	Store     Store
	PublicURL string
	Log       *slog.Logger
}

func (u *Uploads) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/0/organizations/{org}/chunk-upload/", u.auth(u.options))
	mux.HandleFunc("POST /api/0/organizations/{org}/chunk-upload/", u.auth(u.chunks))
	mux.HandleFunc("POST /api/0/organizations/{org}/artifactbundle/assemble/", u.auth(u.assemble))
}

func (u *Uploads) auth(next func(http.ResponseWriter, *http.Request, store.TokenScope)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || !strings.HasPrefix(token, store.TokenPrefix) {
			detail(w, http.StatusUnauthorized, "Invalid token")
			return
		}
		scope, err := u.Store.ResolveToken(r.Context(), strings.TrimSpace(token))
		if errors.Is(err, store.ErrNotFound) {
			detail(w, http.StatusUnauthorized, "Invalid token")
			return
		}
		if err != nil {
			u.Log.Error("resolving upload token", "err", err)
			detail(w, http.StatusServiceUnavailable, "token lookup failed")
			return
		}
		next(w, r, scope)
	}
}

// options tells the client how to upload; accept advertises artifact
// bundles only, so clients don't try release-file or debug-file flows.
func (u *Uploads) options(w http.ResponseWriter, r *http.Request, _ store.TokenScope) {
	writeJSON(w, http.StatusOK, map[string]any{
		"url":              strings.TrimRight(u.PublicURL, "/") + r.URL.Path,
		"chunkSize":        chunkSize,
		"chunksPerRequest": chunksPerReq,
		"maxFileSize":      maxBundleSize,
		"maxRequestSize":   maxRequestSize,
		"concurrency":      4,
		"hashAlgorithm":    "sha1",
		"compression":      []string{"gzip"},
		"accept":           []string{"artifact_bundles", "artifact_bundles_v2"},
	})
}

// chunks stores uploaded chunks. Each multipart part is named "file" (raw)
// or "file_gzip", with the SHA-1 of the raw bytes as its filename.
func (u *Uploads) chunks(w http.ResponseWriter, r *http.Request, _ store.TokenScope) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestSize+1<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		detail(w, http.StatusBadRequest, "expected multipart/form-data")
		return
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			detail(w, http.StatusBadRequest, "malformed multipart body")
			return
		}
		var src io.Reader = part
		switch part.FormName() {
		case "file":
		case "file_gzip":
			zr, err := gzip.NewReader(part)
			if err != nil {
				detail(w, http.StatusBadRequest, "invalid gzip chunk")
				return
			}
			src = zr
		default:
			continue
		}
		data, err := io.ReadAll(io.LimitReader(src, chunkSize+1))
		if err != nil || len(data) > chunkSize {
			detail(w, http.StatusBadRequest, "chunk unreadable or larger than chunkSize")
			return
		}
		sum := sha1.Sum(data)
		if hex.EncodeToString(sum[:]) != part.FileName() {
			detail(w, http.StatusBadRequest, "chunk checksum does not match its name")
			return
		}
		if err := u.Store.SaveChunk(r.Context(), part.FileName(), data); err != nil {
			u.Log.Error("saving upload chunk", "err", err)
			detail(w, http.StatusServiceUnavailable, "could not store chunk")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

type assembleRequest struct {
	Checksum string   `json:"checksum"`
	Chunks   []string `json:"chunks"`
	Projects []string `json:"projects"`
	Version  string   `json:"version"`
	Dist     string   `json:"dist"`
}

func (u *Uploads) assemble(w http.ResponseWriter, r *http.Request, scope store.TokenScope) {
	var req assembleRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || req.Checksum == "" || len(req.Chunks) == 0 || len(req.Projects) == 0 {
		detail(w, http.StatusBadRequest, "checksum, chunks and projects are required")
		return
	}
	ctx := r.Context()
	ids, err := u.Store.ProjectIDs(ctx, req.Projects)
	if errors.Is(err, store.ErrNotFound) {
		detail(w, http.StatusNotFound, "unknown project")
		return
	}
	if err != nil {
		u.internal(w, err)
		return
	}
	for _, id := range ids {
		if scope.ProjectID != nil && *scope.ProjectID != id {
			detail(w, http.StatusForbidden, "this token cannot upload to that project")
			return
		}
	}
	if done, err := u.Store.BundleExists(ctx, ids, req.Checksum); err != nil {
		u.internal(w, err)
		return
	} else if done {
		writeJSON(w, http.StatusOK, map[string]any{"state": "ok", "missingChunks": []string{}})
		return
	}
	missing, err := u.Store.MissingChunks(ctx, req.Chunks)
	if err != nil {
		u.internal(w, err)
		return
	}
	if len(missing) > 0 {
		writeJSON(w, http.StatusOK, map[string]any{"state": "not_found", "missingChunks": missing})
		return
	}
	if len(req.Chunks)*chunkSize > maxBundleSize+chunkSize {
		assembleError(w, "bundle too large")
		return
	}
	data, err := u.Store.ChunkData(ctx, req.Chunks)
	if err != nil {
		u.internal(w, err)
		return
	}
	if sum := sha1.Sum(data); hex.EncodeToString(sum[:]) != req.Checksum {
		assembleError(w, "assembled file does not match its checksum")
		return
	}
	bundle, files, err := ParseBundle(data)
	if err != nil {
		assembleError(w, err.Error())
		return
	}
	bundle.Checksum = req.Checksum
	if req.Version != "" {
		bundle.Release = req.Version
	}
	if req.Dist != "" {
		bundle.Dist = req.Dist
	}
	if err := u.Store.SaveBundle(ctx, ids, bundle, files, req.Chunks); err != nil {
		u.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": "ok", "missingChunks": []string{}})
}

// ParseBundle reads an artifact bundle zip and its manifest.json.
func ParseBundle(data []byte) (store.Bundle, []store.ArtifactFile, error) {
	var b store.Bundle
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return b, nil, errors.New("artifact bundle is not a zip archive")
	}
	byName := map[string]*zip.File{}
	for _, f := range zr.File {
		byName[f.Name] = f
	}
	var manifest struct {
		Files map[string]struct {
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"files"`
		DebugID string `json:"debug_id"`
		Release string `json:"release"`
		Dist    string `json:"dist"`
	}
	mf, ok := byName["manifest.json"]
	if !ok {
		return b, nil, errors.New("artifact bundle has no manifest.json")
	}
	raw, err := readZipFile(mf, 1<<20)
	if err != nil || json.Unmarshal(raw, &manifest) != nil {
		return b, nil, errors.New("artifact bundle manifest is invalid")
	}
	b = store.Bundle{BundleID: manifest.DebugID, Release: manifest.Release, Dist: manifest.Dist}
	if len(manifest.Files) > maxBundleFiles || len(zr.File) > maxBundleFiles+1 {
		return b, nil, errors.New("artifact bundle contains too many files")
	}
	// Inspect declared sizes before allocating, then bound actual reads too.
	var total uint64
	for name := range manifest.Files {
		if f := byName[name]; f != nil {
			if f.UncompressedSize64 > maxFileInBundle || f.UncompressedSize64 > uint64(maxBundleSize)-total {
				return b, nil, errors.New("artifact bundle decompressed size exceeds limit")
			}
			total += f.UncompressedSize64
		}
	}
	remaining := int64(maxBundleSize)
	var files []store.ArtifactFile
	for name, meta := range manifest.Files {
		zf, ok := byName[name]
		if !ok {
			continue
		}
		content, err := readZipFile(zf, min(int64(maxFileInBundle), remaining))
		if err != nil {
			return b, nil, fmt.Errorf("reading %s from the bundle: %w", name, err)
		}
		remaining -= int64(len(content))
		h := map[string]string{}
		for k, v := range meta.Headers {
			h[strings.ToLower(k)] = v
		}
		files = append(files, store.ArtifactFile{
			Kind: meta.Type, URL: meta.URL, DebugID: strings.ToLower(h["debug-id"]), SourcemapRef: h["sourcemap"], Content: content,
		})
	}
	if len(files) == 0 {
		return b, nil, errors.New("artifact bundle contains no files")
	}
	return b, files, nil
}

// readZipFile guards against zip bombs by bounding the decompressed size.
func readZipFile(f *zip.File, limit int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("file too large")
	}
	return data, nil
}

func (u *Uploads) internal(w http.ResponseWriter, err error) {
	u.Log.Error("source map upload failed", "err", err)
	detail(w, http.StatusInternalServerError, "internal error")
}

func assembleError(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusOK, map[string]any{"state": "error", "detail": msg, "missingChunks": []string{}})
}

func detail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"detail": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
