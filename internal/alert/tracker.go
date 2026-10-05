package alert

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/olucurious/watchtower/internal/event"
	"github.com/olucurious/watchtower/internal/store"
)

// TrackerStore is what issue tracking needs from the store.
type TrackerStore interface {
	IssueLink(ctx context.Context, issueID int64, provider string) (*store.IssueLink, error)
	SaveIssueLink(ctx context.Context, l store.IssueLink, actor string, actorID int64) (store.IssueLink, error)
	IssueEvent(ctx context.Context, issueID int64, which string) (store.EventDetail, error)
	LinksToCheck(ctx context.Context, provider string, limit int) ([]store.LinkToCheck, error)
	LinkChecked(ctx context.Context, issueID int64, provider, state string, done bool) (bool, error)
}

// Tracker files Watchtower issues in Linear, comments on them as they
// change, and resolves Watchtower issues whose Linear issue is completed.
type Tracker struct {
	Store     TrackerStore
	Linear    *Linear
	Sealer    *Sealer
	PublicURL string
	Log       *slog.Logger
}

// LinearChannel is a Linear destination: its sealed key and team.
type LinearChannel struct {
	ID     int64
	Name   string
	Sealed []byte
	TeamID string
}

// Link returns the issue's Linear issue, creating it first if needed.
func (t *Tracker) Link(ctx context.Context, ch LinearChannel, issue *store.IssueRow, project, actor string, actorID int64) (store.IssueLink, bool, error) {
	if l, err := t.Store.IssueLink(ctx, issue.ID, "linear"); err != nil || l != nil {
		if l != nil {
			return *l, false, nil
		}
		return store.IssueLink{}, false, err
	}
	key, err := t.Sealer.Open(ch.Sealed)
	if err != nil {
		return store.IssueLink{}, false, err
	}
	// One ID per Watchtower issue, whichever destination files it: Linear
	// rejects a second creation, so concurrent filings converge on one issue.
	id, err := t.Sealer.DeriveUUID(fmt.Sprintf("linear-issue:%d", issue.ID))
	if err != nil {
		return store.IssueLink{}, false, err
	}
	var ev *event.Event
	if d, err := t.Store.IssueEvent(ctx, issue.ID, "latest"); err == nil {
		var e event.Event
		if json.Unmarshal(d.Data, &e) == nil {
			ev = &e
		}
	}
	li, err := t.Linear.CreateIssue(ctx, key, id, ch.TeamID, truncate(issue.Title, 240), t.description(issue, project, ev))
	if err != nil {
		return store.IssueLink{}, false, err
	}
	// The URL is shown as a link; accept only what Linear legitimately returns.
	if !strings.HasPrefix(li.URL, "https://") {
		return store.IssueLink{}, false, fmt.Errorf("Linear returned an unexpected issue URL")
	}
	chID := ch.ID
	saved, err := t.Store.SaveIssueLink(ctx, store.IssueLink{IssueID: issue.ID, Provider: "linear", ChannelID: &chID,
		ExternalID: li.ID, Identifier: li.Identifier, URL: li.URL, State: li.State.Type}, actor, actorID)
	return saved, err == nil, err
}

