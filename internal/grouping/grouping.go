// Package grouping decides which issue an event belongs to.
//
// Rules are versioned: the version is part of every fingerprint, so a rule
// change creates new issues instead of silently merging or splitting
// existing ones. Bump Version whenever the output of Components changes.
package grouping

import (
	"crypto/sha256"
	"encoding/hex"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/olucurious/watchtower/internal/event"
)

const Version = 2 // v2: recursive frames collapse

// defaultMarker in an SDK-supplied fingerprint expands to the default
// grouping components, as in Sentry.
const defaultMarker = "{{ default }}"

// Fingerprint returns the stable issue key for e.
func Fingerprint(e *event.Event) string {
	h := sha256.New()
	for _, c := range Components(e) {
		h.Write([]byte(c))
		h.Write([]byte{0})
	}
	return "v" + strconv.Itoa(Version) + ":" + hex.EncodeToString(h.Sum(nil)[:16])
}

// Components lists the values that identify e's issue, in order.
func Components(e *event.Event) []string {
	if len(e.Fingerprint) > 0 {
		var out []string
		for _, part := range e.Fingerprint {
			if part == defaultMarker {
				out = append(out, defaults(e)...)
			} else {
				out = append(out, "custom:"+part)
			}
		}
		return out
	}
	return defaults(e)
}

// defaults groups by each exception's type plus its in-app frames (module
// or file, and function; never line numbers, which move with unrelated
// edits). Without frames it falls back to type plus normalized value, and
// without exceptions to the normalized message.
func defaults(e *event.Event) []string {
	var out []string
	for _, ex := range e.Exceptions {
		if ex.Mechanism != nil && ex.Mechanism.Synthetic {
			// The stack is meaningful, the SDK-invented type/value are not.
			out = append(out, frameComponents(ex.Frames)...)
			continue
		}
		out = append(out, "type:"+ex.Type)
		if frames := frameComponents(ex.Frames); len(frames) > 0 {
			out = append(out, frames...)
		} else {
			out = append(out, "value:"+NormalizeMessage(ex.Value))
		}
	}
	if len(out) == 0 || allSynthetic(e) {
		out = append(out, "message:"+NormalizeMessage(e.Message))
	}
	return out
}

func allSynthetic(e *event.Event) bool {
	for _, ex := range e.Exceptions {
		if ex.Mechanism == nil || !ex.Mechanism.Synthetic {
			return false
		}
	}
	return len(e.Exceptions) > 0
}

// frameComponents identifies a stack by its frames, collapsing runs of the
// same frame so recursion depth does not split one bug into many issues.
func frameComponents(frames []event.Frame) []string {
	var inApp, all []string
	for _, f := range frames {
		c := "frame:" + frameLocation(f) + ":" + f.Function
		if len(all) == 0 || all[len(all)-1] != c {
			all = append(all, c)
		}
		if f.InApp && (len(inApp) == 0 || inApp[len(inApp)-1] != c) {
			inApp = append(inApp, c)
		}
	}
	if len(inApp) > 0 {
		return inApp
	}
	return all
}

// frameLocation prefers the module; otherwise the file's base name, since
// absolute paths differ between hosts and deploy directories.
func frameLocation(f event.Frame) string {
	if f.Module != "" {
		return f.Module
	}
	return path.Base(strings.ReplaceAll(f.Filename, `\`, "/"))
}

var (
	uuidRe = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	hexRe  = regexp.MustCompile(`(?i)\b(?:0x)?[0-9a-f]{12,}\b`)
	numRe  = regexp.MustCompile(`\b\d+(?:\.\d+)?\b`)
)

// NormalizeMessage replaces volatile tokens (IDs, hashes, numbers) so that
// "user 42 not found" and "user 43 not found" group together.
func NormalizeMessage(s string) string {
	s = uuidRe.ReplaceAllString(s, "<uuid>")
	s = hexRe.ReplaceAllString(s, "<hex>")
	s = numRe.ReplaceAllString(s, "<num>")
	return strings.TrimSpace(s)
}
