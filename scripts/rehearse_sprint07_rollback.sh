#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d)"
source_tree="$work/sprint06"
sprint06_commit="${FORJA_SPRINT06_COMMIT:-115b6c117c5ffcc42b7a86f786aa89fb41aac554}"
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

git -C "$root" cat-file -e "${sprint06_commit}^{commit}"
git -C "$root" worktree add --quiet --detach "$source_tree" "$sprint06_commit"
worktree_added=true

(
  cd "$source_tree"
  go build -trimpath -buildvcs=false -o "$work/forjad-sprint06" ./cmd/forjad
)

cd "$root"
FORJA_TEST_SPRINT06_BINARY="$work/forjad-sprint06" \
  go test -count=1 ./internal/postgres \
    -run '^TestSprint07RollbackRunsSprint06BinaryAgainstDowngradedSchema$' -v

echo "Sprint 07 rollback rehearsal passed against Sprint 06 commit $sprint06_commit."
