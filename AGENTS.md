# Working on Watchtower

This file is for anyone changing Watchtower, people and coding agents alike.
It describes how the system fits together and the rules every change must
keep. [CONTRIBUTING.md](CONTRIBUTING.md) covers setup and the pull request
process; [README.md](README.md) covers using it.

## What Watchtower is

A self-hosted error tracker. Official vendor SDKs (Sentry, AppSignal) report
to it unchanged: users change only an endpoint or DSN. Each vendor protocol is
an **adapter**; everything behind the adapters is shared. It ships as one Go
binary with the web UI embedded, and Postgres is its only dependency.

```
SDK ─▶ adapter (auth, decode, convert) ─▶ scrub ─▶ ingest_queue ─▶ worker ─▶ issues + events
                                                      (symbolicate, group, regressions,
                                                       alerts and emails in one transaction)
```

## Where things live

| Path | What it is |
|---|---|
| `cmd/watchtower` | The binary: `serve`, `migrate` and admin commands; wires everything together |
| `internal/adapter/<vendor>` | Ingestion protocols (`sentry`, `appsignal`); `adaptertest` has in-memory fakes |
| `internal/event` | The canonical event model every adapter produces |
| `internal/scrub` | Redaction applied before anything is stored |
| `internal/grouping` | Which issue an event belongs to (versioned) |
| `internal/symbolicate`, `internal/sourcemaps` | Source map uploads and frame mapping |
| `internal/store` | Everything in Postgres; migrations in `store/migrations` |
| `internal/worker` | Drains the ingest queue; hourly maintenance |
| `internal/alert` | Slack and Linear delivery, Linear sync, credential sealing |
| `internal/email` | SMTP and Cloudflare senders, rendering, the mailer loop |
| `internal/mcpserver` | The MCP endpoint for coding agents |
| `internal/api` | The JSON API behind the web UI (`/api/v1/`) |
| `internal/server` | Assembles the HTTP surface: adapters, health, metrics |
| `internal/config` | `WATCHTOWER_*` settings, validated at startup |
| `web/` | React UI, built by Vite into `internal/ui/dist` and embedded |
| `install.sh`, `compose.yaml` | The one-line installer and the Compose file it downloads |

## Invariants

These hold everywhere. A change that needs to break one needs a discussion
first.

- **Adapters only translate.** They authenticate, decode, convert to
  `event.Event` and call `Sink.Accept`. Grouping, storage and alerting never
  live in an adapter.
- **Acknowledge only after a durable write.** A 2xx means the event is in
  `ingest_queue`.
- **Scrub before anything durable.** `server.scrubbingSink` runs before the
  queue. A new free-text field in `event.Event` must also be handled in
  `scrub.Event` and `Event.StripNUL`.
