<p align="center">
  <img src="docs/images/cover.png" alt="Watchtower: self-hosted error tracking, drop-in for Sentry or AppSignal" width="100%">
</p>

<p align="center">
  <b>Self-hosted error tracking that speaks Sentry and AppSignal.</b><br>
  Keep the SDK you already use and point it at your own server.
</p>

<p align="center">
  <a href="https://github.com/olucurious/watchtower/actions/workflows/ci.yml"><img src="https://github.com/olucurious/watchtower/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-7c3aed" alt="License: Apache-2.0"></a>
  <img src="https://img.shields.io/badge/go-1.26-7c3aed" alt="Go 1.26">
  <img src="https://img.shields.io/badge/database-Postgres-7c3aed" alt="Postgres">
  <img src="https://img.shields.io/badge/ships%20as-one%20binary-7c3aed" alt="One binary">
</p>

<p align="center">
  <a href="#quick-start">Quick start</a> ·
  <a href="#connect-your-apps">Connect your apps</a> ·
  <a href="#features">Features</a> ·
  <a href="#configuration">Configuration</a> ·
  <a href="CONTRIBUTING.md">Contributing</a>
</p>

---

Watchtower implements the ingestion protocols of hosted error trackers, so
their official SDKs report to it unchanged. Moving off a hosted tracker means
changing one setting, the DSN or the push endpoint, rather than
re-instrumenting every service. Errors are grouped into issues you can triage
with your team, alerted to Slack, Linear and email, and handed to coding
agents through MCP.

It runs as a single Go binary with Postgres as its only dependency.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/images/issues-dark.png">
  <img src="docs/images/issues-light.png" alt="The issues list: errors grouped by cause with event counts, sparklines, owners and releases">
</picture>

## Contents

