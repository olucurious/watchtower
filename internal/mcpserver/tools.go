package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/olucurious/watchtower/internal/alert"
	"github.com/olucurious/watchtower/internal/event"
	"github.com/olucurious/watchtower/internal/store"
)

type noInput struct{}

type listIssuesInput struct {
	Project     string `json:"project,omitempty" jsonschema:"project slug, as shown by list_projects; omit for all projects"`
	Status      string `json:"status,omitempty" jsonschema:"unresolved (default), resolved, muted or all"`
	Query       string `json:"query,omitempty" jsonschema:"text to find in issue titles and code locations"`
	Environment string `json:"environment,omitempty" jsonschema:"only issues seen in this environment, such as production"`
	Assignee    string `json:"assignee,omitempty" jsonschema:"me, none (unassigned) or a member's email"`
	Period      string `json:"period,omitempty" jsonschema:"only issues seen within 1h, 24h, 7d, 14d, 30d or 90d"`
	Sort        string `json:"sort,omitempty" jsonschema:"last_seen (default), first_seen or times_seen"`
	Limit       int    `json:"limit,omitempty" jsonschema:"how many to return, 1 to 50 (default 20)"`
	Offset      int    `json:"offset,omitempty" jsonschema:"how many to skip, for paging"`
}

type issueInput struct {
	Issue string `json:"issue" jsonschema:"the issue: WT-91, 91, or its Watchtower URL"`
}

type eventInput struct {
	Issue string `json:"issue" jsonschema:"the issue: WT-91, 91, or its Watchtower URL"`
	Event string `json:"event,omitempty" jsonschema:"an event ID from list_events, or latest (default) or oldest"`
}

type listEventsInput struct {
	Issue  string `json:"issue" jsonschema:"the issue: WT-91, 91, or its Watchtower URL"`
	Limit  int    `json:"limit,omitempty" jsonschema:"how many to return, 1 to 100 (default 25)"`
	Offset int    `json:"offset,omitempty" jsonschema:"how many to skip, for paging"`
}

type statusInput struct {
	Issue  string `json:"issue" jsonschema:"the issue: WT-91, 91, or its Watchtower URL"`
	Status string `json:"status" jsonschema:"resolved, muted or unresolved"`
}

type assignInput struct {
	Issue    string `json:"issue" jsonschema:"the issue: WT-91, 91, or its Watchtower URL"`
	Assignee string `json:"assignee" jsonschema:"me, a member's email, or an empty string to unassign"`
}

type commentInput struct {
	Issue string `json:"issue" jsonschema:"the issue: WT-91, 91, or its Watchtower URL"`
	Body  string `json:"body" jsonschema:"the comment, up to 2000 characters; plain text"`
}

type linearInput struct {
	Issue       string `json:"issue" jsonschema:"the issue: WT-91, 91, or its Watchtower URL"`
	Destination string `json:"destination,omitempty" jsonschema:"which of the project's Linear destinations, by team or name; needed only when there are several"`
}

func readOnly(title string) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true}
}

func writes(title string, idempotent bool) *mcp.ToolAnnotations {
	no := false
	return &mcp.ToolAnnotations{Title: title, IdempotentHint: idempotent, DestructiveHint: &no}
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func (s *Server) addTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{Name: "list_projects", Description: "List Watchtower projects with their unresolved issue counts and recent volume.",
		Annotations: readOnly("List projects")}, s.listProjects)
	mcp.AddTool(srv, &mcp.Tool{Name: "list_issues", Description: "Find issues (grouped errors). Unresolved issues, most recently seen first, unless filtered otherwise.",
		Annotations: readOnly("List issues")}, s.listIssues)
	mcp.AddTool(srv, &mcp.Tool{Name: "get_issue", Description: "Everything needed to understand and fix an issue: the latest stack trace with source context, where and how often it happens, releases, tags, and the team's activity and comments.",
		Annotations: readOnly("Get issue")}, s.getIssue)
	mcp.AddTool(srv, &mcp.Tool{Name: "list_events", Description: "List an issue's individual occurrences, newest first, with release and environment.",
		Annotations: readOnly("List events")}, s.listEvents)
	mcp.AddTool(srv, &mcp.Tool{Name: "get_event", Description: "Show one occurrence of an issue in full: stack trace with source context, context data, request and tags.",
		Annotations: readOnly("Get event")}, s.getEvent)
	mcp.AddTool(srv, &mcp.Tool{Name: "update_issue_status", Description: "Resolve, mute or reopen an issue. Resolve once a fix is deployed; if the error happens again the issue reopens as a regression. Needs a write token.",
		Annotations: writes("Update issue status", true)}, s.updateStatus)
	mcp.AddTool(srv, &mcp.Tool{Name: "assign_issue", Description: "Assign an issue to yourself or a member, or unassign it. Needs a write token.",
		Annotations: writes("Assign issue", true)}, s.assign)
	mcp.AddTool(srv, &mcp.Tool{Name: "add_comment", Description: "Add a comment to an issue's activity, for example the root cause, a link to the fix, or what remains. Needs a write token.",
		Annotations: writes("Add comment", false)}, s.comment)
	mcp.AddTool(srv, &mcp.Tool{Name: "create_linear_issue", Description: "File the issue in Linear through one of its project's Linear destinations, or return the existing Linear issue. Needs a write token.",
		Annotations: writes("Create Linear issue", true)}, s.createLinear)
}

