package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Linear talks to Linear's GraphQL API with a personal or workspace API
// key. Keys are sent as-is in the Authorization header, as Linear expects.
type Linear struct {
	API    string // https://api.linear.app/graphql; overridden in tests
	Client *http.Client
}

func NewLinear() *Linear {
	return &Linear{API: "https://api.linear.app/graphql", Client: &http.Client{Timeout: 15 * time.Second}}
}

// RateLimited carries Linear's retry hint so the outbox can wait it out.
type RateLimited struct{ After time.Duration }

func (e *RateLimited) Error() string { return "Linear rate limit reached" }

// ValidateLinearKey checks the shape of a Linear API key and returns a hint
// safe to display.
func ValidateLinearKey(key string) (string, error) {
	key = strings.TrimSpace(key)
	if !strings.HasPrefix(key, "lin_api_") || len(key) < 20 || strings.ContainsAny(key, " \t\r\n") {
		return "", errors.New("a Linear API key starts with lin_api_ (Linear › Settings › Security & access › Personal API keys)")
	}
	return "lin_api_…" + key[len(key)-4:], nil
}

func (l *Linear) do(ctx context.Context, key, query string, vars map[string]any, out any) error {
	body, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.API, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := l.Client.Do(req)
	if err != nil {
		return fmt.Errorf("Linear: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message    string `json:"message"`
			Extensions struct {
				Code string `json:"code"`
			} `json:"extensions"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(raw, &env)
	for _, e := range env.Errors {
		if e.Extensions.Code == "RATELIMITED" || resp.StatusCode == http.StatusTooManyRequests {
			after := time.Minute
			if v, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && v > 0 {
				after = time.Duration(v) * time.Second
			}
			return &RateLimited{After: after}
		}
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return errors.New("Linear rejected the API key")
	}
	if len(env.Errors) > 0 {
		msgs := make([]string, 0, len(env.Errors))
		for _, e := range env.Errors {
			msgs = append(msgs, e.Message)
		}
		return fmt.Errorf("Linear: %s", strings.Join(msgs, "; "))
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("Linear responded %d", resp.StatusCode)
	}
	if out != nil {
		return json.Unmarshal(env.Data, out)
	}
	return nil
}

// LinearTeam is a team issues can be filed in.
type LinearTeam struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Key  string `json:"key"`
}

// Teams lists the teams the key can see; it doubles as key validation.
func (l *Linear) Teams(ctx context.Context, key string) ([]LinearTeam, error) {
	var out struct {
		Teams struct{ Nodes []LinearTeam } `json:"teams"`
	}
	err := l.do(ctx, key, `query { teams(first: 100) { nodes { id name key } } }`, nil, &out)
	return out.Teams.Nodes, err
}

// LinearIssue is a created or fetched issue.
type LinearIssue struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	URL        string `json:"url"`
	State      struct {
		Type string `json:"type"`
	} `json:"state"`
}

const issueFields = `id identifier url state { type }`

// CreateIssue files an issue with a caller-chosen ID. Creating the same ID
// twice cannot produce a second issue: if Linear already has it (a retry
// after an unrecorded success), the existing issue is returned.
func (l *Linear) CreateIssue(ctx context.Context, key, id, teamID, title, description string) (LinearIssue, error) {
	var out struct {
		IssueCreate struct {
			Success bool
			Issue   LinearIssue
		} `json:"issueCreate"`
	}
	err := l.do(ctx, key, `mutation($input: IssueCreateInput!) { issueCreate(input: $input) { success issue { `+issueFields+` } } }`,
		map[string]any{"input": map[string]any{"id": id, "teamId": teamID, "title": title, "description": description}}, &out)
	var rl *RateLimited
	if err != nil && !errors.As(err, &rl) {
		if existing, getErr := l.Issue(ctx, key, id); getErr == nil && existing.ID != "" {
			return existing, nil
		}
		return LinearIssue{}, err
	}
	if err != nil {
		return LinearIssue{}, err
	}
	if !out.IssueCreate.Success || out.IssueCreate.Issue.ID == "" {
		return LinearIssue{}, errors.New("Linear did not create the issue")
	}
	return out.IssueCreate.Issue, nil
}

// Issue fetches one issue by ID.
func (l *Linear) Issue(ctx context.Context, key, id string) (LinearIssue, error) {
	var out struct{ Issue LinearIssue }
	err := l.do(ctx, key, `query($id: String!) { issue(id: $id) { `+issueFields+` } }`, map[string]any{"id": id}, &out)
	return out.Issue, err
}

// Comment adds a markdown comment to an issue.
func (l *Linear) Comment(ctx context.Context, key, issueID, body string) error {
	var out struct {
		CommentCreate struct{ Success bool } `json:"commentCreate"`
	}
	if err := l.do(ctx, key, `mutation($input: CommentCreateInput!) { commentCreate(input: $input) { success } }`,
		map[string]any{"input": map[string]any{"issueId": issueID, "body": body}}, &out); err != nil {
		return err
	}
	if !out.CommentCreate.Success {
		return errors.New("Linear did not add the comment")
	}
	return nil
}

// States returns each issue's workflow state type ("completed",
// "canceled", "started", …) by issue ID. Missing issues are omitted.
func (l *Linear) States(ctx context.Context, key string, ids []string) (map[string]string, error) {
	var out struct {
		Issues struct{ Nodes []LinearIssue } `json:"issues"`
	}
	err := l.do(ctx, key, `query($ids: [ID!]) { issues(first: 100, filter: { id: { in: $ids } }) { nodes { `+issueFields+` } } }`,
		map[string]any{"ids": ids}, &out)
	states := map[string]string{}
	for _, n := range out.Issues.Nodes {
		states[n.ID] = n.State.Type
	}
	return states, err
}
