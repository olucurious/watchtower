package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/olucurious/watchtower/internal/alert"
	"github.com/olucurious/watchtower/internal/store"
)

// linearKey returns the API key from the request, or the stored key of an
// existing channel when the request leaves it out.
func (a *API) linearKey(r *http.Request, slug string, apiKey *string, channelID int64) (string, error) {
	if apiKey != nil && strings.TrimSpace(*apiKey) != "" {
		if _, err := alert.ValidateLinearKey(*apiKey); err != nil {
			return "", err
		}
		return strings.TrimSpace(*apiKey), nil
	}
	if channelID == 0 {
		return "", errors.New("api_key is required")
	}
	t, err := a.Store.ChannelTarget(r.Context(), slug, channelID)
	if err != nil || t.Kind != "linear" {
		return "", errors.New("unknown Linear channel")
	}
	return a.Sealer.Open(t.Sealed)
}

// linearTeams lists the teams a Linear key can file issues in, so the
// setup dialog can offer them.
func (a *API) linearTeams(w http.ResponseWriter, r *http.Request) {
	var req struct {
		APIKey    *string `json:"api_key"`
		ChannelID int64   `json:"channel_id"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "expected api_key or channel_id")
		return
	}
	key, err := a.linearKey(r, r.PathValue("slug"), req.APIKey, req.ChannelID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	teams, err := a.Linear.Teams(ctx, key)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"teams": teams})
}

// linearTarget validates a Linear destination: the key must reach the
// chosen team. On update, an omitted key keeps the stored one.
func (a *API) linearTarget(w http.ResponseWriter, r *http.Request, req *alertRequest, existing int64) (store.AlertTarget, bool) {
	if existing != 0 && (req.APIKey == nil || strings.TrimSpace(*req.APIKey) == "") && req.TeamID == "" {
		return store.AlertTarget{}, true // rules only
	}
	if strings.TrimSpace(req.TeamID) == "" {
		writeError(w, http.StatusBadRequest, "team_id is required")
		return store.AlertTarget{}, false
	}
	if a.Sealer == nil {
		writeError(w, http.StatusConflict, alert.ErrNoSecretKey.Error())
		return store.AlertTarget{}, false
	}
	key, err := a.linearKey(r, r.PathValue("slug"), req.APIKey, existing)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return store.AlertTarget{}, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	teams, err := a.Linear.Teams(ctx, key)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return store.AlertTarget{}, false
	}
	var team *alert.LinearTeam
	for i := range teams {
		if teams[i].ID == req.TeamID {
			team = &teams[i]
		}
	}
	if team == nil {
		writeError(w, http.StatusBadRequest, "the Linear key cannot see that team")
		return store.AlertTarget{}, false
	}
	t := store.AlertTarget{Channel: team.ID, Label: team.Name + " (" + team.Key + ")"}
	if req.APIKey != nil && strings.TrimSpace(*req.APIKey) != "" {
		if t.Hint, err = alert.ValidateLinearKey(key); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return store.AlertTarget{}, false
		}
		if t.Sealed, err = a.Sealer.Seal(key); err != nil {
			a.internal(w, err)
			return store.AlertTarget{}, false
		}
	}
	return t, true
}

// createLinearIssue files the issue in Linear through one of its project's
// Linear channels, or returns the existing link.
func (a *API) createLinearIssue(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "id")
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	var req struct {
		ChannelID int64 `json:"channel_id"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "expected channel_id")
		return
	}
	d, err := a.Store.Issue(r.Context(), id)
	if err != nil {
		a.storeError(w, err)
		return
	}
	ch, err := a.Store.ChannelTarget(r.Context(), d.ProjectSlug, req.ChannelID)
	if err != nil || ch.Kind != "linear" {
		writeError(w, http.StatusBadRequest, "pick one of this project's Linear destinations")
		return
	}
	u := currentUser(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	link, _, err := a.Tracker.Link(ctx, alert.LinearChannel{ID: ch.ID, Name: ch.Name, Sealed: ch.Sealed, TeamID: ch.Channel},
		&d.IssueRow, d.ProjectName, actorName(u), u.ID)
	if err != nil {
		a.Log.Warn("creating Linear issue failed", "issue", id, "err", err)
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, link)
}
