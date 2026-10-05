package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// This file holds the read models behind the web UI.

type ProjectSummary struct {
	ID               int64     `json:"id"`
	Slug             string    `json:"slug"`
	Name             string    `json:"name"`
	CreatedAt        time.Time `json:"created_at"`
	UnresolvedIssues int64     `json:"unresolved_issues"`
	Events24h        int64     `json:"events_24h"`
	RepoURL          string    `json:"repo_url"`
}

func (s *Store) Projects(ctx context.Context) ([]ProjectSummary, error) {
	rows, err := s.pool.Query(ctx, `
		select p.id, p.slug, p.name, p.created_at,
			(select count(*) from issues i where i.project_id = p.id and i.status = 'unresolved'),
			coalesce((select sum(h.events) from issue_hourly h join issues i on i.id = h.issue_id
				where i.project_id = p.id and h.hour >= date_trunc('hour', now(), 'UTC') - interval '23 hours'), 0)::bigint,
			p.repo_url
		from projects p order by p.name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[ProjectSummary])
}

type Key struct {
	ID        int64      `json:"id"`
	Adapter   string     `json:"adapter"`
	Label     string     `json:"label"`
	CreatedAt time.Time  `json:"created_at"`
	CreatedBy string     `json:"created_by"`
	RevokedAt *time.Time `json:"revoked_at"`
}

func (s *Store) Keys(ctx context.Context, projectSlug string) ([]Key, error) {
	rows, err := s.pool.Query(ctx, `
		select k.id, k.adapter, k.label, k.created_at, coalesce(u.email, ''), k.revoked_at
		from project_keys k join projects p on p.id = k.project_id left join users u on u.id = k.created_by
		where p.slug = $1 order by k.revoked_at is not null, k.created_at desc`, projectSlug)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Key])
}

func (s *Store) RevokeKey(ctx context.Context, projectSlug string, keyID int64) error {
	tag, err := s.pool.Exec(ctx, `
		update project_keys k set revoked_at = now() from projects p
		where p.id = k.project_id and p.slug = $1 and k.id = $2 and k.revoked_at is null`, projectSlug, keyID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	s.keys.flush() // revocation takes effect immediately
	return nil
}

// IssueFilter selects issues for the issue list.
type IssueFilter struct {
	Project     string // slug; empty means all projects
	Status      string // unresolved, resolved, muted; empty means all
	Query       string // matched against title and culprit
	Environment string
	Period      time.Duration // only issues seen within this window; 0 means any time
	AssigneeID  int64         // only issues assigned to this user; 0 means anyone
	Unassigned  bool          // only issues with no assignee
	Sort        string        // last_seen (default), first_seen, times_seen
	Limit       int
	Offset      int
}

type IssueRow struct {
	ID           int64      `json:"id"`
	ProjectSlug  string     `json:"project"`
	Title        string     `json:"title"`
	Culprit      string     `json:"culprit"`
	Level        string     `json:"level"`
	Status       string     `json:"status"`
	TimesSeen    int64      `json:"times_seen"`
	FirstSeen    time.Time  `json:"first_seen"`
	LastSeen     time.Time  `json:"last_seen"`
	FirstRelease string     `json:"first_release"`
	LastRelease  string     `json:"last_release"`
	RegressedAt  *time.Time `json:"regressed_at"`
	Environments []string   `json:"environments"`
	AssigneeID   *int64     `json:"assignee_id"`
	Assignee     string     `json:"assignee"` // display name, empty when unassigned
	// Trend holds event counts per bucket, oldest first, for the sparkline.
	Trend []int64 `json:"trend"`
}

type IssuePage struct {
	Issues []IssueRow       `json:"issues"`
	Total  int64            `json:"total"`
	Counts map[string]int64 `json:"counts"` // per status, ignoring the status filter
	Trend  TrendSpec        `json:"trend"`
}

type TrendSpec struct {
	Bucket string    `json:"bucket"` // "hour" or "day"
	Start  time.Time `json:"start"`
	Size   int       `json:"size"`
}

const issueRowColumns = `i.id, p.slug, i.title, i.culprit, i.level, i.status, i.times_seen, i.first_seen,
	i.last_seen, i.first_release, i.last_release, i.regressed_at, i.environments, i.assignee_id,
	coalesce((select coalesce(nullif(u.name, ''), u.email) from users u where u.id = i.assignee_id), '')`

func scanIssueRow(r pgx.Row) (IssueRow, error) {
	var i IssueRow
	err := r.Scan(&i.ID, &i.ProjectSlug, &i.Title, &i.Culprit, &i.Level, &i.Status, &i.TimesSeen,
		&i.FirstSeen, &i.LastSeen, &i.FirstRelease, &i.LastRelease, &i.RegressedAt, &i.Environments,
		&i.AssigneeID, &i.Assignee)
	return i, err
}

func (s *Store) ListIssues(ctx context.Context, f IssueFilter) (IssuePage, error) {
	var where []string
	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if f.Project != "" {
		where = append(where, "p.slug = "+arg(f.Project))
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(q) + "%"
		a := arg(like)
		where = append(where, "(i.title ilike "+a+" or i.culprit ilike "+a+")")
	}
	if f.Environment != "" {
		where = append(where, arg(f.Environment)+" = any(i.environments)")
	}
	if f.AssigneeID > 0 {
		where = append(where, "i.assignee_id = "+arg(f.AssigneeID))
	} else if f.Unassigned {
		where = append(where, "i.assignee_id is null")
	}
	if f.Period > 0 {
		where = append(where, "i.last_seen > now() - "+arg(fmt.Sprintf("%d seconds", int(f.Period.Seconds())))+"::interval")
	}
	base := "from issues i join projects p on p.id = i.project_id"
	if len(where) > 0 {
		base += " where " + strings.Join(where, " and ")
	}

	page := IssuePage{Counts: map[string]int64{}, Issues: []IssueRow{}}
	rows, err := s.pool.Query(ctx, "select i.status, count(*) "+base+" group by i.status", args...)
	if err != nil {
		return page, err
	}
	for rows.Next() {
		var st string
		var n int64
		if err := rows.Scan(&st, &n); err != nil {
			return page, err
		}
		page.Counts[st] = n
	}
	if err := rows.Err(); err != nil {
		return page, err
	}

	statusWhere := base
	if f.Status != "" {
		if len(where) > 0 {
			statusWhere += " and i.status = " + arg(f.Status)
		} else {
			statusWhere += " where i.status = " + arg(f.Status)
		}
		page.Total = page.Counts[f.Status]
	} else {
		for _, n := range page.Counts {
			page.Total += n
		}
	}
	order := map[string]string{
		"first_seen": "i.first_seen desc",
		"times_seen": "i.times_seen desc",
	}[f.Sort]
	if order == "" {
		order = "i.last_seen desc"
	}
	limit := f.Limit
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	q := "select " + issueRowColumns + " " + statusWhere + " order by " + order + ", i.id desc limit " +
		arg(limit) + " offset " + arg(max(f.Offset, 0))
	rows, err = s.pool.Query(ctx, q, args...)
	if err != nil {
		return page, err
	}
	issues, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (IssueRow, error) { return scanIssueRow(r) })
	if err != nil {
		return page, err
	}
	page.Issues = issues
	// The sparkline covers the selected period: hourly up to two days,
	// otherwise one bar per day (at most 30); 14 days when unbounded.
	switch days := int(f.Period / (24 * time.Hour)); {
	case f.Period > 0 && f.Period <= 48*time.Hour:
		page.Trend = TrendSpec{Bucket: "hour", Size: 24}
	case f.Period > 0:
		page.Trend = TrendSpec{Bucket: "day", Size: min(days, 30)}
	default:
		page.Trend = TrendSpec{Bucket: "day", Size: 14}
	}
	ids := make([]int64, len(issues))
	for i, is := range issues {
		ids[i] = is.ID
	}
	trends, start, err := s.trends(ctx, ids, page.Trend)
	if err != nil {
		return page, err
	}
	page.Trend.Start = start
	for i := range page.Issues {
		page.Issues[i].Trend = trends[page.Issues[i].ID]
	}
	return page, nil
}

// trends counts events per bucket for each issue over the window ending
// in the current bucket, from the hourly rollup.
func (s *Store) trends(ctx context.Context, ids []int64, spec TrendSpec) (map[int64][]int64, time.Time, error) {
	out := map[int64][]int64{}
	var start time.Time
	if err := s.pool.QueryRow(ctx, `select date_trunc($1, now()) - ($2 - 1) * ('1 ' || $1)::interval`,
		spec.Bucket, spec.Size).Scan(&start); err != nil {
		return nil, start, err
	}
	for _, id := range ids {
		out[id] = make([]int64, spec.Size)
	}
	if len(ids) == 0 {
		return out, start, nil
	}
	rows, err := s.pool.Query(ctx, `
		select issue_id, date_trunc($1, hour) as bucket, sum(events)::bigint
		from issue_hourly where issue_id = any($2) and hour >= $3
		group by 1, 2`, spec.Bucket, ids, start)
	if err != nil {
		return nil, start, err
	}
	step := time.Hour
	if spec.Bucket == "day" {
		step = 24 * time.Hour
	}
	for rows.Next() {
		var id, n int64
		var bucket time.Time
		if err := rows.Scan(&id, &bucket, &n); err != nil {
			return nil, start, err
		}
		if i := int(bucket.Sub(start) / step); i >= 0 && i < spec.Size {
			out[id][i] += n
		}
	}
	return out, start, rows.Err()
}

type IssueDetail struct {
	IssueRow
	ProjectName string        `json:"project_name"`
	ResolvedAt  *time.Time    `json:"resolved_at"`
	Events24h   int64         `json:"events_24h"`
	Events30d   int64         `json:"events_30d"`
	Hourly      Series        `json:"hourly"` // last 24 hours
	Daily       Series        `json:"daily"`  // last 30 days
	Tags        []TagSummary  `json:"tags"`
	Activity    []ActivityRow `json:"activity"`
	Links       []IssueLink   `json:"links"`
	// Trackers are the project's channels that can hold a linked issue.
	Trackers []TrackerChannel `json:"trackers"`
}

type Series struct {
	Start  time.Time `json:"start"`
	Bucket string    `json:"bucket"`
	Counts []int64   `json:"counts"`
}

type TagSummary struct {
	Key    string     `json:"key"`
	Total  int64      `json:"total"`
	Values []TagValue `json:"values"`
}

type TagValue struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

type ActivityRow struct {
	ID     int64             `json:"id"`
	UserID *int64            `json:"user_id"` // the author, for comments
	Kind   string            `json:"kind"`
	Actor  string            `json:"actor"`
	At     time.Time         `json:"at"`
	Detail map[string]string `json:"detail"`
}

// tagSampleSize bounds the tag breakdown to the most recent events.
const tagSampleSize = 1000

func (s *Store) Issue(ctx context.Context, id int64) (IssueDetail, error) {
	var d IssueDetail
	row := s.pool.QueryRow(ctx, `select `+issueRowColumns+`, p.name, i.resolved_at, i.project_id
		from issues i join projects p on p.id = i.project_id where i.id = $1`, id)
	i := &d.IssueRow
	var projectID int64
	err := row.Scan(&i.ID, &i.ProjectSlug, &i.Title, &i.Culprit, &i.Level, &i.Status, &i.TimesSeen,
		&i.FirstSeen, &i.LastSeen, &i.FirstRelease, &i.LastRelease, &i.RegressedAt, &i.Environments,
		&i.AssigneeID, &i.Assignee, &d.ProjectName, &d.ResolvedAt, &projectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrNotFound
	}
	if err != nil {
		return d, err
	}
	for _, spec := range []struct {
		dst  *Series
		spec TrendSpec
	}{{&d.Hourly, TrendSpec{Bucket: "hour", Size: 24}}, {&d.Daily, TrendSpec{Bucket: "day", Size: 30}}} {
		counts, start, err := s.trends(ctx, []int64{id}, spec.spec)
		if err != nil {
			return d, err
		}
		*spec.dst = Series{Start: start, Bucket: spec.spec.Bucket, Counts: counts[id]}
	}
	// The totals are the charts' totals, so the two always agree.
	for _, n := range d.Hourly.Counts {
		d.Events24h += n
	}
	for _, n := range d.Daily.Counts {
		d.Events30d += n
	}
	if d.Tags, err = s.issueTags(ctx, id); err != nil {
		return d, err
	}
	if d.Links, err = s.issueLinks(ctx, id); err != nil {
		return d, err
	}
	if d.Trackers, err = s.trackerChannels(ctx, projectID); err != nil {
		return d, err
	}
	rows, err := s.pool.Query(ctx, `select id, user_id, kind, actor, at, detail from issue_activity where issue_id = $1 order by at desc, id desc limit 200`, id)
	if err != nil {
		return d, err
	}
	d.Activity, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (ActivityRow, error) {
		var a ActivityRow
		var detail []byte
		if err := r.Scan(&a.ID, &a.UserID, &a.Kind, &a.Actor, &a.At, &detail); err != nil {
			return a, err
		}
		_ = json.Unmarshal(detail, &a.Detail)
		return a, nil
	})
	return d, err
}

// issueTags summarizes the top values of each tag, plus environment,
// release and server, across the issue's most recent events.
func (s *Store) issueTags(ctx context.Context, id int64) ([]TagSummary, error) {
	rows, err := s.pool.Query(ctx, `
		with recent as (
			select data from events where issue_id = $1 order by occurred_at desc limit $2
		), pairs as (
			select t.key, t.value from recent, jsonb_each_text(coalesce(recent.data->'tags', '{}')) as t(key, value)
			union all select 'environment', data->>'environment' from recent where coalesce(data->>'environment', '') <> ''
			union all select 'release', data->>'release' from recent where coalesce(data->>'release', '') <> ''
			union all select 'server_name', data->>'server_name' from recent where coalesce(data->>'server_name', '') <> ''
		), counted as (
			select key, value, count(*) as n,
				sum(count(*)) over (partition by key) as total,
				row_number() over (partition by key order by count(*) desc, value) as rank
			from pairs group by key, value
		)
		select key, value, n, total::bigint from counted where rank <= 5
		order by case key when 'environment' then 0 when 'release' then 1 when 'server_name' then 2 else 3 end, key, rank`,
		id, tagSampleSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TagSummary
	for rows.Next() {
		var key, value string
		var n, total int64
		if err := rows.Scan(&key, &value, &n, &total); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].Key != key {
			out = append(out, TagSummary{Key: key, Total: total})
		}
		last := &out[len(out)-1]
		last.Values = append(last.Values, TagValue{Value: value, Count: n})
	}
	return out, rows.Err()
}

type EventRow struct {
	EventID     string    `json:"event_id"`
	OccurredAt  time.Time `json:"occurred_at"`
	Message     string    `json:"message"`
	Release     string    `json:"release"`
	Environment string    `json:"environment"`
	ServerName  string    `json:"server_name"`
	UserID      string    `json:"user_id"`
}

func (s *Store) IssueEvents(ctx context.Context, issueID int64, limit, offset int) ([]EventRow, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		select event_id, occurred_at,
			coalesce(data->'exceptions'->-1->>'value', data->>'message', ''),
			coalesce(data->>'release', ''), coalesce(data->>'environment', ''),
			coalesce(data->>'server_name', ''), coalesce(data->'user'->>'id', '')
		from events where issue_id = $1 order by occurred_at desc, event_id desc limit $2 offset $3`,
		issueID, limit, max(offset, 0))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[EventRow])
}

