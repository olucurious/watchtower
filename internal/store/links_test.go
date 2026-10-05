package store

import (
	"context"
	"testing"
	"time"
)

func TestIssueLinks(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p, _ := s.CreateProject(ctx, "web", "Web")
	ada, _ := s.CreateUser(ctx, "ada@example.com", "Ada", "x", true)
	ch, err := s.CreateAlertChannel(ctx, "web", "linear", AlertTarget{Sealed: []byte("key"), Hint: "lin_api_…abcd", Channel: "team-1", Label: "Reader (LIB)"},
		AlertRules{Name: "Triage", OnRegression: true, MinLevel: "error"}, ada.ID)
	if err != nil {
		t.Fatal(err)
	}
	ingest(t, s, testEvent(p.ID, eventID(1), time.Now()))
	issues, _ := s.ListIssues(ctx, IssueFilter{})
	id := issues.Issues[0].ID

	l := IssueLink{IssueID: id, Provider: "linear", ChannelID: &ch, ExternalID: "uuid-1", Identifier: "LIB-1", URL: "https://linear.app/x/LIB-1", State: "unstarted"}
	saved, err := s.SaveIssueLink(ctx, l, "Ada", ada.ID)
	if err != nil || saved.Identifier != "LIB-1" {
		t.Fatalf("save %+v %v", saved, err)
	}
	// A concurrent second creation keeps the first link and logs nothing new.
	l2 := l
	l2.ExternalID, l2.Identifier = "uuid-2", "LIB-2"
	if again, _ := s.SaveIssueLink(ctx, l2, "Alert: Triage", 0); again.Identifier != "LIB-1" {
		t.Errorf("second link replaced the first: %+v", again)
	}
	d, _ := s.Issue(ctx, id)
	if len(d.Links) != 1 || d.Links[0].Identifier != "LIB-1" || len(d.Trackers) != 1 || d.Trackers[0].Team != "Reader (LIB)" {
		t.Errorf("detail links %+v trackers %+v", d.Links, d.Trackers)
	}
	if d.Activity[0].Kind != "linked" || d.Activity[0].Detail["identifier"] != "LIB-1" || d.Activity[0].Actor != "Ada" || d.Activity[1].Kind == "linked" {
		t.Errorf("activity %+v", d.Activity[:2])
	}
	chs, _ := s.AlertChannels(ctx, "web")
	if chs[0].TargetLabel != "Reader (LIB)" || chs[0].TargetChannel != "team-1" {
		t.Errorf("channel %+v", chs[0])
	}

	check, _ := s.LinksToCheck(ctx, "linear", 10)
	if len(check) != 1 || string(check[0].Sealed) != "key" {
		t.Fatalf("to check %+v", check)
	}
	if done, _ := s.LinkChecked(ctx, id, "linear", "started", false); done {
		t.Error("resolved while still started")
	}
	if done, err := s.LinkChecked(ctx, id, "linear", "completed", true); err != nil || !done {
		t.Fatalf("completion: %v %v", done, err)
	}
	d, _ = s.Issue(ctx, id)
	if d.Status != "resolved" || d.Activity[0].Kind != "resolved" || d.Activity[0].Actor != "Linear" || d.Activity[0].Detail["identifier"] != "LIB-1" || d.Links[0].State != "completed" {
		t.Errorf("after completion: %s %+v", d.Status, d.Activity[0])
	}
	if check, _ := s.LinksToCheck(ctx, "linear", 10); len(check) != 0 {
		t.Error("resolved issues need no checking")
	}
	// Deleting the destination keeps the link but stops syncing it.
	s.SetIssuesStatus(ctx, []int64{id}, "unresolved", "Ada")
	s.DeleteAlertChannel(ctx, "web", ch)
	if check, _ := s.LinksToCheck(ctx, "linear", 10); len(check) != 0 {
		t.Error("link without a channel should not be checked")
	}
	if d, _ = s.Issue(ctx, id); len(d.Links) != 1 {
		t.Error("link should survive its channel")
	}
}
