// Package config reads Watchtower's settings from the environment.
package config

import (
	"errors"
	"fmt"
	"net/mail"
	"os"
	"regexp"
	"strconv"
	"strings"
)

type Config struct {
	DatabaseURL string
	Listen      string
	PublicURL   string // how SDKs reach this server; used to print DSNs

	Adapters []string // enabled adapter names
	Ingest   bool     // serve adapter routes
	Worker   bool     // drain the ingest queue

	MaxBodyBytes         int64
	MaxDecompressedBytes int64
	MaxEventBytes        int64
	MaxQueueDepth        int
	WorkerBatchSize      int
	EventsPerIssueHour   int      // events stored in full per issue each hour; 0 stores all
	RetentionDays        int      // events and issues older than this are deleted
	SecretKey            string   // seals alert webhooks at rest
	SlackWebhookHosts    []string // hosts alert webhooks may point at
	LogLevel             string

	// Email is off unless a provider is configured. "smtp" works with any
	// provider; "cloudflare" uses Cloudflare Email Service's REST API.
	EmailProvider       string
	CloudflareAccountID string
	CloudflareAPIToken  string
	SMTPHost            string
	SMTPPort            int
	SMTPUsername        string
	SMTPPassword        string
	SMTPTLS             string // tls (implicit), starttls or none
	EmailFrom           mail.Address
}

// Load reads WATCHTOWER_* variables, applying defaults.
func Load() (Config, error) {
	c := Config{
		DatabaseURL:       os.Getenv("WATCHTOWER_DATABASE_URL"),
		Listen:            env("WATCHTOWER_LISTEN", ":8080"),
		PublicURL:         strings.TrimRight(env("WATCHTOWER_PUBLIC_URL", "http://localhost:8080"), "/"),
		Adapters:          list(env("WATCHTOWER_ADAPTERS", "sentry,appsignal,appsignal-frontend")),
		LogLevel:          env("WATCHTOWER_LOG_LEVEL", "info"),
		SecretKey:         os.Getenv("WATCHTOWER_SECRET_KEY"),
		SlackWebhookHosts: list(env("WATCHTOWER_SLACK_WEBHOOK_HOSTS", "hooks.slack.com")),
	}
	roles := list(env("WATCHTOWER_ROLES", "ingest,worker"))
	var errs []error
	for _, r := range roles {
		switch r {
		case "ingest":
			c.Ingest = true
		case "worker":
			c.Worker = true
		default:
			errs = append(errs, fmt.Errorf("WATCHTOWER_ROLES: unknown role %q", r))
		}
	}
	c.MaxBodyBytes = intEnv("WATCHTOWER_MAX_BODY_BYTES", 20<<20, &errs)
	c.MaxDecompressedBytes = intEnv("WATCHTOWER_MAX_DECOMPRESSED_BYTES", 50<<20, &errs)
	c.MaxEventBytes = intEnv("WATCHTOWER_MAX_EVENT_BYTES", 1<<20, &errs)
	c.MaxQueueDepth = int(intEnv("WATCHTOWER_MAX_QUEUE_DEPTH", 100_000, &errs))
	c.WorkerBatchSize = int(intEnv("WATCHTOWER_WORKER_BATCH_SIZE", 100, &errs))
	if os.Getenv("WATCHTOWER_EVENTS_PER_ISSUE_HOUR") == "0" {
		c.EventsPerIssueHour = 0
	} else {
		c.EventsPerIssueHour = int(intEnv("WATCHTOWER_EVENTS_PER_ISSUE_HOUR", 100, &errs))
	}
	c.RetentionDays = int(intEnv("WATCHTOWER_RETENTION_DAYS", 90, &errs))
	loadEmail(&c, &errs)
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("WATCHTOWER_DATABASE_URL is required"))
	}
	return c, errors.Join(errs...)
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func list(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func intEnv(k string, def int64, errs *[]error) int64 {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		*errs = append(*errs, fmt.Errorf("%s: want a positive integer, got %q", k, v))
		return def
	}
	return n
}

func loadEmail(c *Config, errs *[]error) {
	c.EmailProvider = strings.ToLower(strings.TrimSpace(os.Getenv("WATCHTOWER_EMAIL_PROVIDER")))
	c.SMTPHost = strings.TrimSpace(os.Getenv("WATCHTOWER_SMTP_HOST"))
	if c.EmailProvider == "" && c.SMTPHost != "" {
		c.EmailProvider = "smtp"
	}
	switch c.EmailProvider {
	case "":
		return
	case "smtp":
		if c.SMTPHost == "" {
			*errs = append(*errs, errors.New("WATCHTOWER_SMTP_HOST is required for the smtp email provider"))
			return
		}
		loadSMTP(c, errs)
	case "cloudflare":
		c.CloudflareAccountID = strings.TrimSpace(os.Getenv("WATCHTOWER_CLOUDFLARE_ACCOUNT_ID"))
		c.CloudflareAPIToken = strings.TrimSpace(os.Getenv("WATCHTOWER_CLOUDFLARE_API_TOKEN"))
		if !hex32.MatchString(c.CloudflareAccountID) || c.CloudflareAPIToken == "" {
			*errs = append(*errs, errors.New("the cloudflare email provider needs WATCHTOWER_CLOUDFLARE_ACCOUNT_ID (32 hex characters) and WATCHTOWER_CLOUDFLARE_API_TOKEN"))
			return
		}
	default:
		*errs = append(*errs, fmt.Errorf("WATCHTOWER_EMAIL_PROVIDER: want smtp or cloudflare, got %q", c.EmailProvider))
		return
	}
	from, err := mail.ParseAddress(os.Getenv("WATCHTOWER_EMAIL_FROM"))
	if err != nil {
		*errs = append(*errs, fmt.Errorf("WATCHTOWER_EMAIL_FROM: want an address such as \"Watchtower <errors@example.com>\": %w", err))
		return
	}
	c.EmailFrom = *from
}

var hex32 = regexp.MustCompile(`^[0-9a-f]{32}$`)

func loadSMTP(c *Config, errs *[]error) {
	c.SMTPUsername = os.Getenv("WATCHTOWER_SMTP_USERNAME")
	c.SMTPPassword = os.Getenv("WATCHTOWER_SMTP_PASSWORD")
	c.SMTPTLS = strings.ToLower(os.Getenv("WATCHTOWER_SMTP_TLS"))
	port := int(intEnv("WATCHTOWER_SMTP_PORT", 0, errs))
	switch {
	case port == 0 && c.SMTPTLS == "tls":
		port = 465
	case port == 0:
		port = 587
	}
	c.SMTPPort = port
	if c.SMTPTLS == "" {
		c.SMTPTLS = "starttls"
		if port == 465 {
			c.SMTPTLS = "tls"
		}
	}
	if c.SMTPTLS != "tls" && c.SMTPTLS != "starttls" && c.SMTPTLS != "none" {
		*errs = append(*errs, fmt.Errorf("WATCHTOWER_SMTP_TLS: want tls, starttls or none, got %q", c.SMTPTLS))
	}
}
