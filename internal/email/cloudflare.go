package email

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"strings"
)

// CloudflareAPI sends through Cloudflare Email Service's REST API. Unlike
// its SMTP endpoint, the API names the account in the URL, so it works with
// user-owned API tokens as well as account-owned ones.
type CloudflareAPI struct {
	AccountID string
	Token     string
	From      mail.Address
	// BaseURL and Client are overridden in tests.
	BaseURL string
	Client  *http.Client
}

type cfAddress struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address"`
}

func (c *CloudflareAPI) Send(ctx context.Context, m Message) error {
	if _, err := mail.ParseAddress(m.To.Address); err != nil {
		return fmt.Errorf("recipient address: %w", err)
	}
	body, _ := json.Marshal(map[string]any{
		"from":    cfAddress{Name: oneLine(c.From.Name), Address: c.From.Address},
		"to":      []string{m.To.Address},
		"subject": oneLine(m.Subject),
		"text":    m.Text,
		"html":    m.HTML,
		"headers": map[string]string{"Auto-Submitted": "auto-generated", "X-Auto-Response-Suppress": "All"},
	})
	base := c.BaseURL
	if base == "" {
		base = "https://api.cloudflare.com/client/v4"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/accounts/"+c.AccountID+"/email/sending/send", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: smtpTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("sending with the Cloudflare email API: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		Success bool `json:"success"`
		Errors  []struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode/100 == 2 && out.Success {
		return nil
	}
	var msgs []string
	for _, e := range out.Errors {
		msgs = append(msgs, fmt.Sprintf("%d %s", e.Code, e.Message))
	}
	if len(msgs) == 0 {
		msgs = append(msgs, http.StatusText(resp.StatusCode))
	}
	return fmt.Errorf("the Cloudflare email API responded %d: %s", resp.StatusCode, strings.Join(msgs, "; "))
}
