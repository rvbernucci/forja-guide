#!/usr/bin/env python3
"""Ingest a public-safe Radeon Sprint 10 summary into the evidence package."""

from __future__ import annotations

import argparse
import importlib.util
import json
import sys
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parents[1]
DEFAULT_EVIDENCE_DIR = ROOT / "docs" / "evidence" / "sprint-10"
APPLIER_SCRIPT = ROOT / "scripts" / "apply_radeon_sprint10_public_summary.py"
READINESS_SCRIPT = ROOT / "scripts" / "verify_sprint10_review_readiness.py"


def load_module(path: Path, name: str):
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Unable to load {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


APPLIER = load_module(APPLIER_SCRIPT, "apply_radeon_sprint10_public_summary_for_ingest")
READINESS = load_module(READINESS_SCRIPT, "verify_sprint10_review_readiness_for_ingest")


def ingest_summary(
    *,
    summary_path: Path,
    evidence_dir: Path,
    recorded_at: str | None,
    dry_run: bool,
) -> tuple[dict[str, Any], int]:
    summary = APPLIER.load_json(summary_path)
    APPLIER.validate_summary(summary)
    timestamp = recorded_at or APPLIER.utc_now()
    updates = APPLIER.build_updates(summary, evidence_dir, timestamp)
    destination = evidence_dir / "radeon-public-summary.json"

    if dry_run:
        readiness_report = {
            "schema_version": "1.0",
            "report_kind": "sprint10_review_readiness",
            "ready_for_independent_review": "not_run_in_dry_run",
            "next_sprint_authorized": False,
            "errors": [],
        }
        exit_code = 0
    else:
        APPLIER.write_json(destination, summary)
        for filename, payload in updates.items():
            APPLIER.write_json(evidence_dir / filename, payload)
        readiness_report, exit_code = READINESS.verify(evidence_dir)

    report = {
        "schema_version": "1.0",
        "report_kind": "radeon_sprint10_public_summary_ingest",
        "dry_run": dry_run,
        "summary": summary_path.as_posix(),
        "destination": destination.as_posix(),
        "evidence_dir": evidence_dir.as_posix(),
        "basis_commit": summary.get("basis_commit"),
        "status": "ready_for_independent_review" if exit_code == 0 else "failed",
        "next_sprint_authorized": False,
        "updated_files": [
            destination.as_posix(),
            *(str(evidence_dir / filename) for filename in sorted(updates)),
        ],
        "readiness": readiness_report,
    }
    return report, exit_code


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--summary", type=Path, required=True)
    parser.add_argument("--evidence-dir", type=Path, default=DEFAULT_EVIDENCE_DIR)
    parser.add_argument("--recorded-at")
    parser.add_argument("--dry-run", action="store_true")
    parser.add_argument("--output", type=Path)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        report, exit_code = ingest_summary(
            summary_path=args.summary,
            evidence_dir=args.evidence_dir,
            recorded_at=args.recorded_at,
            dry_run=args.dry_run,
        )
    except (OSError, ValueError, json.JSONDecodeError, RuntimeError) as exc:
        report = {
            "schema_version": "1.0",
            "report_kind": "radeon_sprint10_public_summary_ingest",
            "status": "failed",
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
