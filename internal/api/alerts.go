package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/olucurious/watchtower/internal/alert"
	"github.com/olucurious/watchtower/internal/store"
)

var levels = map[string]bool{"debug": true, "info": true, "warning": true, "error": true, "fatal": true}

type alertRequest struct {
	Name string `json:"name"`
	// Kind is "slack" (incoming webhook, the default), "slack_bot" or "linear".
	Kind string `json:"kind"`
	// Required on create; omitted on update keeps the current one.
	WebhookURL *string `json:"webhook_url"`
	BotToken   *string `json:"bot_token"`
	APIKey     *string `json:"api_key"` // Linear
	TeamID     string  `json:"team_id"` // Linear
	// Slack channel ID for "slack_bot".
	Channel            string `json:"channel"`
	OnNewIssue         bool   `json:"on_new_issue"`
	OnRegression       bool   `json:"on_regression"`
	FrequencyThreshold *int   `json:"frequency_threshold"`
	MinLevel           string `json:"min_level"`
	Environment        string `json:"environment"`
}

// rules validates the channel's triggers. A Linear destination may have
// none: it is then used only from the issue page.
func (r *alertRequest) rules(kind string) (store.AlertRules, string) {
	if strings.TrimSpace(r.Name) == "" {
		return store.AlertRules{}, "name is required"
	}
	if r.MinLevel == "" {
		r.MinLevel = "error"
	}
	if !levels[r.MinLevel] {
		return store.AlertRules{}, "min_level must be debug, info, warning, error or fatal"
	}
	if r.FrequencyThreshold != nil && *r.FrequencyThreshold <= 0 {
		r.FrequencyThreshold = nil
	}
	if !r.OnNewIssue && !r.OnRegression && r.FrequencyThreshold == nil && kind != "linear" {
		return store.AlertRules{}, "enable at least one trigger"
	}
	return store.AlertRules{Name: strings.TrimSpace(r.Name), OnNewIssue: r.OnNewIssue, OnRegression: r.OnRegression,
		FrequencyThreshold: r.FrequencyThreshold, MinLevel: r.MinLevel, Environment: strings.TrimSpace(r.Environment)}, ""
}

// sealWebhook validates and encrypts a webhook URL.
func (a *API) sealWebhook(w http.ResponseWriter, raw string) ([]byte, string, bool) {
	hint, err := a.Slack.ValidateWebhook(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return nil, "", false
	}
	sealed, err := a.Sealer.Seal(strings.TrimSpace(raw))
	if errors.Is(err, alert.ErrNoSecretKey) {
		writeError(w, http.StatusConflict, err.Error())
		return nil, "", false
	}
	if err != nil {
		a.internal(w, err)
		return nil, "", false
	}
	return sealed, hint, true
}

func (a *API) listAlerts(w http.ResponseWriter, r *http.Request) {
	chs, err := a.Store.AlertChannels(r.Context(), r.PathValue("slug"))
	if err != nil {
		a.internal(w, err)
		return
	}
	if chs == nil {
		chs = []store.AlertChannel{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": chs, "secret_key_configured": a.Sealer != nil})
}

// sealBot validates and encrypts a Slack bot token.
func (a *API) sealBot(w http.ResponseWriter, token, channel string) (store.AlertTarget, bool) {
	hint, err := alert.ValidateBot(token, channel)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return store.AlertTarget{}, false
	}
	sealed, err := a.Sealer.Seal(strings.TrimSpace(token))
	if errors.Is(err, alert.ErrNoSecretKey) {
		writeError(w, http.StatusConflict, err.Error())
		return store.AlertTarget{}, false
	}
	if err != nil {
		a.internal(w, err)
		return store.AlertTarget{}, false
	}
	return store.AlertTarget{Sealed: sealed, Hint: hint, Channel: strings.TrimSpace(channel)}, true
}

// target seals whichever destination the request names for kind. With
// required false (an update), an absent secret keeps the stored one.
func (a *API) target(w http.ResponseWriter, r *http.Request, req *alertRequest, kind string, existing int64) (store.AlertTarget, bool) {
	required := existing == 0
	switch kind {
	case "linear":
		return a.linearTarget(w, r, req, existing)
	case "slack_bot":
		if req.BotToken == nil || strings.TrimSpace(*req.BotToken) == "" {
			if required {
				writeError(w, http.StatusBadRequest, "bot_token and channel are required")
				return store.AlertTarget{}, false
			}
			if req.Channel != "" {
				if err := alert.ValidateChannel(req.Channel); err != nil {
					writeError(w, http.StatusBadRequest, err.Error())
					return store.AlertTarget{}, false
				}
			}
			return store.AlertTarget{Channel: strings.TrimSpace(req.Channel)}, true
		}
		return a.sealBot(w, *req.BotToken, req.Channel)
	default:
		if req.WebhookURL == nil || strings.TrimSpace(*req.WebhookURL) == "" {
			if required {
				writeError(w, http.StatusBadRequest, "name and webhook_url are required")
				return store.AlertTarget{}, false
			}
			return store.AlertTarget{}, true
		}
		sealed, hint, ok := a.sealWebhook(w, *req.WebhookURL)
		return store.AlertTarget{Sealed: sealed, Hint: hint}, ok
	}
}

func (a *API) createAlert(w http.ResponseWriter, r *http.Request) {
	var req alertRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	kind := req.Kind
	if kind == "" {
		kind = "slack"
	}
	if kind != "slack" && kind != "slack_bot" && kind != "linear" {
		writeError(w, http.StatusBadRequest, `kind must be "slack", "slack_bot" or "linear"`)
		return
	}
	rules, msg := req.rules(kind)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	t, ok := a.target(w, r, &req, kind, 0)
	if !ok {
		return
	}
	id, err := a.Store.CreateAlertChannel(r.Context(), r.PathValue("slug"), kind, t, rules, currentUser(r.Context()).ID)
	if err != nil {
		a.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (a *API) updateAlert(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "id")
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	var req alertRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	// The kind is fixed at creation; look it up so the right secret is read.
	kind := "slack"
	chs, err := a.Store.AlertChannels(r.Context(), r.PathValue("slug"))
	if err != nil {
		a.internal(w, err)
		return
	}
	for _, c := range chs {
		if c.ID == id {
			kind = c.Kind
		}
	}
	rules, msg := req.rules(kind)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	t, ok := a.target(w, r, &req, kind, id)
	if !ok {
		return
	}
	if err := a.Store.UpdateAlertChannel(r.Context(), r.PathValue("slug"), id, rules, t); err != nil {
		a.storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) deleteAlert(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "id")
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err := a.Store.DeleteAlertChannel(r.Context(), r.PathValue("slug"), id); err != nil {
		a.storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// testAlert queues a test message; the UI then watches the channel's
// last delivery or error.
func (a *API) testAlert(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "id")
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	u := currentUser(r.Context())
	if err := a.Store.QueueTestNotification(r.Context(), r.PathValue("slug"), id, u.Email); err != nil {
		a.storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
