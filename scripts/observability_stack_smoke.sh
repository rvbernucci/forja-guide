#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
compose="$root/deploy/observability/compose.linux.yaml"
work="$(mktemp -d)"
daemon_pid=""
cd "$root"

cleanup() {
  if [[ -n "$daemon_pid" ]]; then
    kill -TERM "$daemon_pid" 2>/dev/null || true
    wait "$daemon_pid" 2>/dev/null || true
  fi
  FORJA_LOG_DIR="$work/logs" docker compose -f "$compose" down --volumes --remove-orphans >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

wait_for_url() {
  local url="$1"
  local attempts="${2:-60}"
  for ((attempt = 1; attempt <= attempts; attempt++)); do
    if curl --fail --silent --show-error "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  echo "timed out waiting for $url" >&2
  return 1
}

wait_for_json_count() {
  local label="$1"
  local url="$2"
  local expression="$3"
  for ((attempt = 1; attempt <= 45; attempt++)); do
    if payload="$(curl --fail --silent --show-error "$url" 2>/dev/null)" &&
      PAYLOAD="$payload" python3 -c "$expression"; then
      return 0
    fi
    sleep 2
  done
  echo "$label did not observe the daemon" >&2
  return 1
}

mkdir -p "$work/logs"
go build -trimpath -o "$work/forjad" ./cmd/forjad
FORJA_LOG_DIR="$work/logs" docker compose -f "$compose" config --quiet
FORJA_LOG_DIR="$work/logs" docker compose -f "$compose" up -d

wait_for_url http://127.0.0.1:9090/-/ready
wait_for_url http://127.0.0.1:3100/ready
wait_for_url http://127.0.0.1:3200/ready
wait_for_url http://127.0.0.1:3000/api/health

export FORJA_HTTP_BEARER_TOKEN="observability-smoke-token"
export FORJA_HTTP_ACTOR_TYPE="system"
export FORJA_HTTP_ACTOR_ID="observability-smoke"
export FORJA_OTEL_ENABLED="true"
export FORJA_TRACE_SAMPLE_RATIO="1"
export OTEL_EXPORTER_OTLP_ENDPOINT="http://127.0.0.1:4318"
export OTEL_EXPORTER_OTLP_PROTOCOL="http/protobuf"
"$work/forjad" --listen 127.0.0.1:8080 >"$work/logs/forjad.jsonl" 2>&1 &
daemon_pid="$!"

wait_for_url http://127.0.0.1:8080/healthz
curl --fail --silent --show-error http://127.0.0.1:8080/readyz >/dev/null
curl --fail --silent --show-error http://127.0.0.1:8080/metrics |
  grep -q '^forja_operations_total'

prometheus_url='http://127.0.0.1:9090/api/v1/query?query=up%7Bjob%3D%22forjad%22%7D'
wait_for_json_count \
  "Prometheus" \
  "$prometheus_url" \
  'import json,os,sys; data=json.loads(os.environ["PAYLOAD"]); sys.exit(0 if data.get("data",{}).get("result") and data["data"]["result"][0]["value"][1]=="1" else 1)'

tempo_url='http://127.0.0.1:3200/api/search?tags=service.name%3Dforjad'
wait_for_json_count \
  "Tempo" \
  "$tempo_url" \
  'import json,os,sys; data=json.loads(os.environ["PAYLOAD"]); sys.exit(0 if data.get("traces") else 1)'

loki_url='http://127.0.0.1:3100/loki/api/v1/query_range?query=%7Bservice_name%3D%22forja%22%7D&limit=10'
wait_for_json_count \
  "Loki" \
  "$loki_url" \
  'import json,os,sys; data=json.loads(os.environ["PAYLOAD"]); sys.exit(0 if data.get("data",{}).get("result") else 1)'

echo "observability stack smoke passed"
