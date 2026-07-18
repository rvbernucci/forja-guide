#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d)"
source_tree="$work/sprint04"
sprint04_commit="${FORJA_SPRINT04_COMMIT:-d6eda8dc12a5ecf5a6e2783f7302a6d38a9b9ed4}"
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

git -C "$root" cat-file -e "${sprint04_commit}^{commit}"
git -C "$root" worktree add --quiet --detach "$source_tree" "$sprint04_commit"
worktree_added=true

(
  cd "$source_tree"
  go build -trimpath -buildvcs=false -o "$work/forjad-sprint04" ./cmd/forjad
)

cd "$root"
FORJA_TEST_SPRINT04_BINARY="$work/forjad-sprint04" \
  go test -count=1 ./internal/postgres \
    -run '^TestSprint05RollbackRunsSprint04BinaryAgainstDowngradedSchema$' -v

echo "Sprint 05 rollback rehearsal passed against Sprint 04 commit $sprint04_commit."
