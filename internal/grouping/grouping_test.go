package grouping

import (
	"testing"

	"github.com/olucurious/watchtower/internal/event"
)

func exceptionEvent(line int, value string) *event.Event {
	return &event.Event{Exceptions: []event.Exception{{
		Type:  "KeyError",
		Value: value,
		Frames: []event.Frame{
			{Module: "Phoenix.Router", Function: "call/2", Lineno: 10},
			{Module: "Library.Catalog", Function: "fetch!/1", Lineno: line, InApp: true},
		},
	}}}
}

func TestLineNumbersAndValuesDoNotSplitIssues(t *testing.T) {
	a := Fingerprint(exceptionEvent(42, "key :a not found"))
	b := Fingerprint(exceptionEvent(57, "key :b not found"))
	if a != b {
		t.Error("same code path grouped separately")
	}
}

func TestDifferentInAppPathsSplitIssues(t *testing.T) {
	other := exceptionEvent(42, "x")
	other.Exceptions[0].Frames[1].Function = "reverse!/1"
	if Fingerprint(exceptionEvent(42, "x")) == Fingerprint(other) {
		t.Error("different functions grouped together")
	}
}

func TestDeployPathDoesNotMatter(t *testing.T) {
	ev := func(path string) *event.Event {
		return &event.Event{Exceptions: []event.Exception{{Type: "TypeError", Frames: []event.Frame{{Filename: path, Function: "render", InApp: true}}}}}
	}
	if Fingerprint(ev("/srv/releases/101/app/catalog.js")) != Fingerprint(ev("/app/catalog.js")) {
		t.Error("absolute paths changed the fingerprint")
	}
}

func TestMessageNormalization(t *testing.T) {
	a := &event.Event{Message: "user 42 not found (req 3f2a9c1e-77aa-4b1c-9d2e-0a1b2c3d4e5f)"}
	b := &event.Event{Message: "user 99 not found (req 0b9e7c11-1234-4abc-8def-111111111111)"}
	if Fingerprint(a) != Fingerprint(b) {
		t.Error("volatile tokens split message issues")
	}
}

func TestCustomFingerprint(t *testing.T) {
	base := exceptionEvent(1, "x")
	withDefault := exceptionEvent(1, "x")
	withDefault.Fingerprint = []string{"search", "{{ default }}"}
	custom := exceptionEvent(1, "x")
	custom.Fingerprint = []string{"search"}
	if Fingerprint(withDefault) == Fingerprint(base) || Fingerprint(custom) == Fingerprint(withDefault) {
		t.Error("custom fingerprints not applied")
	}
	got := Components(withDefault)
	if got[0] != "custom:search" || got[1] != "type:KeyError" {
		t.Errorf("{{ default }} not expanded: %v", got)
	}
}

func TestSyntheticExceptionGroupsByMessageAndStack(t *testing.T) {
	ev := func(msg string) *event.Event {
		return &event.Event{Message: msg, Exceptions: []event.Exception{{
			Type: "Error", Value: msg, Mechanism: &event.Mechanism{Synthetic: true},
			Frames: []event.Frame{{Module: "inventory", Function: "check", InApp: true}},
		}}}
	}
	if Fingerprint(ev("stock low: 3")) != Fingerprint(ev("stock low: 7")) {
		t.Error("synthetic events with the same template split")
	}
	if Fingerprint(ev("stock low: 3")) == Fingerprint(ev("supplier offline")) {
		t.Error("different messages merged")
	}
}

func TestFingerprintCarriesVersion(t *testing.T) {
	if fp := Fingerprint(exceptionEvent(1, "x")); fp[:3] != "v2:" {
		t.Errorf("fingerprint %q lacks version prefix", fp)
	}
}

func TestRecursionDepthDoesNotSplitIssues(t *testing.T) {
	ev := func(depth int) *event.Event {
		frames := []event.Frame{{Module: "Library.Catalog", Function: "run", InApp: true}}
		for i := 0; i < depth; i++ {
			frames = append(frames, event.Frame{Module: "Library.Catalog", Function: "deep", InApp: true})
		}
		return &event.Event{Exceptions: []event.Exception{{Type: "RuntimeError", Frames: frames}}}
	}
	if Fingerprint(ev(1)) != Fingerprint(ev(4)) {
		t.Error("recursion depth split the issue")
	}
	other := ev(2)
	other.Exceptions[0].Frames[0].Function = "start"
	if Fingerprint(other) == Fingerprint(ev(2)) {
		t.Error("different callers merged")
	}
}
