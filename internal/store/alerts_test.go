package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olucurious/watchtower/internal/event"
)

func pendingKinds(t *testing.T, s *Store) []string {
	t.Helper()
	rows, _ := s.pool.Query(context.Background(), `select kind from notifications where sent_at is null order by id`)
	var out []string
	for rows.Next() {
		var k string
		rows.Scan(&k)
		out = append(out, k)
	}
	return out
}

func ingest(t *testing.T, s *Store, events ...event.Event) {
	t.Helper()
	ctx := context.Background()
	if err := s.Accept(ctx, events); err != nil {
		t.Fatal(err)
	}
	if out, err := s.ProcessQueue(ctx, 100, group); err != nil || out.Failed > 0 {
		t.Fatalf("process: %+v %v", out, err)
	}
}

func TestAlertTriggers(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p, _ := s.CreateProject(ctx, "web", "Web")
	three := 3
	if _, err := s.CreateAlertChannel(ctx, "web", "slack", AlertTarget{Sealed: []byte("sealed"), Hint: "hint"},
		AlertRules{Name: "all", OnNewIssue: true, OnRegression: true, FrequencyThreshold: &three, MinLevel: "error", Environment: "production"}, 0); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	ev := func(n int, env string, level event.Level) event.Event {
		e := testEvent(p.ID, eventID(n), now)
		e.Environment, e.Level = env, level
		return e
	}

	ingest(t, s, ev(1, "production", event.LevelError))
	if k := pendingKinds(t, s); len(k) != 1 || k[0] != "new_issue" {
		t.Fatalf("after first event: %v", k)
	}
	ingest(t, s, ev(2, "staging", event.LevelError), ev(3, "production", event.LevelWarning))
	if k := pendingKinds(t, s); len(k) != 1 {
		t.Errorf("environment and level filters: %v", k)
	}
	ingest(t, s, ev(4, "production", event.LevelError))
	// Four events in the last hour reach the threshold of 3.
	if k := pendingKinds(t, s); len(k) != 2 || k[1] != "frequency" {
		t.Fatalf("frequency: %v", k)
	}
	ingest(t, s, ev(5, "production", event.LevelError))
	if k := pendingKinds(t, s); len(k) != 2 {
		t.Errorf("frequency alert must respect its cooldown: %v", k)
	}

	issues, _ := s.ListIssues(ctx, IssueFilter{Project: "web"})
	id := issues.Issues[0].ID
	s.SetIssuesStatus(ctx, []int64{id}, "resolved", "ada")
	ingest(t, s, ev(6, "production", event.LevelError))
	if k := pendingKinds(t, s); len(k) != 3 || k[2] != "regression" {
		t.Fatalf("regression: %v", k)
	}
	s.SetIssuesStatus(ctx, []int64{id}, "muted", "ada")
	s.pool.Exec(ctx, `delete from notifications`)
	ingest(t, s, ev(7, "production", event.LevelError))
	if k := pendingKinds(t, s); len(k) != 0 {
		t.Errorf("muted issues must not alert: %v", k)
	}
}

func TestDeliverNotifications(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p, _ := s.CreateProject(ctx, "web", "Web")
	id, _ := s.CreateAlertChannel(ctx, "web", "slack", AlertTarget{Sealed: []byte("sealed"), Hint: "hint"}, AlertRules{Name: "#alerts", OnNewIssue: true, MinLevel: "error"}, 0)
	ingest(t, s, testEvent(p.ID, eventID(1), time.Now()))
	s.QueueTestNotification(ctx, "web", id, "ada@example.com")

	var seen []*Notification
	sent, failed, err := s.DeliverNotifications(ctx, 10, 8, func(_ context.Context, n *Notification) (time.Duration, error) {
		seen = append(seen, n)
		if n.Kind == "test" {
			return 0, errors.New("slack responded 404: no_service")
		}
		return 0, nil
	})
	if err != nil || sent != 1 || failed != 1 {
		t.Fatalf("sent=%d failed=%d err=%v", sent, failed, err)
	}
	if seen[0].Issue == nil || seen[0].Issue.Title != "KeyError: boom" || string(seen[0].Channel.Sealed) != "sealed" || seen[0].Project.Name != "Web" {
		t.Errorf("notification context: %+v", seen[0])
	}
	if seen[1].Issue != nil {
		t.Error("test notifications have no issue")
	}
	chs, _ := s.AlertChannels(ctx, "web")
	if chs[0].LastError == nil || *chs[0].LastError != "slack responded 404: no_service" {
		t.Errorf("channel last error: %+v", chs[0].LastError)
	}
	// The failed one is backed off, so nothing is due right now.
	sent, failed, _ = s.DeliverNotifications(ctx, 10, 8, func(context.Context, *Notification) (time.Duration, error) { return 0, nil })
	if sent+failed != 0 {
		t.Error("failed notification retried before its backoff")
	}
	s.pool.Exec(ctx, `update notifications set available_at = now()`)
	sent, _, _ = s.DeliverNotifications(ctx, 10, 8, func(context.Context, *Notification) (time.Duration, error) { return 0, nil })
	chs, _ = s.AlertChannels(ctx, "web")
	if sent != 1 || chs[0].LastError != nil || chs[0].LastDeliveryAt == nil {
		t.Errorf("retry: sent=%d channel=%+v", sent, chs[0])
	}
}