type EventDetail struct {
	EventID    string          `json:"event_id"`
	IssueID    int64           `json:"issue_id"`
	OccurredAt time.Time       `json:"occurred_at"`
	ReceivedAt time.Time       `json:"received_at"`
	Data       json.RawMessage `json:"data"`
	// Neighbours in time order within the issue, for Older/Newer navigation.
	Older  string `json:"older"`
	Newer  string `json:"newer"`
	Oldest string `json:"oldest"`
	Latest string `json:"latest"`
}

// IssueEvent returns one event of an issue; which is an event ID, or
// "latest" / "oldest".
func (s *Store) IssueEvent(ctx context.Context, issueID int64, which string) (EventDetail, error) {
	var d EventDetail
	q := `select event_id, issue_id, occurred_at, received_at, data from events where issue_id = $1 `
	var row pgx.Row
	switch which {
	case "latest":
		row = s.pool.QueryRow(ctx, q+`order by occurred_at desc, event_id desc limit 1`, issueID)
	case "oldest":
		row = s.pool.QueryRow(ctx, q+`order by occurred_at, event_id limit 1`, issueID)
	default:
		row = s.pool.QueryRow(ctx, q+`and event_id = $2`, issueID, which)
	}
	err := row.Scan(&d.EventID, &d.IssueID, &d.OccurredAt, &d.ReceivedAt, &d.Data)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrNotFound
	}
	if err != nil {
		return d, err
	}
	err = s.pool.QueryRow(ctx, `
		select
			coalesce((select event_id from events where issue_id = $1 and (occurred_at, event_id) < ($2, $3)
				order by occurred_at desc, event_id desc limit 1), ''),
			coalesce((select event_id from events where issue_id = $1 and (occurred_at, event_id) > ($2, $3)
				order by occurred_at, event_id limit 1), ''),
			coalesce((select event_id from events where issue_id = $1 order by occurred_at, event_id limit 1), ''),
			coalesce((select event_id from events where issue_id = $1 order by occurred_at desc, event_id desc limit 1), '')`,
		issueID, d.OccurredAt, d.EventID).Scan(&d.Older, &d.Newer, &d.Oldest, &d.Latest)
	return d, err
}

// SetIssuesStatus changes several issues at once, recording the actor.
// Issues already in the target status are left untouched.
func (s *Store) SetIssuesStatus(ctx context.Context, ids []int64, status, actor string) (int64, error) {
	var n int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			update issues set status = $2,
				resolved_at = case when $2 = 'resolved' then now() else resolved_at end
			where id = any($1) and status <> $2 returning id`, ids, status)
		if err != nil {
			return err
		}
		changed, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		n = int64(len(changed))
		if n == 0 {
			return nil
		}
		_, err = tx.Exec(ctx, `
			insert into issue_activity (issue_id, kind, actor) select unnest($1::bigint[]), $2, $3`, changed, status, actor)
		return err
	})
	return n, err
}

// Environments lists environments seen in a project's issues.
func (s *Store) Environments(ctx context.Context, projectSlug string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		select distinct env from issues i join projects p on p.id = i.project_id, unnest(i.environments) env
		where ($1 = '' or p.slug = $1) order by env`, projectSlug)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}
