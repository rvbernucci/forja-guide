#!/usr/bin/env python3
"""Promote Sprint 10's fail-closed candidate into a review-bound receipt."""

from __future__ import annotations

import argparse
import hashlib
import json
import sys
from datetime import UTC, datetime
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parents[1]
DEFAULT_CANDIDATE = ROOT / "docs" / "evidence" / "sprint-10" / "closure-candidate.json"
DEFAULT_OUTPUT = ROOT / "docs" / "evidence" / "sprint-10" / "close-receipt.json"
COMMIT_SHA_LENGTH = 40
REAL_ACCEPTANCE_GATES = (
    "real_radeon_runtime_receipt_captured",
    "two_local_instruction_candidates_benchmarked_on_radeon",
    "local_embedding_endpoint_benchmarked_on_radeon",
    "destroy_recreate_recovery_verified",
)


def load_json(path: Path) -> dict[str, Any]:
    payload = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(payload, dict):
        raise ValueError(f"JSON document must be an object: {path}")
    return payload


def sha256_file(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def ensure_inside_root(path: Path, root: Path) -> str:
    resolved = path.resolve()
    resolved_root = root.resolve()
    if not resolved.is_relative_to(resolved_root):
        raise ValueError(f"path escapes repository root: {path}")
    return resolved.relative_to(resolved_root).as_posix()


def successor_for_sprint(candidate: dict[str, Any]) -> str | None:
    sprint_id = candidate.get("sprint_id")
    if not isinstance(sprint_id, str) or not sprint_id.isdigit():
        raise ValueError("candidate has invalid sprint_id")
    number = int(sprint_id)
    if not 0 <= number <= 14:
        raise ValueError("candidate sprint_id is outside planned range")
    if number == 14:
        return None
    return f"{number + 1:02d}"


def validate_candidate(candidate: dict[str, Any]) -> None:
    if candidate.get("sprint_id") != "10":
        raise ValueError("this promoter only closes Sprint 10")
    if candidate.get("status") != "candidate":
        raise ValueError("candidate status must remain candidate")
    if candidate.get("closure_protocol_version") != "2.0":
        raise ValueError("candidate must use closure protocol 2.0")
    if candidate.get("authoritative") is not False:
        raise ValueError("candidate must be non-authoritative")
    if candidate.get("next_sprint_authorized") is not None:
        raise ValueError("candidate must not authorize the next sprint")

    definition = candidate.get("definition_of_done")
    if not isinstance(definition, dict):
        raise ValueError("candidate definition_of_done must be an object")
    if definition.get("independent_validation_recorded") is not False:
        raise ValueError("candidate must not pre-record independent validation")
    if definition.get("rollback_demonstrated") is not True:
        raise ValueError("rollback must be demonstrated before promotion")

    acceptance = candidate.get("acceptance")
    if not isinstance(acceptance, dict):
        raise ValueError("candidate acceptance must be an object")
    missing = [key for key in REAL_ACCEPTANCE_GATES if acceptance.get(key) is not True]
    if missing:
        raise ValueError("real Radeon gates are not closed: " + ", ".join(missing))

    supporting_artifacts = candidate.get("supporting_artifacts")
    if not isinstance(supporting_artifacts, list):
        raise ValueError("candidate supporting_artifacts must be a list")
    recorded_at = candidate.get("recorded_at")
    if not isinstance(recorded_at, str) or not recorded_at.strip():
        raise ValueError("candidate recorded_at is required")


def validate_commit(value: str) -> None:
    valid_hex = all(char in "0123456789abcdef" for char in value)
    if len(value) != COMMIT_SHA_LENGTH or not valid_hex:
        raise ValueError("reviewed candidate commit must be a 40-character lowercase SHA")


def build_receipt(
    *,
    candidate_path: Path,
    review_artifact: Path,
    reviewed_candidate_commit: str,
    model: str,
    closed_at: str,
    root: Path = ROOT,
) -> dict[str, Any]:
    validate_commit(reviewed_candidate_commit)
    candidate = load_json(candidate_path)
    validate_candidate(candidate)

    if not review_artifact.is_file():
        raise ValueError(f"review artifact is missing: {review_artifact}")
    review_relative = ensure_inside_root(review_artifact, root)
    expected_prefix = "docs/evidence/sprint-10/reviews/"
    if not review_relative.startswith(expected_prefix):
        raise ValueError("review artifact must live under docs/evidence/sprint-10/reviews")
    if review_relative in candidate["supporting_artifacts"]:
        raise ValueError("review artifact is already listed in supporting_artifacts")

    receipt = dict(candidate)
    definition = dict(candidate["definition_of_done"])
    definition["independent_validation_recorded"] = True
    receipt["status"] = "closed"
    receipt["authoritative"] = True
    receipt["definition_of_done"] = definition
    receipt["supporting_artifacts"] = [*candidate["supporting_artifacts"], review_relative]
    receipt["next_sprint_authorized"] = successor_for_sprint(candidate)
    receipt["candidate_recorded_at"] = receipt.pop("recorded_at")
    receipt["reviewed_candidate_commit"] = reviewed_candidate_commit
    receipt["immutable_review"] = {
        "artifact_path": review_relative,
        "artifact_sha256": sha256_file(review_artifact),
        "model": model,
        "result": "passed",
        "reviewed_commit": reviewed_candidate_commit,
    }
    receipt["closed_at"] = closed_at
    return receipt


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--candidate", type=Path, default=DEFAULT_CANDIDATE)
    parser.add_argument("--review-artifact", type=Path, required=True)
    parser.add_argument("--reviewed-candidate-commit", required=True)
    parser.add_argument("--model", default="independent-review")
    parser.add_argument(
        "--closed-at",
        default=datetime.now(UTC).replace(microsecond=0).isoformat().replace("+00:00", "Z"),
    )
    parser.add_argument("--output", type=Path, default=DEFAULT_OUTPUT)
    parser.add_argument("--dry-run", action="store_true")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        receipt = build_receipt(
            candidate_path=args.candidate,
            review_artifact=args.review_artifact,
            reviewed_candidate_commit=args.reviewed_candidate_commit,
            model=args.model,
            closed_at=args.closed_at,
        )
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        print(f"promotion rejected: {exc}", file=sys.stderr)
        return 2

    body = json.dumps(receipt, indent=2, sort_keys=True) + "\n"
    if args.dry_run:
        sys.stdout.write(body)
        return 0
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(body, encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
