package email

import (
	"bytes"
	"fmt"
	htmltemplate "html/template"
	"net/mail"
	"strings"
	texttemplate "text/template"

	"github.com/olucurious/watchtower/internal/event"
	"github.com/olucurious/watchtower/internal/store"
)

// Content is what an email says, before it becomes text and HTML.
type Content struct {
	Subject  string
	Preview  string // the short summary mail clients show beside the subject
	Heading  string
	Lines    []string // short paragraphs
	Quote    string   // a comment, shown set apart
	Issue    *IssueLink
	Sections []Section // digest lists
	Action   *Link
	Settings string // where to change email preferences
}

type IssueLink struct {
	Ref, Title, Culprit, URL, Meta string
}

type Link struct{ Label, URL string }

type Section struct {
	Title  string
	Issues []IssueLink
	More   int // how many more were not listed
}

// Compose turns a claimed outbox email into its content. digest is the
// period's summary for digest emails and nil otherwise.
func Compose(e *store.Email, digest *store.Digest, publicURL string) (Content, error) {
	c := Content{Settings: publicURL + "/settings/account"}
	actor := e.Detail["actor"]
	switch e.Kind {
	case "assigned", "regressed", "commented":
		if e.Issue == nil {
			return c, fmt.Errorf("%s email without an issue", e.Kind)
		}
		il := issueLink(e.Issue, publicURL, e.Project)
		if id := event.NormalizeID(e.Detail["event_id"]); id != "" {
			il.URL += "/events/" + id
		}
		c.Issue = &il
		c.Action = &Link{"Open " + il.Ref, il.URL}
		switch e.Kind {
		case "assigned":
			c.Subject = fmt.Sprintf("%s assigned you %s: %s", actor, il.Ref, trim(e.Issue.Title, 120))
			c.Heading = fmt.Sprintf("%s assigned you an issue", actor)
			c.Preview = fmt.Sprintf("%s in %s", il.Ref, e.Project)
		case "regressed":
			release := event.ShortRelease(e.Detail["release"])
			c.Subject = fmt.Sprintf("Regression in %s: %s", e.Project, trim(e.Issue.Title, 120))
			c.Heading = "An issue you own came back"
			if release != "" {
				c.Lines = append(c.Lines, fmt.Sprintf("It was resolved, then happened again in release %s.", release))
			} else {
				c.Lines = append(c.Lines, "It was resolved, then happened again.")
			}
			if env := e.Detail["environment"]; env != "" {
				c.Lines[0] = strings.TrimSuffix(c.Lines[0], ".") + " (" + env + ")."
			}
			c.Preview = c.Lines[0]
		case "commented":
			c.Subject = fmt.Sprintf("%s commented on %s: %s", actor, il.Ref, trim(e.Issue.Title, 120))
			c.Heading = fmt.Sprintf("%s commented on an issue you own", actor)
			c.Quote = e.Detail["body"]
			c.Preview = trim(c.Quote, 140)
		}
	case "digest":
		if digest == nil {
			return c, fmt.Errorf("digest email without a digest")
		}
		period := "Daily"
		span := "yesterday"
		if e.Detail["period"] == "weekly" {
			period, span = "Weekly", "last week"
		}
		var parts []string
		if digest.NewTotal > 0 {
			parts = append(parts, plural(int(digest.NewTotal), "new issue", "new issues"))
		}
		if n := len(digest.Regressions); n > 0 {
			parts = append(parts, plural(n, "regression", "regressions"))
		}
		summary := strings.Join(parts, ", ")
		if summary == "" {
			summary = "no new issues"
		}
		c.Subject = fmt.Sprintf("%s digest: %s", period, summary)
		c.Heading = period + " digest"
		c.Lines = []string{fmt.Sprintf("Across all projects %s: %s and %s.", span, summary, plural(int(digest.Events), "event", "events"))}
		c.Preview = c.Lines[0]
		listed := map[int64]bool{}
		add := func(title string, rows []store.IssueRow, total int, meta func(store.IssueRow) string) {
			// "Busiest" leaves out issues already listed as new or regressed.
			if title == "Busiest" {
				var rest []store.IssueRow
				for _, r := range rows {
					if !listed[r.ID] {
						rest = append(rest, r)
					}
				}
				rows, total = rest, len(rest)
			}
			if len(rows) == 0 {
				return
			}
			s := Section{Title: title, More: max(total-len(rows), 0)}
			for _, r := range rows {
				listed[r.ID] = true
				il := issueLink(&r, publicURL, r.ProjectSlug)
				il.Meta = meta(r)
				s.Issues = append(s.Issues, il)
			}
			c.Sections = append(c.Sections, s)
		}
		add("New issues", digest.NewIssues, int(digest.NewTotal), func(r store.IssueRow) string {
			return fmt.Sprintf("%s · %s", r.ProjectSlug, plural(int(r.TimesSeen), "event", "events"))
		})
		add("Regressions", digest.Regressions, len(digest.Regressions), func(r store.IssueRow) string {
			if rel := event.ShortRelease(r.LastRelease); rel != "" {
				return r.ProjectSlug + " · back in " + rel
			}
			return r.ProjectSlug
		})
		add("Busiest", digest.Busiest, len(digest.Busiest), func(r store.IssueRow) string {
			return fmt.Sprintf("%s · %s %s", r.ProjectSlug, plural(int(r.TimesSeen), "event", "events"), span)
		})
		c.Action = &Link{"Open Watchtower", publicURL + "/issues"}
	default:
		return c, fmt.Errorf("unknown email kind %q", e.Kind)
	}
	return c, nil
}

