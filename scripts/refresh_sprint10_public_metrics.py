#!/usr/bin/env python3
"""Refresh Sprint 10 public-code evidence without changing real Radeon gates."""

from __future__ import annotations

import argparse
import json
import subprocess
import sys
from datetime import UTC, datetime
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parents[1]
DEFAULT_EVIDENCE_DIR = ROOT / "docs" / "evidence" / "sprint-10"
REAL_GATE_KEYS = (
    "real_radeon_runtime_receipts",
    "real_radeon_model_benchmarks",
    "real_radeon_embedding_benchmarks",
    "real_destroy_recreate_recovery_reports",
)


def utc_now() -> str:
    return datetime.now(UTC).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def git_head() -> str:
    result = subprocess.run(
        ["git", "-C", str(ROOT), "rev-parse", "HEAD"],
        check=True,
        capture_output=True,
        text=True,
    )
    return result.stdout.strip()


def load_json(path: Path) -> dict[str, Any]:
    payload = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(payload, dict):
        raise ValueError(f"JSON document must be an object: {path}")
    return payload


def write_json(path: Path, payload: dict[str, Any]) -> None:
    path.write_text(json.dumps(payload, indent=2, sort_keys=False) + "\n", encoding="utf-8")


def refresh(
    *,
    evidence_dir: Path,
    basis_commit: str,
    recorded_at: str,
    python_tests: int,
    markdown_files: int,
    json_schemas: int,
) -> dict[str, Any]:
    metrics_path = evidence_dir / "metrics-summary.json"
    test_path = evidence_dir / "test-report.json"
    validation_path = evidence_dir / "validation-report.json"
    metrics = load_json(metrics_path)
    test_report = load_json(test_path)
    validation = load_json(validation_path)

    metric_values = metrics.get("metrics")
    if not isinstance(metric_values, dict):
        raise ValueError("metrics.metrics must be an object")
    for key in REAL_GATE_KEYS:
        if metric_values.get(key) != 0:
            raise ValueError(f"refusing to refresh public metrics after real gate changed: {key}")

    metrics["basis_commit"] = basis_commit
    metrics["recorded_at"] = recorded_at
    metric_values["python_tests"] = python_tests
    metric_values["markdown_files_validated"] = markdown_files
    metric_values["json_schemas_validated"] = json_schemas

    test_report["basis_commit"] = basis_commit
    test_report["recorded_at"] = recorded_at
    update_quality_summary(test_report, python_tests)

    validation["basis_commit"] = basis_commit
    validation["recorded_at"] = recorded_at

    write_json(metrics_path, metrics)
    write_json(test_path, test_report)
    write_json(validation_path, validation)

    return {
        "schema_version": "1.0",
        "report_kind": "sprint10_public_metrics_refresh",
        "basis_commit": basis_commit,
        "recorded_at": recorded_at,
        "python_tests": python_tests,
        "markdown_files_validated": markdown_files,
        "json_schemas_validated": json_schemas,
        "real_gates_preserved_zero": True,
        "updated_files": [
            display_path(metrics_path),
            display_path(test_path),
            display_path(validation_path),
        ],
    }


def update_quality_summary(test_report: dict[str, Any], python_tests: int) -> None:
    tests = test_report.get("tests")
    if not isinstance(tests, list):
        raise ValueError("test-report tests must be a list")
    for item in tests:
        if isinstance(item, dict) and item.get("name") == "local repository quality gate":
            summary = item.get("summary")
            if not isinstance(summary, str):
                raise ValueError("local repository quality gate summary must be a string")
            item["summary"] = replace_python_test_count(summary, python_tests)
            return
    raise ValueError("local repository quality gate row is missing")


def display_path(path: Path) -> str:
    try:
        return path.relative_to(ROOT).as_posix()
    except ValueError:
        return path.as_posix()


def replace_python_test_count(summary: str, python_tests: int) -> str:
    marker = " Python tests"
    before, separator, after = summary.partition(marker)
    if not separator:
        raise ValueError("summary does not contain Python test count marker")
    prefix = before.rsplit(" ", 1)[0]
    return f"{prefix} {python_tests}{separator}{after}"


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--evidence-dir", type=Path, default=DEFAULT_EVIDENCE_DIR)
    parser.add_argument("--basis-commit", default=None)
    parser.add_argument("--recorded-at", default=None)
    parser.add_argument("--python-tests", type=int, required=True)
    parser.add_argument("--markdown-files", type=int, required=True)
    parser.add_argument("--json-schemas", type=int, required=True)
    parser.add_argument("--output", type=Path)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        report = refresh(
            evidence_dir=args.evidence_dir,
            basis_commit=args.basis_commit or git_head(),
            recorded_at=args.recorded_at or utc_now(),
            python_tests=args.python_tests,
            markdown_files=args.markdown_files,
            json_schemas=args.json_schemas,
        )
    except (OSError, ValueError, json.JSONDecodeError, subprocess.CalledProcessError) as exc:
        print(f"public metrics refresh rejected: {exc}", file=sys.stderr)
        return 2
    body = json.dumps(report, indent=2, sort_keys=True) + "\n"
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(body, encoding="utf-8")
    else:
        sys.stdout.write(body)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
