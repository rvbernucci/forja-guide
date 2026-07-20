#!/usr/bin/env python3
"""Diagnose partial Sprint 10 Radeon evidence artifacts without mutating them."""

from __future__ import annotations

import argparse
import importlib.util
import json
import sys
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parents[1]
RUNNER_SCRIPT = ROOT / "scripts" / "run_radeon_sprint10_evidence.py"
SUMMARY_SCRIPT = ROOT / "scripts" / "apply_radeon_sprint10_public_summary.py"
DEFAULT_EVIDENCE_DIR = Path("/workspace/forja-alpha-sprint10-evidence")
ORDER = (
    "runtime_receipt",
    "runtime_readiness",
    "source_restore",
    "model_benchmark",
    "embedding_benchmark",
    "recovery",
    "public_summary",
)


def load_module(path: Path, name: str):
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Unable to load {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


RUNNER = load_module(RUNNER_SCRIPT, "run_radeon_sprint10_evidence_for_diagnose")
SUMMARY = load_module(SUMMARY_SCRIPT, "apply_radeon_sprint10_public_summary_for_diagnose")


def load_json_if_possible(path: Path) -> tuple[dict[str, Any] | None, str | None]:
    try:
        payload = json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError:
        return None, "missing"
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
        return None, exc.__class__.__name__
    if not isinstance(payload, dict):
        return None, "not_object"
    return payload, None


def artifact_valid(key: str, payload: dict[str, Any] | None, error: str | None) -> tuple[bool, list[str]]:
    if error:
        return False, [error]
    if payload is None:
        return False, ["missing"]
    if key == "public_summary":
        try:
            SUMMARY.validate_summary(payload)
        except ValueError as exc:
            return False, [str(exc)]
        return True, []
    for field in ("verified", "ready", "valid", "status"):
        if field in payload:
            value = payload[field]
            if value is True or value == "passed":
                return True, []
            if value is False or value in {"failed", "partial_or_failed"}:
                return False, [f"{field}={value}"]
    errors = payload.get("errors")
    if isinstance(errors, list) and errors:
        return False, [str(error) for error in errors]
    return True, []


def diagnose(evidence_dir: Path) -> tuple[dict[str, Any], int]:
    artifacts = []
    first_incomplete = None
    for key in ORDER:
        filename = RUNNER.REPORT_NAMES[key]
        path = evidence_dir / filename
        payload, error = load_json_if_possible(path)
        valid, errors = artifact_valid(key, payload, error)
        present = path.is_file()
        if first_incomplete is None and not valid:
            first_incomplete = key
        artifacts.append(
            {
                "key": key,
                "filename": filename,
                "path": path.as_posix(),
                "present": present,
                "valid": valid,
                "errors": errors,
            }
        )

    public_summary_ready = all(item["valid"] for item in artifacts)
    if public_summary_ready:
        stage = "ready_to_ingest_public_summary"
        next_action = (
            "Copy radeon-public-summary.json to the workstation and run "
            "scripts/ingest_radeon_sprint10_public_summary.py."
        )
        exit_code = 0
    else:
        stage = f"blocked_at_{first_incomplete}"
        next_action = next_action_for(first_incomplete)
        exit_code = 2
    report = {
        "schema_version": "1.0",
        "report_kind": "radeon_sprint10_artifact_diagnosis",
        "evidence_dir": evidence_dir.as_posix(),
        "public_summary_ready": public_summary_ready,
        "stage": stage,
        "next_sprint_authorized": False,
        "next_action": next_action,
        "artifacts": artifacts,
    }
    return report, exit_code


def next_action_for(key: str | None) -> str:
    if key is None:
        return "No missing artifact was identified; inspect the evidence directory manually."
    return {
        "runtime_receipt": "Run scripts/capture_radeon_runtime_receipt.py through the Sprint 10 evidence runner.",
        "runtime_readiness": "Start loopback model and embedding endpoints, then rerun runtime readiness.",
        "source_restore": "Restore required private snapshots under /secure/forja and rerun source restore.",
        "model_benchmark": "Serve at least two local instruction candidates and rerun model benchmark.",
        "embedding_benchmark": "Serve the loopback embedding endpoint and rerun embedding benchmark.",
        "recovery": "Rerun scripts/verify_competition_profile_recovery.py after prerequisite reports pass.",
        "public_summary": "Rerun scripts/summarize_radeon_sprint10_evidence.py after recovery passes.",
    }[key]


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--evidence-dir", type=Path, default=DEFAULT_EVIDENCE_DIR)
    parser.add_argument("--output", type=Path)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        report, exit_code = diagnose(args.evidence_dir)
    except (OSError, RuntimeError, ValueError, json.JSONDecodeError) as exc:
        report = {
            "schema_version": "1.0",
            "report_kind": "radeon_sprint10_artifact_diagnosis",
            "stage": "failed",
            "next_sprint_authorized": False,
            "error": type(exc).__name__,
            "message": str(exc),
        }
        exit_code = 2
    body = json.dumps(report, indent=2, sort_keys=True) + "\n"
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(body, encoding="utf-8")
    else:
        sys.stdout.write(body)
    return exit_code


if __name__ == "__main__":
    raise SystemExit(main())
