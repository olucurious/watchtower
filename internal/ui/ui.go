// Package ui serves the web UI compiled from web/ into dist/ and embedded
// in the binary.
package ui

import (
	"bytes"
	"compress/gzip"
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"sync"
)

//go:embed all:dist
var dist embed.FS

// Handler serves static assets and falls back to index.html for client-side
// routes.
func Handler() http.Handler {
	root, _ := fs.Sub(dist, "dist")
	files := http.FileServerFS(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w)
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name != "" && name != "index.html" {
			if f, err := root.Open(name); err == nil {
				f.Close()
				if strings.HasPrefix(name, "assets/") {
					// Vite fingerprints asset names, so they never change.
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
					if serveGzipped(w, r, root, name) {
						return
					}
				}
				files.ServeHTTP(w, r)
				return
			}
			if path.Ext(name) != "" {
				http.NotFound(w, r)
				return
			}
		}
		index, err := fs.ReadFile(root, "index.html")
		if err != nil {
			http.Error(w, "The web UI was not built into this binary. Run `make ui` and rebuild.", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	})
}

var (
	gzipMu    sync.Mutex
	gzipCache = map[string][]byte{}
)

// serveGzipped answers with a gzip copy of a text asset, compressed once
// and kept in memory, when the client accepts it.
func serveGzipped(w http.ResponseWriter, r *http.Request, root fs.FS, name string) bool {
	ext := path.Ext(name)
	if ext != ".js" && ext != ".css" && ext != ".svg" && ext != ".json" {
		return false
	}
	if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		return false
	}
	gzipMu.Lock()
	body, ok := gzipCache[name]
	if !ok {
		raw, err := fs.ReadFile(root, name)
		if err != nil {
			gzipMu.Unlock()
			return false
		}
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		_, _ = zw.Write(raw)
		_ = zw.Close()
		body = buf.Bytes()
		gzipCache[name] = body
	}
	gzipMu.Unlock()
	w.Header().Set("Content-Type", mime.TypeByExtension(ext))
	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Set("Vary", "Accept-Encoding")
	_, _ = w.Write(body)
	return true
}

func setSecurityHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; "+
		"script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("X-Frame-Options", "DENY")
}
