package scrub

import (
	"fmt"
	"testing"

	"github.com/olucurious/watchtower/internal/event"
)

// benchEvent resembles a typical exception: a dozen in-app frames, each
// with source context, and a handful of tags.
func benchEvent() event.Event {
	e := event.Event{Message: "", Release: "web@4f8e2c1a9b3d", Environment: "production", ServerName: "web-1",
		Tags: map[string]string{"browser": "Chrome 141", "route": "/books/:id", "region": "eu-west"}}
	var frames []event.Frame
	for i := range 12 {
		frames = append(frames, event.Frame{
			Function: fmt.Sprintf("handle_request_%d", i), Module: "app.handlers.books", Filename: "app/handlers/books.py",
			AbsPath: "/srv/app/handlers/books.py", Lineno: 40 + i,
			PreContext:  []string{"def reserve_book(request, book_id):", "    member = request.user.member", "    book = Book.objects.get(pk=book_id)", "    copies = available_copies(book, member)", "    if not copies:"},
			ContextLine: "        raise HoldError(\"Could not place a hold on this book\")",
			PostContext: []string{"    hold = Hold.objects.create(book=book, member=member)", "    notify(member, hold)", "    return hold", "", "class HoldError(Exception):"},
		})
	}
	e.Exceptions = []event.Exception{{Type: "HoldError", Value: "Could not place a hold on this book", Frames: frames}}
	return e
}

func BenchmarkEvent(b *testing.B) {
	src := benchEvent()
	b.ReportAllocs()
	for b.Loop() {
		e := src
		e.Exceptions = []event.Exception{src.Exceptions[0]}
		e.Exceptions[0].Frames = append([]event.Frame(nil), src.Exceptions[0].Frames...)
		Event(&e)
	}
}

// slowText is Text without its prefilters: the reference behaviour.
func slowText(s string) string {
	s = panCandidate.ReplaceAllStringFunc(s, func(m string) string {
		if luhnValid(m) {
			return "[Filtered:card]"
		}
		return m
	})
	s = secretPair.ReplaceAllString(s, "$1$2"+filtered)
	return bearer.ReplaceAllString(s, "$1 "+filtered)
}

// TestTextPrefilterMatchesRegexps checks that skipping the regexps never
// changes the result, including for Unicode that case-folds to ASCII.
func TestTextPrefilterMatchesRegexps(t *testing.T) {
	words := []string{"password", "PassWord", "paſſword", "passwd", "pwd", "secret", "api_key", "API-KEY", "apikey",
		"access_token", "refresh-token", "auth_token", "Authorization", "private_key", "pin", "PIN", "cvv", "cvc",
		"bearer", "Basic", "token", "toKen", "TOKEN", "pinned", "mapping", "4111 1111 1111 1111", "4111-1111-1111-1111",
		"4111111111111111", "1234567890123", "123456789012", "12345678901234567890", "x", "", "=", ":", "\"", " ", "abcdefgh123",
		"Bearer abcdefghijkl", "token=abc", "é", "ß", "日本"}
	seps := []string{"", " ", "=", ": ", "\"=\"", "&", "\n"}
	n := 0
	for _, a := range words {
		for _, sep := range seps {
			for _, c := range words {
				s := a + sep + c
				if got, want := Text(s), slowText(s); got != want {
					t.Fatalf("Text(%q) = %q, want %q", s, got, want)
				}
				n++
			}
		}
	}
	if n < 1000 {
		t.Fatal("too few cases")
	}
}