var issueRef = regexp.MustCompile(`(?i)^(?:WT-)?(\d+)$|/issues/(\d+)`)

func parseIssue(ref string) (int64, error) {
	m := issueRef.FindStringSubmatch(strings.TrimSpace(ref))
	if m == nil {
		return 0, fmt.Errorf("%q is not an issue reference; use WT-91, 91 or an issue URL", ref)
	}
	n, _ := strconv.ParseInt(m[1]+m[2], 10, 64)
	return n, nil
}

func (s *Server) loadIssue(ctx context.Context, ref string) (store.IssueDetail, error) {
	id, err := parseIssue(ref)
	if err != nil {
		return store.IssueDetail{}, err
	}
	d, err := s.Store.Issue(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return d, fmt.Errorf("there is no issue WT-%d", id)
	}
	return d, err
}

var errReadOnly = errors.New("this access token is read-only; create a write token on the Watchtower account page to change issues")

func (s *Server) listProjects(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, any, error) {
	ps, err := s.Store.Projects(ctx)
	if err != nil {
		return nil, nil, err
	}
	var b strings.Builder
	if len(ps) == 0 {
		b.WriteString("No projects yet.")
	}
	for _, p := range ps {
		fmt.Fprintf(&b, "- %s (slug %s): %s unresolved, %s in the last 24 hours\n", p.Name, p.Slug, plural(p.UnresolvedIssues, "issue"), plural(p.Events24h, "event"))
	}
	return text(b.String()), nil, nil
}

var periods = map[string]time.Duration{"1h": time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour,
	"14d": 14 * 24 * time.Hour, "30d": 30 * 24 * time.Hour, "90d": 90 * 24 * time.Hour}

func (s *Server) listIssues(ctx context.Context, req *mcp.CallToolRequest, in listIssuesInput) (*mcp.CallToolResult, any, error) {
	f := store.IssueFilter{Project: in.Project, Query: in.Query, Environment: in.Environment, Sort: in.Sort, Offset: max(in.Offset, 0)}
	switch in.Status {
	case "", "unresolved":
		f.Status = "unresolved"
	case "all":
	case "resolved", "muted":
		f.Status = in.Status
	default:
		return nil, nil, fmt.Errorf("status must be unresolved, resolved, muted or all")
	}
	if in.Period != "" {
		d, ok := periods[in.Period]
		if !ok {
			return nil, nil, fmt.Errorf("period must be 1h, 24h, 7d, 14d, 30d or 90d")
		}
		f.Period = d
	}
	switch a := strings.TrimSpace(in.Assignee); a {
	case "":
	case "me":
		u, _ := caller(req)
		f.AssigneeID = u.ID
	case "none":
		f.Unassigned = true
	default:
		u, err := s.Store.UserByEmail(ctx, a)
		if err != nil {
			return nil, nil, fmt.Errorf("no member with email %q", a)
		}
		f.AssigneeID = u.ID
	}
	f.Limit = in.Limit
	if f.Limit <= 0 {
		f.Limit = 20
	}
	f.Limit = min(f.Limit, 50)
	page, err := s.Store.ListIssues(ctx, f)
	if err != nil {
		return nil, nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Found %s", plural(page.Total, "issue"))
	if len(page.Issues) < int(page.Total) {
		fmt.Fprintf(&b, " (showing %d from offset %d)", len(page.Issues), f.Offset)
	}
	b.WriteString(".\n")
	for _, i := range page.Issues {
		fmt.Fprintf(&b, "\nWT-%d [%s, %s] %s\n", i.ID, i.Status, i.Level, i.Title)
		if i.Culprit != "" {
			fmt.Fprintf(&b, "  at %s\n", i.Culprit)
		}
		fmt.Fprintf(&b, "  project %s · %s · last seen %s · first seen %s", i.ProjectSlug, plural(i.TimesSeen, "event"), ago(i.LastSeen), ago(i.FirstSeen))
		if r := event.ShortRelease(i.LastRelease); r != "" {
			fmt.Fprintf(&b, " · release %s", r)
		}
		if i.Assignee != "" {
			fmt.Fprintf(&b, " · assigned to %s", i.Assignee)
		}
		if i.RegressedAt != nil && i.Status == "unresolved" {
			b.WriteString(" · regressed")
		}
		b.WriteString("\n")
	}
	return text(b.String()), nil, nil
}

func (s *Server) getIssue(ctx context.Context, _ *mcp.CallToolRequest, in issueInput) (*mcp.CallToolResult, any, error) {
	d, err := s.loadIssue(ctx, in.Issue)
	if err != nil {
		return nil, nil, err
	}
	ev, err := s.Store.IssueEvent(ctx, d.ID, "latest")
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, nil, err
	}
	return text(issueBrief(d, ev, s.PublicURL)), nil, nil
}

