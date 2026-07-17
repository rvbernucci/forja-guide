#!/usr/bin/env bash
set -euo pipefail

if [[ "$#" -ne 1 ]]; then
  echo "usage: FORJA_DATABASE_URL=<database-url> postgres_restore.sh <backup-file>" >&2
  exit 2
fi

backup_file="$1"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$root/scripts/postgres_connection.sh"
forja_prepare_postgres_connection
guard_dump="$(mktemp "${TMPDIR:-/tmp}/forja-restore-guard.XXXXXX")"

cleanup() {
  rm -f "$guard_dump"
}
trap cleanup EXIT

if [[ ! -r "$backup_file" ]]; then
  echo "backup is not readable: $backup_file" >&2
  exit 1
fi

pg_restore --list "$backup_file" >/dev/null

pg_dump \
  --format=custom \
  --schema-only \
  --no-owner \
  --no-acl \
  --file="$guard_dump" \
  "$FORJA_PG_SAFE_URL"
object_count="$(
  pg_restore --list "$guard_dump" |
    awk 'substr($0, 1, 1) != ";" && NF { count++ } END { print count + 0 }'
)"
if [[ "$object_count" != "0" ]]; then
  echo "refusing destructive restore: target database is not empty" >&2
  echo "restore into a dedicated empty staging database, validate it, then promote it" >&2
  exit 1
fi

pg_restore \
  --single-transaction \
  --no-owner \
  --no-acl \
  --dbname="$FORJA_PG_SAFE_URL" \
  "$backup_file"

FORJA_DATABASE_URL="$FORJA_PG_SAFE_URL" \
  "$root/scripts/postgres_verify.sh" >/dev/null

echo "PostgreSQL staging restore verified and ready for controlled promotion"
