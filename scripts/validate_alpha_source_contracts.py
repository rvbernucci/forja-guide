#!/usr/bin/env python3
"""Validate public Forja Alpha source-family contracts."""

from __future__ import annotations

import argparse
import importlib.util
import json
import sys
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parents[1]
DEFAULT_CONTRACTS = (
    ROOT
    / "internal"
    / "alpha"
    / "testdata"
    / "alpha_source_family_contracts_public_v1.json"
)


def load_module(script_name: str, module_name: str):
    path = ROOT / "scripts" / script_name
    spec = importlib.util.spec_from_file_location(module_name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Unable to load {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[module_name] = module
    spec.loader.exec_module(module)
    return module


PRIVATE_INPUTS = load_module(
    "check_radeon_sprint10_private_inputs.py",
    "check_radeon_sprint10_private_inputs_for_source_contracts",
)
SNAPSHOT_MANIFEST = load_module(
    "build_alpha_snapshot_manifest.py",
    "build_alpha_snapshot_manifest_for_source_contracts",
)


def load_json_object(path: Path) -> dict[str, Any]:
    payload = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(payload, dict):
        raise ValueError(f"contract file must be a JSON object: {path}")
    return payload


def non_empty_string(value: Any) -> bool:
    return isinstance(value, str) and bool(value.strip())


def non_empty_string_list(value: Any) -> bool:
    return (
        isinstance(value, list)
        and bool(value)
        and all(non_empty_string(item) for item in value)
    )


def validate_contracts(path: Path) -> tuple[dict[str, Any], int]:
    errors: list[str] = []
    payload = load_json_object(path)
    if payload.get("schema_version") != "1.0":
        errors.append("schema_version")
    if payload.get("contract_kind") != "forja_alpha_source_family_contracts":
        errors.append("contract_kind")
    families = payload.get("families")
    if not isinstance(families, list) or not families:
        errors.append("families")
        families = []

    seen: set[str] = set()
    sprint10_paths: dict[str, str] = {}
    for index, family in enumerate(families):
        if not isinstance(family, dict):
            errors.append(f"family_{index}_shape")
            continue
        source_family = family.get("source_family")
        if not non_empty_string(source_family):
            errors.append(f"family_{index}_source_family")
            continue
        if source_family in seen:
            errors.append(f"family_{source_family}_duplicate")
        seen.add(source_family)
        if family.get("sprint10_required") is True:
            path_value = family.get("expected_snapshot_path")
            if not non_empty_string(path_value) or "<" in path_value:
                errors.append(f"family_{source_family}_expected_snapshot_path")
            else:
                sprint10_paths[source_family] = path_value
        elif family.get("sprint10_required") is not False:
            errors.append(f"family_{source_family}_sprint10_required")
        for field in (
            "preserved_object",
            "authority_store",
            "projection_policy",
        ):
            if not non_empty_string(family.get(field)):
                errors.append(f"family_{source_family}_{field}")
        for field in (
            "canonical_rows",
            "required_receipt_fields",
            "forbidden_runtime_authority",
        ):
            if not non_empty_string_list(family.get(field)):
                errors.append(f"family_{source_family}_{field}")

    required_snapshots = dict(PRIVATE_INPUTS.REQUIRED_SNAPSHOTS)
    if sprint10_paths != required_snapshots:
        errors.append("sprint10_required_snapshot_mismatch")
    required_manifest_families = set(SNAPSHOT_MANIFEST.REQUIRED_FAMILIES)
    if set(required_snapshots) != required_manifest_families:
        errors.append("manifest_required_family_mismatch")
    allowed_manifest_families = set(SNAPSHOT_MANIFEST.ALLOWED_FAMILIES)
    unknown = sorted(seen - allowed_manifest_families - {"thirteen_f"})
    if unknown:
        errors.append("unknown_source_families:" + ",".join(unknown))
    missing_core = sorted(required_manifest_families - seen)
    if missing_core:
        errors.append("missing_required_families:" + ",".join(missing_core))

    report = {
        "schema_version": "1.0",
        "report_kind": "forja_alpha_source_contract_validation",
        "contract_path": path.relative_to(ROOT).as_posix() if path.is_relative_to(ROOT) else path.as_posix(),
        "family_count": len(seen),
        "sprint10_required_families": sorted(sprint10_paths),
        "sprint10_required_snapshots": sprint10_paths,
        "ready": not errors,
        "errors": errors,
    }
    return report, 0 if not errors else 2


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--contracts", type=Path, default=DEFAULT_CONTRACTS)
    parser.add_argument("--output", type=Path)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        report, exit_code = validate_contracts(args.contracts)
    except (OSError, ValueError, json.JSONDecodeError, RuntimeError) as exc:
        report = {
            "schema_version": "1.0",
            "report_kind": "forja_alpha_source_contract_validation",
            "ready": False,
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
