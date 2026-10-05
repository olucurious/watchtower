#!/bin/bash
# Runs the harness for each version; each gets its own receiver and capture dir.
cd "$(dirname "$0")"
mkdir -p out
port=18800
for spec in "$@"; do
  v=${spec%%=*}; extra=""; [[ "$spec" == *=* ]] && extra=${spec#*=}
  port=$((port+1)); out=out/$v; rm -rf $out; mkdir -p $out
  python3 capture_receiver.py $port $out & rpid=$!
  sleep 1
  docker run --rm --network host -v $PWD:/w -w /w -e MIX_HOME=/w/.mixhome -e HEX_HOME=/w/.hexhome \
    -e MIX_INSTALL_DIR=/w/.mixinstall -e APPSIGNAL_VSN=$v -e EXTRA_DEPS=$extra \
    -e APPSIGNAL_PUSH_API_ENDPOINT=http://127.0.0.1:$port -e APPSIGNAL_LOGGING_ENDPOINT=http://127.0.0.1:$port \
    -e APPSIGNAL_DIAGNOSE_ENDPOINT=http://127.0.0.1:$port \
    elixir:1.17.3-otp-27 elixir harness.exs > $out/harness.log 2>&1
  echo "$v exit=$? requests=$(ls $out/*.json 2>/dev/null | wc -l) $(grep -c '^STEP' $out/harness.log) steps"
  kill $rpid; wait $rpid 2>/dev/null
done
