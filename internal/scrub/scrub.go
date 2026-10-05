// Package scrub removes sensitive values from events before they are
// stored. It complements the allowlisted event model: fields that are
// never carried (bodies, headers, cookies, breadcrumbs) need no scrubbing,
// so this only covers free text that SDKs do carry.
package scrub

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/olucurious/watchtower/internal/event"
)

const filtered = "[Filtered]"

var (
	// Candidate card numbers: 13–19 digits, optionally grouped by spaces
	// or dashes. Only Luhn-valid candidates are redacted.
	panCandidate = regexp.MustCompile(`\b(?:\d[ -]?){12,18}\d\b`)
	bearer       = regexp.MustCompile(`(?i)\b(bearer|basic|token)\s+[A-Za-z0-9._~+/=-]{8,}`)
	secretPair   = regexp.MustCompile(`(?i)\b(password|passwd|pwd|secret|api[_-]?key|access[_-]?token|refresh[_-]?token|auth[_-]?token|authorization|private[_-]?key|pin|cvv|cvc)("?\s*[=:]\s*"?)(?:(?:bearer|basic)\s+)?[^\s"&,;]+`)
)

// sensitiveKey reports whether a tag or query parameter name implies a
// secret value. Short words match whole name segments only, so "span_id"
// is not mistaken for a card number field ("pan").
func sensitiveKey(k string) bool {
	k = strings.ToLower(k)
	for _, s := range []string{"password", "passwd", "secret", "token", "apikey", "api_key", "auth", "cookie", "credential", "signature", "cvv", "cvc"} {
		if strings.Contains(k, s) {
			return true
		}
	}
	for _, seg := range strings.FieldsFunc(k, func(r rune) bool { return (r < 'a' || r > 'z') && (r < '0' || r > '9') }) {
		switch seg {
		case "key", "pin", "pan", "card", "sig", "session", "pwd":
			return true
		}
	}
	return false
}

// Event scrubs e in place.
func Event(e *event.Event) {
	e.StripNUL()
	for _, p := range []*string{&e.Platform, &e.Message, &e.Logger, &e.Transaction,
		&e.Release, &e.Environment, &e.ServerName, &e.SDK.Name, &e.SDK.Version} {
		*p = Text(*p)
	}
	for i := range e.Fingerprint {
		e.Fingerprint[i] = Text(e.Fingerprint[i])
	}
	if e.User != nil {
		e.User.ID = Text(e.User.ID)
	}
	if e.Trace != nil {
		e.Trace.TraceID, e.Trace.SpanID = Text(e.Trace.TraceID), Text(e.Trace.SpanID)
	}
	for i := range e.DebugImages {
		d := &e.DebugImages[i]
		d.Type, d.DebugID, d.CodeFile = Text(d.Type), Text(d.DebugID), URL(d.CodeFile)
	}
	for i := range e.Exceptions {
		ex := &e.Exceptions[i]
		ex.Type, ex.Value, ex.Module = Text(ex.Type), Text(ex.Value), Text(ex.Module)
		if ex.Mechanism != nil {
			ex.Mechanism.Type = Text(ex.Mechanism.Type)
		}
		for j := range ex.Frames {
			f := &ex.Frames[j]
			f.Function, f.Module = Text(f.Function), Text(f.Module)
			f.Filename, f.AbsPath = URL(f.Filename), URL(f.AbsPath)
			if f.Minified != nil {
				f.Minified.Filename = URL(f.Minified.Filename)
				f.Minified.Function = Text(f.Minified.Function)
			}
			f.ContextLine = Text(f.ContextLine)
			for _, lines := range [][]string{f.PreContext, f.PostContext} {
				for k := range lines {
					lines[k] = Text(lines[k])
				}
			}
		}
	}
	if len(e.Tags) > 0 {
		tags := make(map[string]string, len(e.Tags))
		for k, v := range e.Tags {
			if sensitiveKey(k) {
				v = filtered
			} else {
				v = Text(v)
			}
			tags[Text(k)] = v
		}
		e.Tags = tags
	}
	if len(e.Context) > 0 {
		ctx := make(map[string]string, len(e.Context))
		for k, v := range e.Context {
			if sensitiveKey(k) {
				v = filtered
			} else {
				v = Text(v)
			}
			ctx[Text(k)] = v
		}
		e.Context = ctx
	}
	if e.Request != nil {
		e.Request.Method = Text(e.Request.Method)
		e.Request.URL = URL(e.Request.URL)
	}
}

// Text redacts card numbers and credential-looking substrings.
func Text(s string) string {
	if s == "" {
		return s
	}
	// Most text (source lines, function names) cannot match at all; cheap
	// literal checks skip the regexps for it. They are exact for ASCII
	// only, because (?i) also folds a few non-ASCII letters to ASCII.
	ascii := isASCII(s)
	if !ascii || digits(s) >= 13 {
		s = panCandidate.ReplaceAllStringFunc(s, func(m string) string {
			if luhnValid(m) {
				return "[Filtered:card]"
			}
			return m
		})
	}
	if ascii && !containsKeyword(s) {
		return s
	}
	// Pairs first, so "Authorization: Bearer x" collapses to one marker.
	s = secretPair.ReplaceAllString(s, "$1$2"+filtered)
	s = bearer.ReplaceAllString(s, "$1 "+filtered)
	return s
}

// keywordsByFirst indexes, by first letter, the literals that secretPair
// and bearer cannot match without.
var keywordsByFirst = func() (t [256][]string) {
	for _, w := range []string{"password", "passwd", "pwd", "secret", "api", "access", "refresh", "auth", "private",
		"pin", "cvv", "cvc", "bearer", "basic", "token"} {
		t[w[0]] = append(t[w[0]], w)
	}
	return t
}()

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func digits(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			n++
		}
	}
	return n
}

// containsKeyword reports whether ASCII s contains a keyword, ignoring
// case, without allocating.
func containsKeyword(s string) bool {
	for i := 0; i < len(s); i++ {
		for _, w := range keywordsByFirst[s[i]|0x20] { // |0x20 lower-cases ASCII letters
			if i+len(w) <= len(s) && strings.EqualFold(s[i:i+len(w)], w) {
				return true
			}
		}
	}
	return false
}

// URL redacts userinfo and sensitive query parameters.
func URL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return Text(raw)
	}
	if u.User != nil {
		u.User = url.User(filtered)
	}
	q := u.Query()
	changed := false
	for k, vs := range q {
		for i, v := range vs {
			nv := filtered
			if !sensitiveKey(k) {
				nv = Text(v)
			}
			if nv != v {
				vs[i], changed = nv, true
			}
		}
	}
	if changed {
		u.RawQuery = q.Encode()
	}
	return Text(u.String())
}

func luhnValid(s string) bool {
	sum, n, double := 0, 0, false
	for i := len(s) - 1; i >= 0; i-- {
		c := s[i]
		if c == ' ' || c == '-' {
			continue
		}
		d := int(c - '0')
		if double {
			if d *= 2; d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
		n++
	}
	return n >= 13 && sum%10 == 0
}
