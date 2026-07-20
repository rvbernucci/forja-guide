#!/usr/bin/env python3
"""Preflight private Sprint 10 Radeon inputs before GPU evidence is collected."""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from pathlib import Path
from typing import Any
from urllib.parse import urlparse


REQUIRED_SNAPSHOTS = {
    "sec_identity": "sec/company_tickers.json",
    "sec_submissions": "sec/submissions/CIK0001045810.json",
    "sec_company_facts": "sec/companyfacts/CIK0001045810.json",
    "treasury": "treasury/real-yield-10y.csv",
    "fred": "fred/FEDFUNDS.csv",
    "market": "market/NVDA-adjusted.csv",
}
LOOPBACK_HOSTS = {"127.0.0.1", "localhost", "::1"}
PLACEHOLDER = re.compile(r"<[^>]+>")


def is_loopback_url(value: str) -> bool:
    parsed = urlparse(value)
    return parsed.scheme in {"http", "https"} and parsed.hostname in LOOPBACK_HOSTS


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def safe_snapshot_path(root: Path, logical_path: str) -> Path:
    if Path(logical_path).is_absolute():
        raise ValueError(f"snapshot path must be relative: {logical_path}")
    resolved_root = root.resolve()
    resolved = (resolved_root / logical_path).resolve()
    try:
        inside = resolved.is_relative_to(resolved_root)
    except AttributeError:
        inside = resolved == resolved_root or resolved_root in resolved.parents
    if not inside:
        raise ValueError(f"snapshot path escapes root: {logical_path}")
    return resolved


def load_json_object(path: Path) -> dict[str, Any]:
    payload = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(payload, dict):
        raise ValueError(f"JSON document must be an object: {path}")
    return payload


def check_candidate_file(path: Path, errors: list[str]) -> dict[str, Any]:
    if not path.is_file():
        errors.append("missing_model_candidates")
        return {"path": path.as_posix(), "candidate_count": 0}
    try:
        body = path.read_text(encoding="utf-8")
        payload = json.loads(body)
    except (OSError, UnicodeDecodeError, json.JSONDecodeError):
        errors.append("invalid_model_candidates_json")
        return {"path": path.as_posix(), "candidate_count": 0}
    if not isinstance(payload, dict):
        errors.append("model_candidates_shape")
        return {"path": path.as_posix(), "candidate_count": 0}
    if PLACEHOLDER.search(body):
        errors.append("model_candidates_placeholder")
    if payload.get("schema_version") != "1.0":
        errors.append("model_candidates_schema_version")
    rows = payload.get("candidates")
    if not isinstance(rows, list) or len(rows) < 2:
        errors.append("model_candidates_count")
        return {"path": path.as_posix(), "candidate_count": 0}
    seen: set[str] = set()
    for index, row in enumerate(rows):
        if not isinstance(row, dict):
            errors.append(f"candidate_{index}_shape")
            continue
        candidate_id = row.get("candidate_id")
        if not isinstance(candidate_id, str) or not candidate_id:
            errors.append(f"candidate_{index}_id")
        elif candidate_id in seen:
            errors.append(f"candidate_{index}_duplicate_id")
        else:
            seen.add(candidate_id)
        base_url = row.get("base_url")
        if not isinstance(base_url, str) or not is_loopback_url(base_url):
            errors.append(f"candidate_{index}_base_url")
        model = row.get("model")
        if not isinstance(model, str) or not model.strip() or PLACEHOLDER.search(model):
            errors.append(f"candidate_{index}_model")
    return {"path": path.as_posix(), "candidate_count": len(rows), "candidate_ids": sorted(seen)}


def check_snapshots(snapshot_root: Path, errors: list[str]) -> list[dict[str, Any]]:
    snapshots = []
    for family, logical_path in sorted(REQUIRED_SNAPSHOTS.items()):
        try:
            path = safe_snapshot_path(snapshot_root, logical_path)
        except ValueError:
            errors.append(f"snapshot_{family}_path")
            continue
        if not path.is_file():
            errors.append(f"missing_snapshot_{family}")
            snapshots.append(
                {
                    "source_family": family,
                    "logical_path": logical_path,
                    "present": False,
                }
            )
            continue
        snapshots.append(
            {
                "source_family": family,
                "logical_path": logical_path,
                "present": True,
                "size_bytes": path.stat().st_size,
                "sha256": sha256_file(path),
            }
        )
    return snapshots


def check_private_inputs(args: argparse.Namespace) -> tuple[dict[str, Any], int]:
    errors: list[str] = []
    for name, value in (
        ("model_base_url", args.model_base_url),
        ("embedding_base_url", args.embedding_base_url),
    ):
        if not is_loopback_url(value):
            errors.append(f"{name}_not_loopback")
    if not args.embedding_model or PLACEHOLDER.search(args.embedding_model):
        errors.append("embedding_model_placeholder")
    snapshots = check_snapshots(args.snapshot_root, errors)
    candidates = check_candidate_file(args.model_candidates, errors)
    report = {
        "schema_version": "1.0",
        "report_kind": "radeon_sprint10_private_input_preflight",
        "ready_to_run": not errors,
        "snapshot_root": args.snapshot_root.as_posix(),
        "model_base_url": args.model_base_url,
        "embedding_base_url": args.embedding_base_url,
        "embedding_model": args.embedding_model,
        "snapshots": snapshots,
        "model_candidates": candidates,
        "errors": errors,
    }
    return report, 0 if not errors else 2


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--snapshot-root", type=Path, default=Path("/secure/forja"))
    parser.add_argument(
        "--model-candidates",
        type=Path,
        default=Path("/secure/forja/radeon-model-candidates.json"),
    )
    parser.add_argument("--model-base-url", default="http://127.0.0.1:8000/v1")
    parser.add_argument("--embedding-base-url", default="http://127.0.0.1:8081/v1")
    parser.add_argument("--embedding-model", required=True)
    parser.add_argument("--output", type=Path)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        report, exit_code = check_private_inputs(args)
    except (OSError, ValueError) as exc:
        report = {
            "schema_version": "1.0",
            "report_kind": "radeon_sprint10_private_input_preflight",
            "ready_to_run": False,
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
