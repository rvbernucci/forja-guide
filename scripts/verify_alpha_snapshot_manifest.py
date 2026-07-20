#!/usr/bin/env python3
"""Verify a Forja Alpha source snapshot manifest after restore."""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import os
import sys
from pathlib import Path
from typing import Any


REQUIRED_FAMILIES = {
    "sec_identity",
    "sec_submissions",
    "sec_company_facts",
    "treasury",
    "fred",
    "market",
}
ALLOWED_FAMILIES = REQUIRED_FAMILIES | {"sec_filing_document", "sec_xbrl", "metadata"}
SHA256_HEX_LENGTH = 64


def utc_now() -> str:
    """Return second-precision UTC for stable public evidence."""
    return dt.datetime.now(dt.UTC).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def sha256_file(path: Path) -> str:
    """Return the SHA-256 digest of a file without loading it all into memory."""
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def load_manifest(path: Path) -> dict[str, Any]:
    """Load and minimally validate the manifest envelope."""
    payload = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(payload, dict):
        raise ValueError("manifest must be a JSON object")
    if payload.get("schema_version") != "1.0":
        raise ValueError("manifest schema_version must be 1.0")
    if payload.get("manifest_kind") != "forja_alpha_source_snapshots":
        raise ValueError("manifest_kind must be forja_alpha_source_snapshots")
    snapshots = payload.get("snapshots")
    if not isinstance(snapshots, list) or not snapshots:
        raise ValueError("manifest snapshots must be a non-empty list")
    return payload


def safe_snapshot_path(root: Path, relative_path: str) -> Path | None:
    """Resolve a manifest path and reject absolute or escaping paths."""
    candidate = Path(relative_path)
    if candidate.is_absolute():
        return None
    resolved_root = root.resolve()
    resolved = (resolved_root / candidate).resolve()
    try:
        if not resolved.is_relative_to(resolved_root):
            return None
    except AttributeError:
        if resolved_root not in resolved.parents and resolved != resolved_root:
            return None
    return resolved


def validate_snapshot(index: int, root: Path, entry: Any) -> dict[str, Any]:
    """Validate one manifest entry and return a sanitized result."""
    errors: list[str] = []
    if not isinstance(entry, dict):
        return {
            "index": index,
            "ok": False,
            "source_family": None,
            "logical_path": None,
            "errors": ["entry_not_object"],
        }
    family = entry.get("source_family")
    logical_path = entry.get("logical_path")
    expected_sha256 = entry.get("sha256")
    expected_size = entry.get("size_bytes")
    required = entry.get("required", True)
    if family not in ALLOWED_FAMILIES:
        errors.append("invalid_source_family")
    if not isinstance(logical_path, str) or not logical_path:
        errors.append("invalid_logical_path")
        path = None
    else:
        path = safe_snapshot_path(root, logical_path)
        if path is None:
            errors.append("unsafe_logical_path")
    if not isinstance(expected_sha256, str) or len(expected_sha256) != SHA256_HEX_LENGTH:
        errors.append("invalid_sha256")
    if not isinstance(expected_size, int) or expected_size < 0:
        errors.append("invalid_size_bytes")
    if not isinstance(required, bool):
        errors.append("invalid_required_flag")
    actual_size = None
    actual_sha256 = None
    if path is not None and not path.is_file():
        errors.append("missing_file")
    elif path is not None and isinstance(expected_size, int) and isinstance(expected_sha256, str):
        actual_size = path.stat().st_size
        actual_sha256 = sha256_file(path)
        if actual_size != expected_size:
            errors.append("size_mismatch")
        if actual_sha256 != expected_sha256:
            errors.append("sha256_mismatch")
    return {
        "index": index,
        "ok": not errors,
        "source_family": family,
        "logical_path": logical_path,
        "required": required,
        "expected_size_bytes": expected_size if isinstance(expected_size, int) else None,
        "actual_size_bytes": actual_size,
        "expected_sha256": expected_sha256 if isinstance(expected_sha256, str) else None,
        "actual_sha256": actual_sha256,
        "errors": errors,
    }


def build_report(args: argparse.Namespace) -> tuple[dict[str, Any], int]:
    """Verify all manifest entries and return report plus process exit code."""
    manifest = load_manifest(args.manifest)
    root = args.snapshot_root
    results = [
        validate_snapshot(index, root, entry)
        for index, entry in enumerate(manifest["snapshots"])
    ]
    present_required_families = {
        result["source_family"]
        for result in results
        if result["ok"] and result.get("required") and result["source_family"] in REQUIRED_FAMILIES
    }
    missing_families = sorted(REQUIRED_FAMILIES - present_required_families)
    failed = [result for result in results if not result["ok"]]
    report = {
        "schema_version": "1.0",
        "report_kind": "forja_alpha_snapshot_restore_verification",
        "recorded_at": args.recorded_at or utc_now(),
        "manifest": {
            "path": args.manifest.name,
            "sha256": sha256_file(args.manifest),
            "snapshot_root": str(root),
            "snapshot_count": len(results),
        },
        "coverage": {
            "required_families": sorted(REQUIRED_FAMILIES),
            "present_required_families": sorted(present_required_families),
            "missing_required_families": missing_families,
        },
        "results": results,
        "verified": not failed and not missing_families,
    }
    return report, 0 if report["verified"] else 2


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manifest", type=Path, required=True, help="Alpha source snapshot manifest JSON.")
    parser.add_argument("--snapshot-root", type=Path, required=True, help="Root directory containing restored snapshots.")
    parser.add_argument("--output", type=Path, help="Write verification report JSON to this path.")
    parser.add_argument("--recorded-at", help="Override UTC timestamp for reproducible tests.")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        report, exit_code = build_report(args)
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        report = {
            "schema_version": "1.0",
            "report_kind": "forja_alpha_snapshot_restore_verification",
            "recorded_at": args.recorded_at or utc_now(),
            "verified": False,
            "error": type(exc).__name__,
        }
        exit_code = 2
    body = json.dumps(report, indent=2, sort_keys=True) + "\n"
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(body, encoding="utf-8")
        os.chmod(args.output, 0o600)
    else:
        sys.stdout.write(body)
    return exit_code


if __name__ == "__main__":
    raise SystemExit(main())
