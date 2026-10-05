package config

import "testing"

func TestSMTPSettings(t *testing.T) {
	t.Setenv("WATCHTOWER_DATABASE_URL", "postgres://x")
	t.Setenv("WATCHTOWER_EMAIL_FROM", "Watchtower <errors@example.com>")
	for _, tc := range []struct {
		port, tls, wantTLS string
		wantPort           int
	}{
		{"", "", "starttls", 587},
		{"465", "", "tls", 465},
		{"", "tls", "tls", 465},
		{"25", "none", "none", 25},
	} {
		t.Setenv("WATCHTOWER_SMTP_HOST", "smtp.example.com")
		t.Setenv("WATCHTOWER_SMTP_PORT", tc.port)
		t.Setenv("WATCHTOWER_SMTP_TLS", tc.tls)
		c, err := Load()
		if err != nil || c.SMTPTLS != tc.wantTLS || c.SMTPPort != tc.wantPort || c.EmailFrom.Address != "errors@example.com" {
			t.Errorf("port=%q tls=%q: %s:%d %v", tc.port, tc.tls, c.SMTPTLS, c.SMTPPort, err)
		}
	}
	t.Setenv("WATCHTOWER_SMTP_TLS", "ssl")
	if _, err := Load(); err == nil {
		t.Error("unknown TLS mode accepted")
	}
	t.Setenv("WATCHTOWER_SMTP_TLS", "")
	t.Setenv("WATCHTOWER_EMAIL_FROM", "")
	if _, err := Load(); err == nil {
		t.Error("missing sender accepted")
	}
	t.Setenv("WATCHTOWER_SMTP_HOST", "")
	if c, err := Load(); err != nil || c.SMTPHost != "" {
		t.Errorf("email should be optional: %v", err)
	}
}

func TestEmailProvider(t *testing.T) {
	t.Setenv("WATCHTOWER_DATABASE_URL", "postgres://x")
	t.Setenv("WATCHTOWER_EMAIL_FROM", "Watchtower <alerts@example.com>")
	t.Setenv("WATCHTOWER_EMAIL_PROVIDER", "cloudflare")
	t.Setenv("WATCHTOWER_CLOUDFLARE_ACCOUNT_ID", "0123456789abcdef0123456789abcdef")
	t.Setenv("WATCHTOWER_CLOUDFLARE_API_TOKEN", "tok")
	if c, err := Load(); err != nil || c.EmailProvider != "cloudflare" || c.CloudflareAPIToken != "tok" || c.EmailFrom.Address != "alerts@example.com" {
		t.Errorf("cloudflare: %+v %v", c.EmailProvider, err)
	}
	t.Setenv("WATCHTOWER_CLOUDFLARE_ACCOUNT_ID", "not-an-id")
	if _, err := Load(); err == nil {
		t.Error("bad account ID accepted")
	}
	t.Setenv("WATCHTOWER_EMAIL_PROVIDER", "smtp")
	if _, err := Load(); err == nil {
		t.Error("smtp provider without a host accepted")
	}
	t.Setenv("WATCHTOWER_EMAIL_PROVIDER", "sendgrid")
	if _, err := Load(); err == nil {
		t.Error("unknown provider accepted")
	}
}
