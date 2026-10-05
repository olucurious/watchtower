#!/usr/bin/env bash
# Finds appsignal-elixir releases on Hex newer than the newest captured fixture,
# captures each with the synthetic harness (placeholder key, loopback
# receiver), and runs the version-matrix test against them.
#
# Usage: scripts/appsignal-check.sh [version ...]   (default: all new releases)
# Needs docker, python3 and go. New fixtures are left in place for review.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
td=$root/internal/adapter/appsignal/testdata

if [ $# -gt 0 ]; then
  versions=("$@")
else
  mapfile -t versions < <(curl -fsS https://hex.pm/api/packages/appsignal | python3 -c '
import json, sys, os
# Only releases newer than the newest captured fixture: older gaps are
# versions nobody upgraded to.
key = lambda v: tuple(map(int, v.split(".")))
have = [f[len("elixir-"):-len(".deflate")] for f in os.listdir(sys.argv[1])]
newest = max(map(key, have))
for r in json.load(sys.stdin)["releases"]:
    v = r["version"]
    if "-" not in v and key(v) > newest: print(v)  # skip pre-releases
' "$td/matrix")
fi

if [ ${#versions[@]} -eq 0 ]; then
  echo "No new appsignal releases since the last capture."
  exit 0
fi

mkdir -p "$td/out"
echo "Capturing: ${versions[*]}"
failed=()
(cd "$td" && ./capture.sh "${versions[@]}") | tee "$td/out/last-run.log" || true
for v in "${versions[@]}"; do
  if [ -s "$td/out/$v/req-1.bin" ]; then
    cp "$td/out/$v/req-1.bin" "$td/matrix/elixir-$v.deflate"
  else
    echo "!! $v: no batch captured (see $td/out/$v/harness.log)"; failed+=("$v")
  fi
done

echo "Running the version matrix test..."
if ! (cd "$root" && go test -count=1 -run TestVersionMatrix ./internal/adapter/appsignal/ -v | grep -E '^(---|\s+---|ok|FAIL|\s+matrix_test)'); then
  failed+=("matrix-test")
fi

if [ ${#failed[@]} -gt 0 ]; then
  echo "FAILED: ${failed[*]}. Do not upgrade services to these versions until the adapter is updated."
  exit 1
fi
echo "All new versions decode correctly: ${versions[*]}. Review and commit the new fixtures."
