# Other AppSignal integrations

Captures from the unmodified integrations running the harness next to them:
synthetic library-catalogue code, the all-zero placeholder push key, and a
loopback receiver. `TestRubyIntegration`, `TestNodeIntegration`,
`TestPythonIntegration` and `TestFrontendSDK` decode them.

| Fixture | Integration | Layout |
|---|---|---|
| `ruby-5.0.1.deflate` | `appsignal` gem 5.0.1, agent 0.37.3 | transaction aggregates (field 6) |
| `nodejs-3.9.1.deflate` | `@appsignal/nodejs` 3.9.1, agent 0.37.1 | span samples (field 12) |
| `python-1.9.0.deflate` | `appsignal` (PyPI) 1.9.0, agent 0.37.3 | span samples, backtrace one line per field |
| `frontend-1.6.1-*.json` | `@appsignal/javascript` 1.6.1 in Chromium | JSON to `/collect` |

To re-capture a server integration, run a receiver (`../capture_receiver.py
PORT out`) and the harness in the language's image with
`APPSIGNAL_PUSH_API_ENDPOINT=http://127.0.0.1:PORT`, for example:

```sh
python3 ../capture_receiver.py 18901 out &
docker run --rm --network host -v "$PWD":/w -w /w \
  -e APPSIGNAL_PUSH_API_ENDPOINT=http://127.0.0.1:18901 ruby:3.3 \
  sh -c 'gem install appsignal && ruby harness.rb'
```

Node.js: `npm i @appsignal/nodejs @opentelemetry/api && node harness.js`.
Python: `pip install appsignal opentelemetry-api && python harness.py`.

The browser SDK ignores endpoint settings it doesn't recognise and falls back
to AppSignal's servers, so run it only as `frontend/` does: bundle `app.js`
(`esbuild app.js --bundle --format=iife --outfile=static/app.js`), serve it
with `serve.py`, and drive it with `drive.mjs`, which aborts every request
that is not to 127.0.0.1.
