package main

import (
	"strings"
	"testing"
)

func TestReadLine(t *testing.T) {
	for in, want := range map[string]string{
		"correct horse battery staple\n": "correct horse battery staple",
		"  edges kept  \n":               "  edges kept  ",
		"windows\r\n":                    "windows",
		"no newline":                     "no newline",
		"first\nsecond\n":                "first",
	} {
		got, err := readLine(strings.NewReader(in))
		if err != nil || got != want {
			t.Errorf("readLine(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := readLine(strings.NewReader("")); err == nil {
		t.Error("empty input must be an error")
	}
}
