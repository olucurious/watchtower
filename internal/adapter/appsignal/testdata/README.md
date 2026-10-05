# AppSignal agent fixtures

All bodies are exact `/2/collect` payloads from unmodified appsignal-elixir
packages and their native agents, sent to a loopback receiver with the
all-zero placeholder push key. Only the synthetic harness app ran; no real
application, credentials or production data were involved.

- `matrix/elixir-<version>.deflate`: one batch per appsignal-elixir release,
  produced by `harness.exs`. See `docs/adapters/appsignal.md` for the
  scenarios and how new releases are added (`scripts/appsignal-check.sh`).
- `sdks/`: the Ruby, Node.js, Python and browser integrations; see
  `sdks/README.md`.

`capture.sh`, `harness.exs` and `capture_receiver.py` reproduce them. Run
captures only with synthetic apps and placeholder keys.
