#!/usr/bin/env python3
"""Verify a private Sprint 10 Radeon operator bundle before GPU time is spent."""

from __future__ import annotations

import argparse
import importlib.util
import json
import re
import stat
import sys
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parents[1]
DEFAULT_BUNDLE_DIR = Path("/workspace/forja-alpha-sprint10-operator-bundle")
PREPARE_SCRIPT = ROOT / "scripts" / "prepare_radeon_sprint10_operator_bundle.py"
PLACEHOLDER = re.compile(r"<[^>]+>")
REQUIRED_FILES = {
    "README.md": 0o600,
    "radeon-model-candidates.template.json": 0o600,
    "run-sprint10-evidence.sh": 0o700,
    "sprint10-env.template.sh": 0o700,
}


def load_prepare_module():
    spec = importlib.util.spec_from_file_location("prepare_radeon_bundle", PREPARE_SCRIPT)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Unable to load {PREPARE_SCRIPT}")
    module = importlib.util.module_from_spec(spec)
    sys.modules["prepare_radeon_bundle"] = module
    spec.loader.exec_module(module)
    return module


PREPARE = load_prepare_module()


def load_json(path: Path) -> dict[str, Any]:
    payload = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(payload, dict):
        raise ValueError(f"JSON document must be an object: {path}")
    return payload


def verify_bundle(bundle_dir: Path, *, allow_placeholders: bool = False) -> tuple[dict[str, Any], int]:
    errors: list[str] = []
    checked_files: list[str] = []

    if not bundle_dir.is_dir():
        errors.append("missing_bundle_dir")
    else:
        bundle_mode = stat.S_IMODE(bundle_dir.stat().st_mode)
        if bundle_mode != 0o700:
            errors.append("bundle_dir_mode")

    for filename, expected_mode in REQUIRED_FILES.items():
        path = bundle_dir / filename
        if not path.is_file():
            errors.append(f"missing_{filename}")
            continue
        checked_files.append(filename)
        mode = stat.S_IMODE(path.stat().st_mode)
        if mode != expected_mode:
            errors.append(f"mode_{filename}")
        try:
            body = path.read_text(encoding="utf-8")
        except UnicodeDecodeError:
            errors.append(f"encoding_{filename}")
            continue
        if not allow_placeholders and PLACEHOLDER.search(body):
            errors.append(f"placeholder_{filename}")

    candidates_path = bundle_dir / "radeon-model-candidates.template.json"
    if candidates_path.is_file():
        try:
            candidates = load_json(candidates_path)
        except (OSError, ValueError, json.JSONDecodeError):
            candidates = {}
            errors.append("invalid_candidate_json")
        verify_candidates(candidates, errors)

    command_path = bundle_dir / "run-sprint10-evidence.sh"
    if command_path.is_file():
        body = command_path.read_text(encoding="utf-8")
        for required_snippet in (
            "check_radeon_sprint10_private_inputs.py",
            "capture_radeon_runtime_receipt.py",
            "verify_radeon_runtime_readiness.py",
            "run_radeon_sprint10_evidence.py",
            "forja-radeon-private-input-preflight.json",
            "--require-endpoints",
        ):
            if required_snippet not in body:
                errors.append(f"command_missing_{required_snippet}")

    report = {
        "schema_version": "1.0",
        "report_kind": "radeon_operator_bundle_readiness",
        "bundle_dir": bundle_dir.as_posix(),
        "checked_files": sorted(checked_files),
        "allow_placeholders": allow_placeholders,
        "ready_to_run": not errors,
        "errors": errors,
    }
    return report, 0 if not errors else 2


def verify_candidates(candidates: dict[str, Any], errors: list[str]) -> None:
    if candidates.get("schema_version") != "1.0":
        errors.append("candidate_schema_version")
    rows = candidates.get("candidates")
    if not isinstance(rows, list) or len(rows) < 2:
        errors.append("candidate_count")
        return
    ids: set[str] = set()
    for index, row in enumerate(rows):
        if not isinstance(row, dict):
            errors.append(f"candidate_{index}_shape")
            continue
        candidate_id = row.get("candidate_id")
        if not isinstance(candidate_id, str) or not candidate_id:
            errors.append(f"candidate_{index}_id")
        elif candidate_id in ids:
            errors.append(f"candidate_{index}_duplicate_id")
        else:
            ids.add(candidate_id)
        base_url = row.get("base_url")
        if not isinstance(base_url, str) or not PREPARE.is_loopback_url(base_url):
            errors.append(f"candidate_{index}_base_url")
        model = row.get("model")
        if not isinstance(model, str) or not model.strip():
            errors.append(f"candidate_{index}_model")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bundle-dir", type=Path, default=DEFAULT_BUNDLE_DIR)
    parser.add_argument(
        "--allow-placeholders",
        action="store_true",
        help="Permit placeholder values when validating freshly generated templates.",
    )
    parser.add_argument("--output", type=Path)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    report, exit_code = verify_bundle(
        args.bundle_dir,
        allow_placeholders=args.allow_placeholders,
    )
    body = json.dumps(report, indent=2, sort_keys=True) + "\n"
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(body, encoding="utf-8")
    else:
        sys.stdout.write(body)
    return exit_code


if __name__ == "__main__":
    raise SystemExit(main())
