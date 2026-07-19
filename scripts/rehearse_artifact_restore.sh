#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
base_url="${FORJA_TEST_DATABASE_URL:-${FORJA_DATABASE_URL:-}}"
minio_bin="${FORJA_MINIO_BIN:-minio}"

if [[ -z "$base_url" ]]; then
  echo "FORJA_TEST_DATABASE_URL or FORJA_DATABASE_URL is required" >&2
  exit 2
fi

for command in createdb dropdb pg_dump pg_restore psql go curl python3 shasum; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "required command is unavailable: $command" >&2
    exit 2
  fi
done
if ! command -v "$minio_bin" >/dev/null 2>&1; then
  echo "configured MinIO binary is unavailable: $minio_bin" >&2
  exit 2
fi

work="$(mktemp -d "${TMPDIR:-/tmp}/forja-artifact-restore.XXXXXX")"
suffix="$(printf '%s' "$$-$(date +%s%N)" | shasum -a 256 | cut -c1-12)"
source_db="forja_s07_source_${suffix}"
restore_db="forja_s07_restore_${suffix}"
source_objects="$work/source-objects"
restore_objects="$work/restore-objects"
backup="$work/postgres.dump"
minio_log="$work/minio.log"
minio_pid=""

read -r api_port console_port < <(python3 - <<'PY'
import socket

sockets = []
ports = []
for _ in range(2):
    sock = socket.socket()
    sock.bind(("127.0.0.1", 0))
    sockets.append(sock)
    ports.append(sock.getsockname()[1])
print(*ports)
for sock in sockets:
    sock.close()
PY
)

database_url() {
  python3 - "$base_url" "$1" <<'PY'
import sys
from urllib.parse import urlsplit, urlunsplit

parts = urlsplit(sys.argv[1])
print(urlunsplit((parts.scheme, parts.netloc, "/" + sys.argv[2], parts.query, parts.fragment)))
PY
}

source_url="$(database_url "$source_db")"
restore_url="$(database_url "$restore_db")"
endpoint="http://127.0.0.1:$api_port"
bucket="forja-sprint07-$suffix"

stop_minio() {
  if [[ -n "$minio_pid" ]] && kill -0 "$minio_pid" 2>/dev/null; then
    kill "$minio_pid"
    wait "$minio_pid" 2>/dev/null || true
  fi
  minio_pid=""
}

cleanup() {
  local status=$?
  stop_minio
  dropdb --maintenance-db="$base_url" --if-exists "$source_db" >/dev/null 2>&1 || true
  dropdb --maintenance-db="$base_url" --if-exists "$restore_db" >/dev/null 2>&1 || true
  if [[ "${FORJA_KEEP_RESTORE_DRILL:-0}" == "1" ]]; then
    echo "restore drill workspace retained: $work" >&2
  else
    rm -rf "$work"
  fi
  exit "$status"
}
trap cleanup EXIT INT TERM

start_minio() {
  local data_dir=$1
  mkdir -p "$data_dir"
  MINIO_ROOT_USER=forja-restore-drill \
  MINIO_ROOT_PASSWORD=forja-restore-drill-secret \
    "$minio_bin" server "$data_dir" \
      --address "127.0.0.1:$api_port" \
      --console-address "127.0.0.1:$console_port" \
      >"$minio_log" 2>&1 &
  minio_pid=$!
  for _ in $(seq 1 100); do
    if curl --fail --silent "$endpoint/minio/health/ready" >/dev/null; then
      return 0
    fi
    if ! kill -0 "$minio_pid" 2>/dev/null; then
      echo "MinIO exited before becoming ready" >&2
      sed -n '1,160p' "$minio_log" >&2
      return 1
    fi
    sleep 0.1
  done
  echo "MinIO did not become ready" >&2
  return 1
}

run_drill_phase() {
  local database=$1
  local phase=$2
  AWS_ACCESS_KEY_ID=forja-restore-drill \
  AWS_SECRET_ACCESS_KEY=forja-restore-drill-secret \
  AWS_EC2_METADATA_DISABLED=true \
  FORJA_TEST_DATABASE_URL="$database" \
  FORJA_TEST_S3_ENDPOINT="$endpoint" \
  FORJA_TEST_S3_BUCKET="$bucket" \
  FORJA_TEST_S3_DRILL_PHASE="$phase" \
    go test -count=1 -run '^TestRealS3ArtifactBundleRestoreDrill$' ./internal/postgres
}

object_inventory_sha256() {
  local data_dir=$1
  (
    cd "$data_dir"
    find . -type f -print0 |
      sort -z |
      xargs -0 shasum -a 256 |
      shasum -a 256 |
      awk '{print $1}'
  )
}

createdb --maintenance-db="$base_url" "$source_db"
createdb --maintenance-db="$base_url" "$restore_db"

start_minio "$source_objects"
run_drill_phase "$source_url" seed
FORJA_DATABASE_URL="$source_url" "$root/scripts/postgres_backup.sh" "$backup"
stop_minio

mkdir -p "$restore_objects"
cp -a "$source_objects/." "$restore_objects/"
source_inventory_sha256="$(object_inventory_sha256 "$source_objects")"
restore_inventory_sha256="$(object_inventory_sha256 "$restore_objects")"
if [[ "$source_inventory_sha256" != "$restore_inventory_sha256" ]]; then
  echo "restored object snapshot differs from the stopped source snapshot" >&2
  exit 1
fi
FORJA_DATABASE_URL="$restore_url" "$root/scripts/postgres_restore.sh" "$backup"

: >"$minio_log"
start_minio "$restore_objects"
run_drill_phase "$restore_url" verify
FORJA_DATABASE_URL="$restore_url" "$root/scripts/postgres_verify.sh"

backup_sha256="$(shasum -a 256 "$backup" | awk '{print $1}')"
printf '%s\n' \
  "ARTIFACT_RESTORE_DRILL=passed" \
  "POSTGRES_BACKUP_SHA256=$backup_sha256" \
  "SOURCE_OBJECT_INVENTORY_SHA256=$source_inventory_sha256" \
  "RESTORED_OBJECT_INVENTORY_SHA256=$restore_inventory_sha256" \
  "RESTORED_ARTIFACTS=3" \
  "OBJECT_VERSIONING=enabled"
