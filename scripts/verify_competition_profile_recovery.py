#!/usr/bin/env python3
"""Verify Forja Alpha competition-profile recovery evidence."""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import os
import sys
from pathlib import Path
from typing import Any


EXPECTED_REPORTS = {
    "runtime_receipt": "radeon_runtime_environment",
    "runtime_readiness": "radeon_runtime_readiness",
    "source_restore": "forja_alpha_snapshot_restore_verification",
    "model_benchmark": "radeon_model_candidate_benchmark",
}


def utc_now() -> str:
    """Return second-precision UTC for stable public evidence."""
    return dt.datetime.now(dt.UTC).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def sha256_file(path: Path) -> str:
    """Hash one evidence file for the integrated recovery receipt."""
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def load_json(path: Path) -> dict[str, Any]:
    """Load a JSON object from disk."""
    payload = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(payload, dict):
        raise ValueError(f"evidence file must contain a JSON object: {path}")
    return payload


def verify_runtime_receipt(payload: dict[str, Any]) -> list[str]:
    errors: list[str] = []
    if payload.get("receipt_kind") != EXPECTED_REPORTS["runtime_receipt"]:
        errors.append("runtime_receipt_kind")
    profile = payload.get("competition_profile")
    if not isinstance(profile, dict) or profile.get("core_remote_inference_allowed") is not False:
        errors.append("runtime_receipt_remote_inference_policy")
    checks = payload.get("checks")
    if not isinstance(checks, dict):
        errors.append("runtime_receipt_checks")
    elif checks.get("gpu_probe_available") is not True:
        errors.append("runtime_receipt_gpu_probe")
    git = payload.get("git")
    if not isinstance(git, dict) or not isinstance(git.get("commit"), str):
        errors.append("runtime_receipt_git_commit")
    return errors


def verify_runtime_readiness(payload: dict[str, Any]) -> list[str]:
    errors: list[str] = []
    if payload.get("report_kind") != EXPECTED_REPORTS["runtime_readiness"]:
        errors.append("runtime_readiness_kind")
    if payload.get("ready") is not True:
        errors.append("runtime_readiness_not_ready")
    policy = payload.get("policy")
    if not isinstance(policy, dict):
        errors.append("runtime_readiness_policy")
    else:
        if policy.get("core_remote_inference_allowed") is not False:
            errors.append("runtime_readiness_remote_inference_policy")
        if policy.get("model_endpoint_loopback") is not True:
            errors.append("runtime_readiness_model_loopback")
        if policy.get("embedding_endpoint_loopback") is not True:
            errors.append("runtime_readiness_embedding_loopback")
        if policy.get("zero_remote_core_inference_proved") is not True:
            errors.append("runtime_readiness_zero_remote_not_proved")
    return errors


def verify_source_restore(payload: dict[str, Any]) -> list[str]:
    errors: list[str] = []
    if payload.get("report_kind") != EXPECTED_REPORTS["source_restore"]:
        errors.append("source_restore_kind")
    if payload.get("verified") is not True:
        errors.append("source_restore_not_verified")
    coverage = payload.get("coverage")
    if not isinstance(coverage, dict):
        errors.append("source_restore_coverage")
    elif coverage.get("missing_required_families") != []:
        errors.append("source_restore_missing_families")
    return errors


def verify_model_benchmark(payload: dict[str, Any], minimum_candidates: int) -> list[str]:
    errors: list[str] = []
    if payload.get("report_kind") != EXPECTED_REPORTS["model_benchmark"]:
        errors.append("model_benchmark_kind")
    if payload.get("candidate_count", 0) < minimum_candidates:
        errors.append("model_benchmark_candidate_count")
    privacy = payload.get("privacy")
    if not isinstance(privacy, dict):
        errors.append("model_benchmark_privacy")
    else:
        if privacy.get("stores_response_bodies") is not False:
            errors.append("model_benchmark_response_body_storage")
        if privacy.get("requires_loopback_endpoints") is not True:
            errors.append("model_benchmark_loopback_policy")
    candidates = payload.get("candidates")
    if not isinstance(candidates, list) or not candidates:
        errors.append("model_benchmark_candidates")
    else:
        successful_candidates = 0
        for index, candidate in enumerate(candidates):
            if not isinstance(candidate, dict):
                errors.append(f"model_benchmark_candidate_{index}_shape")
                continue
            endpoint = candidate.get("endpoint")
            if not isinstance(endpoint, dict) or endpoint.get("loopback") is not True:
                errors.append(f"model_benchmark_candidate_{index}_loopback")
            summary = candidate.get("summary")
            if isinstance(summary, dict) and summary.get("ok_count", 0) > 0 and summary.get("failed_count") == 0:
                successful_candidates += 1
        if successful_candidates < minimum_candidates:
            errors.append("model_benchmark_successful_candidate_count")
    return errors


def build_report(args: argparse.Namespace) -> tuple[dict[str, Any], int]:
    evidence_paths = {
        "runtime_receipt": args.runtime_receipt,
        "runtime_readiness": args.runtime_readiness,
        "source_restore": args.source_restore,
        "model_benchmark": args.model_benchmark,
    }
    loaded = {name: load_json(path) for name, path in evidence_paths.items()}
    failures = {
        "runtime_receipt": verify_runtime_receipt(loaded["runtime_receipt"]),
        "runtime_readiness": verify_runtime_readiness(loaded["runtime_readiness"]),
        "source_restore": verify_source_restore(loaded["source_restore"]),
        "model_benchmark": verify_model_benchmark(loaded["model_benchmark"], args.minimum_model_candidates),
    }
    git_commit = loaded["runtime_receipt"].get("git", {}).get("commit")
    if args.expected_commit and git_commit != args.expected_commit:
        failures["runtime_receipt"].append("runtime_receipt_unexpected_commit")
    evidence = {
        name: {
            "path": path.name,
            "sha256": sha256_file(path),
            "valid": not failures[name],
            "errors": failures[name],
        }
        for name, path in evidence_paths.items()
    }
    verified = all(item["valid"] for item in evidence.values())
    report = {
        "schema_version": "1.0",
        "report_kind": "forja_alpha_competition_profile_recovery",
        "recorded_at": args.recorded_at or utc_now(),
        "expected_commit": args.expected_commit,
        "minimum_model_candidates": args.minimum_model_candidates,
        "evidence": evidence,
        "verified": verified,
    }
    return report, 0 if verified else 2


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--runtime-receipt", type=Path, required=True)
    parser.add_argument("--runtime-readiness", type=Path, required=True)
    parser.add_argument("--source-restore", type=Path, required=True)
    parser.add_argument("--model-benchmark", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--expected-commit", help="Expected Git commit recorded by the runtime receipt.")
    parser.add_argument("--minimum-model-candidates", type=int, default=2)
    parser.add_argument("--recorded-at", help="Override UTC timestamp for reproducible tests.")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        report, exit_code = build_report(args)
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        report = {
            "schema_version": "1.0",
            "report_kind": "forja_alpha_competition_profile_recovery",
            "recorded_at": args.recorded_at or utc_now(),
            "verified": False,
            "error": type(exc).__name__,
        }
        exit_code = 2
    body = json.dumps(report, indent=2, sort_keys=True) + "\n"
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(body, encoding="utf-8")
    os.chmod(args.output, 0o600)
    return exit_code


if __name__ == "__main__":
    raise SystemExit(main())
