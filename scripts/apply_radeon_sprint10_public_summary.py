#!/usr/bin/env python3
"""Apply a verified Radeon public summary to the Sprint 10 evidence package.

This script never closes Sprint 10. It updates public evidence to
`ready_for_independent_review` while keeping the closure candidate
non-authoritative and `next_sprint_authorized: null`.
"""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import sys
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parents[1]
SPRINT_ID = "10"
EVIDENCE_DIR = ROOT / "docs" / "evidence" / "sprint-10"
REQUIRED_FILES = {
    "metrics-summary.json",
    "validation-report.json",
    "closure-candidate.json",
}


def utc_now() -> str:
    return dt.datetime.now(dt.UTC).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def load_json(path: Path) -> dict[str, Any]:
    payload = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(payload, dict):
        raise ValueError(f"JSON document must be an object: {path}")
    return payload


def write_json(path: Path, payload: dict[str, Any]) -> None:
    path.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def validate_summary(summary: dict[str, Any]) -> None:
    if summary.get("summary_kind") != "radeon_sprint10_public_summary":
        raise ValueError("summary_kind must be radeon_sprint10_public_summary")
    if summary.get("sprint_id") != SPRINT_ID:
        raise ValueError("summary sprint_id must be 10")
    if summary.get("status") != "passed":
        raise ValueError("summary status must be passed")
    if summary.get("private_recovery_verified") is not True:
        raise ValueError("summary private_recovery_verified must be true")
    counts = summary.get("counts")
    if not isinstance(counts, dict) or counts.get("valid_evidence_items") != counts.get("required_evidence_items"):
        raise ValueError("summary evidence counts are not complete")
    policy = summary.get("policy")
    if not isinstance(policy, dict):
        raise ValueError("summary policy is missing")
    for key in ("stores_private_logs", "stores_model_outputs", "stores_vectors", "stores_credentials"):
        if policy.get(key) is not False:
            raise ValueError(f"summary policy permits unsafe public data: {key}")


def build_updates(summary: dict[str, Any], evidence_dir: Path, recorded_at: str) -> dict[str, dict[str, Any]]:
    missing = sorted(name for name in REQUIRED_FILES if not (evidence_dir / name).is_file())
    if missing:
        raise FileNotFoundError(f"missing Sprint 10 evidence files: {', '.join(missing)}")
    metrics = load_json(evidence_dir / "metrics-summary.json")
    validation = load_json(evidence_dir / "validation-report.json")
    candidate = load_json(evidence_dir / "closure-candidate.json")
    if candidate.get("status") != "candidate" or candidate.get("authoritative") is not False:
        raise ValueError("closure candidate is not fail-closed")
    if candidate.get("next_sprint_authorized") is not None:
        raise ValueError("closure candidate already authorizes the next Sprint")

    metrics["status"] = "ready_for_independent_review"
    metrics["basis_commit"] = summary.get("basis_commit")
    metrics["recorded_at"] = recorded_at
    metric_values = metrics.setdefault("metrics", {})
    metric_values["real_radeon_runtime_receipts"] = 1
    metric_values["real_radeon_model_benchmarks"] = 1
    metric_values["real_radeon_embedding_benchmarks"] = 1
    metric_values["real_destroy_recreate_recovery_reports"] = 1
    metric_values["private_recovery_verified"] = True
    metric_values["public_summary_valid_evidence_items"] = summary["counts"]["valid_evidence_items"]

    validation["status"] = "ready_for_independent_review"
    validation["basis_commit"] = summary.get("basis_commit")
    validation["recorded_at"] = recorded_at
    validation["validation"] = [
        {
            "gate": "data spine implementation",
            "result": "passed_for_public_code",
            "summary": "Migrations, adapters, point-in-time views, source coverage, and restore-manifest verification exist in public source and are covered by tests.",
        },
        {
            "gate": "Radeon evidence collection automation",
            "result": "passed_for_public_code",
            "summary": "The one-shot evidence runner produced the private recovery report summarized by the public Radeon Sprint 10 summary.",
        },
        {
            "gate": "local inference proof",
            "result": "ready_for_independent_review",
            "summary": "The public summary reports verified private Radeon runtime, local model, local embedding, source restore, and recovery evidence by hash without publishing private artifacts.",
        },
        {
            "gate": "next Sprint authorization",
            "result": "not_authorized",
            "summary": "Sprint 11 remains blocked until an independent immutable review promotes this candidate to a review-bound close receipt.",
        },
    ]

    candidate["basis_commit"] = summary.get("basis_commit")
    candidate["recorded_at"] = recorded_at
    candidate["definition_of_done"]["rollback_demonstrated"] = True
    acceptance = candidate["acceptance"]
    acceptance["real_radeon_runtime_receipt_captured"] = True
    acceptance["two_local_instruction_candidates_benchmarked_on_radeon"] = True
    acceptance["local_embedding_endpoint_benchmarked_on_radeon"] = True
    acceptance["destroy_recreate_recovery_verified"] = True
    supporting = candidate.setdefault("supporting_artifacts", [])
    summary_artifact = "docs/evidence/sprint-10/radeon-public-summary.json"
    if summary_artifact not in supporting:
        supporting.append(summary_artifact)
    candidate["status"] = "candidate"
    candidate["authoritative"] = False
    candidate["next_sprint_authorized"] = None
    candidate["definition_of_done"]["independent_validation_recorded"] = False

    return {
        "metrics-summary.json": metrics,
        "validation-report.json": validation,
        "closure-candidate.json": candidate,
    }


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--summary", type=Path, required=True)
    parser.add_argument("--evidence-dir", type=Path, default=EVIDENCE_DIR)
    parser.add_argument("--dry-run", action="store_true")
    parser.add_argument("--recorded-at")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        summary = load_json(args.summary)
        validate_summary(summary)
        recorded_at = args.recorded_at or utc_now()
        updates = build_updates(summary, args.evidence_dir, recorded_at)
        if args.dry_run:
            sys.stdout.write(json.dumps(updates, indent=2, sort_keys=True) + "\n")
            return 0
        for filename, payload in updates.items():
            write_json(args.evidence_dir / filename, payload)
        return 0
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        sys.stderr.write(f"{type(exc).__name__}: {exc}\n")
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
