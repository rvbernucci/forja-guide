#!/usr/bin/env python3
"""Verify a Radeon Sprint 10 public summary without mutating evidence."""

from __future__ import annotations

import argparse
import importlib.util
import json
import sys
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parents[1]
APPLIER_SCRIPT = ROOT / "scripts" / "apply_radeon_sprint10_public_summary.py"


def load_module(path: Path, name: str):
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Unable to load {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


APPLIER = load_module(APPLIER_SCRIPT, "apply_radeon_sprint10_public_summary_for_verify")


def verify_summary(summary_path: Path) -> tuple[dict[str, Any], int]:
    errors: list[str] = []
    summary: dict[str, Any] = {}
    try:
        summary = APPLIER.load_json(summary_path)
        APPLIER.validate_summary(summary)
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        errors.append(f"{type(exc).__name__}: {exc}")

    counts = summary.get("counts") if isinstance(summary, dict) else None
    policy = summary.get("policy") if isinstance(summary, dict) else None
    evidence = summary.get("evidence") if isinstance(summary, dict) else None
    report = {
        "schema_version": "1.0",
        "report_kind": "radeon_sprint10_public_summary_verification",
        "summary": summary_path.as_posix(),
        "ready_to_ingest": not errors,
        "next_sprint_authorized": False,
        "summary_kind": summary.get("summary_kind") if isinstance(summary, dict) else None,
        "sprint_id": summary.get("sprint_id") if isinstance(summary, dict) else None,
        "status": summary.get("status") if isinstance(summary, dict) else None,
        "basis_commit": summary.get("basis_commit") if isinstance(summary, dict) else None,
        "private_recovery_verified": summary.get("private_recovery_verified")
        if isinstance(summary, dict)
        else None,
        "counts": counts if isinstance(counts, dict) else None,
        "policy": policy if isinstance(policy, dict) else None,
        "evidence_item_count": len(evidence) if isinstance(evidence, list) else None,
        "errors": errors,
        "next_action": "Run ingest_radeon_sprint10_public_summary.py --dry-run first."
        if not errors
        else "Fix or regenerate radeon-public-summary.json on the Radeon instance.",
    }
    return report, 0 if not errors else 2


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--summary", type=Path, required=True)
    parser.add_argument("--output", type=Path)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    report, exit_code = verify_summary(args.summary)
    body = json.dumps(report, indent=2, sort_keys=True) + "\n"
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(body, encoding="utf-8")
    else:
        sys.stdout.write(body)
    return exit_code


if __name__ == "__main__":
    raise SystemExit(main())