// Notify delivers an outbox notification to a Linear channel: a new or
// spiking issue gets a Linear issue; a linked one gets a comment instead.
func (t *Tracker) Notify(ctx context.Context, n *store.Notification) (time.Duration, error) {
	ch := LinearChannel{ID: n.Channel.ID, Name: n.Channel.Name, Sealed: n.Channel.Sealed, TeamID: n.Channel.Target}
	key, err := t.Sealer.Open(ch.Sealed)
	if err != nil {
		return 0, err
	}
	retry := func(err error) (time.Duration, error) {
		var rl *RateLimited
		if errors.As(err, &rl) {
			return rl.After, err
		}
		return 0, err
	}
	if n.Issue == nil { // a test: check the key still reaches the team, without filing anything
		teams, err := t.Linear.Teams(ctx, key)
		if err != nil {
			return retry(err)
		}
		for _, tm := range teams {
			if tm.ID == ch.TeamID {
				return 0, nil
			}
		}
		return 0, errors.New("the Linear key can no longer see this team")
	}
	existing, err := t.Store.IssueLink(ctx, n.Issue.ID, "linear")
	if err != nil {
		return 0, err
	}
	if existing == nil {
		_, _, err := t.Link(ctx, ch, n.Issue, n.Project.Name, "Alert: "+ch.Name, 0)
		return retry(err)
	}
	var body string
	release := event.ShortRelease(str(n.Detail["release"]))
	env := str(n.Detail["environment"])
	switch n.Kind {
	case "regression":
		body = "**Regressed** in Watchtower"
		if release != "" {
			body += " in release `" + release + "`"
		}
	case "frequency":
		count, _ := n.Detail["count"].(float64)
		body = fmt.Sprintf("**%d+ events in the last hour** in Watchtower", int(count))
	default:
		return 0, nil // already linked: a new-issue alert has nothing to add
	}
	if env != "" {
		body += " (" + env + ")"
	}
	body += fmt.Sprintf(". [Open in Watchtower](%s/issues/%d)", t.PublicURL, n.Issue.ID)
	return retry(t.Linear.Comment(ctx, key, existing.ExternalID, body))
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// description renders the Linear issue body in markdown.
func (t *Tracker) description(issue *store.IssueRow, project string, ev *event.Event) string {
	var b strings.Builder
	link := fmt.Sprintf("%s/issues/%d", t.PublicURL, issue.ID)
	if issue.Culprit != "" {
		fmt.Fprintf(&b, "`%s`\n\n", strings.ReplaceAll(issue.Culprit, "`", "'"))
	}
	fmt.Fprintf(&b, "**%s** · %d %s · first seen %s · last seen %s\n", project, issue.TimesSeen, plural(issue.TimesSeen, "event", "events"),
		issue.FirstSeen.UTC().Format("2 Jan 2006 15:04 UTC"), issue.LastSeen.UTC().Format("2 Jan 2006 15:04 UTC"))
	var meta []string
	if r := event.ShortRelease(issue.LastRelease); r != "" {
		meta = append(meta, "release `"+r+"`")
	}
	if len(issue.Environments) > 0 {
		meta = append(meta, strings.Join(issue.Environments, ", "))
	}
	if len(meta) > 0 {
		b.WriteString(strings.Join(meta, " · ") + "\n")
	}
	if ev != nil {
		if trace := stackText(ev); trace != "" {
			b.WriteString("\n```\n" + trace + "\n```\n")
		}
	}
	fmt.Fprintf(&b, "\n[Open WT-%d in Watchtower](%s)\n", issue.ID, link)
	return b.String()
}

// stackText summarizes the raised exception and its causes, newest frame
// first, preferring the app's own frames.
func stackText(ev *event.Event) string {
	var b strings.Builder
	for i := len(ev.Exceptions) - 1; i >= 0 && i >= len(ev.Exceptions)-3; i-- {
		ex := ev.Exceptions[i]
		if i < len(ev.Exceptions)-1 {
			b.WriteString("\nCaused by: ")
		}
		b.WriteString(strings.TrimSpace(ex.Type + ": " + firstLine(ex.Value)))
		b.WriteString("\n")
		shown := 0
		for j := len(ex.Frames) - 1; j >= 0 && shown < 8; j-- {
			f := ex.Frames[j]
			if !f.InApp && hasInApp(ex.Frames) {
				continue
			}
			fn := f.Function
			if f.Module != "" && !strings.Contains(fn, f.Module) {
				fn = f.Module + "." + fn
			}
			loc := f.Filename
			if loc == "" {
				loc = f.AbsPath
			}
			if f.Lineno > 0 {
				loc += fmt.Sprintf(":%d", f.Lineno)
			}
			fmt.Fprintf(&b, "  at %s (%s)\n", strings.TrimSpace(fn), loc)
			shown++
		}
	}
	return strings.TrimRight(strings.ReplaceAll(b.String(), "```", "'''"), "\n")
}

func hasInApp(fs []event.Frame) bool {
	for _, f := range fs {
		if f.InApp {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func plural(n int64, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// Sync resolves Watchtower issues whose Linear issue was completed. It
// polls rather than receiving webhooks, so it works on private networks.
// A key that fails (revoked, rate limited) does not hold up the others;
// the first such error is returned after every key has been tried.
func (t *Tracker) Sync(ctx context.Context) (resolved int, err error) {
	links, err := t.Store.LinksToCheck(ctx, "linear", 200)
	if err != nil {
		return 0, err
	}
	// Links are checked oldest first, so every link examined is marked as
	// checked, even when it can't be: otherwise deleted Linear issues or a
	// revoked key would stay at the front and starve all the others.
	byKey := map[string][]store.LinkToCheck{}
	var unreadable []store.LinkToCheck
	for _, l := range links {
		key, err := t.Sealer.Open(l.Sealed)
		if err != nil {
			unreadable = append(unreadable, l)
			continue
		}
		byKey[key] = append(byKey[key], l)
	}
	if len(unreadable) > 0 {
		t.Log.Warn("cannot read the Linear key of some linked issues; was WATCHTOWER_SECRET_KEY changed?", "links", len(unreadable))
		if err := t.markChecked(ctx, unreadable); err != nil {
			return resolved, err
		}
	}
	var firstErr error
	for key, ls := range byKey {
	batches:
		for start := 0; start < len(ls); start += 100 {
			batch := ls[start:min(start+100, len(ls))]
			ids := make([]string, len(batch))
			for i, l := range batch {
				ids[i] = l.ExternalID
			}
			states, err := t.Linear.States(ctx, key, ids)
			if err != nil {
				if ctx.Err() != nil {
					return resolved, ctx.Err()
				}
				firstErr = cmp.Or(firstErr, err)
				var limited *RateLimited
				if errors.As(err, &limited) {
					break batches // retried first next time
				}
				if err := t.markChecked(ctx, batch); err != nil {
					return resolved, err
				}
				continue
			}
			for _, l := range batch {
				state, ok := states[l.ExternalID]
				if !ok {
					state = l.State // deleted, or no longer visible to this key
				}
				done, err := t.Store.LinkChecked(ctx, l.IssueID, "linear", state, ok && state == "completed")
				if err != nil {
					return resolved, err
				}
				if done {
					resolved++
				}
			}
		}
	}
	return resolved, firstErr
}

// markChecked records links as checked without changing their state, so
// links that can't be checked move to the back of the queue.
func (t *Tracker) markChecked(ctx context.Context, links []store.LinkToCheck) error {
	for _, l := range links {
		if _, err := t.Store.LinkChecked(ctx, l.IssueID, "linear", l.State, false); err != nil {
			return err
		}
	}
	return nil
}

// RunSync calls Sync every interval until ctx ends.
func (t *Tracker) RunSync(ctx context.Context, every time.Duration) {
	for {
		if n, err := t.Sync(ctx); err != nil && ctx.Err() == nil {
			t.Log.Warn("Linear sync failed", "err", err)
		} else if n > 0 {
			t.Log.Info("resolved issues completed in Linear", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}
