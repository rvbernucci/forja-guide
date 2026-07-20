#!/usr/bin/env python3
"""Create a public Sprint 10 summary from private Radeon recovery evidence."""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import sys
from pathlib import Path
from typing import Any


EXPECTED_RECOVERY_KIND = "forja_alpha_competition_profile_recovery"
EXPECTED_EVIDENCE_KEYS = {
    "runtime_receipt",
    "runtime_readiness",
    "source_restore",
    "model_benchmark",
    "embedding_benchmark",
}


def utc_now() -> str:
    return dt.datetime.now(dt.UTC).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def load_json(path: Path) -> dict[str, Any]:
    payload = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(payload, dict):
        raise ValueError(f"JSON document must be an object: {path}")
    return payload


def summarize_evidence_item(name: str, item: Any) -> dict[str, Any]:
    if not isinstance(item, dict):
        return {
            "name": name,
            "valid": False,
            "sha256": None,
            "errors": ["invalid_evidence_item"],
        }
    errors = item.get("errors")
    if not isinstance(errors, list) or not all(isinstance(error, str) for error in errors):
        errors = ["invalid_error_list"]
    sha256 = item.get("sha256")
    if not isinstance(sha256, str) or len(sha256) != 64:
        sha256 = None
        if "invalid_sha256" not in errors:
            errors = [*errors, "invalid_sha256"]
    return {
        "name": name,
        "valid": item.get("valid") is True,
        "sha256": sha256,
        "errors": errors,
    }


def build_summary(args: argparse.Namespace) -> tuple[dict[str, Any], int]:
    recovery = load_json(args.recovery)
    errors: list[str] = []
    if recovery.get("report_kind") != EXPECTED_RECOVERY_KIND:
        errors.append("recovery_kind")
    evidence = recovery.get("evidence")
    if not isinstance(evidence, dict):
        evidence = {}
        errors.append("recovery_evidence")
    missing = sorted(EXPECTED_EVIDENCE_KEYS - set(evidence))
    unexpected = sorted(set(evidence) - EXPECTED_EVIDENCE_KEYS)
    if missing:
        errors.append("missing_evidence")
    if unexpected:
        errors.append("unexpected_evidence")
    items = [
        summarize_evidence_item(name, evidence.get(name))
        for name in sorted(EXPECTED_EVIDENCE_KEYS)
    ]
    valid_count = sum(1 for item in items if item["valid"])
    public_item_errors = any(item["errors"] or item["sha256"] is None for item in items)
    verified = (
        recovery.get("verified") is True
        and not errors
        and not public_item_errors
        and valid_count == len(EXPECTED_EVIDENCE_KEYS)
    )
    summary = {
        "evidence_version": "1.0",
        "sprint_id": "10",
        "summary_kind": "radeon_sprint10_public_summary",
        "status": "passed" if verified else "partial_or_failed",
        "recorded_at": args.recorded_at or utc_now(),
        "basis_commit": recovery.get("expected_commit"),
        "private_recovery_verified": recovery.get("verified") is True,
        "minimum_model_candidates": recovery.get("minimum_model_candidates"),
        "counts": {
            "required_evidence_items": len(EXPECTED_EVIDENCE_KEYS),
            "valid_evidence_items": valid_count,
            "missing_evidence_items": len(missing),
            "unexpected_evidence_items": len(unexpected),
        },
        "policy": {
            "raw_artifacts_outside_git": True,
            "stores_private_logs": False,
            "stores_model_outputs": False,
            "stores_vectors": False,
            "stores_credentials": False,
        },
        "evidence": items,
        "missing_evidence": missing,
        "unexpected_evidence": unexpected,
        "errors": errors,
    }
    return summary, 0 if verified else 2


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--recovery", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--recorded-at")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        summary, exit_code = build_summary(args)
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        summary = {
            "evidence_version": "1.0",
            "sprint_id": "10",
            "summary_kind": "radeon_sprint10_public_summary",
            "status": "failed",
            "recorded_at": args.recorded_at or utc_now(),
            "error": type(exc).__name__,
        }
        exit_code = 2
    body = json.dumps(summary, indent=2, sort_keys=True) + "\n"
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(body, encoding="utf-8")
    os.chmod(args.output, 0o600)
    return exit_code


if __name__ == "__main__":
    raise SystemExit(main())