func (s *Server) listEvents(ctx context.Context, _ *mcp.CallToolRequest, in listEventsInput) (*mcp.CallToolResult, any, error) {
	d, err := s.loadIssue(ctx, in.Issue)
	if err != nil {
		return nil, nil, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 25
	}
	rows, err := s.Store.IssueEvents(ctx, d.ID, min(limit, 100), max(in.Offset, 0))
	if err != nil {
		return nil, nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "WT-%d has %s; newest first:\n", d.ID, plural(d.TimesSeen, "event"))
	for _, r := range rows {
		fmt.Fprintf(&b, "- %s at %s", r.EventID, r.OccurredAt.UTC().Format(time.RFC3339))
		for _, kv := range [][2]string{{"release", event.ShortRelease(r.Release)}, {"environment", r.Environment}, {"server", r.ServerName}, {"user", r.UserID}} {
			if kv[1] != "" {
				fmt.Fprintf(&b, " · %s %s", kv[0], kv[1])
			}
		}
		b.WriteString("\n")
	}
	return text(b.String()), nil, nil
}

func (s *Server) getEvent(ctx context.Context, _ *mcp.CallToolRequest, in eventInput) (*mcp.CallToolResult, any, error) {
	d, err := s.loadIssue(ctx, in.Issue)
	if err != nil {
		return nil, nil, err
	}
	which := strings.TrimSpace(in.Event)
	if which == "" {
		which = "latest"
	}
	if which != "latest" && which != "oldest" {
		which = event.NormalizeID(which)
		if which == "" {
			return nil, nil, fmt.Errorf("event must be an event ID, latest or oldest")
		}
	}
	ev, err := s.Store.IssueEvent(ctx, d.ID, which)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, fmt.Errorf("WT-%d has no event %s", d.ID, in.Event)
	}
	if err != nil {
		return nil, nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Event %s of WT-%d (%s)\n%s\n\n", ev.EventID, d.ID, d.Title, untrustedNote)
	writeEvent(&b, ev)
	return text(b.String()), nil, nil
}

func (s *Server) updateStatus(ctx context.Context, req *mcp.CallToolRequest, in statusInput) (*mcp.CallToolResult, any, error) {
	u, write := caller(req)
	if !write {
		return nil, nil, errReadOnly
	}
	if in.Status != "resolved" && in.Status != "muted" && in.Status != "unresolved" {
		return nil, nil, fmt.Errorf("status must be resolved, muted or unresolved")
	}
	d, err := s.loadIssue(ctx, in.Issue)
	if err != nil {
		return nil, nil, err
	}
	n, err := s.Store.SetIssuesStatus(ctx, []int64{d.ID}, in.Status, actorName(u))
	if err != nil {
		return nil, nil, err
	}
	if n == 0 {
		return text(fmt.Sprintf("WT-%d was already %s.", d.ID, in.Status)), nil, nil
	}
	return text(fmt.Sprintf("WT-%d is now %s.", d.ID, in.Status)), nil, nil
}