- **The event model is an allowlist.** Request bodies, headers, cookies,
  breadcrumbs, process state and free-form SDK "extra" data are dropped at the
  adapter. `Event.Context` keeps only named diagnostic values (logger metadata
  an app chose to send, a failed job's identity); extend its allowlist
  deliberately, never by passing extras through.
- **Side effects share the event's transaction.** Issues, regressions, alert
  notifications and emails are written together, so an event is never stored
  without its alert, or alerted twice. Delivery happens later from those
  outboxes.
- **Grouping is versioned.** Any change to `grouping.Components` output bumps
  `grouping.Version`.
- **Shed load explicitly.** Overload returns the vendor's own back-off
  response (for example 429 with `X-Sentry-Rate-Limits`); never block or drop
  silently. The queue limit is an estimate (`depthGauge`); never reintroduce
  a shared counter row, which serializes every ingest commit.
- **Lock issues in (project, fingerprint) order.** The worker locks every
  issue a batch touches in that order before writing events, so concurrent
  workers wait for each other but never deadlock. Any new code that locks
  several issues in one transaction must do the same.
- **Count from the rollup.** Charts, totals and digests read `issue_hourly`,
  which the worker keeps with the events. Don't count `events` rows on a
  request path; it grows with traffic.
- **Migrations are append-only.** Never edit a migration that has been
  released; add a new one. They run automatically at startup, so they must be
  safe on a live database.

## Security rules

- **Never log request bodies or query strings.** SDKs put credentials in both;
  AppSignal embeds its key in the body.
- **Secrets are never stored in plaintext.** Keys, tokens and sessions are
  stored as SHA-256 digests and shown once. Credentials Watchtower must use
  later (Slack webhooks and bot tokens, Linear keys) are sealed with
  `alert.Sealer` (AES-256-GCM under `WATCHTOWER_SECRET_KEY`). Error messages
  and logs never include them.
- **Event content is untrusted.** Anyone who can send an app errors, including
  through a browser SDK's public key, controls titles, messages, frames, tags
  and context. Render it escaped (React, `html/template`), keep it out of email
  headers (`email.oneLine`), and label it as data wherever an agent reads it
  (`mcpserver` briefs).
- **State-changing API endpoints** go through `API.mutation` (JSON only, same
  origin) and a session check (`API.user` or `API.admin`). The MCP endpoint
  uses bearer tokens instead and rejects cross-origin requests.
- **Outbound requests** go only to hosts Watchtower chose or an admin
  configured: Slack webhooks are limited to `WATCHTOWER_SLACK_WEBHOOK_HOSTS`;
  Linear and Cloudflare URLs are fixed.
- **Fixtures and screenshots** use synthetic apps and placeholder
  credentials, never production data or real keys.

## Background work

The worker role (`WATCHTOWER_ROLES=worker`) runs these loops. Each one is safe
to run in several processes at once; they coordinate through Postgres row
locks (`FOR UPDATE SKIP LOCKED`) and unique keys.

| Loop | Package | Does |
|---|---|---|
| Ingest | `worker` | Symbolicate, group and store a batch, one write per issue; alerts, emails and hourly counts in the same transaction. A failed batch is retried event by event, so a bad event fails alone |
| Notifier | `alert` | Deliver the notification outbox to Slack and Linear, with retries |
| Mailer | `email` | Schedule digests (deduplicated per user and period) and deliver the email outbox |
| Linear sync | `alert` | Every five minutes, resolve issues whose Linear issue was completed |
| Maintenance | `worker` | Hourly retention and expired sessions |

Outbox deliveries keep their row locked until the outcome commits, back off on
failure, and are at-least-once: design every receiver to tolerate a repeat
(Linear issues use an ID derived from the Watchtower issue, so a repeat
returns the existing one).

## Common changes

**Supporting a new SDK version or adapter**

1. Capture real payloads from the unmodified SDK, sending to a loopback
   receiver with placeholder credentials and a synthetic app.
2. Commit them under `internal/adapter/<vendor>/testdata/` with a README on
   how they were captured.
3. Test that every fixture decodes, and the HTTP contract: auth failures, size
   limits, back-off.
4. List the exact tested versions in the adapter's `Describe()` and in the
   README. Mark it `Experimental` if the protocol is reverse-engineered.

**Adding an alert destination**

Add a `kind` to `alert_channels` (a migration widening its check), validate
and seal its credentials in `internal/api/alerts.go`, dispatch it in
`alert.Notifier.send`, and add it to `web/src/components/alerts.tsx`. Test
delivery against an `httptest` fake of the service, including its error and
rate-limit responses.

**Adding an MCP tool**

Register it in `mcpserver.addTools` with a description written for an agent,
read-only or write annotations, and typed input with `jsonschema` tags. Write
tools must check `caller(req)` for the write scope and record the token's
owner as the actor. Extend `TestMCP`, which drives the server through the
official MCP client.

**Adding a setting**

Add it to `internal/config` with validation that fails at startup, document it
in the README's configuration table, and add it to `compose.yaml` if a
self-hoster is likely to need it.

**Releasing**

Tag `vX.Y.Z` on `main` and push the tag. The release workflow publishes
`ghcr.io/olucurious/watchtower` as `X.Y.Z`, `X.Y` and `latest` for amd64 and
arm64; the installer and `compose.yaml` pull `latest`. Changes to
`install.sh` or `compose.yaml` reach users as soon as they are on `main`, so
test them with a real install before merging.

**Adding an API endpoint**

Register it in `API.Register` with a method-specific pattern (a method-less
pattern conflicts with the UI's `GET /` catch-all and panics at startup), wrap
mutations in `API.mutation`, and return errors through `writeError` or
`storeError` so internals never leak.

## Web UI

`web/` is React 19, TanStack Query and shadcn/ui in its Base UI flavour.

- Base UI composes with the `render` prop, not `asChild`.
- A menu's `DropdownMenuLabel` must sit inside a `DropdownMenuGroup`;
  otherwise Base UI throws when the menu opens.
- Content inside a `Dialog` needs `min-w-0` on its wrapper, or a long line
  (such as a command) pushes the dialog's buttons off-screen. Long dialogs get
  `max-h-[90svh] overflow-y-auto`.
- Check every new screen at phone width and in both light and dark themes.
- Keep components quiet: text and spacing do the work, colour marks state.

## Testing

```sh
make lint test                                 # golangci-lint, UI type-check and lint, unit tests
make test-db test-integration test-db-stop     # also the Postgres-backed tests
```

- Postgres-backed tests use `storetest.New(t)` (or the store's
  `newTestStore`), which creates a throwaway database per test; they skip when
  `WATCHTOWER_TEST_DATABASE_URL` is unset.
- External services are faked with `httptest` (Slack, Linear, Cloudflare) or
  in-process servers (SMTP with STARTTLS and implicit TLS). Never call a real
  third-party service from a test.
- Test the behaviour a user or SDK sees, including the failure paths:
  rejected credentials, retries, rate limits and permission checks.
- A UI change is done when it has been looked at in a browser, not when it
  type-checks.
- Performance claims need a measurement. Per-event code (adapters, scrub,
  grouping) runs thousands of times a second: check it with a benchmark
  (`go test -bench . ./internal/scrub`) before and after a change, and keep
  queries on request paths independent of the number of events.

## Style

- Go: `gofmt`, small functions, errors wrapped with context
  (`fmt.Errorf("doing x: %w", err)`). Comments explain why, not what.
- Error messages a user sees say what to do next, for example
  "a Linear API key starts with lin_api_ (Linear › Settings › …)".
- UI copy is plain and short. No jargon where a common word works.
- Match the surrounding code's naming and comment density.
