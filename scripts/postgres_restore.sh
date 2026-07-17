#!/usr/bin/env bash
set -euo pipefail

if [[ "$#" -ne 2 ]]; then
  echo "usage: postgres_restore.sh <database-url> <backup-file>" >&2
  exit 2
fi

database_url="$1"
backup_file="$2"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if [[ ! -r "$backup_file" ]]; then
  echo "backup is not readable: $backup_file" >&2
  exit 1
fi

pg_restore --list "$backup_file" >/dev/null

relation_count="$(
  psql "$database_url" \
    --no-psqlrc \
    --set=ON_ERROR_STOP=1 \
    --tuples-only \
    --no-align \
    --command="
      SELECT count(*)
      FROM pg_class AS c
      JOIN pg_namespace AS n ON n.oid=c.relnamespace
      WHERE n.nspname NOT IN ('pg_catalog', 'information_schema')
        AND n.nspname !~ '^pg_toast'
        AND c.relkind IN ('r', 'p', 'v', 'm', 'S', 'f');
    "
)"
if [[ "$relation_count" != "0" ]]; then
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
