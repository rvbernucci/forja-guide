#!/usr/bin/env python3
"""Benchmark a local OpenAI-compatible embedding endpoint for Forja Alpha."""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import math
import os
import statistics
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from typing import Any


def utc_now() -> str:
    """Return second-precision UTC for stable public evidence."""
    return dt.datetime.now(dt.UTC).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def sha256_text(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8", errors="replace")).hexdigest()


def sha256_vector(vector: list[float]) -> str:
    rounded = [round(value, 10) for value in vector]
    return hashlib.sha256(json.dumps(rounded, separators=(",", ":")).encode("utf-8")).hexdigest()


def host_is_loopback(hostname: str | None) -> bool:
    import socket

    if not hostname:
        return False
    candidate = hostname.strip().lower().rstrip(".")
    if candidate == "localhost":
        return True
    try:
        return socket.inet_pton(socket.AF_INET, candidate).startswith(b"\x7f")
    except OSError:
        pass
    try:
        packed = socket.inet_pton(socket.AF_INET6, candidate)
    except OSError:
        return False
    return packed == b"\x00" * 15 + b"\x01"


def sanitize_base_url(value: str) -> dict[str, Any]:
    parsed = urllib.parse.urlparse(value)
    loopback = parsed.scheme in {"http", "https"} and host_is_loopback(parsed.hostname)
    return {
        "scheme": parsed.scheme or None,
        "host_class": "loopback" if loopback else "remote",
        "path": parsed.path or "/",
        "loopback": loopback,
    }


def openai_embeddings_path(base_url: str) -> str:
    parsed = urllib.parse.urlparse(base_url)
    path = parsed.path.rstrip("/")
    if path.endswith("/v1"):
        next_path = f"{path}/embeddings"
    else:
        next_path = f"{path}/v1/embeddings"
    return urllib.parse.urlunparse((parsed.scheme, parsed.netloc, next_path, "", "", ""))


def load_input_set(path: Path) -> tuple[dict[str, Any], list[dict[str, str]]]:
    payload = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(payload, dict):
        raise ValueError("embedding input set must be a JSON object")
    if payload.get("schema_version") != "1.0":
        raise ValueError("embedding input set schema_version must be 1.0")
    inputs = payload.get("inputs")
    if not isinstance(inputs, list) or not inputs:
        raise ValueError("embedding input set requires non-empty inputs")
    seen: set[str] = set()
    validated: list[dict[str, str]] = []
    for index, item in enumerate(inputs):
        if not isinstance(item, dict):
            raise ValueError(f"input {index} must be an object")
        input_id = item.get("input_id")
        category = item.get("category")
        text = item.get("text")
        if not isinstance(input_id, str) or not input_id:
            raise ValueError(f"input {index} missing input_id")
        if input_id in seen:
            raise ValueError(f"duplicate input_id {input_id}")
        if not isinstance(category, str) or not category:
            raise ValueError(f"input {input_id} missing category")
        if not isinstance(text, str) or not text.strip():
            raise ValueError(f"input {input_id} missing text")
        seen.add(input_id)
        validated.append({"input_id": input_id, "category": category, "text": text})
    return payload, validated


def post_embedding(url: str, model: str, text: str, timeout: float) -> tuple[int | None, dict[str, Any] | None, str | None]:
    request_body = json.dumps({"model": model, "input": text}).encode("utf-8")
    request = urllib.request.Request(
        url,
        data=request_body,
        method="POST",
        headers={"Accept": "application/json", "Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            raw = response.read(2 * 1024 * 1024)
            return response.status, json.loads(raw.decode("utf-8")), None
    except urllib.error.HTTPError as exc:
        return exc.code, None, f"http_{exc.code}"
    except urllib.error.URLError as exc:
        reason = getattr(exc, "reason", exc)
        return None, None, type(reason).__name__
    except (TimeoutError, json.JSONDecodeError, OSError) as exc:
        return None, None, type(exc).__name__


def extract_embedding(payload: dict[str, Any] | None) -> list[float]:
    if not isinstance(payload, dict):
        return []
    data = payload.get("data")
    if not isinstance(data, list) or not data:
        return []
    first = data[0]
    if not isinstance(first, dict):
        return []
    embedding = first.get("embedding")
    if not isinstance(embedding, list):
        return []
    vector: list[float] = []
    for value in embedding:
        if not isinstance(value, (int, float)) or not math.isfinite(float(value)):
            return []
        vector.append(float(value))
    return vector


def run_one(base_url: str, model: str, item: dict[str, str], timeout: float) -> dict[str, Any]:
    started = time.perf_counter()
    status, payload, error = post_embedding(openai_embeddings_path(base_url), model, item["text"], timeout)
    latency_ms = round((time.perf_counter() - started) * 1000, 3)
    vector = extract_embedding(payload)
    norm = math.sqrt(sum(value * value for value in vector)) if vector else None
    return {
        "input_id": item["input_id"],
        "category": item["category"],
        "input_sha256": sha256_text(item["text"]),
        "status_code": status,
        "ok": status == 200 and len(vector) > 0,
        "latency_ms": latency_ms,
        "embedding_dimensions": len(vector),
        "embedding_sha256": sha256_vector(vector) if vector else None,
        "l2_norm": round(norm, 8) if norm is not None else None,
        "error": error,
    }


def summarize(results: list[dict[str, Any]]) -> dict[str, Any]:
    ok_results = [result for result in results if result["ok"]]
    latencies = [result["latency_ms"] for result in ok_results]
    dimensions = sorted({result["embedding_dimensions"] for result in ok_results})
    norms = [result["l2_norm"] for result in ok_results if isinstance(result.get("l2_norm"), (int, float))]
    return {
        "input_count": len(results),
        "ok_count": len(ok_results),
        "failed_count": len(results) - len(ok_results),
        "consistent_dimensions": len(dimensions) == 1 and bool(dimensions),
        "embedding_dimensions": dimensions[0] if len(dimensions) == 1 else None,
        "mean_latency_ms": round(statistics.mean(latencies), 3) if latencies else None,
        "max_latency_ms": max(latencies) if latencies else None,
        "mean_l2_norm": round(statistics.mean(norms), 8) if norms else None,
    }


def build_report(args: argparse.Namespace) -> tuple[dict[str, Any], int]:
    boundary = sanitize_base_url(args.base_url)
    if not boundary["loopback"]:
        raise ValueError("embedding base URL must be loopback")
    input_set, inputs = load_input_set(args.input_set)
    results = [run_one(args.base_url, args.model, item, args.timeout) for item in inputs]
    summary = summarize(results)
    verified = summary["failed_count"] == 0 and summary["consistent_dimensions"]
    report = {
        "schema_version": "1.0",
        "report_kind": "radeon_embedding_benchmark",
        "recorded_at": args.recorded_at or utc_now(),
        "input_set": {
            "input_set_id": input_set.get("input_set_id"),
            "split": input_set.get("split"),
            "sha256": sha256_file(args.input_set),
            "input_count": len(inputs),
        },
        "model_sha256": sha256_text(args.model),
        "endpoint": boundary,
        "privacy": {
            "stores_input_text": False,
            "stores_vectors": False,
            "stores_hashes": True,
            "requires_loopback_endpoint": True,
        },
        "summary": summary,
        "results": results,
        "verified": verified,
    }
    return report, 0 if verified else 2


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input-set", type=Path, required=True)
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--model", required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--timeout", type=float, default=20.0)
    parser.add_argument("--recorded-at", help="Override UTC timestamp for reproducible tests.")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        report, exit_code = build_report(args)
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        report = {
            "schema_version": "1.0",
            "report_kind": "radeon_embedding_benchmark",
            "recorded_at": args.recorded_at or utc_now(),
            "verified": False,
            "error": type(exc).__name__,
        }
        exit_code = 2
    body = json.dumps(report, indent=2, sort_keys=True) + "\n"
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(body, encoding="utf-8")
    os.chmod(args.output, 0o600)
    return exit_code


if __name__ == "__main__":
    raise SystemExit(main())
