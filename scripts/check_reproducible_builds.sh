#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

version="${FORJA_BUILD_VERSION:-0.2.0-dev}"
commit="${FORJA_BUILD_COMMIT:-unknown}"
build_time="${FORJA_BUILD_TIME:-1970-01-01T00:00:00Z}"
ldflags="-s -w -buildid= \
  -X github.com/rvbernucci/forja-guide/internal/version.Version=$version \
  -X github.com/rvbernucci/forja-guide/internal/version.Commit=$commit \
  -X github.com/rvbernucci/forja-guide/internal/version.BuildTime=$build_time"

build_set() {
  local destination="$1"
  local architecture command
  mkdir -p "$destination"
  for architecture in amd64 arm64; do
    for command in forja forjad; do
      CGO_ENABLED=0 GOOS=linux GOARCH="$architecture" \
        go build \
          -trimpath \
          -buildvcs=false \
          -ldflags "$ldflags" \
          -o "$destination/${command}-linux-${architecture}" \
          "./cmd/${command}"
    done
  done
}

cd "$root"
build_set "$work/first"
build_set "$work/second"

for binary in "$work"/first/*; do
  name="$(basename "$binary")"
  cmp "$binary" "$work/second/$name"
done

echo "Reproducible release builds passed for linux/amd64 and linux/arm64."
