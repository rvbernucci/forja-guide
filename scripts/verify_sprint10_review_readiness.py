#!/usr/bin/env python3
"""Verify Sprint 10 is ready for independent review, not closed."""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parents[1]
DEFAULT_EVIDENCE_DIR = ROOT / "docs" / "evidence" / "sprint-10"


def load_json(path: Path) -> dict[str, Any]:
    payload = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(payload, dict):
        raise ValueError(f"JSON document must be an object: {path}")
    return payload


def verify(evidence_dir: Path) -> tuple[dict[str, Any], int]:
    required = {
        "summary": evidence_dir / "radeon-public-summary.json",
        "metrics": evidence_dir / "metrics-summary.json",
        "validation": evidence_dir / "validation-report.json",
        "candidate": evidence_dir / "closure-candidate.json",
    }
    errors: list[str] = []
    loaded: dict[str, dict[str, Any]] = {}
    for name, path in required.items():
        if not path.is_file():
            errors.append(f"missing_{name}")
            continue
        try:
            loaded[name] = load_json(path)
        except (OSError, ValueError, json.JSONDecodeError):
            errors.append(f"invalid_{name}")

    summary = loaded.get("summary", {})
    metrics = loaded.get("metrics", {})
    validation = loaded.get("validation", {})
    candidate = loaded.get("candidate", {})

    if summary.get("summary_kind") != "radeon_sprint10_public_summary":
        errors.append("summary_kind")
    if summary.get("status") != "passed":
        errors.append("summary_not_passed")
    if summary.get("private_recovery_verified") is not True:
        errors.append("summary_recovery_not_verified")

    if metrics.get("status") != "ready_for_independent_review":
        errors.append("metrics_not_ready")
    metric_values = metrics.get("metrics")
    if not isinstance(metric_values, dict):
        errors.append("metrics_shape")
    else:
        for key in (
            "real_radeon_runtime_receipts",
            "real_radeon_model_benchmarks",
            "real_radeon_embedding_benchmarks",
            "real_destroy_recreate_recovery_reports",
        ):
            if metric_values.get(key) != 1:
                errors.append(f"metric_{key}")

    if validation.get("status") != "ready_for_independent_review":
        errors.append("validation_not_ready")
    validation_rows = validation.get("validation")
    if not isinstance(validation_rows, list) or not any(
        isinstance(row, dict)
        and row.get("gate") == "local inference proof"
        and row.get("result") == "ready_for_independent_review"
        for row in validation_rows
    ):
        errors.append("validation_local_inference_not_ready")

    if candidate.get("status") != "candidate":
        errors.append("candidate_status")
    if candidate.get("authoritative") is not False:
        errors.append("candidate_authoritative")
    if candidate.get("next_sprint_authorized") is not None:
        errors.append("candidate_authorizes_next_sprint")
    definition = candidate.get("definition_of_done")
    if not isinstance(definition, dict):
        errors.append("candidate_definition")
    else:
        if definition.get("independent_validation_recorded") is not False:
            errors.append("candidate_independent_validation_must_remain_false")
        if definition.get("rollback_demonstrated") is not True:
            errors.append("candidate_rollback_not_demonstrated")
    acceptance = candidate.get("acceptance")
    if not isinstance(acceptance, dict):
        errors.append("candidate_acceptance")
    else:
        for key in (
            "real_radeon_runtime_receipt_captured",
            "two_local_instruction_candidates_benchmarked_on_radeon",
            "local_embedding_endpoint_benchmarked_on_radeon",
            "destroy_recreate_recovery_verified",
        ):
            if acceptance.get(key) is not True:
                errors.append(f"acceptance_{key}")

    summary_commit = summary.get("basis_commit")
    for name, payload in (("metrics", metrics), ("validation", validation), ("candidate", candidate)):
        if payload.get("basis_commit") != summary_commit:
            errors.append(f"{name}_basis_commit_mismatch")

    report = {
        "schema_version": "1.0",
        "report_kind": "sprint10_review_readiness",
        "ready_for_independent_review": not errors,
        "next_sprint_authorized": False,
        "errors": errors,
    }
    return report, 0 if not errors else 2


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--evidence-dir", type=Path, default=DEFAULT_EVIDENCE_DIR)
    parser.add_argument("--output", type=Path)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    report, exit_code = verify(args.evidence_dir)
    body = json.dumps(report, indent=2, sort_keys=True) + "\n"
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(body, encoding="utf-8")
    else:
        sys.stdout.write(body)
    return exit_code


if __name__ == "__main__":
    raise SystemExit(main())
