# AppSignal adapters

Two adapters cover AppSignal:

- `appsignal`: the push API (`/1/auth`, `/2/collect`) used by every server
  integration through AppSignal's agent: **Elixir, Ruby, Node.js and
  Python**. Set `APPSIGNAL_PUSH_API_ENDPOINT` and `APPSIGNAL_PUSH_API_KEY`.
- `appsignal-frontend`: the browser SDK `@appsignal/javascript`, which posts
  JSON to `/collect`. It uses its own keys, because front-end keys are public.

| Integration | Tested | Notes |
|---|---|---|
| Elixir (`appsignal`) | 2.9.2–2.18.0, 17 releases | See below |
| Ruby (`appsignal` gem) | 5.0.1 | Exception causes become the cause chain |
| Node.js (`@appsignal/nodejs`) | 3.9.1 | W3C trace IDs, tags from span attributes |
| Python (`appsignal`) | 1.9.0 | Tags from span attributes; `module.Class` names split |
| Browser (`@appsignal/javascript`) | 1.6.1 | V8 and Firefox/Safari stacks; source maps apply |

## Browser SDK

```js
import Appsignal from "@appsignal/javascript"
const appsignal = new Appsignal({
  key: "<key from watchtower key create PROJECT appsignal-frontend>",
  uri: "https://watchtower.example.com/collect", // the full collect URL
})
```

The option is `uri`. The SDK silently ignores options it does not know
(such as `endpoint`) and keeps sending to AppSignal. Browser frames are
JavaScript, so uploaded source maps apply: upload with the release set to the
SDK's `revision`.

## Elixir

AppSignal does not publish its agent protocol. This adapter decodes the
layout observed in agent batches, and is verified against 17 appsignal-elixir
releases: 2.9.2, 2.10.1, 2.12.3, 2.13.2, 2.15.4, 2.15.7, 2.15.10, 2.16.0 and
2.17.0–2.18.0 (agents 0.34.1–0.37.4). The field layout is identical across
all of them.

## Switching a service

Set two environment variables before the application starts, and change nothing else:

```sh
APPSIGNAL_PUSH_API_ENDPOINT=https://watchtower.example.com
APPSIGNAL_PUSH_API_KEY=<key from `watchtower key create <project> appsignal`>
```

Every appsignal-elixir release from 2.9.2 to 2.17.4 reads the endpoint from
this variable, and it takes precedence over application config.

The endpoint only redirects the push API. Logging and check-ins
(`logging_endpoint`) and diagnose uploads (`diagnose_endpoint`) have
separate settings and are not supported.

## Protocol

| Request | Purpose | Watchtower response |
|---|---|---|
| `GET/POST /1/auth?api_key=…&name=…&environment=…` | SDK push-key validation | 200 valid, 401 unknown |
| `POST /2/collect?api_key=…&app_name=…&environment=…&hostname=…` | Agent batch: `application/x-protobuf`, `Content-Encoding: deflate` (zlib) | 200 `{}` once error samples are queued |

The push key appears in the query string **and** inside the body (field 2).
Watchtower never logs query strings or bodies.

## Payload layouts

Span-based integrations (Elixir, Node.js, Python) send error samples in
field 12, shown below. Ruby sends transaction aggregates in field 6
(`{1 action, 2 namespace, 7 repeated sample}`), where each sample holds
`{1 id, 2 time, 5 revision, 7 error {1 name, 2 message, 4 backtrace JSON},
12 repeated sample data}`; its sample data adds `error_causes`.

Backtraces differ by language: JSON arrays of Elixir, Ruby or V8 lines,
innermost first (field 4), or Python traceback lines, one per field and
oldest first (field 5). Node.js and Python send tags as span attributes;
Elixir and Ruby send a `tags` JSON blob.

### Span samples (agent 0.36.12)

