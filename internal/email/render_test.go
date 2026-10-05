package email

import (
	"net/mail"
	"strings"
	"testing"

	"github.com/olucurious/watchtower/internal/store"
)

func issueEmail(kind string, detail map[string]string) *store.Email {
	return &store.Email{Kind: kind, Detail: detail, Project: "Storefront",
		Issue: &store.IssueRow{ID: 91, Title: `Error: <script>alert("x")</script> reservation failed`, Culprit: "node_app in reserve", ProjectSlug: "web"}}
}

func TestComposePersonal(t *testing.T) {
	const base = "https://watchtower.example"
	for _, tc := range []struct {
		e                     *store.Email
		subject, text, inHTML string
	}{
		{issueEmail("assigned", map[string]string{"actor": "Ada"}),
			"[Watchtower] Ada assigned you WT-91: Error:", "https://watchtower.example/issues/91\n", "Ada assigned you an issue"},
		{issueEmail("regressed", map[string]string{"release": "b240901873c07c4dd3e0c6b8986259d12ec770a6", "environment": "production",
			"event_id": "4C2B33B7-2AE8-4760-9F79-6AA65E96BD9D"}),
			"[Watchtower] Regression in Storefront:", "happened again in release b240901 (production).", "/issues/91/events/4c2b33b72ae847609f796aa65e96bd9d"},
		{issueEmail("commented", map[string]string{"actor": "Grace", "body": "Looks like <b>loadMember</b>"}),
			"[Watchtower] Grace commented on WT-91:", "> Looks like <b>loadMember</b>", "Looks like &lt;b&gt;loadMember&lt;/b&gt;"},
	} {
		c, err := Compose(tc.e, nil, base)
		if err != nil {
			t.Fatal(err)
		}
		m, err := Render(mail.Address{Address: "ada@example.com"}, c)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(m.Subject, tc.subject) || !strings.Contains(m.Text, tc.text) || !strings.Contains(m.HTML, tc.inHTML) {
			t.Errorf("%s:\nsubject %q\ntext %q", tc.e.Kind, m.Subject, m.Text)
		}
		if strings.Contains(m.HTML, "<script>") || !strings.Contains(m.HTML, "&lt;script&gt;") {
			t.Errorf("%s: issue title not escaped in HTML", tc.e.Kind)
		}
		if !strings.Contains(m.Text, base+"/settings/account") {
			t.Errorf("%s: no way to change settings", tc.e.Kind)
		}
	}
}

func TestComposeDigest(t *testing.T) {
	d := &store.Digest{
		NewIssues:   []store.IssueRow{{ID: 1, Title: "KeyError: missing", ProjectSlug: "reader", TimesSeen: 4}},
		Regressions: []store.IssueRow{{ID: 2, Title: "Error: reservation failed", ProjectSlug: "web", LastRelease: "b240901873c07c4dd3e0c6b8986259d12ec770a6"}},
		Busiest:     []store.IssueRow{{ID: 1, Title: "KeyError: missing", ProjectSlug: "reader", TimesSeen: 4}, {ID: 3, Title: "TimeoutError", ProjectSlug: "web", TimesSeen: 90}},
		NewTotal:    11, Events: 230,
	}
	c, err := Compose(&store.Email{Kind: "digest", Detail: map[string]string{"period": "weekly"}}, d, "https://w.example")
	if err != nil {
		t.Fatal(err)
	}
	m, _ := Render(mail.Address{Address: "ada@example.com"}, c)
	if m.Subject != "[Watchtower] Weekly digest: 11 new issues, 1 regression" {
		t.Errorf("subject %q", m.Subject)
	}
	if strings.Count(m.Text, "https://w.example/issues/1\n") != 1 || !strings.Contains(m.Text, "Busiest\n- WT-3 TimeoutError") {
		t.Errorf("busiest should only list issues not already shown:\n%s", m.Text)
	}
	for _, want := range []string{"last week: 11 new issues, 1 regression and 230 events", "and 10 more", "web · back in b240901", "https://w.example/issues/1"} {
		if !strings.Contains(m.Text, want) {
			t.Errorf("digest text lacks %q:\n%s", want, m.Text)
		}
	}
}