func (s *Server) assign(ctx context.Context, req *mcp.CallToolRequest, in assignInput) (*mcp.CallToolResult, any, error) {
	u, write := caller(req)
	if !write {
		return nil, nil, errReadOnly
	}
	d, err := s.loadIssue(ctx, in.Issue)
	if err != nil {
		return nil, nil, err
	}
	var target *int64
	name := ""
	switch a := strings.TrimSpace(in.Assignee); a {
	case "":
	case "me":
		target, name = &u.ID, actorName(u)
	default:
		m, err := s.Store.UserByEmail(ctx, a)
		if err != nil || m.Disabled {
			return nil, nil, fmt.Errorf("no active member with email %q", a)
		}
		target, name = &m.ID, actorName(m)
	}
	if err := s.Store.SetAssignee(ctx, d.ID, target, actorName(u), u.ID); err != nil {
		return nil, nil, err
	}
	if target == nil {
		return text(fmt.Sprintf("WT-%d is unassigned.", d.ID)), nil, nil
	}
	return text(fmt.Sprintf("WT-%d is assigned to %s.", d.ID, name)), nil, nil
}

func (s *Server) comment(ctx context.Context, req *mcp.CallToolRequest, in commentInput) (*mcp.CallToolResult, any, error) {
	u, write := caller(req)
	if !write {
		return nil, nil, errReadOnly
	}
	body := strings.TrimSpace(in.Body)
	if body == "" || len([]rune(body)) > store.MaxCommentLength || strings.ContainsRune(body, 0) {
		return nil, nil, fmt.Errorf("a comment must be 1 to %d characters", store.MaxCommentLength)
	}
	d, err := s.loadIssue(ctx, in.Issue)
	if err != nil {
		return nil, nil, err
	}
	if _, err := s.Store.AddComment(ctx, d.ID, body, actorName(u), u.ID); err != nil {
		return nil, nil, err
	}
	return text(fmt.Sprintf("Comment added to WT-%d.", d.ID)), nil, nil
}

func (s *Server) createLinear(ctx context.Context, req *mcp.CallToolRequest, in linearInput) (*mcp.CallToolResult, any, error) {
	u, write := caller(req)
	if !write {
		return nil, nil, errReadOnly
	}
	d, err := s.loadIssue(ctx, in.Issue)
	if err != nil {
		return nil, nil, err
	}
	for _, l := range d.Links {
		if l.Provider == "linear" {
			return text(fmt.Sprintf("WT-%d is already %s in Linear: %s", d.ID, l.Identifier, l.URL)), nil, nil
		}
	}
	var pick *store.TrackerChannel
	want := strings.ToLower(strings.TrimSpace(in.Destination))
	for i, t := range d.Trackers {
		if want == "" || strings.Contains(strings.ToLower(t.Team), want) || strings.Contains(strings.ToLower(t.Name), want) {
			if pick != nil {
				return nil, nil, fmt.Errorf("several Linear destinations match; pass destination as one of: %s", trackerNames(d.Trackers))
			}
			pick = &d.Trackers[i]
		}
	}
	if pick == nil {
		if len(d.Trackers) == 0 {
			return nil, nil, fmt.Errorf("project %s has no Linear destination; an administrator can add one on the project page", d.ProjectSlug)
		}
		return nil, nil, fmt.Errorf("no Linear destination matches %q; choose one of: %s", in.Destination, trackerNames(d.Trackers))
	}
	ch, err := s.Store.ChannelTarget(ctx, d.ProjectSlug, pick.ID)
	if err != nil {
		return nil, nil, err
	}
	link, _, err := s.Tracker.Link(ctx, alert.LinearChannel{ID: ch.ID, Name: ch.Name, Sealed: ch.Sealed, TeamID: ch.Channel}, &d.IssueRow, d.ProjectName, actorName(u), u.ID)
	if err != nil {
		return nil, nil, err
	}
	return text(fmt.Sprintf("WT-%d is %s in Linear: %s", d.ID, link.Identifier, link.URL)), nil, nil
}

func trackerNames(ts []store.TrackerChannel) string {
	names := make([]string, len(ts))
	for i, t := range ts {
		names[i] = fmt.Sprintf("%q (%s)", t.Team, t.Name)
	}
	return strings.Join(names, ", ")
}

// decodeEvent parses a stored event; a malformed one yields nil.
func decodeEvent(raw json.RawMessage) *event.Event {
	var e event.Event
	if json.Unmarshal(raw, &e) != nil {
		return nil
	}
	return &e
}
