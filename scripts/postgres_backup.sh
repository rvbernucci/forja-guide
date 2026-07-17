#!/usr/bin/env bash
set -euo pipefail

if [[ "$#" -ne 2 ]]; then
  echo "usage: postgres_backup.sh <database-url> <backup-file>" >&2
  exit 2
fi

database_url="$1"
destination="$2"
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
  "$database_url"

pg_restore --list "$temporary" >/dev/null
if ! ln "$temporary" "$destination"; then
  echo "backup destination appeared during publication: $destination" >&2
  exit 1
fi
rm -f "$temporary"
trap - EXIT

echo "PostgreSQL backup validated: $destination"
