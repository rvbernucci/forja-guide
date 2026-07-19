#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
base_url="${FORJA_TEST_DATABASE_URL:-${FORJA_DATABASE_URL:-}}"

if [[ -z "$base_url" ]]; then
  echo "FORJA_TEST_DATABASE_URL or FORJA_DATABASE_URL is required" >&2
  exit 2
fi
for command in createdb dropdb go python3 shasum; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "required command is unavailable: $command" >&2
    exit 2
  fi
done

suffix="$(printf '%s' "$$-$(date +%s%N)" | shasum -a 256 | cut -c1-12)"
database="forja_s08_index_${suffix}"

database_url() {
  python3 - "$base_url" "$database" <<'PY'
import sys
from urllib.parse import urlsplit, urlunsplit

parts = urlsplit(sys.argv[1])
if parts.scheme and not parts.netloc:
    suffix = ("?" + parts.query) if parts.query else ""
    print(f"{parts.scheme}:///{sys.argv[2]}{suffix}")
else:
    print(urlunsplit((parts.scheme, parts.netloc, "/" + sys.argv[2], parts.query, parts.fragment)))
PY
}

target_url="$(database_url)"

cleanup() {
  local status=$?
  dropdb --maintenance-db="$base_url" --if-exists "$database" >/dev/null 2>&1 || true
  exit "$status"
}
trap cleanup EXIT INT TERM

createdb --maintenance-db="$base_url" "$database"
FORJA_TEST_INDEX_DATABASE_URL="$target_url" \
  go test -count=1 -run '^TestIndexerCommandPublishesIncrementalSnapshot$' ./cmd/forja-index

FORJA_DATABASE_URL="$target_url" "$root/scripts/postgres_verify.sh"
printf '%s\n' \
  "SPRINT08_INDEX_COMMAND_DRILL=passed" \
  "SNAPSHOTS=4" \
  "REPOSITORY_AUTHORITIES=2" \
  "ADAPTER_REUSE=go,typescript" \
  "ADAPTER_REEXTRACT=python" \
  "ADAPTER_CONFIGURATION_REEXTRACT=go" \
  "CANONICAL_RECEIPTS=4"
