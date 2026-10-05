# Source map fixtures

- `sentry-cli-3.8.0-bundle.zip`: the artifact bundle sentry-cli 3.8.0 uploaded
  for a small esbuild-minified app (`src/main.js` and `src/catalog.js`) after
  `sentry-cli sourcemaps inject`.
- `node-event.json`: the event @sentry/node 11.2.0 sent when that minified
  build threw, with its debug image pointing at the same debug ID. Local
  paths are replaced with `/app`.
