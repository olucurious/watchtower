package api

import "testing"

func TestNormalizeRepoURL(t *testing.T) {
	for in, want := range map[string]string{
		"":                                   "",
		" https://github.com/acme/web.git/ ": "https://github.com/acme/web",
		"https://gitlab.com/acme/web":        "https://gitlab.com/acme/web",
	} {
		if got, ok := normalizeRepoURL(in); !ok || got != want {
			t.Errorf("%q: %q %v", in, got, ok)
		}
	}
	for _, bad := range []string{"http://github.com/acme/web", "javascript:alert(1)", "https://user:pw@github.com/a/b", "https://github.com/a/b?x=1", "github.com/acme/web"} {
		if _, ok := normalizeRepoURL(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}