- [Features](#features)
- [Quick start](#quick-start)
- [Connect your apps](#connect-your-apps)
- [Triage](#triage)
- [Alerts and integrations](#alerts-and-integrations)
- [Coding agents (MCP)](#coding-agents-mcp)
- [JavaScript source maps](#javascript-source-maps)
- [Configuration](#configuration)
- [Running in production](#running-in-production)
- [How it works](#how-it-works)
- [Data handling](#data-handling)
- [CLI](#cli)
- [Contributing](#contributing)
- [License](#license)

## Features

- **Drop-in ingestion.** Sentry SDKs in any language, the AppSignal agents for
  Elixir, Ruby, Node.js and Python, and the AppSignal browser SDK. Each vendor
  protocol is an *adapter*; everything behind the adapters is shared.
- **Grouping you can trust.** Errors group by their cause, with versioned
  rules that stay stable across builds, minified code and recursion.
  Resolved issues that happen again reopen as regressions.
- **Calm during error storms.** Every event is counted, but past 100 events of
  an issue in an hour only a sample is stored, so a crash loop costs counts,
  not disk.
- **Triage together.** Owners, comments, an activity timeline, bulk actions,
  search and filters, and releases linked to their commits.
- **Alerts where you work.** Slack (incoming webhook or bot token), Linear
  issues that resolve here when they're completed in Linear, and personal email
  notifications and digests through any SMTP provider.
- **Built for coding agents.** An MCP server gives Claude Code, Codex, Cursor
  and other agents a complete brief of an issue, with source context, and lets
  them record their work.
- **JavaScript source maps** uploaded with `sentry-cli` or Sentry's bundler
  plugins.
- **Private by design.** An allowlisted event model and redaction of card
  numbers, tokens and secrets before anything is stored.
- **Simple to run.** One binary, Postgres, health and readiness endpoints,
  Prometheus metrics, retention, and horizontal scaling.

## Quick start

You need Docker with Compose. Run:

```sh
curl -fsSL https://raw.githubusercontent.com/olucurious/watchtower/main/install.sh | sh
```

It sets Watchtower up in `./watchtower` with generated secrets, starts it with
Postgres, and prints the address and a one-time setup code. To use another
folder, run `… | WATCHTOWER_DIR=my-folder sh`; the installer never takes over a
folder that belongs to another project. Open the address,
enter the code, and create your administrator account. Then create a project;
its page walks you through creating a key, adding the SDK and sending a first
error, and links to it as soon as it arrives.

Images are published for amd64 and arm64. To run a specific version, set
`WATCHTOWER_VERSION` (for example `WATCHTOWER_VERSION=0.1.0`) when you run the
installer, or in `watchtower/.env` later.

<details>
<summary>Installing by hand, or building from source</summary>

```sh
mkdir watchtower && cd watchtower
curl -fsSLO https://raw.githubusercontent.com/olucurious/watchtower/main/compose.yaml
cat > .env <<EOF
WATCHTOWER_DB_PASSWORD=$(openssl rand -hex 16)
WATCHTOWER_SECRET_KEY=$(openssl rand -hex 32)
EOF
docker compose up -d
docker compose exec watchtower watchtower setup-code   # the code for the first account
```

To build the image from a checkout instead of pulling it:

```sh
git clone https://github.com/olucurious/watchtower.git && cd watchtower
# create .env as above, then:
docker compose -f compose.yaml -f compose.build.yaml up -d --build
```

</details>

<p align="center">
  <img src="docs/images/setup.png" alt="A new project's setup guide: create a key, add the SDK, send a test error" width="88%">
</p>

`WATCHTOWER_SECRET_KEY` encrypts the credentials of Slack, Linear and other
destinations. Keep it safe and keep it stable: changing it makes those stored
credentials unreadable. To use another host port, set `WATCHTOWER_PORT`; for
anything beyond a local trial, see [Running in production](#running-in-production).

## Connect your apps

Create a key on the project's page for the SDK the service already uses. The
key's settings are shown once; Watchtower stores only a hash.

### Sentry SDKs

Replace the DSN; nothing else changes.

```sh
SENTRY_DSN=https://<key>@watchtower.example.com/<project-id>
```

<details>
<summary>Setting up a Sentry SDK from scratch</summary>

**Elixir** (`{:sentry, "~> 13.4"}`)

```elixir
# config/runtime.exs
config :sentry, dsn: System.get_env("SENTRY_DSN"), environment_name: config_env()

# lib/my_app/application.ex, in start/2: report crashed processes, and say
# where log-reported errors (such as failed database connections) came from
:logger.add_handler(:sentry_handler, Sentry.LoggerHandler, %{config: %{metadata: [:mfa, :application]}})
```

**Node.js** (`npm install @sentry/node`)

```js
const Sentry = require("@sentry/node")
Sentry.init({ dsn: process.env.SENTRY_DSN, environment: process.env.NODE_ENV })
```

**Python** (`pip install sentry-sdk`)

```python
import os, sentry_sdk
sentry_sdk.init(dsn=os.environ["SENTRY_DSN"], environment="production")
```

**Browser** (`npm install @sentry/browser`)

```js
import * as Sentry from "@sentry/browser"
Sentry.init({ dsn: "https://<key>@watchtower.example.com/<project-id>" })
```

</details>

### AppSignal (Elixir, Ruby, Node.js, Python)

Set two variables. Packages and instrumentation stay as they are.

```sh
APPSIGNAL_PUSH_API_ENDPOINT=https://watchtower.example.com
APPSIGNAL_PUSH_API_KEY=<key>
```

For the AppSignal browser SDK, create an *AppSignal (browser)* key and pass
its settings to `new Appsignal({ key, uri })`.

### Tested SDKs

| Adapter | Tested with |
|---|---|
| `sentry` | sentry-python 2.71.0, @sentry/node 11.2.0, sentry-elixir 11.0.4; source map uploads from sentry-cli 3.8.0 |
| `appsignal` | Elixir 2.9.2–2.18.0 (17 releases), Ruby 5.0.1, Node.js 3.9.1, Python 1.9.0 |
| `appsignal-frontend` | `@appsignal/javascript` 1.6.1 |

Every tested version has a captured payload in the test suite. The AppSignal
protocol is reverse-engineered, so that adapter is marked experimental; see
[docs/adapters/appsignal.md](docs/adapters/appsignal.md). A weekly workflow
checks for new AppSignal releases.

## Triage

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/images/issue-dark.png">
  <img src="docs/images/issue-light.png" alt="An issue: the stack trace with source context, its cause, highlights, event chart, tag breakdowns and the team's comments">
</picture>

- **Issues** filter by project, environment, owner and period, with search
  (`/`), status tabs, sparklines and bulk resolve, mute or reopen.
- **An issue** shows the exception chain with your own frames first and source
  context around each line, highlights, how events vary by release,
  environment and server, and charts for the last 24 hours and 30 days.
  Errors reported from a log line show where they were logged.
- **Ownership and discussion:** assign an issue, comment on it, and follow its
  timeline of status changes, regressions, alerts and links.
- **Releases** that are commit SHAs link to the commit once the project names
  its repository.
- **On phones,** the summary comes first and everything stays usable.

<p align="center">
  <img src="docs/images/phone.png" alt="An issue on a phone" width="300">
</p>

## Alerts and integrations

<p align="center">
  <img src="docs/images/integrations.png" alt="A project's alerts: a Slack channel and a Linear team" width="88%">
</p>

Alerts fire when an issue is new, when a resolved issue comes back, or when an
issue reaches a number of events in an hour, optionally limited to a minimum
level and one environment. They're written to an outbox in the same
transaction as the event that triggered them, then delivered with retries, so
an event is never stored without its alert. Each destination shows its last
delivery or error and has a Test button.

### Slack

Use an incoming webhook, or an existing Slack app's bot token (`xoxb-…`) with
a channel ID and the bot invited to that channel. Messages link straight to
the event that triggered them.

### Linear

Add a Linear destination with an API key (Linear › Settings › Security &
access › Personal API keys) and pick a team.

- **Create Linear issue** on an issue's page files it with the error, its
  location, the top stack frames, counts, release and a link back. The issue
  shows its Linear identifier and state from then on.
- Alert rules can also file issues automatically. An issue already in Linear
  gets a comment instead of a duplicate. With no rules, the destination is
  used only by hand.
- **Completing the Linear issue resolves it here.** Watchtower polls Linear
  every five minutes instead of receiving webhooks, so it works on a private
  network.

Each Linear issue is created with an ID derived from the Watchtower issue and
your secret key, so a retried or concurrent request can never file it twice.

### Email

Members get email when an issue is assigned to them, when an issue they own
regresses, and when someone comments on one, and can opt in to a daily or
weekly digest of new issues, regressions and the busiest issues. Each member
chooses on their Account page, which also has a **Send test email** button
that shows the mail server's answer.

Set `WATCHTOWER_EMAIL_PROVIDER` to turn email on:

- `smtp` works with any provider: Postmark, Amazon SES, Mailgun, Resend,
  Google Workspace, Cloudflare Email Service, or a relay of your own.
- `cloudflare` uses Cloudflare Email Service's REST API, which also accepts
  user-owned tokens (its SMTP endpoint accepts only account-owned ones).

| Provider | Host | Port and TLS | Username |
|---|---|---|---|
| Postmark | `smtp.postmarkapp.com` | 587, `starttls` | the server API token (also the password) |
| Amazon SES | `email-smtp.<region>.amazonaws.com` | 587, `starttls` | SMTP credentials from the SES console |
| Cloudflare Email Service | `smtp.mx.cloudflare.net` | 465, `tls` | `api_token`; the password is an account-owned API token with *Email Sending: Edit* |
| Local relay (Postfix, …) | its address | 25, `none` | usually none |

The sender's domain must be verified with your provider.

## Coding agents (MCP)

Watchtower runs a [Model Context Protocol](https://modelcontextprotocol.io)
server at `/mcp`, so an agent can pick up an error, understand it and fix it.
Create a token under **Account › Agent access**; the dialog shows the setup for
Claude Code, Codex, Cursor and other clients. For Claude Code:

```sh
claude mcp add --transport http watchtower https://watchtower.example.com/mcp \
  --header "Authorization: Bearer wtp_…"
```

Then ask, for example, *"Find the most frequent unresolved error in the
reader project in Watchtower and fix it."*

<p align="center">
  <img src="docs/images/agent.png" alt="The brief an agent receives from get_issue: status, owner, Linear issue, frequency, releases and the stack trace with source context" width="88%">
</p>

| Tool | What it does |
|---|---|
| `list_projects` | Projects with unresolved counts and recent volume |
| `list_issues` | Find issues by project, status, text, environment, owner or period |
| `get_issue` | The brief above: stack trace with source context, cause chain, frequency, releases, how events vary, and the team's comments |
| `list_events`, `get_event` | Individual occurrences in full |
| `assign_issue`, `add_comment`, `update_issue_status`, `create_linear_issue` | Record work; these need a write token |

Tokens act as the member who created them, so an agent's changes appear under
that member's name. They are read-only or read-write, can expire after 30 or
90 days, are stored only as SHA-256 digests, and stop working when revoked or
when their owner is disabled. Error text reported by applications is labelled
as untrusted data in every brief, and the endpoint rejects cross-origin
browser requests.

## JavaScript source maps

Watchtower accepts uploads from `sentry-cli` and Sentry's Vite, webpack and
esbuild plugins. Create an upload token on the project's page, then in CI:

```sh
export SENTRY_URL=https://watchtower.example.com SENTRY_AUTH_TOKEN=wtk_… SENTRY_ORG=watchtower SENTRY_PROJECT=web
npx @sentry/cli sourcemaps inject ./dist
npx @sentry/cli sourcemaps upload ./dist
```

Before grouping, minified frames are mapped back to the original file,
function and line, with surrounding source. Bundles match by debug ID, or by
release and file URL for uploads without one, so issues group by your
original code and stay stable across builds.

## Configuration

Watchtower reads `WATCHTOWER_*` environment variables.

| Variable | Default | Purpose |
|---|---|---|
| `WATCHTOWER_DATABASE_URL` | required | Postgres connection URL. Each process opens up to 25 connections; set `pool_max_conns` in the URL to change that |
| `WATCHTOWER_PUBLIC_URL` | `http://localhost:8080` | Address SDKs and browsers use; printed in DSNs and links. With `https://`, session cookies are marked Secure |
| `WATCHTOWER_SECRET_KEY` | none | 32 bytes, hex or base64. Encrypts destination credentials at rest; required for Slack and Linear |
| `WATCHTOWER_LISTEN` | `:8080` | HTTP listen address |
| `WATCHTOWER_ADAPTERS` | `sentry,appsignal,appsignal-frontend` | Enabled adapters |
| `WATCHTOWER_ROLES` | `ingest,worker` | Serve ingestion, run the background workers, or both |
| `WATCHTOWER_EVENTS_PER_ISSUE_HOUR` | 100 | Events of one issue stored in full each hour; after that, every event is counted but only one in 1,000 is stored. `0` stores every event |
| `WATCHTOWER_RETENTION_DAYS` | 90 | Older events, issues with no recent occurrences, source maps and alert and email history are deleted hourly |
| `WATCHTOWER_MAX_BODY_BYTES` | 20 MiB | Request size on the wire |
| `WATCHTOWER_MAX_DECOMPRESSED_BYTES` | 50 MiB | Request size after decompression |
| `WATCHTOWER_MAX_EVENT_BYTES` | 1 MiB | Size of one event |
| `WATCHTOWER_MAX_QUEUE_DEPTH` | 100000 | Queued events before SDKs are told to back off. Approximate: each process recounts the queue every second |
| `WATCHTOWER_WORKER_BATCH_SIZE` | 100 | Events per worker transaction |
| `WATCHTOWER_SLACK_WEBHOOK_HOSTS` | `hooks.slack.com` | Hosts that Slack webhooks may point at |
| `WATCHTOWER_EMAIL_PROVIDER` | `smtp` when `WATCHTOWER_SMTP_HOST` is set, otherwise off | `smtp` or `cloudflare` |
| `WATCHTOWER_EMAIL_FROM` | required with email | Sender, such as `Watchtower <errors@example.com>` |
| `WATCHTOWER_SMTP_HOST` | none | Mail server for the `smtp` provider |
| `WATCHTOWER_SMTP_PORT` | 587, or 465 with `tls` | Mail server port |
| `WATCHTOWER_SMTP_TLS` | `starttls`, or `tls` on port 465 | `tls` (implicit), `starttls` (required, never downgraded) or `none` (a trusted relay; credentials are only ever sent unencrypted to localhost) |
| `WATCHTOWER_SMTP_USERNAME`, `WATCHTOWER_SMTP_PASSWORD` | none | SMTP credentials |
| `WATCHTOWER_CLOUDFLARE_ACCOUNT_ID`, `WATCHTOWER_CLOUDFLARE_API_TOKEN` | none | For the `cloudflare` email provider |
| `WATCHTOWER_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |

Invalid settings stop Watchtower at startup with a message naming the
variable, rather than failing later.

## Running in production

- **TLS and a stable address.** Put Watchtower behind a reverse proxy that
  terminates TLS, and set `WATCHTOWER_PUBLIC_URL` to the address SDKs and
  people use. SDKs need to reach the ingestion endpoints; the web UI and
  `/mcp` can stay on a private network.
- **Upgrades.** Run the installer again, or `docker compose pull && docker
  compose up -d` in the install directory. Database migrations run
  automatically at startup.
- **Backups.** Everything lives in Postgres; back it up with `pg_dump` like
  any other database, and keep `WATCHTOWER_SECRET_KEY` with it.
- **Health and metrics.** `GET /healthz` checks the process; `GET /readyz`
  also checks the database. `GET /metrics` serves Prometheus counters for
  accepted, rejected and shed events per adapter, worker outcomes, alerts,
  emails and retention.
- **Scaling.** Run more replicas, or split ingestion and background work with
  `WATCHTOWER_ROLES`. Workers coordinate through Postgres `SKIP LOCKED` and
  lock issues in a fixed order, so any number can run at once. Each process
  uses up to 25 database connections (`pool_max_conns` in the URL); keep
  replicas × pool size below Postgres's `max_connections`.
- **Throughput.** On one 6-core machine shared with Postgres, a single
  process accepted about 2,000 events a second, and one worker stored about
  2,000 a second; an error storm on one issue is the cheapest case, because
  a batch updates each issue once.
- **Back-pressure.** When the queue is full, SDKs get the vendor's own
  back-off response (for example 429 with `X-Sentry-Rate-Limits`) instead of
  events being dropped silently. The limit is approximate: replicas together
  can pass it by about a second of traffic. Events that fail processing five
  times move to `ingest_dead_letter`.

## How it works

```
Sentry SDKs      ─▶ sentry adapter              ─┐
AppSignal agents ─▶ appsignal adapter           ─┼─▶ scrub ─▶ queue ─▶ worker ─▶ issues & events
Browser SDK      ─▶ appsignal-frontend adapter  ─┘                  (symbolicate,     │
                                                                     group, alert)    ├─▶ web UI and API
                                                                                      ├─▶ Slack · Linear · email
                                                                                      └─▶ MCP for coding agents
```

1. An **adapter** authenticates the request, decodes the vendor's format and
   converts it to Watchtower's event model. Adapters only translate.
2. Events are **scrubbed** and written to a durable queue in Postgres. The SDK
   gets its success response only after that write.
3. A **worker** maps minified frames through source maps, groups the event
   into an issue, detects regressions and writes any alerts and emails, all in
   one transaction.
4. Separate loops deliver alerts and emails with retries, sync Linear, and
   apply retention.

## Data handling

- **Allowlist, not blocklist.** Request bodies, headers, cookies, breadcrumbs
  and process state sent by SDKs are dropped at the adapter. Of the free-form
  "extra" data, only the logger metadata an app explicitly sends and a failed
  job's worker, queue and attempt are kept. User data is reduced to an opaque
  ID.
- **Redaction.** Before storage, card numbers (Luhn-valid), bearer tokens and
  secret-looking `key=value` pairs are redacted, as are tags, context values
  and URL parameters with sensitive names.
- **No sensitive logging.** Request bodies and query strings are never logged.
- **Credentials at rest.** Keys and tokens are stored as SHA-256 digests;
  Slack and Linear credentials are encrypted with AES-256-GCM.
- **Errors only.** Transactions, sessions, profiles, replays, logs and metrics
  are acknowledged and counted but not stored. Watchtower is an error tracker,
  not an APM.

## CLI

```sh
watchtower setup-code                           # a new code for creating the first account in the browser
watchtower user create you@example.com -admin   # the password is prompted, never in argv
watchtower project create reader -name "Reader Web"
watchtower key create reader sentry
watchtower issues reader -status unresolved
watchtower issue resolve 12 -actor ada
watchtower adapters                             # adapters and tested SDK versions
watchtower healthcheck                          # exits 0 when the local server is healthy
```

## Contributing

Contributions are welcome. [CONTRIBUTING.md](CONTRIBUTING.md) covers setting
up a development environment, running the tests and preparing a pull request,
and [AGENTS.md](AGENTS.md) describes the architecture and the rules every
change must keep, for people and coding agents alike.

## License

Watchtower is licensed under the [Apache License, Version 2.0](LICENSE).
Contributions are accepted under the same licence.
