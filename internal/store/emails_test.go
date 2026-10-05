package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func queuedEmails(t *testing.T, s *Store) []string {
	t.Helper()
	rows, _ := s.pool.Query(context.Background(), `select u.email || ':' || m.kind from emails m join users u on u.id = m.user_id order by m.id`)
	var out []string
	for rows.Next() {
		var v string
		rows.Scan(&v)
		out = append(out, v)
	}
	return out
}

func TestEmailTriggers(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p, _ := s.CreateProject(ctx, "web", "Web")
	ada, _ := s.CreateUser(ctx, "ada@example.com", "Ada", "x", true)
	grace, _ := s.CreateUser(ctx, "grace@example.com", "Grace", "x", false)
	ingest(t, s, testEvent(p.ID, eventID(1), time.Now()))
	issues, _ := s.ListIssues(ctx, IssueFilter{})
	id := issues.Issues[0].ID

	// Without a mail server nothing is queued.
	s.SetAssignee(ctx, id, &grace.ID, "Ada", ada.ID)
	if q := queuedEmails(t, s); len(q) != 0 {
		t.Fatalf("queued with email disabled: %v", q)
	}
	s.SetAssignee(ctx, id, nil, "Ada", ada.ID)

	s.EmailEnabled = true
	s.SetAssignee(ctx, id, &grace.ID, "Ada", ada.ID)  // tells Grace
	s.AddComment(ctx, id, "on it", "Grace", grace.ID) // own comment: nothing
	s.AddComment(ctx, id, "see logs", "Ada", ada.ID)  // tells Grace
	s.SetIssuesStatus(ctx, []int64{id}, "resolved", "Grace")
	ingest(t, s, testEvent(p.ID, eventID(2), time.Now())) // regression tells Grace
	s.SetAssignee(ctx, id, &ada.ID, "Ada", ada.ID)        // taking it yourself: nothing
	want := []string{"grace@example.com:assigned", "grace@example.com:commented", "grace@example.com:regressed"}
	if q := queuedEmails(t, s); len(q) != 3 || q[0] != want[0] || q[1] != want[1] || q[2] != want[2] {
		t.Fatalf("queued %v", q)
	}

	// Preferences and disabled accounts are respected.
	s.SetEmailPrefs(ctx, ada.ID, EmailPrefs{Assigned: true, Regressed: true, Comments: false, Digest: "off"})
	s.AddComment(ctx, id, "ping", "Grace", grace.ID)
	s.SetUserDisabled(ctx, grace.ID, true)
	s.SetUserDisabled(ctx, grace.ID, false)
	s.SetAssignee(ctx, id, &grace.ID, "Ada", ada.ID)
	s.SetUserDisabled(ctx, grace.ID, true)
	s.SetAssignee(ctx, id, nil, "Ada", ada.ID)
	if q := queuedEmails(t, s); len(q) != 4 || q[3] != "grace@example.com:assigned" {
		t.Errorf("after preferences: %v", q)
	}
	prefs, _ := s.EmailPrefs(ctx, ada.ID)
	if prefs.Comments || !prefs.Assigned || prefs.Digest != "off" {
		t.Errorf("prefs %+v", prefs)
	}
}

func TestDigestScheduling(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	s.EmailEnabled = true
	ada, _ := s.CreateUser(ctx, "ada@example.com", "Ada", "x", true)
	grace, _ := s.CreateUser(ctx, "grace@example.com", "Grace", "x", false)
	s.SetEmailPrefs(ctx, ada.ID, EmailPrefs{Digest: "daily"})
	s.SetEmailPrefs(ctx, grace.ID, EmailPrefs{Digest: "weekly"})

	monday := time.Date(2026, 10, 5, 7, 30, 0, 0, time.UTC)
	if n, _ := s.QueueDigests(ctx, monday); n != 0 {
		t.Errorf("digests before %d:00 UTC: %d", DigestHour, n)
	}
	for i := 0; i < 2; i++ { // a second worker, or a second check, adds nothing
		s.QueueDigests(ctx, monday.Add(2*time.Hour))
	}
	s.QueueDigests(ctx, monday.Add(26*time.Hour)) // Tuesday: daily only
	q := queuedEmails(t, s)
	if len(q) != 3 || q[0] != "ada@example.com:digest" || q[1] != "grace@example.com:digest" || q[2] != "ada@example.com:digest" {
		t.Errorf("digests %v", q)
	}
}

func TestDigestAndDelivery(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	s.EmailEnabled = true
	p, _ := s.CreateProject(ctx, "web", "Web")
	ada, _ := s.CreateUser(ctx, "ada@example.com", "Ada", "x", true)
	grace, _ := s.CreateUser(ctx, "grace@example.com", "Grace", "x", false)
	now := time.Now()
	ingest(t, s, testEvent(p.ID, eventID(1), now.Add(-2*time.Hour)), testEvent(p.ID, eventID(2), now.Add(-time.Hour)))

	d, err := s.Digest(ctx, now.Add(-24*time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	if d.NewTotal != 1 || d.Events != 2 || len(d.NewIssues) != 1 || len(d.Busiest) != 1 || d.Busiest[0].TimesSeen != 2 || d.Empty() {
		t.Errorf("digest %+v", d)
	}
	if empty, _ := s.Digest(ctx, now.Add(-72*time.Hour), now.Add(-48*time.Hour)); !empty.Empty() {
		t.Errorf("quiet period should be empty: %+v", empty)
	}

	issues, _ := s.ListIssues(ctx, IssueFilter{})
	s.SetAssignee(ctx, issues.Issues[0].ID, &grace.ID, "Ada", ada.ID)
	s.AddComment(ctx, issues.Issues[0].ID, "skip me", "Ada", ada.ID)
	var got []*Email
	sent, failed, err := s.DeliverEmails(ctx, 10, 8, func(_ context.Context, e *Email) error {
		got = append(got, e)
		switch e.Kind {
		case "commented":
			return ErrSkipEmail
		}
		return errors.New("421 try later")
	})
	if err != nil || sent != 1 || failed != 1 {
		t.Fatalf("sent=%d failed=%d err=%v", sent, failed, err)
	}
	a := got[0]
	if a.Kind != "assigned" || a.To.Email != "grace@example.com" || a.Issue == nil || a.Project != "Web" || a.Detail["actor"] != "Ada" {
		t.Errorf("claimed %+v", a)
	}
	var note, lastErr *string
	s.pool.QueryRow(ctx, `select last_error from emails where kind = 'commented'`).Scan(&note)
	s.pool.QueryRow(ctx, `select last_error from emails where kind = 'assigned'`).Scan(&lastErr)
	if note == nil || *note != "skipped: nothing to send" || lastErr == nil || *lastErr != "421 try later" {
		t.Errorf("outcomes: skipped=%v failed=%v", note, lastErr)
	}
	// The failure is backed off, so nothing is due now.
	if sent, failed, _ := s.DeliverEmails(ctx, 10, 8, func(context.Context, *Email) error { return nil }); sent+failed != 0 {
		t.Error("retried before backoff")
	}
	s.pool.Exec(ctx, `update emails set available_at = now()`)
	if sent, _, _ := s.DeliverEmails(ctx, 10, 8, func(context.Context, *Email) error { return nil }); sent != 1 {
		t.Error("retry after backoff")
	}
}
