package sourcemaps

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/olucurious/watchtower/internal/store"
	"github.com/olucurious/watchtower/internal/store/storetest"
)

type harness struct {
	srv   *httptest.Server
	store *store.Store
	token string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	s := storetest.New(t)
	ctx := context.Background()
	for _, slug := range []string{"web", "api"} {
		if _, err := s.CreateProject(ctx, slug, slug); err != nil {
			t.Fatal(err)
		}
	}
	token, err := s.CreateToken(ctx, "ci", "web", 0)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	u := &Uploads{Store: s, PublicURL: "https://errors.example.com", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	u.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &harness{srv: srv, store: s, token: token}
}

func (h *harness) do(t *testing.T, method, path, ctype string, body []byte, token string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, h.srv.URL+path, bytes.NewReader(body))
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// uploadChunk posts one gzip chunk the way sentry-cli does; name overrides
// the checksum to simulate corruption.
func (h *harness) uploadChunk(t *testing.T, data []byte, name string) int {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, _ := mw.CreateFormFile("file_gzip", name)
	zw := gzip.NewWriter(part)
	zw.Write(data)
	zw.Close()
	mw.Close()
	status, _ := h.do(t, "POST", "/api/0/organizations/any/chunk-upload/", mw.FormDataContentType(), body.Bytes(), h.token)
	return status
}

func sha(b []byte) string { s := sha1.Sum(b); return hex.EncodeToString(s[:]) }

func assembleBody(checksum string, project string) []byte {
	b, _ := json.Marshal(map[string]any{"checksum": checksum, "chunks": []string{checksum}, "projects": []string{project}, "version": "web@1.0"})
	return b
}

func TestUploadProtocol(t *testing.T) {
	h := newHarness(t)
	bundle, err := os.ReadFile("../symbolicate/testdata/sentry-cli-3.8.0-bundle.zip")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha(bundle)

	if status, _ := h.do(t, "GET", "/api/0/organizations/any/chunk-upload/", "", nil, ""); status != 401 {
		t.Errorf("no token: %d", status)
	}
	status, opts := h.do(t, "GET", "/api/0/organizations/any/chunk-upload/", "", nil, h.token)
	if status != 200 || opts["url"] != "https://errors.example.com/api/0/organizations/any/chunk-upload/" || opts["hashAlgorithm"] != "sha1" {
		t.Fatalf("options: %d %v", status, opts)
	}

	_, res := h.do(t, "POST", "/api/0/organizations/any/artifactbundle/assemble/", "application/json", assembleBody(sum, "web"), h.token)
	if res["state"] != "not_found" || len(res["missingChunks"].([]any)) != 1 {
		t.Fatalf("assemble before upload: %v", res)
	}
	if status := h.uploadChunk(t, bundle, strings.Repeat("0", 40)); status != 400 {
		t.Errorf("chunk with wrong checksum accepted: %d", status)
	}
	if status := h.uploadChunk(t, bundle, sum); status != 200 {
		t.Fatalf("chunk upload: %d", status)
	}
	if status, res := h.do(t, "POST", "/api/0/organizations/any/artifactbundle/assemble/", "application/json", assembleBody(sum, "api"), h.token); status != 403 {
		t.Errorf("token scoped to web uploaded to api: %d %v", status, res)
	}
	_, res = h.do(t, "POST", "/api/0/organizations/any/artifactbundle/assemble/", "application/json", assembleBody(sum, "web"), h.token)
	if res["state"] != "ok" {
		t.Fatalf("assemble: %v", res)
	}
	_, res = h.do(t, "POST", "/api/0/organizations/any/artifactbundle/assemble/", "application/json", assembleBody(sum, "web"), h.token)
	if res["state"] != "ok" {
		t.Errorf("re-assembling an uploaded bundle should be a no-op: %v", res)
	}
	bundles, _ := h.store.Bundles(context.Background(), "web", 10)
	if len(bundles) != 1 || bundles[0].Release != "web@1.0" || bundles[0].FileCount != 2 {
		t.Errorf("stored bundles %+v", bundles)
	}

	// Chunks that assemble into something other than a bundle are refused.
	junk := []byte("not a zip")
	h.uploadChunk(t, junk, sha(junk))
	_, res = h.do(t, "POST", "/api/0/organizations/any/artifactbundle/assemble/", "application/json", assembleBody(sha(junk), "web"), h.token)
	if res["state"] != "error" {
		t.Errorf("junk bundle: %v", res)
	}

	tokens, _ := h.store.Tokens(context.Background(), "web")
	h.store.RevokeToken(context.Background(), tokens[0].ID)
	if status, _ := h.do(t, "GET", "/api/0/organizations/any/chunk-upload/", "", nil, h.token); status != 401 {
		t.Errorf("revoked token accepted: %d", status)
	}
}

func TestBundleExpansionLimits(t *testing.T) {
	build := func(n int) []byte {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		files := map[string]any{}
		for i := 0; i < n; i++ {
			name := fmt.Sprintf("file-%d", i)
			f, err := zw.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
			files[name] = map[string]string{"type": "source", "url": name}
		}
		f, err := zw.Create("manifest.json")
		if err != nil {
			t.Fatal(err)
		}
		if err := json.NewEncoder(f).Encode(map[string]any{"files": files}); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	t.Run("combined size", func(t *testing.T) {
		data := build(3)
		// ZIP metadata advertises individually valid files whose combined
		// expansion exceeds the budget. Reject before opening any entry.
		for offset := 0; offset < len(data); {
			i := bytes.Index(data[offset:], []byte{'P', 'K', 1, 2})
			if i < 0 {
				break
			}
			i += offset
			nameLen := int(binary.LittleEndian.Uint16(data[i+28 : i+30]))
			name := string(data[i+46 : i+46+nameLen])
			if name != "manifest.json" {
				binary.LittleEndian.PutUint32(data[i+24:i+28], uint32(maxFileInBundle))
			}
			offset = i + 46 + nameLen + int(binary.LittleEndian.Uint16(data[i+30:i+32])) + int(binary.LittleEndian.Uint16(data[i+32:i+34]))
		}
		if _, _, err := ParseBundle(data); err == nil || !strings.Contains(err.Error(), "decompressed size") {
			t.Fatalf("expected expansion rejection, got %v", err)
		}
	})
	t.Run("file count", func(t *testing.T) {
		if _, _, err := ParseBundle(build(maxBundleFiles + 1)); err == nil || !strings.Contains(err.Error(), "too many files") {
			t.Fatalf("expected count rejection, got %v", err)
		}
	})
}
