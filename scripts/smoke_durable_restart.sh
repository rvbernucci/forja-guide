#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d)"
daemon_pid=""
endpoint=""
export FORJA_HTTP_BEARER_TOKEN="forja-durable-smoke-bearer-token-001"
export FORJA_HTTP_ACTOR_TYPE="system"
export FORJA_HTTP_ACTOR_ID="durable-restart-smoke"

cleanup() {
  if [[ -n "$daemon_pid" ]] && kill -0 "$daemon_pid" 2>/dev/null; then
    kill -TERM "$daemon_pid"
    wait "$daemon_pid" || true
  fi
  rm -rf "$work"
}
trap cleanup EXIT

if [[ -z "${FORJA_TEST_DATABASE_URL:-}" ]]; then
  echo "FORJA_TEST_DATABASE_URL is required" >&2
  exit 2
fi

for command in curl psql python3; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "$command is required for the durable restart smoke test" >&2
    exit 1
  fi
done

start_daemon() {
  local generation="$1"
  : >"$work/daemon-${generation}.log"
  : >"$work/daemon-${generation}.err"
  FORJA_DATABASE_URL="$FORJA_TEST_DATABASE_URL" \
    "$work/forjad" \
      --listen 127.0.0.1:0 \
      --environment integration \
      --shutdown-timeout 2s \
      >"$work/daemon-${generation}.log" \
      2>"$work/daemon-${generation}.err" &
  daemon_pid="$!"
  endpoint=""

  for _ in $(seq 1 200); do
    endpoint="$(
      sed -n 's/.*"listen":"\([^"]*\)".*/http:\/\/\1/p' \
        "$work/daemon-${generation}.log" |
        head -n 1
    )"
    if [[ -n "$endpoint" ]] &&
      curl \
        --connect-timeout 0.5 \
        --max-time 2 \
        --fail \
        --silent \
        "$endpoint/readyz" >/dev/null; then
      return 0
    fi
    if ! kill -0 "$daemon_pid" 2>/dev/null; then
      echo "forjad generation $generation exited before readiness" >&2
      cat "$work/daemon-${generation}.err" >&2
      return 1
    fi
    sleep 0.05
  done
  echo "forjad generation $generation did not become ready" >&2
  return 1
}

stop_daemon() {
  kill -TERM "$daemon_pid"
  wait "$daemon_pid"
  daemon_pid=""
}

cd "$root"
go build -trimpath -buildvcs=false -o "$work/forjad" ./cmd/forjad
go build -trimpath -buildvcs=false -o "$work/forja" ./cmd/forja

psql "$FORJA_TEST_DATABASE_URL" \
  --no-psqlrc \
  --set=ON_ERROR_STOP=1 \
  --command="DROP SCHEMA IF EXISTS forja CASCADE" >/dev/null

start_daemon first
"$work/forja" run create \
  --endpoint "$endpoint" \
  --objective "Durable restart acceptance run" >"$work/create.json"
run_id="$(
  python3 -c \
    'import json,sys; print(json.load(open(sys.argv[1], encoding="utf-8"))["run_id"])' \
    "$work/create.json"
)"
stop_daemon

start_daemon second
"$work/forja" run get \
  --endpoint "$endpoint" \
  --id "$run_id" >"$work/get.json"
stop_daemon

python3 -c '
import json
import sys

created = json.load(open(sys.argv[1], encoding="utf-8"))
recovered = json.load(open(sys.argv[2], encoding="utf-8"))
if created != recovered:
    raise SystemExit("recovered run differs after daemon restart")
' "$work/create.json" "$work/get.json"

echo "Durable restart smoke test passed: process restart preserved canonical state."
