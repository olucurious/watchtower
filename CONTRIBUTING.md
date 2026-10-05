# Contributing to Watchtower

Thanks for helping. Bug reports, SDK compatibility reports, documentation and
code are all welcome. This guide covers getting set up, making a change and
opening a pull request. [AGENTS.md](AGENTS.md) explains how Watchtower fits
together and the rules every change keeps; read it before changing code.

## Ways to contribute

- **Report a bug.** Open an issue with the Watchtower version or commit, the
  SDK and its version, what you expected and what happened. Never paste DSNs,
  keys, tokens or real event data; reduce it to a synthetic example.
- **Report an SDK that doesn't work,** or a new SDK version that does. These
  are especially valuable: Watchtower's promise is that the official SDKs work
  unchanged.
- **Improve the docs.** If something confused you, it will confuse the next
  person too.
- **Send code.** For anything larger than a small fix, open an issue first to
  agree on the approach; it saves rework on both sides.

## Reporting a security issue

Please don't open a public issue. Use GitHub's private vulnerability reporting
("Report a vulnerability" on the repository's Security tab), with steps to
reproduce. You'll get a reply as soon as possible, and credit in the fix
unless you'd rather not.

## Development setup

You need Go 1.26, Node.js 24, Docker (for Postgres) and
[golangci-lint v2](https://golangci-lint.run/welcome/install/) for `make lint`.

```sh
git clone https://github.com/olucurious/watchtower.git && cd watchtower

make test-db                 # Postgres 18 on 127.0.0.1:55432 (user and password: watchtower)
make build                   # builds the web UI and bin/watchtower with it embedded

export WATCHTOWER_DATABASE_URL='postgres://watchtower:watchtower@127.0.0.1:55432/postgres?sslmode=disable'
./bin/watchtower user create you@example.com -admin
./bin/watchtower serve       # http://localhost:8080
```

For UI work, run Vite's dev server alongside it. It reloads as you edit and
proxies `/api` to the running server:

```sh
cd web && npm run dev        # http://localhost:5173
```

To test the production image, build it from your checkout with
`docker compose -f compose.yaml -f compose.build.yaml up -d --build`.

Point an SDK at it with a key from a project's page to send real errors, or
run the tests, which exercise every supported SDK's captured payloads.
`make test-db-stop` removes the database when you're done.

## Making a change

1. **Keep the invariants** in [AGENTS.md](AGENTS.md). If a change needs to
   break one, raise it in an issue first.
2. **Add tests** for the behaviour you changed, including how it fails.
   Postgres-backed tests use `storetest.New(t)`; external services are faked
   with `httptest`, never called for real.
3. **Run the checks:**

   ```sh
   make lint test                                # golangci-lint, UI type-check and lint, unit tests
   make test-db test-integration test-db-stop    # the full suite, with Postgres
   ```

4. **Look at UI changes in a browser,** at desktop and phone widths, in light
   and dark themes. Include before and after screenshots in the pull request,
   taken with synthetic data.
5. **Update the docs** that your change affects: the README for anything a
   user sees or configures, AGENTS.md for anything about how the code fits
   together.

### Adding or updating SDK support

Capture payloads from the unmodified SDK against a loopback receiver, with
placeholder credentials and a synthetic app, and commit them as test fixtures
with a note on how they were captured. [AGENTS.md](AGENTS.md#common-changes)
has the full checklist. Never commit payloads from a real application.

### Database changes

Add a new numbered file in `internal/store/migrations`; never edit one that
has been released. Migrations run at startup on live databases, so prefer
additive changes, and make sure they're safe to run while old versions are
still serving.

## Commit messages

Write a short summary in the imperative, under about 72 characters, then a
body explaining why the change is needed and anything a reviewer should know.

```
Resolve issues when their Linear issue is completed

Watchtower polls linked issues every five minutes instead of receiving
webhooks, so it works on a private network.
```

## Pull requests

Before asking for a review, check that:

- [ ] the checks above pass;
- [ ] new behaviour has tests, including failure paths;
- [ ] no secrets, real event data or production identifiers are in code,
      fixtures, screenshots or the description;
- [ ] user-facing changes are documented, with screenshots for UI changes;
- [ ] the description says what changed, why, and how you tested it.

CI runs the same lint and tests, a dependency vulnerability scan and a
Docker build on every pull request; it must pass before review.

Small, focused pull requests are reviewed fastest. Reviews aim to be prompt
and specific; if something is unclear, ask.

## Conduct

Be kind and assume good intent. Critique code, not people. Maintainers may
remove comments or contributions that are hostile or harassing.

## License

Watchtower is licensed under the [Apache License, Version 2.0](LICENSE). By
contributing, you agree that your contributions are licensed under the same
terms (section 5 of the licence).
