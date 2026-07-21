#!/usr/bin/env python3
"""Render a Sprint 10 immutable review request template."""

from __future__ import annotations

import argparse
import hashlib
import json
import subprocess
import sys
from collections.abc import Sequence
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
DEFAULT_EVIDENCE_DIR = ROOT / "docs" / "evidence" / "sprint-10"
PUBLIC_EVIDENCE_FILES = (
    "plan.json",
    "test-report.json",
    "security-report.json",
    "rollback-report.json",
    "metrics-summary.json",
    "validation-report.json",
    "closure-candidate.json",
    "radeon-public-summary.json",
)


def sha256_file(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def evidence_label(path: Path, evidence_dir: Path) -> str:
    try:
        return path.relative_to(ROOT).as_posix()
    except ValueError:
        return (Path("docs/evidence/sprint-10") / path.relative_to(evidence_dir)).as_posix()


def git_value(args: Sequence[str], *, root: Path = ROOT) -> str:
    result = subprocess.run(
        ["git", *args],
        cwd=root,
        check=True,
        text=True,
        capture_output=True,
    )
    return result.stdout.strip()


def file_rows(evidence_dir: Path) -> tuple[list[str], list[str]]:
    rows: list[str] = []
    missing: list[str] = []
    for filename in PUBLIC_EVIDENCE_FILES:
        path = evidence_dir / filename
        relative = evidence_label(path, evidence_dir)
        if not path.is_file():
            missing.append(relative)
            rows.append(f"| `{relative}` | MISSING | MISSING |")
            continue
        rows.append(f"| `{relative}` | `{path.stat().st_size}` | `{sha256_file(path)}` |")
    return rows, missing


def load_json(path: Path) -> dict[str, object]:
    payload = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(payload, dict):
        raise ValueError(f"JSON document must be an object: {path}")
    return payload


def render_request(*, evidence_dir: Path, reviewer: str, subject_commit: str, subject_tree: str) -> str:
    rows, missing = file_rows(evidence_dir)
    candidate_path = evidence_dir / "closure-candidate.json"
    candidate = load_json(candidate_path) if candidate_path.is_file() else {}
    basis_commit = candidate.get("basis_commit")
    recorded_at = candidate.get("recorded_at")
    missing_text = "\n".join(f"- `{item}`" for item in missing) if missing else "- None"

    return f"""# Sprint 10 Immutable Review Request

Status: review template, not a verdict. This file is designed to be copied into
`docs/evidence/sprint-10/reviews/immutable-candidate-review.md` only after an
independent reviewer fills the findings and verdict sections. Rendering this
template does not close Sprint 10 and does not authorize Sprint 11.

## Reviewer

- Reviewer: `{reviewer}`
- Subject commit: `{subject_commit}`
- Subject tree: `{subject_tree}`
- Candidate basis commit from evidence: `{basis_commit}`
- Candidate recorded at: `{recorded_at}`

## Scope To Review

The reviewer must audit the exact public Sprint 10 evidence candidate:

- real Radeon runtime receipt summarized by `radeon-public-summary.json`;
- local model candidate benchmark summarized by `radeon-public-summary.json`;
- local embedding benchmark summarized by `radeon-public-summary.json`;
- destroy/recreate recovery summarized by `radeon-public-summary.json`;
- fail-closed `closure-candidate.json` that remains non-authoritative and keeps
  `next_sprint_authorized` unset;
- readiness verifier result that may become ready for independent review but
  must not itself create a close receipt.

## Required Public Evidence Files

| File | Size bytes | SHA256 |
| --- | ---: | --- |
{chr(10).join(rows)}

Missing files:

{missing_text}

## Mechanical Review Commands

Run from a clean checkout of the subject commit:

```bash
git rev-parse HEAD
git rev-parse HEAD^{{tree}}
git fsck --full --no-dangling
git diff --check
python3 scripts/verify_radeon_sprint10_public_summary.py \\
  --summary docs/evidence/sprint-10/radeon-public-summary.json
python3 scripts/verify_sprint10_review_readiness.py \\
  --evidence-dir docs/evidence/sprint-10
python3 scripts/promote_sprint10_close_receipt.py \\
  --review-artifact docs/evidence/sprint-10/reviews/immutable-candidate-review.md \\
  --reviewed-candidate-commit {subject_commit} \\
  --model {reviewer} \\
  --dry-run
python3 scripts/validate_repository.py
```

Optional when the review environment permits dependency installation:

```bash
make validate
```

## Findings

- TODO: State whether the public summary is complete, public-safe, and
  consistent with the private recovery claims it summarizes.
- TODO: State whether all real Radeon acceptance gates are represented in
  metrics, validation, and the closure candidate.
- TODO: State whether the candidate remains fail-closed and does not authorize
  Sprint 11 before promotion.
- TODO: State whether the promoter dry-run would create a review-bound v2
  close receipt.
- TODO: State any blocking findings.

## Blocking Findings

- TODO: `None` or list each blocker.

## Deferred Non-Blocking Risks

- TODO: List remaining risks that do not block Sprint 10 closure.

## Verdict

TODO: `PASS` or `FAIL`.

If the verdict is `PASS`, commit the completed review artifact under
`docs/evidence/sprint-10/reviews/immutable-candidate-review.md`, then run the
fail-closed promoter without `--dry-run`. If the verdict is `FAIL`, Sprint 10
remains open and Sprint 11 remains unauthorized.
"""


def parse_args(argv: Sequence[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--evidence-dir", type=Path, default=DEFAULT_EVIDENCE_DIR)
    parser.add_argument("--reviewer", default="independent-review")
    parser.add_argument("--subject-commit")
    parser.add_argument("--subject-tree")
    parser.add_argument("--output", type=Path)
    return parser.parse_args(argv)


def main(argv: Sequence[str] | None = None) -> int:
    args = parse_args(argv)
    try:
        subject_commit = args.subject_commit or git_value(["rev-parse", "HEAD"])
        subject_tree = args.subject_tree or git_value(["rev-parse", "HEAD^{tree}"])
        body = render_request(
            evidence_dir=args.evidence_dir,
            reviewer=args.reviewer,
            subject_commit=subject_commit,
            subject_tree=subject_tree,
        )
    except (OSError, ValueError, json.JSONDecodeError, subprocess.CalledProcessError) as exc:
        print(f"review request rejected: {exc}", file=sys.stderr)
        return 2
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(body, encoding="utf-8")
    else:
        print(body, end="")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
