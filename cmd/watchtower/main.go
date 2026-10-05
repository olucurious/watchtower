// Command watchtower runs the Watchtower error tracker and its admin tasks.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/olucurious/watchtower/internal/adapter"
	"github.com/olucurious/watchtower/internal/adapter/appsignal"
	"github.com/olucurious/watchtower/internal/adapter/sentry"
	"github.com/olucurious/watchtower/internal/alert"
	"github.com/olucurious/watchtower/internal/api"
	"github.com/olucurious/watchtower/internal/auth"
	"github.com/olucurious/watchtower/internal/config"
	"github.com/olucurious/watchtower/internal/email"
	"github.com/olucurious/watchtower/internal/mcpserver"
	"github.com/olucurious/watchtower/internal/metrics"
	"github.com/olucurious/watchtower/internal/server"
	"github.com/olucurious/watchtower/internal/sourcemaps"
	"github.com/olucurious/watchtower/internal/store"
	"github.com/olucurious/watchtower/internal/symbolicate"
	webui "github.com/olucurious/watchtower/internal/ui"
	"github.com/olucurious/watchtower/internal/worker"
)

const usage = `Usage: watchtower <command> [arguments]

Commands:
  serve                                  run ingestion and/or workers (see WATCHTOWER_ROLES)
  migrate                                apply database migrations
  adapters                               list ingestion adapters and tested SDKs
  healthcheck                            exit 0 if the local server answers /healthz
  project create <slug> [-name NAME]     create a project
  key create <project> <adapter> [-label L]
                                         issue a credential and print SDK settings
  issues <project> [-status S] [-limit N]
                                         list issues, most recently seen first
  issue <resolve|mute|unresolve> <id> [-actor NAME]
                                         change an issue's status
  user create <email> [-name NAME] [-admin]
                                         create a UI account (prompts for the password)
  user password <email>                  set a new password (prompts; ends sessions)
  setup-code                             print a new code for creating the first
                                         administrator in the browser

Configuration is read from WATCHTOWER_* environment variables; see README.md.
`

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "watchtower:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return errors.New("missing command")
	}
	if args[0] == "adapters" {
		return listAdapters()
	}
	if args[0] == "healthcheck" {
		return healthcheck()
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := newLogger(cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	db.MaxQueueDepth = cfg.MaxQueueDepth
	// Every command brings the schema up to date first, so admin commands
	// work on a fresh database before the server has ever started.
	if err := db.Migrate(ctx); err != nil {
		return fmt.Errorf("migrating: %w", err)
	}

	switch args[0] {
	case "serve":
		return serve(ctx, cfg, log, db)
	case "migrate":
		return nil // done above
	case "project":
		return projectCmd(ctx, db, args[1:])
	case "key":
		return keyCmd(ctx, cfg, db, args[1:])
	case "issues":
		return issuesCmd(ctx, db, args[1:])
	case "issue":
		return issueCmd(ctx, db, args[1:])
	case "user":
		return userCmd(ctx, db, args[1:])
	case "setup-code":
		code, err := db.NewSetupCode(ctx)
		if errors.Is(err, store.ErrSetupDone) {
			return errors.New("an account already exists; sign in, or use `watchtower user` to add or reset one")
		}
		if err != nil {
			return err
		}
		fmt.Printf("Setup code: %s\nOpen %s and enter it to create the administrator.\n", code, cfg.PublicURL)
		return nil
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// healthcheck probes the local server so container images without a shell
// or curl can still declare a HEALTHCHECK.
func healthcheck() error {
	addr := os.Getenv("WATCHTOWER_LISTEN")
	if addr == "" {
		addr = ":8080"
	}
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + addr + "/healthz")
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz responded %d", resp.StatusCode)
	}
	return nil
}

func allAdapters(log *slog.Logger) []adapter.Adapter {
	return []adapter.Adapter{sentry.New(log), appsignal.New(log), appsignal.NewFrontend(log)}
}

func enabledAdapters(log *slog.Logger, names []string) ([]adapter.Adapter, error) {
	byName := map[string]adapter.Adapter{}
	for _, s := range allAdapters(log) {
		byName[s.Name()] = s
	}
	var out []adapter.Adapter
	for _, n := range names {
		s, ok := byName[n]
		if !ok {
			return nil, fmt.Errorf("WATCHTOWER_ADAPTERS: unknown adapter %q", n)
		}
		out = append(out, s)
	}
	return out, nil
}

func serve(ctx context.Context, cfg config.Config, log *slog.Logger, db *store.Store) error {
	adapters, err := enabledAdapters(log, cfg.Adapters)
	if err != nil {
		return err
	}
	reg := metrics.New()
	enabled := map[string]bool{}
	for _, a := range adapters {
		enabled[a.Name()] = true
	}
	sealer, err := alert.NewSealer(cfg.SecretKey)
	if err != nil {
		return err
	}
	slack := alert.NewSlack(cfg.PublicURL, cfg.SlackWebhookHosts)
	linear := alert.NewLinear()
	tracker := &alert.Tracker{Store: db, Linear: linear, Sealer: sealer, PublicURL: cfg.PublicURL, Log: log}
	ui := &api.API{Linear: linear, Tracker: tracker, Sealer: sealer, Slack: slack, Store: db, Log: log, Adapters: allAdapters(log), Enabled: enabled, PublicURL: cfg.PublicURL, Version: version, RetentionDays: cfg.RetentionDays}
	var mailer *email.Mailer
	var sender email.Sender
	switch cfg.EmailProvider {
	case "smtp":
		sender = &email.SMTP{Host: cfg.SMTPHost, Port: cfg.SMTPPort, Username: cfg.SMTPUsername, Password: cfg.SMTPPassword, TLS: cfg.SMTPTLS, From: cfg.EmailFrom}
		log.Info("email enabled", "provider", "smtp", "server", net.JoinHostPort(cfg.SMTPHost, strconv.Itoa(cfg.SMTPPort)), "tls", cfg.SMTPTLS, "from", cfg.EmailFrom.Address)
	case "cloudflare":
		sender = &email.CloudflareAPI{AccountID: cfg.CloudflareAccountID, Token: cfg.CloudflareAPIToken, From: cfg.EmailFrom}
		log.Info("email enabled", "provider", "cloudflare", "from", cfg.EmailFrom.Address)
	}
	if sender != nil {
		ui.Email = sender
		db.EmailEnabled = true
		mailer = &email.Mailer{Outbox: db, Sender: sender, PublicURL: cfg.PublicURL, Log: log, Metrics: reg, Idle: 5 * time.Second, DigestCheck: 10 * time.Minute}
	}
	// Background loops get their own context, so shutdown can stop them
	// after in-flight requests finish.
	var wg sync.WaitGroup
	workerCtx, stopWorker := context.WithCancel(context.WithoutCancel(ctx))
	defer func() {
		stopWorker()
		wg.Wait()
	}()
	if cfg.Worker {
		w := &worker.Worker{Queue: db, Grouper: &worker.Grouper{Symbolicator: symbolicate.New(), Log: log}, Log: log, Metrics: reg, BatchSize: cfg.WorkerBatchSize, Idle: 500 * time.Millisecond}
		m := &worker.Maintenance{Store: db, Log: log, Metrics: reg, Every: time.Hour,
			Retention: time.Duration(cfg.RetentionDays) * 24 * time.Hour}
		n := &alert.Notifier{Outbox: db, Sealer: sealer, Slack: slack, Tracker: tracker, Log: log, Metrics: reg, Idle: 2 * time.Second}
		wg.Go(func() { tracker.RunSync(workerCtx, 5*time.Minute) })
		wg.Go(func() { w.Run(workerCtx) })
		wg.Go(func() { m.Run(workerCtx) })
		wg.Go(func() { n.Run(workerCtx) })
		if mailer != nil {
			wg.Go(func() { mailer.Run(workerCtx) })
		}
	}
	if !cfg.Ingest {
		adapters = nil // still serve health and metrics
	}
	deps := adapter.Deps{
		Keys:    db,
		Sink:    db,
		Metrics: reg,
		Limits: adapter.Limits{
			MaxBodyBytes: cfg.MaxBodyBytes, MaxDecompressedBytes: cfg.MaxDecompressedBytes, MaxEventBytes: cfg.MaxEventBytes,
		},
	}
	uploads := &sourcemaps.Uploads{Store: db, PublicURL: cfg.PublicURL, Log: log}
	agents := &mcpserver.Server{Store: db, Tracker: tracker, PublicURL: cfg.PublicURL, Version: version, Log: log}
	srv := &http.Server{
		Addr: cfg.Listen,
		Handler: server.Handler(log, adapters, deps, db, reg, ui.Register, uploads.Register, agents.Register, func(mux *http.ServeMux) {
			mux.Handle("GET /", webui.Handler())
		}),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	names := make([]string, 0, len(adapters))
	for _, s := range adapters {
		names = append(names, s.Name())
	}
	log.Info("watchtower listening", "addr", cfg.Listen, "adapters", names, "worker", cfg.Worker)
	// A fresh install gets a one-time code for creating the administrator
	// in the browser; each start issues a new one until an account exists.
	switch code, err := db.NewSetupCode(ctx); {
	case err == nil:
		log.Warn("no accounts yet: open the web UI and enter this setup code to create the administrator",
			"setup_code", code, "url", cfg.PublicURL)
	case !errors.Is(err, store.ErrSetupDone):
		return fmt.Errorf("issuing setup code: %w", err)
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	// Stop accepting first so in-flight requests finish; the deferred
	// stop then lets the background loops finish their current work.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func listAdapters() error {
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ADAPTER\tSTATUS\tROUTES\tTESTED SDKS")
	for _, s := range allAdapters(slog.Default()) {
		d := s.Describe()
		status := "stable"
		if d.Experimental {
			status = "experimental"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", s.Name(), status, strings.Join(d.Routes, ", "), strings.Join(d.TestedSDK, "; "))
	}
	return tw.Flush()
}

func projectCmd(ctx context.Context, db *store.Store, args []string) error {
	if len(args) < 2 || args[0] != "create" {
		return errors.New("usage: watchtower project create <slug> [-name NAME]")
	}
	slug := args[1]
	fs := flag.NewFlagSet("project create", flag.ContinueOnError)
	name := fs.String("name", slug, "display name")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	p, err := db.CreateProject(ctx, slug, *name)
	if err != nil {
		return err
	}
	fmt.Printf("created project %s (id %d)\n", p.Slug, p.ID)
	return nil
}

func keyCmd(ctx context.Context, cfg config.Config, db *store.Store, args []string) error {
	if len(args) < 3 || args[0] != "create" {
		return errors.New("usage: watchtower key create <project> <adapter> [-label L]")
	}
	projectSlug, adapterName := args[1], args[2]
	if _, err := enabledAdapters(slog.Default(), []string{adapterName}); err != nil {
		return err
	}
	fs := flag.NewFlagSet("key create", flag.ContinueOnError)
	label := fs.String("label", "", "description of where the key is used")
	if err := fs.Parse(args[3:]); err != nil {
		return err
	}
	key, p, err := db.CreateKey(ctx, projectSlug, adapterName, *label, 0)
	if err != nil {
		return err
	}
	settings, err := api.SDKSettings(cfg.PublicURL, adapterName, key, p.ID)
	if err != nil {
		return err
	}
	fmt.Println("Store this now; Watchtower keeps only a hash and cannot show it again.")
	for _, k := range slices.Sorted(maps.Keys(settings)) {
		fmt.Printf("%s=%s\n", k, settings[k])
	}
	return nil
}

func issuesCmd(ctx context.Context, db *store.Store, args []string) error {
	if len(args) < 1 {
		return errors.New("usage: watchtower issues <project> [-status S] [-limit N]")
	}
	fs := flag.NewFlagSet("issues", flag.ContinueOnError)
	status := fs.String("status", "all", "unresolved, resolved, muted or all")
	limit := fs.Int("limit", 50, "maximum issues to list")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	switch *status {
	case "all":
		*status = ""
	case "unresolved", "resolved", "muted":
	default:
		return fmt.Errorf("-status must be unresolved, resolved, muted or all; got %q", *status)
	}
	issues, err := db.Issues(ctx, args[0], *status, *limit)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATUS\tLEVEL\tSEEN\tLAST SEEN\tRELEASE\tTITLE\tCULPRIT")
	for _, i := range issues {
		status := i.Status
		if i.RegressedAt != nil && i.Status == "unresolved" {
			status = "regressed"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%d\t%s\t%s\t%s\t%s\n", i.ID, status, i.Level, i.TimesSeen,
			i.LastSeen.Local().Format(time.DateTime), i.LastRelease, i.Title, i.Culprit)
	}
	return tw.Flush()
}

func issueCmd(ctx context.Context, db *store.Store, args []string) error {
	if len(args) < 2 {
		return errors.New("usage: watchtower issue <resolve|mute|unresolve> <id> [-actor NAME]")
	}
	status := map[string]string{"resolve": "resolved", "mute": "muted", "unresolve": "unresolved"}[args[0]]
	if status == "" {
		return fmt.Errorf("unknown issue action %q", args[0])
	}
	id, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		return fmt.Errorf("issue id: %w", err)
	}
	fs := flag.NewFlagSet("issue", flag.ContinueOnError)
	actor := fs.String("actor", os.Getenv("USER"), "who is making the change")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	n, err := db.SetIssuesStatus(ctx, []int64{id}, status, *actor)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("issue %d not found or already %s", id, status)
	}
	fmt.Printf("issue %d is now %s\n", id, status)
	return nil
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}

func userCmd(ctx context.Context, db *store.Store, args []string) error {
	if len(args) < 2 || (args[0] != "create" && args[0] != "password") {
		return errors.New("usage: watchtower user create <email> [-name NAME] [-admin] | user password <email>")
	}
	email := args[1]
	fs := flag.NewFlagSet("user", flag.ContinueOnError)
	name := fs.String("name", "", "display name")
	admin := fs.Bool("admin", false, "grant administrator access")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	password, err := readPassword()
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	if args[0] == "password" {
		u, err := db.UserByEmail(ctx, email)
		if err != nil {
			return fmt.Errorf("user %q: %w", email, err)
		}
		if err := db.SetPassword(ctx, u.ID, hash); err != nil {
			return err
		}
		fmt.Printf("password updated for %s; existing sessions were signed out\n", u.Email)
		return nil
	}
	u, err := db.CreateUser(ctx, email, *name, hash, *admin)
	if err != nil {
		return err
	}
	role := "member"
	if u.IsAdmin {
		role = "administrator"
	}
	fmt.Printf("created %s %s (id %d)\n", role, u.Email, u.ID)
	return nil
}

// readPassword prompts twice on a terminal, or reads one line from stdin
// when piped (for automation), so the password never appears in argv.
func readPassword() (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return readLine(os.Stdin)
	}
	fmt.Fprint(os.Stderr, "Password: ")
	first, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	fmt.Fprint(os.Stderr, "Repeat password: ")
	second, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if string(first) != string(second) {
		return "", errors.New("passwords do not match")
	}
	return string(first), nil
}

// readLine reads one line, keeping everything but the line ending:
// passphrases have spaces, and leading or trailing spaces are part of the
// password.
func readLine(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		return "", fmt.Errorf("reading password from stdin: %w", err)
	}
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), nil
}