```
CollectPayload
  1  hostname            string
  2  push API key        string   (ignored, never logged)
  3  environment         string
  4  app name            string
  6  metrics             message  (ignored)
  7  agent version       string   "0.36.12"
  8  language/SDK        string   "elixir-2.17.4"
  12 sample              repeated message
       1 sample id       string
       5 span            repeated message
            1  trace id          string (AppSignal format, not W3C hex)
            2  span id           string
            5  namespace         string   "http_request"
            6  start             {1 seconds, 2 nanos}
            7  end               {1 seconds, 2 nanos}
            8  attribute         repeated {1 key, 2 {1 string value}}; "revision" -> release
            10 error             repeated message
                 2 name          string   "ArgumentError"
                 3 message       string   "** (ArgumentError) …"
                 4 backtrace     string   JSON array of Elixir stack lines, innermost first
```

The agent sent the same error record twice within one span, so the adapter
keeps one record per distinct error. Event IDs are a hash of app,
environment, sample, span and error, so a retried batch deduplicates.

Unknown fields are skipped, so new fields in later agents are harmless. If a
later agent renumbers fields, decoding produces wrong or empty events rather
than an error. Capture a fixture for each new agent version before switching
services that run it (see below).

A batch that cannot be decoded gets a 400 and is counted in
`watchtower_adapter_requests_total{outcome="rejected_undecodable_payload"}`.
The agent keeps the batch and retries it, so once the decoder is fixed the
retried batches are accepted rather than lost.

## What is kept

| Sample data | Kept as |
|---|---|
| Span name (field 4), e.g. `POST /books/:id/reserve` | transaction |
| `tags` | tags |
| `environment`: method, `request_path`, status | request line and `http.status_code` tag |
| attribute `revision` | release |
| namespace (`http_request`, `background_job`, ...) | `namespace` tag |
| `params`, `session_data`, `custom_data` | never decoded or stored |

Performance samples without errors, metrics and host data are acknowledged
and counted, not stored.

## Behaviour that differs between AppSignal versions

- **GenServer crashes:** versions up to 2.15.10 report them under the
  `background_job` namespace. From 2.16.0, AppSignal's default configuration
  no longer reports them, so services on 2.16+ send nothing for those crashes
  to AppSignal or to Watchtower. Explicit `send_error`, instrumented spans and
  Plug request errors are reported by every version.
- **Agent download at compile time:** the package downloads its native agent
  from AppSignal's CDN while compiling. If the download fails, the app still
  compiles and runs but reports nothing, with only a warning in the build log.
  Versions 2.15.4–2.17.0 also fail to download with a newer Finch than their
  lockfiles pin. Keep lockfiles as they are and cache the agent in base images.

## New AppSignal releases

`scripts/appsignal-check.sh` asks Hex for appsignal-elixir releases newer
than the newest fixture, captures each with the harness (placeholder key,
loopback receiver) and runs the version-matrix test. Run it before upgrading
any service; a failure means the adapter needs updating first. The
`AppSignal versions` GitHub workflow runs it weekly and opens a pull request
with the new fixtures when they pass.

```sh
scripts/appsignal-check.sh            # every new release
scripts/appsignal-check.sh 2.19.0     # or specific versions
```

To capture by hand, `testdata/capture.sh 2.15.4=finch:0.19.0` pins
dependencies for versions whose installer needs an older Finch.

The harness exercises `send_error` with tags and sensitive params, an
instrumented span error, a Plug request crash via `appsignal_plug`, a GenServer
crash, `exit` and `throw`, and a burst of 25 distinct errors.

## Backtrace mapping

Lines such as `(library 0.4.1) lib/library/catalog.ex:42: Library.Catalog.fetch!/1`
become frames. A frame counts as in-app unless its application is OTP,
Elixir, or a common dependency (`frameworkApps` in `backtrace.go`), or its path
is inside `deps/`. Frames without an application prefix, like scripts, count
as in-app. Location-less Erlang frames such as `:erlang.map_get(...)` become
module and function. Lines that don't parse are kept verbatim as the frame's
function.