func TestDeliveryCancellationKeepsCompletedOutcome(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if _, err := s.CreateProject(ctx, "delivery", "Delivery"); err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateAlertChannel(ctx, "delivery", "slack", AlertTarget{Sealed: []byte("sealed"), Hint: "hint"}, AlertRules{Name: "delivery", OnNewIssue: true, MinLevel: "error"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.QueueTestNotification(ctx, "delivery", id, "test"); err != nil {
			t.Fatal(err)
		}
	}
	sendCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	calls := 0
	sent, _, err := s.DeliverNotifications(sendCtx, 10, 8, func(context.Context, *Notification) (time.Duration, error) { calls++; cancel(); return 0, nil })
	if !errors.Is(err, context.Canceled) || sent != 1 {
		t.Fatalf("sent=%d err=%v", sent, err)
	}
	sent, _, err = s.DeliverNotifications(ctx, 10, 8, func(context.Context, *Notification) (time.Duration, error) { calls++; return 0, nil })
	if err != nil || sent != 1 || calls != 2 {
		t.Fatalf("sent=%d calls=%d err=%v", sent, calls, err)
	}
}

func TestNotificationDeliveryDeadlinePreservesOutcomeBudget(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if _, err := s.CreateProject(ctx, "deadline", "Deadline"); err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateAlertChannel(ctx, "deadline", "slack", AlertTarget{Sealed: []byte("sealed"), Hint: "hint"}, AlertRules{Name: "deadline", OnNewIssue: true, MinLevel: "error"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.QueueTestNotification(ctx, "deadline", id, "test"); err != nil {
		t.Fatal(err)
	}
	sent, _, err := s.DeliverNotifications(ctx, 1, 8, func(sendCtx context.Context, _ *Notification) (time.Duration, error) {
		deadline, ok := sendCtx.Deadline()
		if !ok || time.Until(deadline) > 15*time.Second || sendCtx.Err() != nil {
			t.Fatalf("invalid delivery deadline: %v, %v", deadline, sendCtx.Err())
		}
		return 0, nil
	})
	if err != nil || sent != 1 {
		t.Fatalf("sent=%d err=%v", sent, err)
	}
}

func TestSlackBotChannel(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	s.CreateProject(ctx, "web", "Web")
	id, err := s.CreateAlertChannel(ctx, "web", "slack_bot", AlertTarget{Sealed: []byte("token"), Hint: "xoxb-…abcd", Channel: "C0123ABCD"},
		AlertRules{Name: "#errors", OnNewIssue: true, MinLevel: "error"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.QueueTestNotification(ctx, "web", id, "ada@example.com"); err != nil {
		t.Fatal(err)
	}
	var got *Notification
	s.DeliverNotifications(ctx, 10, 8, func(_ context.Context, n *Notification) (time.Duration, error) {
		got = n
		return 0, nil
	})
	if got == nil || got.Channel.Kind != "slack_bot" || got.Channel.Target != "C0123ABCD" || string(got.Channel.Sealed) != "token" {
		t.Fatalf("notification channel %+v", got)
	}
	// An update without a new token keeps the sealed token but may move channel.
	if err := s.UpdateAlertChannel(ctx, "web", id, AlertRules{Name: "#errors", OnNewIssue: true, MinLevel: "error"}, AlertTarget{Channel: "C9999ZZZZ"}); err != nil {
		t.Fatal(err)
	}
	chs, _ := s.AlertChannels(ctx, "web")
	if chs[0].TargetChannel != "C9999ZZZZ" || chs[0].TargetHint != "xoxb-…abcd" || chs[0].Kind != "slack_bot" {
		t.Errorf("after update %+v", chs[0])
	}
}
