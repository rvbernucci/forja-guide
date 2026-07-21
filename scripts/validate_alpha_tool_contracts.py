#!/usr/bin/env python3
"""Validate public Forja Alpha deterministic-tool contracts."""

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parents[1]
DEFAULT_CONTRACTS = (
    ROOT / "internal" / "alpha" / "testdata" / "alpha_tool_contracts_public_v1.json"
)
TOOL_NAME = re.compile(r"^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$")
REQUIRED_TOOLS = {
    "evidence.pack",
    "factors.estimate",
    "filings.compare",
    "filings.timeline",
    "fundamentals.compute",
    "holdings.compare",
}
REQUIRED_RECEIPT_FIELDS = {
    "contract_version",
    "tool_name",
    "tool_version",
    "capability_id",
    "request_sha256",
    "input_refs",
    "result_state",
    "result_sha256",
    "formula_or_method_version",
    "diagnostics",
    "limitations",
    "evidence_refs",
}


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
    if payload.get("contract_kind") != "forja_alpha_deterministic_tool_contracts":
        errors.append("contract_kind")

    receipt = payload.get("receipt_contract")
    if not isinstance(receipt, dict):
        errors.append("receipt_contract")
        receipt = {}
    receipt_fields = set(receipt.get("required_fields") or [])
    if receipt_fields != REQUIRED_RECEIPT_FIELDS:
        errors.append("receipt_required_fields")
    if receipt.get("storage_table") != "forja.alpha_tool_invocations":
        errors.append("receipt_storage_table")
    if not non_empty_string(receipt.get("authority_rule")):
        errors.append("receipt_authority_rule")

    tools = payload.get("tools")
    if not isinstance(tools, list) or not tools:
        errors.append("tools")
        tools = []
    seen: set[str] = set()
    for index, tool in enumerate(tools):
        if not isinstance(tool, dict):
            errors.append(f"tool_{index}_shape")
            continue
        name = tool.get("tool_name")
        if not isinstance(name, str) or TOOL_NAME.fullmatch(name) is None:
            errors.append(f"tool_{index}_name")
            continue
        if name in seen:
            errors.append(f"tool_{name}_duplicate")
        seen.add(name)
        for field in ("tool_version", "capability_id", "output_kind"):
            if not non_empty_string(tool.get(field)):
                errors.append(f"tool_{name}_{field}")
        for field in (
            "claim_classes",
            "input_authorities",
            "required_diagnostics",
            "forbidden_behavior",
        ):
            if not non_empty_string_list(tool.get(field)):
                errors.append(f"tool_{name}_{field}")

    missing = sorted(REQUIRED_TOOLS - seen)
    extra = sorted(seen - REQUIRED_TOOLS)
    if missing:
        errors.append("missing_required_tools:" + ",".join(missing))
    if extra:
        errors.append("unknown_tools:" + ",".join(extra))

    report = {
        "schema_version": "1.0",
        "report_kind": "forja_alpha_tool_contract_validation",
        "contract_path": path.relative_to(ROOT).as_posix()
        if path.is_relative_to(ROOT)
        else path.as_posix(),
        "tool_count": len(seen),
        "tools": sorted(seen),
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
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        report = {
            "schema_version": "1.0",
            "report_kind": "forja_alpha_tool_contract_validation",
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
