#!/usr/bin/env bash

forja_prepare_postgres_connection() {
  if [[ -z "${FORJA_DATABASE_URL:-}" ]]; then
    echo "FORJA_DATABASE_URL is required" >&2
    return 2
  fi
  if ! command -v python3 >/dev/null 2>&1; then
    echo "python3 is required to sanitize the PostgreSQL connection URL" >&2
    return 1
  fi

  local helper_dir safe_url embedded_password
  helper_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  exec 9< <(python3 "$helper_dir/postgres_connection.py")
  if ! IFS= read -r -d '' safe_url <&9; then
    exec 9<&-
    return 1
  fi
  if ! IFS= read -r -d '' embedded_password <&9; then
    exec 9<&-
    return 1
  fi
  exec 9<&-

  FORJA_PG_SAFE_URL="$safe_url"
  export FORJA_PG_SAFE_URL
  if [[ -n "$embedded_password" ]]; then
    PGPASSWORD="$embedded_password"
    export PGPASSWORD
  fi
  unset FORJA_DATABASE_URL
}
