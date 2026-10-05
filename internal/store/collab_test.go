package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAssignmentAndComments(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p, _ := s.CreateProject(ctx, "web", "Web")
	ada, _ := s.CreateUser(ctx, "ada@example.com", "Ada", "x", true)
	grace, _ := s.CreateUser(ctx, "grace@example.com", "", "x", false)
	ingest(t, s, testEvent(p.ID, eventID(1), time.Now()))
	issues, _ := s.ListIssues(ctx, IssueFilter{})
	id := issues.Issues[0].ID

	if err := s.SetAssignee(ctx, id, &grace.ID, "Ada", ada.ID); err != nil {
		t.Fatal(err)
	}
	// Reassigning to the same person records nothing new.
	if err := s.SetAssignee(ctx, id, &grace.ID, "Ada", ada.ID); err != nil {
		t.Fatal(err)
	}
	d, _ := s.Issue(ctx, id)
	if d.AssigneeID == nil || *d.AssigneeID != grace.ID || d.Assignee != "grace@example.com" {
		t.Errorf("assignee %v %q", d.AssigneeID, d.Assignee)
	}
	if d.Activity[0].Kind != "assigned" || d.Activity[0].Detail["assignee"] != "grace@example.com" || d.Activity[1].Kind != "first_seen" {
		t.Errorf("activity %+v", d.Activity)
	}
	mine, _ := s.ListIssues(ctx, IssueFilter{AssigneeID: grace.ID})
	none, _ := s.ListIssues(ctx, IssueFilter{Unassigned: true})
	if mine.Total != 1 || none.Total != 0 || mine.Issues[0].Assignee != "grace@example.com" {
		t.Errorf("assignee filters: mine=%d none=%d", mine.Total, none.Total)
	}

	s.SetUserDisabled(ctx, ada.ID, true)
	if err := s.SetAssignee(ctx, id, &ada.ID, "Grace", grace.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("assigning a disabled user: %v", err)
	}
	s.SetUserDisabled(ctx, ada.ID, false)
	if err := s.SetAssignee(ctx, id, nil, "Grace", grace.ID); err != nil {
		t.Fatal(err)
	}

	cid, err := s.AddComment(ctx, id, "PlanetScale's connection limit during deploys", "Grace", grace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddComment(ctx, 99999, "x", "Grace", grace.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("comment on a missing issue: %v", err)
	}
	d, _ = s.Issue(ctx, id)
	c := d.Activity[0]
	if c.Kind != "comment" || c.ID != cid || c.UserID == nil || *c.UserID != grace.ID || c.Detail["body"] == "" || d.Activity[1].Kind != "unassigned" {
		t.Errorf("comment activity %+v", d.Activity)
	}
	if err := s.DeleteComment(ctx, id, cid, ada.ID, false); !errors.Is(err, ErrNotFound) {
		t.Error("only the author or an admin may delete a comment")
	}
	if err := s.DeleteComment(ctx, id, cid, ada.ID, true); err != nil {
		t.Errorf("admin delete: %v", err)
	}
	// Only comments can be deleted, never system history.
	if err := s.DeleteComment(ctx, id, d.Activity[1].ID, grace.ID, true); !errors.Is(err, ErrNotFound) {
		t.Error("deleted a non-comment activity row")
	}

	if err := s.SetProjectRepo(ctx, "web", "https://github.com/acme/web"); err != nil {
		t.Fatal(err)
	}
	ps, _ := s.Projects(ctx)
	if ps[0].RepoURL != "https://github.com/acme/web" {
		t.Errorf("repo url %q", ps[0].RepoURL)
	}
	ms, _ := s.Members(ctx)
	if len(ms) != 2 || ms[0].Name != "Ada" {
		t.Errorf("members %+v", ms)
	}
}

func TestDeliveredAlertIsRecorded(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p, _ := s.CreateProject(ctx, "web", "Web")
	s.CreateAlertChannel(ctx, "web", "slack", AlertTarget{Sealed: []byte("sealed"), Hint: "hint"}, AlertRules{Name: "#errors", OnNewIssue: true, MinLevel: "error"}, 0)
	ingest(t, s, testEvent(p.ID, eventID(1), time.Now()))
	if sent, _, err := s.DeliverNotifications(ctx, 10, 8, func(context.Context, *Notification) (time.Duration, error) { return 0, nil }); err != nil || sent != 1 {
		t.Fatalf("sent=%d err=%v", sent, err)
	}
	issues, _ := s.ListIssues(ctx, IssueFilter{})
	d, _ := s.Issue(ctx, issues.Issues[0].ID)
	if d.Activity[0].Kind != "alerted" || d.Activity[0].Detail["channel"] != "#errors" || d.Activity[0].Detail["alert"] != "new_issue" {
		t.Errorf("activity %+v", d.Activity)
	}
}