// TestContent is the message sent from the account page.
func TestContent(publicURL string) Content {
	return Content{
		Subject:  "Test email",
		Preview:  "Email from this Watchtower server works.",
		Heading:  "Email works",
		Lines:    []string{"This test came from your Watchtower server. Notifications and digests will arrive at this address."},
		Action:   &Link{"Open Watchtower", publicURL + "/issues"},
		Settings: publicURL + "/settings/account",
	}
}

func issueLink(r *store.IssueRow, publicURL, project string) IssueLink {
	return IssueLink{
		Ref:     fmt.Sprintf("WT-%d", r.ID),
		Title:   trim(r.Title, 200),
		Culprit: trim(r.Culprit, 160),
		URL:     fmt.Sprintf("%s/issues/%d", publicURL, r.ID),
		Meta:    project,
	}
}

func trim(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// Render produces the message for a recipient. Subjects are prefixed with
// [Watchtower] so they are easy to filter.
func Render(to mail.Address, c Content) (Message, error) {
	var text, html bytes.Buffer
	if err := textTmpl.Execute(&text, c); err != nil {
		return Message{}, err
	}
	if err := htmlTmpl.Execute(&html, c); err != nil {
		return Message{}, err
	}
	return Message{To: to, Subject: "[Watchtower] " + c.Subject, Text: text.String(), HTML: html.String()}, nil
}

var textTmpl = texttemplate.Must(texttemplate.New("text").Parse(`{{.Heading}}
{{range .Lines}}
{{.}}
{{end}}{{with .Issue}}
{{.Ref}}: {{.Title}}{{if .Culprit}}
{{.Culprit}}{{end}}
{{.URL}}
{{end}}{{with .Quote}}
> {{.}}
{{end}}{{range .Sections}}
{{.Title}}
{{range .Issues}}- {{.Ref}} {{.Title}} ({{.Meta}})
  {{.URL}}
{{end}}{{if .More}}  and {{.More}} more
{{end}}{{end}}{{if and .Action (not .Issue)}}
{{.Action.Label}}: {{.Action.URL}}
{{end}}
--
Change which emails you get: {{.Settings}}
`))

var htmlTmpl = htmltemplate.Must(htmltemplate.New("html").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="color-scheme" content="light"><title>{{.Subject}}</title></head>
<body style="margin:0;padding:0;background:#f6f6f8;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;color:#18181b">
<span style="display:none;max-height:0;overflow:hidden;opacity:0">{{.Preview}}</span>
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:#f6f6f8"><tr><td align="center" style="padding:32px 16px">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:560px">
<tr><td style="padding:0 4px 16px;font-size:13px;font-weight:600;color:#6d28d9">Watchtower</td></tr>
<tr><td style="background:#ffffff;border:1px solid #e4e4e7;border-radius:12px;padding:28px">
<h1 style="margin:0 0 12px;font-size:18px;line-height:1.35;font-weight:600">{{.Heading}}</h1>
{{range .Lines}}<p style="margin:0 0 12px;font-size:14px;line-height:1.55;color:#3f3f46">{{.}}</p>{{end}}
{{with .Issue}}<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="margin:16px 0;border:1px solid #e4e4e7;border-left:3px solid #dc2626;border-radius:8px"><tr><td style="padding:12px 14px">
<div style="font-size:12px;color:#71717a">{{.Ref}} · {{.Meta}}</div>
<a href="{{.URL}}" style="display:block;margin-top:4px;font-size:14px;font-weight:600;line-height:1.4;color:#18181b;text-decoration:none">{{.Title}}</a>
{{if .Culprit}}<div style="margin-top:4px;font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:12px;color:#71717a">{{.Culprit}}</div>{{end}}
</td></tr></table>{{end}}
{{with .Quote}}<div style="margin:16px 0;padding:10px 14px;background:#f4f4f5;border-radius:8px;font-size:14px;line-height:1.55;color:#27272a;white-space:pre-wrap">{{.}}</div>{{end}}
{{range .Sections}}<h2 style="margin:22px 0 8px;font-size:12px;font-weight:600;letter-spacing:.04em;text-transform:uppercase;color:#71717a">{{.Title}}</h2>
{{range .Issues}}<div style="padding:8px 0;border-top:1px solid #f4f4f5"><a href="{{.URL}}" style="font-size:14px;font-weight:500;color:#18181b;text-decoration:none">{{.Title}}</a><div style="margin-top:2px;font-size:12px;color:#71717a">{{.Ref}} · {{.Meta}}</div></div>{{end}}
{{if .More}}<div style="padding:8px 0;border-top:1px solid #f4f4f5;font-size:12px;color:#71717a">and {{.More}} more</div>{{end}}{{end}}
{{with .Action}}<p style="margin:20px 0 0"><a href="{{.URL}}" style="display:inline-block;padding:9px 16px;background:#6d28d9;border-radius:8px;color:#ffffff;font-size:14px;font-weight:500;text-decoration:none">{{.Label}}</a></p>{{end}}
</td></tr>
<tr><td style="padding:16px 4px;font-size:12px;line-height:1.5;color:#71717a">You get this because of your Watchtower email settings. <a href="{{.Settings}}" style="color:#71717a">Change them</a>.</td></tr>
</table></td></tr></table></body></html>
`))
