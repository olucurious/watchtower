# Sentry SDK fixtures

Real envelopes captured on 2 October 2026 from unmodified official SDKs,
run by a small example library-catalogue app and sent to a local capture
receiver. Each `.request.json` records the path,
query parameter names and non-credential headers of the original request.

| Fixture | SDK | Notable wire behaviour |
|---|---|---|
| `python-2.71.0-*` | `sentry-sdk` 2.71.0 | gzip body, `X-Sentry-Auth` header, length-prefixed items, RFC 3339 timestamps |
| `node-11.2.0-*` | `@sentry/node` 11.2.0 | chunked transfer, key in query string, items without `length`, epoch timestamps, separate `session` envelope, synthetic stack on `captureMessage` |
| `elixir-11.0.4-*` | `sentry` (Hex) 11.0.4 | `X-Sentry-Auth`, bare `exception` list, `message` as an object, zoneless timestamps |

Bodies are stored decompressed. Machine-specific paths were replaced
(`/app`, `/home/app`) and item lengths recomputed. The DSN key in them,
`0123456789abcdef0123456789abcdef`, is a placeholder that was never valid
anywhere.

To add an SDK version: run the SDK against a local receiver with a
placeholder DSN, never real credentials or production data, then add the
envelope here, a case to `sentry_test.go`, and the version to
`Adapter.Describe`.
