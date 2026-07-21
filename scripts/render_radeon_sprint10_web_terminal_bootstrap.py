#!/usr/bin/env python3
"""Render a public-safe Radeon web-terminal bootstrap script for Sprint 10."""

from __future__ import annotations

import argparse
import shlex
from collections.abc import Sequence
from pathlib import Path


DEFAULT_BRANCH = "feat/sprint-10-radeon-runtime-v2"
DEFAULT_REPO = "https://github.com/rvbernucci/forja-guide"
DEFAULT_REPO_DIR = "/workspace/forja-guide"
DEFAULT_BUNDLE_DIR = "/workspace/forja-alpha-sprint10-operator-bundle"
DEFAULT_SHEET_PATH = "/workspace/forja-radeon-web-terminal-evidence.md"


def q(value: str) -> str:
    return shlex.quote(value)


def render_script(
    *,
    repo_url: str,
    branch: str,
    repo_dir: str,
    bundle_dir: str,
    sheet_path: str,
) -> str:
    return f"""#!/usr/bin/env bash
set -euo pipefail

# Public-safe Sprint 10 bootstrap for the Radeon web terminal.
# This prepares source checkout, validates the repository, generates private
# operator templates, and renders the evidence command sheet. It does not
# download models, run benchmarks, collect evidence, or close Sprint 10.

repo_url={q(repo_url)}
branch={q(branch)}
repo_dir={q(repo_dir)}
bundle_dir={q(bundle_dir)}
sheet_path={q(sheet_path)}

mkdir -p "$repo_dir"
cd "$repo_dir"

if [ ! -d .git ]; then
  git clone "$repo_url" .
fi

git fetch origin
git checkout "$branch"
git pull --ff-only origin "$branch"

python3 scripts/validate_repository.py

python3 scripts/prepare_radeon_sprint10_operator_bundle.py \\
  --output-dir "$bundle_dir"

python3 scripts/verify_radeon_operator_bundle.py \\
  --bundle-dir "$bundle_dir" \\
  --allow-placeholders

python3 scripts/render_radeon_sprint10_web_terminal_sheet.py \\
  --repo-url "$repo_url" \\
  --branch "$branch" \\
  --repo-dir "$repo_dir" \\
  --bundle-dir "$bundle_dir" \\
  --output "$sheet_path"

cat <<EOF

Sprint 10 web-terminal bootstrap prepared.

Next private steps inside Radeon:
1. Open: $sheet_path
2. Copy $bundle_dir/radeon-model-candidates.template.json to /secure/forja/radeon-model-candidates.json
3. Fill local model IDs, quantization notes, and source snapshots under /secure/forja
4. Start loopback instruction and embedding endpoints
5. Run the strict preflights from the sheet before collecting evidence

Sprint 10 remains open until real Radeon public-summary evidence is ingested
and independently reviewed. Sprint 11 is still not authorized.
EOF
"""


def parse_args(argv: Sequence[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo-url", default=DEFAULT_REPO)
    parser.add_argument("--branch", default=DEFAULT_BRANCH)
    parser.add_argument("--repo-dir", default=DEFAULT_REPO_DIR)
    parser.add_argument("--bundle-dir", default=DEFAULT_BUNDLE_DIR)
    parser.add_argument("--sheet-path", default=DEFAULT_SHEET_PATH)
    parser.add_argument("--output", type=Path)
    return parser.parse_args(argv)


def main(argv: Sequence[str] | None = None) -> int:
    args = parse_args(argv)
    body = render_script(
        repo_url=args.repo_url,
        branch=args.branch,
        repo_dir=args.repo_dir,
        bundle_dir=args.bundle_dir,
        sheet_path=args.sheet_path,
    )
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(body, encoding="utf-8")
        args.output.chmod(0o700)
    else:
        print(body, end="")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
