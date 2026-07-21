#!/usr/bin/env python3
"""Build a Forja Alpha source snapshot manifest from local preserved files."""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import mimetypes
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


def utc_now() -> str:
    return dt.datetime.now(dt.UTC).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def safe_relative_path(root: Path, logical_path: str) -> Path:
    candidate = Path(logical_path)
    if candidate.is_absolute():
        raise ValueError(f"snapshot path must be relative: {logical_path}")
    resolved_root = root.resolve()
    resolved = (resolved_root / candidate).resolve()
    try:
        inside = resolved.is_relative_to(resolved_root)
    except AttributeError:
        inside = resolved == resolved_root or resolved_root in resolved.parents
    if not inside:
        raise ValueError(f"snapshot path escapes root: {logical_path}")
    return resolved


def parse_spec(value: str) -> tuple[str, str]:
    if "=" not in value:
        raise ValueError(f"snapshot spec must be source_family=relative/path: {value}")
    family, logical_path = value.split("=", 1)
    family = family.strip()
    logical_path = logical_path.strip()
    if family not in ALLOWED_FAMILIES:
        raise ValueError(f"unsupported source family: {family}")
    if not logical_path:
        raise ValueError(f"missing snapshot path for family: {family}")
    return family, logical_path


def build_snapshot(root: Path, spec: str, *, required: bool) -> dict[str, Any]:
    family, logical_path = parse_spec(spec)
    path = safe_relative_path(root, logical_path)
    if not path.is_file():
        raise FileNotFoundError(f"snapshot file does not exist: {logical_path}")
    media_type = mimetypes.guess_type(path.name)[0] or "application/octet-stream"
    return {
        "source_family": family,
        "logical_path": logical_path,
        "sha256": sha256_file(path),
        "size_bytes": path.stat().st_size,
        "media_type": media_type,
        "required": required,
    }


def build_manifest(args: argparse.Namespace) -> tuple[dict[str, Any], int]:
    snapshots = [
        build_snapshot(args.snapshot_root, spec, required=True)
        for spec in args.required_snapshot
    ]
    snapshots.extend(
        build_snapshot(args.snapshot_root, spec, required=False)
        for spec in args.optional_snapshot
    )
    present_required = {
        snapshot["source_family"]
        for snapshot in snapshots
        if snapshot["required"] and snapshot["source_family"] in REQUIRED_FAMILIES
    }
    missing_required = sorted(REQUIRED_FAMILIES - present_required)
    duplicate_keys: list[str] = []
    seen: set[tuple[str, str]] = set()
    for snapshot in snapshots:
        key = (snapshot["source_family"], snapshot["logical_path"])
        if key in seen:
            duplicate_keys.append(f"{key[0]}={key[1]}")
        seen.add(key)
    manifest = {
        "schema_version": "1.0",
        "manifest_kind": "forja_alpha_source_snapshots",
        "generated_at": args.recorded_at or utc_now(),
        "snapshot_root": str(args.snapshot_root),
        "required_families": sorted(REQUIRED_FAMILIES),
        "missing_required_families": missing_required,
        "snapshots": snapshots,
    }
    if duplicate_keys:
        manifest["duplicate_snapshots"] = sorted(duplicate_keys)
    verified = not missing_required and not duplicate_keys
    return manifest, 0 if verified else 2


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--snapshot-root", type=Path, required=True)
    parser.add_argument("--required-snapshot", action="append", default=[])
    parser.add_argument("--optional-snapshot", action="append", default=[])
    parser.add_argument("--output", type=Path)
    parser.add_argument("--recorded-at")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        manifest, exit_code = build_manifest(args)
    except (OSError, ValueError) as exc:
        manifest = {
            "schema_version": "1.0",
            "manifest_kind": "forja_alpha_source_snapshots",
            "generated_at": args.recorded_at or utc_now(),
            "verified": False,
            "error": type(exc).__name__,
            "message": str(exc),
        }
        exit_code = 2
    body = json.dumps(manifest, indent=2, sort_keys=True) + "\n"
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(body, encoding="utf-8")
        os.chmod(args.output, 0o600)
    else:
        sys.stdout.write(body)
    return exit_code


if __name__ == "__main__":
    raise SystemExit(main())
