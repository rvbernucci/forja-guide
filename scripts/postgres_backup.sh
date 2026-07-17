#!/usr/bin/env bash
set -euo pipefail

if [[ "$#" -ne 1 ]]; then
  echo "usage: FORJA_DATABASE_URL=<database-url> postgres_backup.sh <backup-file>" >&2
  exit 2
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$root/scripts/postgres_connection.sh"
forja_prepare_postgres_connection
destination="$1"
destination_dir="$(dirname "$destination")"
temporary="${destination}.tmp.$$"

umask 077
mkdir -p "$destination_dir"
if [[ -e "$destination" ]]; then
  echo "backup destination already exists: $destination" >&2
  exit 1
fi
trap 'rm -f "$temporary"' EXIT

pg_dump \
  --format=custom \
  --compress=6 \
  --no-owner \
  --no-acl \
  --file="$temporary" \
  "$FORJA_PG_SAFE_URL"

pg_restore --list "$temporary" >/dev/null
if ! ln "$temporary" "$destination"; then
  echo "backup destination appeared during publication: $destination" >&2
  exit 1
fi
rm -f "$temporary"
trap - EXIT

echo "PostgreSQL backup validated: $destination"
