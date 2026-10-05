// Package symbolicate rewrites minified JavaScript stack frames to their
// original source using uploaded source maps.
package symbolicate

import (
	"context"
	"net/url"
	"path"
	"strings"
	"sync"

	"github.com/go-sourcemap/sourcemap"

	"github.com/olucurious/watchtower/internal/event"
	"github.com/olucurious/watchtower/internal/store"
)

const (
	contextLines   = 5
	maxContextLine = 300 // characters; minified one-liners are not useful context
	cacheSize      = 128
)

// Symbolicator applies source maps, caching parsed maps by file ID.
type Symbolicator struct {
	mu    sync.Mutex
	cache map[int64]*sourcemap.Consumer
}

func New() *Symbolicator { return &Symbolicator{cache: map[int64]*sourcemap.Consumer{}} }

type mapped struct {
	ok           bool
	source, name string
	line, col    int
	consumer     *sourcemap.Consumer
}

// Event symbolicates every JavaScript frame it has a source map for and
// returns how many frames were mapped. Frames without a map are left as
// they are; lookup errors are returned after processing what it could.
func (s *Symbolicator) Event(ctx context.Context, q store.ArtifactQuerier, e *event.Event) (int, error) {
	if !isJavaScript(e) {
		return 0, nil
	}
	total := 0
	var firstErr error
	for i := range e.Exceptions {
		frames := e.Exceptions[i].Frames
		results := make([]mapped, len(frames))
		for j := range frames {
			r, err := s.lookup(ctx, q, e, &frames[j])
			if err != nil && firstErr == nil {
				firstErr = err
			}
			results[j] = r
		}
		for j := range frames {
			r := results[j]
			if !r.ok {
				continue
			}
			f := &frames[j]
			f.Minified = &event.MinifiedLocation{Filename: firstNonEmpty(f.AbsPath, f.Filename), Function: f.Function, Lineno: f.Lineno, Colno: f.Colno}
			f.Filename = cleanSource(r.source)
			f.AbsPath, f.Module = "", ""
			f.Lineno, f.Colno = r.line, r.col+1
			// A frame's original function name is the identifier at its
			// caller's call site (frames run oldest first).
			if j > 0 && results[j-1].ok && results[j-1].name != "" {
				f.Function = results[j-1].name
			}
			f.InApp = !strings.Contains(r.source, "node_modules/")
			f.ContextLine, f.PreContext, f.PostContext = sourceContext(r.consumer.SourceContent(r.source), r.line)
			total++
		}
	}
	return total, firstErr
}

func (s *Symbolicator) lookup(ctx context.Context, q store.ArtifactQuerier, e *event.Event, f *event.Frame) (mapped, error) {
	if f.Lineno <= 0 || f.Colno <= 0 || f.Minified != nil {
		return mapped{}, nil
	}
	loc := firstNonEmpty(f.AbsPath, f.Filename)
	var (
		id      int64
		content []byte
		ok      bool
		err     error
	)
	if debugID := debugIDFor(e, loc); debugID != "" {
		id, content, ok, err = store.SourceMapByDebugID(ctx, q, e.ProjectID, debugID)
	}
	if !ok && err == nil && e.Release != "" {
		for _, u := range releaseURLs(loc) {
			if id, content, ok, err = store.SourceMapByURL(ctx, q, e.ProjectID, e.Release, "", u); ok || err != nil {
				break
			}
		}
	}
	if !ok || err != nil {
		return mapped{}, err
	}
	c, err := s.consumer(id, content)
	if err != nil {
		return mapped{}, nil //nolint:nilerr // a broken map leaves the frame minified, not the event failed
	}
	src, name, line, col, found := c.Source(f.Lineno, f.Colno-1)
	if !found {
		return mapped{}, nil
	}
	return mapped{ok: true, source: src, name: name, line: line, col: col, consumer: c}, nil
}

func (s *Symbolicator) consumer(id int64, content []byte) (*sourcemap.Consumer, error) {
	s.mu.Lock()
	c, ok := s.cache[id]
	s.mu.Unlock()
	if ok {
		return c, nil
	}
	// No base URL: sources keep the map's own paths, and a cached map does
	// not depend on which frame loaded it first.
	c, err := sourcemap.Parse("", content)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if len(s.cache) >= cacheSize {
		s.cache = map[int64]*sourcemap.Consumer{}
	}
	s.cache[id] = c
	s.mu.Unlock()
	return c, nil
}

func isJavaScript(e *event.Event) bool {
	switch e.Platform {
	case "javascript", "node":
		return true
	}
	return len(e.DebugImages) > 0
}

// debugIDFor matches a frame's file against the event's debug images.
func debugIDFor(e *event.Event, loc string) string {
	for _, img := range e.DebugImages {
		if img.CodeFile == loc || (img.CodeFile != "" && strings.HasSuffix(loc, img.CodeFile)) {
			return img.DebugID
		}
	}
	return ""
}

// releaseURLs lists the "~/..." names a file may have been uploaded under:
// the URL path for browser frames, and the path and base name otherwise.
func releaseURLs(loc string) []string {
	if u, err := url.Parse(loc); err == nil && u.Host != "" {
		return []string{"~" + u.Path}
	}
	loc = strings.TrimPrefix(loc, "app://")
	out := []string{"~/" + strings.TrimLeft(loc, "/")}
	if base := path.Base(loc); base != strings.TrimLeft(loc, "/") {
		out = append(out, "~/"+base)
	}
	return out
}

// cleanSource turns map source paths like "webpack://app/./src/x.js" or
// "../src/x.js" into "src/x.js".
func cleanSource(src string) string {
	for _, p := range []string{"webpack:///", "webpack://"} {
		if strings.HasPrefix(src, p) {
			src = strings.TrimPrefix(src, p)
			if i := strings.Index(src, "/"); p == "webpack://" && i >= 0 && !strings.HasPrefix(src, ".") {
				src = src[i+1:] // drop the webpack namespace
			}
		}
	}
	for strings.HasPrefix(src, "../") || strings.HasPrefix(src, "./") {
		src = strings.TrimPrefix(strings.TrimPrefix(src, "../"), "./")
	}
	return src
}

func sourceContext(content string, line int) (string, []string, []string) {
	if content == "" || line <= 0 {
		return "", nil, nil
	}
	lines := strings.Split(content, "\n")
	if line > len(lines) {
		return "", nil, nil
	}
	clip := func(s string) string {
		s = strings.TrimRight(s, "\r")
		if len(s) > maxContextLine {
			return s[:maxContextLine] + "…"
		}
		return s
	}
	var pre, post []string
	for i := max(0, line-1-contextLines); i < line-1; i++ {
		pre = append(pre, clip(lines[i]))
	}
	for i := line; i < min(len(lines), line+contextLines); i++ {
		post = append(post, clip(lines[i]))
	}
	return clip(lines[line-1]), pre, post
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
