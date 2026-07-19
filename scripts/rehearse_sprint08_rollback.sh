#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d)"
source_tree="$work/sprint07"
sprint07_commit="${FORJA_SPRINT07_COMMIT:-e763f85bef2de14d92d924300839d88b00ff00d0}"
worktree_added=false

cleanup() {
  if [[ "$worktree_added" == true ]]; then
    git -C "$root" worktree remove "$source_tree" >/dev/null 2>&1 || true
  fi
  rm -rf "$work"
}
trap cleanup EXIT

if [[ -z "${FORJA_TEST_DATABASE_URL:-}" ]]; then
  echo "FORJA_TEST_DATABASE_URL is required" >&2
  exit 2
fi

git -C "$root" cat-file -e "${sprint07_commit}^{commit}"
git -C "$root" worktree add --quiet --detach "$source_tree" "$sprint07_commit"
worktree_added=true

(
  cd "$source_tree"
  go build -trimpath -buildvcs=false -o "$work/forjad-sprint07" ./cmd/forjad
)

cd "$root"
FORJA_TEST_SPRINT07_BINARY="$work/forjad-sprint07" \
  go test -count=1 ./internal/postgres \
    -run '^TestSprint08RollbackRunsSprint07BinaryAgainstDowngradedSchema$' -v

echo "Sprint 08 rollback rehearsal passed against Sprint 07 commit $sprint07_commit."
