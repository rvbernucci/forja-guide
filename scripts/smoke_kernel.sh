#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d)"
daemon_pid=""

cleanup() {
  if [[ -n "$daemon_pid" ]] && kill -0 "$daemon_pid" 2>/dev/null; then
    kill -TERM "$daemon_pid"
    wait "$daemon_pid" || true
  fi
  rm -rf "$work"
}
trap cleanup EXIT

cd "$root"
go build -trimpath -buildvcs=false -o "$work/forjad" ./cmd/forjad
go build -trimpath -buildvcs=false -o "$work/forja" ./cmd/forja

"$work/forjad" \
  --listen 127.0.0.1:0 \
  --environment smoke \
  --shutdown-timeout 2s >"$work/daemon.log" 2>"$work/daemon.err" &
daemon_pid="$!"

endpoint=""
for _ in $(seq 1 100); do
  endpoint="$(
    sed -n 's/.*"listen":"\([^"]*\)".*/http:\/\/\1/p' "$work/daemon.log" |
      head -n 1
  )"
  if [[ -n "$endpoint" ]] && curl --fail --silent "$endpoint/readyz" >/dev/null; then
    break
  fi
  if ! kill -0 "$daemon_pid" 2>/dev/null; then
    echo "forjad exited before becoming ready" >&2
    cat "$work/daemon.err" >&2
    exit 1
  fi
  sleep 0.05
done

if [[ -z "$endpoint" ]]; then
  echo "forjad did not publish its listen address" >&2
  exit 1
fi

"$work/forja" run create \
  --endpoint "$endpoint" \
  --objective "Sprint 01 smoke run" >"$work/create.json"

run_id="$(
  python3 -c \
    'import json,sys; print(json.load(open(sys.argv[1], encoding="utf-8"))["run_id"])' \
    "$work/create.json"
)"

"$work/forja" run get \
  --endpoint "$endpoint" \
  --id "$run_id" >"$work/get.json"

python3 -c '
import json
import sys

created = json.load(open(sys.argv[1], encoding="utf-8"))
inspected = json.load(open(sys.argv[2], encoding="utf-8"))
if created != inspected:
    raise SystemExit("create/get responses differ")
if created["state"] != "draft" or created["version"] != 1:
    raise SystemExit("unexpected initial run state")
' "$work/create.json" "$work/get.json"

kill -TERM "$daemon_pid"
wait "$daemon_pid"
daemon_pid=""

echo "Kernel smoke test passed: daemon readiness, CLI create/get, and graceful shutdown."
