#!/usr/bin/env python3
"""Render the final Sprint 10 promotion checklist without mutating evidence."""

from __future__ import annotations

import argparse
import subprocess
import sys
from collections.abc import Sequence
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
DEFAULT_REVIEW = "docs/evidence/sprint-10/reviews/immutable-candidate-review.md"
DEFAULT_CLOSE = "docs/evidence/sprint-10/close-receipt.json"
DEFAULT_CANDIDATE = "docs/evidence/sprint-10/closure-candidate.json"


def git_value(args: Sequence[str], *, root: Path = ROOT) -> str:
    result = subprocess.run(
        ["git", *args],
        cwd=root,
        check=True,
        text=True,
        capture_output=True,
    )
    return result.stdout.strip()


def render_checklist(
    *,
    reviewer: str,
    reviewed_candidate_commit: str,
    review_artifact: str,
    close_receipt: str,
    closure_candidate: str,
) -> str:
    return f"""# Sprint 10 Final Promotion Checklist

Status: operator checklist, not a closure receipt. Run this only after the
independent reviewer has completed `{review_artifact}` with a `PASS` verdict.
This checklist does not execute commands and does not authorize Sprint 11.

## Preconditions

- `python3 scripts/verify_sprint10_review_readiness.py --evidence-dir docs/evidence/sprint-10` passes.
- `{review_artifact}` exists, is committed or staged for the final promotion,
  and records the exact candidate commit reviewed.
- The reviewed candidate commit is `{reviewed_candidate_commit}`.
- The review verdict is `PASS` and no blocking findings remain.
- `{closure_candidate}` still exists before promotion.
- `{close_receipt}` does not exist before promotion.

## Dry Run

```bash
python3 scripts/promote_sprint10_close_receipt.py \\
  --review-artifact {review_artifact} \\
  --reviewed-candidate-commit {reviewed_candidate_commit} \\
  --model {reviewer} \\
  --dry-run
```

## Write The Close Receipt

```bash
python3 scripts/promote_sprint10_close_receipt.py \\
  --review-artifact {review_artifact} \\
  --reviewed-candidate-commit {reviewed_candidate_commit} \\
  --model {reviewer} \\
  --output {close_receipt}
```

## Replace Candidate With Receipt

```bash
git rm {closure_candidate}
git add {review_artifact} {close_receipt}
python3 scripts/validate_repository.py
make validate
git status --short
```

The final promotion commit may include the completed review artifact, the new
close receipt, removal of the candidate, and allowed roadmap/documentation
updates. It must not include implementation changes, private Radeon artifacts,
model outputs, vectors, credentials, or raw runtime logs.

## Expected Final State

- `{close_receipt}` exists.
- `{closure_candidate}` is absent.
- `close-receipt.json` is protocol v2, authoritative, review-bound, and
  authorizes Sprint 11.
- `scripts/validate_repository.py` passes.
- Only after that final promotion commit may Sprint 11 start.
"""


def parse_args(argv: Sequence[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--reviewer", default="independent-review")
    parser.add_argument("--reviewed-candidate-commit")
    parser.add_argument("--review-artifact", default=DEFAULT_REVIEW)
    parser.add_argument("--close-receipt", default=DEFAULT_CLOSE)
    parser.add_argument("--closure-candidate", default=DEFAULT_CANDIDATE)
    parser.add_argument("--output", type=Path)
    return parser.parse_args(argv)


def main(argv: Sequence[str] | None = None) -> int:
    args = parse_args(argv)
    try:
        reviewed_candidate_commit = args.reviewed_candidate_commit or git_value(["rev-parse", "HEAD"])
        body = render_checklist(
            reviewer=args.reviewer,
            reviewed_candidate_commit=reviewed_candidate_commit,
            review_artifact=args.review_artifact,
            close_receipt=args.close_receipt,
            closure_candidate=args.closure_candidate,
        )
    except subprocess.CalledProcessError as exc:
        print(f"promotion checklist rejected: {exc}", file=sys.stderr)
        return 2
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(body, encoding="utf-8")
    else:
        print(body, end="")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
