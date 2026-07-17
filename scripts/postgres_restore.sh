#!/usr/bin/env bash
set -euo pipefail

if [[ "$#" -ne 2 ]]; then
  echo "usage: postgres_restore.sh <database-url> <backup-file>" >&2
  exit 2
fi

database_url="$1"
backup_file="$2"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
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
  "$database_url"
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
  --dbname="$database_url" \
  "$backup_file"

"$root/scripts/postgres_verify.sh" "$database_url" >/dev/null

echo "PostgreSQL staging restore verified and ready for controlled promotion"
