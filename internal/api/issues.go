package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/olucurious/watchtower/internal/store"
)

var periods = map[string]time.Duration{
	"1h": time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour,
	"14d": 14 * 24 * time.Hour, "30d": 30 * 24 * time.Hour, "90d": 90 * 24 * time.Hour,
}

var statuses = map[string]bool{"unresolved": true, "resolved": true, "muted": true}

func (a *API) listIssues(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.IssueFilter{
		Project:     q.Get("project"),
		Status:      q.Get("status"),
		Query:       q.Get("query"),
		Environment: q.Get("environment"),
		Sort:        q.Get("sort"),
		Period:      periods[q.Get("period")],
	}
	if f.Status == "all" {
		f.Status = ""
	} else if f.Status != "" && !statuses[f.Status] {
		writeError(w, http.StatusBadRequest, "unknown status")
		return
	}
	switch v := q.Get("assignee"); v {
	case "":
	case "me":
		f.AssigneeID = currentUser(r.Context()).ID
	case "none":
		f.Unassigned = true
	default:
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "assignee must be me, none or a user ID")
			return
		}
		f.AssigneeID = id
	}
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	f.Offset, _ = strconv.Atoi(q.Get("offset"))
	page, err := a.Store.ListIssues(r.Context(), f)
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (a *API) getIssue(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "id")
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	d, err := a.Store.Issue(r.Context(), id)
	if err != nil {
		a.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (a *API) listIssueEvents(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "id")
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	events, err := a.Store.IssueEvents(r.Context(), id, limit, offset)
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func (a *API) getIssueEvent(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "id")
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	ev, err := a.Store.IssueEvent(r.Context(), id, r.PathValue("which"))
	if err != nil {
		a.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ev)
}

func (a *API) setIssuesStatus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs    []int64 `json:"ids"`
		Status string  `json:"status"`
	}
	if err := decode(r, &req); err != nil || len(req.IDs) == 0 || len(req.IDs) > 500 || !statuses[req.Status] {
		writeError(w, http.StatusBadRequest, "expected ids (1-500) and a status of unresolved, resolved or muted")
		return
	}
	u := currentUser(r.Context())
	actor := u.Name
	if actor == "" {
		actor = u.Email
	}
	n, err := a.Store.SetIssuesStatus(r.Context(), req.IDs, req.Status, actor)
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"updated": n})
}

func (a *API) listEnvironments(w http.ResponseWriter, r *http.Request) {
	envs, err := a.Store.Environments(r.Context(), r.URL.Query().Get("project"))
	if err != nil {
		a.internal(w, err)
		return
	}
	if envs == nil {
		envs = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"environments": envs})
}
