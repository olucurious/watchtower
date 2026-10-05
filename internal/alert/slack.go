package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/olucurious/watchtower/internal/event"
	"github.com/olucurious/watchtower/internal/store"
)

// Slack posts notifications to Slack, through an incoming webhook or as
// a bot with chat.postMessage.
type Slack struct {
	// AllowedHosts restricts webhook hosts (default hooks.slack.com), so a
	// stored URL cannot make Watchtower call arbitrary internal services.
	AllowedHosts []string
	PublicURL    string
	Client       *http.Client
	// BotAPI is Slack's Web API base URL; tests point it at a fake.
	BotAPI string
}

func NewSlack(publicURL string, allowedHosts []string) *Slack {
	if len(allowedHosts) == 0 {
		allowedHosts = []string{"hooks.slack.com"}
	}
	return &Slack{
		AllowedHosts: allowedHosts,
		PublicURL:    strings.TrimRight(publicURL, "/"),
		BotAPI:       "https://slack.com/api",
		Client: &http.Client{
			Timeout: 10 * time.Second,
			// Never follow redirects: the allowlist applies to the final host.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// ValidateWebhook checks a webhook URL before it is stored, and returns
// the display hint (host plus last four characters).
func (s *Slack) ValidateWebhook(raw string) (hint string, err error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", errors.New("webhook URL must be an https:// URL")
	}
	allowed := false
	for _, h := range s.AllowedHosts {
		allowed = allowed || strings.EqualFold(u.Hostname(), h)
	}
	if !allowed {
		return "", fmt.Errorf("webhook host %q is not allowed (allowed: %s)", u.Hostname(), strings.Join(s.AllowedHosts, ", "))
	}
	p := strings.TrimRight(u.Path, "/")
	tail := p
	if len(tail) > 4 {
		tail = tail[len(tail)-4:]
	}
	return u.Hostname() + "/…" + tail, nil
}

// Send posts one notification. It never includes the webhook URL in errors.
func (s *Slack) Send(ctx context.Context, webhook string, n *store.Notification) (time.Duration, error) {
	if _, err := s.ValidateWebhook(webhook); err != nil {
		return 0, err
	}
	body, err := json.Marshal(s.Message(n))
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook, bytes.NewReader(body))
	if err != nil {
		return 0, errors.New("invalid webhook request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.Client.Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err // drop the URL, which contains the secret
		}
		return 0, fmt.Errorf("posting to slack: %w", err)
	}
	defer resp.Body.Close()
	reply, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	switch resp.StatusCode {
	case http.StatusOK:
		return 0, nil
	case http.StatusTooManyRequests:
		after, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
		return time.Duration(max(after, 1)) * time.Second, errors.New("slack rate limited the webhook (429)")
	default:
		return 0, fmt.Errorf("slack responded %d: %s", resp.StatusCode, strings.TrimSpace(string(reply)))
	}
}

var channelID = regexp.MustCompile(`^[CGD][A-Z0-9]{6,}$`)

// ValidateBot checks a bot token and channel ID before they are stored,
// and returns the token's display hint.
func ValidateBot(token, channel string) (hint string, err error) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, "xoxb-") || len(token) < 20 || strings.ContainsAny(token, " \t\n") {
		return "", errors.New("bot token must be a Slack bot token (xoxb-…)")
	}
	if err := ValidateChannel(channel); err != nil {
		return "", err
	}
	return "xoxb-…" + token[len(token)-4:], nil
}

// ValidateChannel checks a Slack channel ID.
func ValidateChannel(channel string) error {
	if !channelID.MatchString(strings.TrimSpace(channel)) {
		return errors.New("channel must be a Slack channel ID such as C0123ABCD (not a #name)")
	}
	return nil
}

// SendBot posts one notification with chat.postMessage. It never includes
// the token in errors.
func (s *Slack) SendBot(ctx context.Context, token, channel string, n *store.Notification) (time.Duration, error) {
	if _, err := ValidateBot(token, channel); err != nil {
		return 0, err
	}
	msg := s.Message(n)
	msg["channel"] = channel
	msg["unfurl_links"] = false
	body, err := json.Marshal(msg)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.BotAPI+"/chat.postMessage", bytes.NewReader(body))
	if err != nil {
		return 0, errors.New("invalid slack request")
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := s.Client.Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return 0, fmt.Errorf("posting to slack: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		after, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
		return time.Duration(max(after, 1)) * time.Second, errors.New("slack rate limited the bot (429)")
	}
	var reply struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&reply); err != nil {
		return 0, fmt.Errorf("slack responded %d with an unreadable body", resp.StatusCode)
	}
	if !reply.OK {
		// e.g. not_in_channel, channel_not_found, invalid_auth
		return 0, fmt.Errorf("slack rejected the message: %s", reply.Error)
	}
	return 0, nil
}

// Message builds the Slack Block Kit payload.
func (s *Slack) Message(n *store.Notification) map[string]any {
	if n.Issue == nil {
		text := fmt.Sprintf("Test alert from Watchtower for *%s* (channel %q). Alerts for this project will arrive here.",
			escape(n.Project.Name), n.Channel.Name)
		return map[string]any{"text": "Watchtower test alert", "blocks": []any{section(text)}}
	}
	is := n.Issue
	link := fmt.Sprintf("%s/issues/%d", s.PublicURL, is.ID)
	// Open the event that triggered the alert, not just the issue.
	if id, _ := n.Detail["event_id"].(string); event.NormalizeID(id) != "" {
		link += "/events/" + event.NormalizeID(id)
	}
	env, _ := n.Detail["environment"].(string)
	release, _ := n.Detail["release"].(string)
	release = event.ShortRelease(release)
	var headline, summary string
	switch n.Kind {
	case "new_issue":
		headline = ":rotating_light: *New issue* in " + escape(n.Project.Name)
		summary = "New issue in " + n.Project.Name
	case "regression":
		headline = ":warning: *Regression* in " + escape(n.Project.Name)
		summary = "Regression in " + n.Project.Name
		if release != "" {
			headline += ": it came back in `" + escape(release) + "`"
		}
	case "frequency":
		count, _ := n.Detail["count"].(float64)
		headline = fmt.Sprintf(":chart_with_upwards_trend: *%d+ events in the last hour* in %s", int(count), escape(n.Project.Name))
		summary = fmt.Sprintf("%d+ events in the last hour in %s", int(count), n.Project.Name)
	}
	if env != "" {
		headline += " (" + escape(env) + ")"
	}
	body := fmt.Sprintf("*<%s|%s>*", link, escape(truncate(is.Title, 150)))
	if is.Culprit != "" {
		body += "\n`" + escape(truncate(is.Culprit, 120)) + "`"
	}
	ctxItems := []string{strings.ToUpper(is.Level[:1]) + is.Level[1:], fmt.Sprintf("%d events total", is.TimesSeen)}
	if release != "" {
		ctxItems = append(ctxItems, "Release "+escape(release))
	}
	return map[string]any{
		"text": summary + ": " + is.Title,
		"blocks": []any{
			section(headline),
			section(body),
			map[string]any{"type": "context", "elements": []any{map[string]any{"type": "mrkdwn", "text": strings.Join(ctxItems, "  ·  ")}}},
		},
	}
}

func section(text string) map[string]any {
	return map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": text}}
}

// escape applies Slack's mrkdwn escaping.
func escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
