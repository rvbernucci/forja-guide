#!/usr/bin/env python3
"""Benchmark local OpenAI-compatible model candidates for Forja Alpha."""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import os
import statistics
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parents[1]
DEFAULT_SYSTEM_PROMPT = (
    "You are Forja Alpha, a private local financial research agent. "
    "Answer in English. Be concise, evidence-grounded, and avoid investment advice."
)


def utc_now() -> str:
    """Return second-precision UTC for stable public evidence."""
    return dt.datetime.now(dt.UTC).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def sha256_text(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8", errors="replace")).hexdigest()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def load_json_object(path: Path, label: str) -> dict[str, Any]:
    payload = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(payload, dict):
        raise ValueError(f"{label} must be a JSON object")
    return payload


def validate_task_set(payload: dict[str, Any]) -> list[dict[str, Any]]:
    if payload.get("schema_version") != "1.0":
        raise ValueError("task set schema_version must be 1.0")
    tasks = payload.get("tasks")
    if not isinstance(tasks, list) or not tasks:
        raise ValueError("task set requires non-empty tasks")
    seen: set[str] = set()
    validated: list[dict[str, Any]] = []
    for index, task in enumerate(tasks):
        if not isinstance(task, dict):
            raise ValueError(f"task {index} must be an object")
        task_id = task.get("task_id")
        prompt = task.get("prompt")
        category = task.get("category")
        max_output_tokens = task.get("max_output_tokens", 160)
        if not isinstance(task_id, str) or not task_id:
            raise ValueError(f"task {index} missing task_id")
        if task_id in seen:
            raise ValueError(f"duplicate task_id {task_id}")
        if not isinstance(prompt, str) or not prompt.strip():
            raise ValueError(f"task {task_id} missing prompt")
        if not isinstance(category, str) or not category:
            raise ValueError(f"task {task_id} missing category")
        if not isinstance(max_output_tokens, int) or max_output_tokens <= 0 or max_output_tokens > 2048:
            raise ValueError(f"task {task_id} invalid max_output_tokens")
        seen.add(task_id)
        validated.append(task)
    return validated


def validate_candidates(payload: dict[str, Any]) -> list[dict[str, str]]:
    candidates = payload.get("candidates")
    if not isinstance(candidates, list) or not candidates:
        raise ValueError("candidate config requires non-empty candidates")
    validated: list[dict[str, str]] = []
    seen: set[str] = set()
    for index, candidate in enumerate(candidates):
        if not isinstance(candidate, dict):
            raise ValueError(f"candidate {index} must be an object")
        candidate_id = candidate.get("candidate_id")
        base_url = candidate.get("base_url")
        model = candidate.get("model")
        if not isinstance(candidate_id, str) or not candidate_id:
            raise ValueError(f"candidate {index} missing candidate_id")
        if candidate_id in seen:
            raise ValueError(f"duplicate candidate_id {candidate_id}")
        if not isinstance(base_url, str) or not base_url:
            raise ValueError(f"candidate {candidate_id} missing base_url")
        if not isinstance(model, str) or not model:
            raise ValueError(f"candidate {candidate_id} missing model")
        boundary = sanitize_base_url(base_url)
        if not boundary["loopback"]:
            raise ValueError(f"candidate {candidate_id} base_url is not loopback")
        seen.add(candidate_id)
        validated.append({"candidate_id": candidate_id, "base_url": base_url, "model": model})
    return validated


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


def openai_path(base_url: str, suffix: str) -> str:
    parsed = urllib.parse.urlparse(base_url)
    path = parsed.path.rstrip("/")
    if path.endswith("/v1"):
        next_path = f"{path}/{suffix.lstrip('/')}"
    else:
        next_path = f"{path}/v1/{suffix.lstrip('/')}"
    return urllib.parse.urlunparse((parsed.scheme, parsed.netloc, next_path, "", "", ""))


def post_json(url: str, payload: dict[str, Any], timeout: float) -> tuple[int | None, dict[str, Any] | None, str | None]:
    body = json.dumps(payload).encode("utf-8")
    request = urllib.request.Request(
        url,
        data=body,
        method="POST",
        headers={"Content-Type": "application/json", "Accept": "application/json"},
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


def run_one(candidate: dict[str, str], task: dict[str, Any], timeout: float, system_prompt: str) -> dict[str, Any]:
    request = {
        "model": candidate["model"],
        "messages": [
            {"role": "system", "content": system_prompt},
            {"role": "user", "content": task["prompt"]},
        ],
        "temperature": 0,
        "max_tokens": task.get("max_output_tokens", 160),
    }
    started = time.perf_counter()
    status, payload, error = post_json(openai_path(candidate["base_url"], "chat/completions"), request, timeout)
    latency_ms = round((time.perf_counter() - started) * 1000, 3)
    content = ""
    finish_reason = None
    usage = {}
    if isinstance(payload, dict):
        choices = payload.get("choices")
        if isinstance(choices, list) and choices:
            first = choices[0]
            if isinstance(first, dict):
                message = first.get("message")
                if isinstance(message, dict) and isinstance(message.get("content"), str):
                    content = message["content"]
                finish_reason = first.get("finish_reason")
        if isinstance(payload.get("usage"), dict):
            usage = {
                key: value
                for key, value in payload["usage"].items()
                if key in {"prompt_tokens", "completion_tokens", "total_tokens"} and isinstance(value, int)
            }
    return {
        "task_id": task["task_id"],
        "category": task["category"],
        "status_code": status,
        "ok": status == 200 and bool(content.strip()),
        "latency_ms": latency_ms,
        "finish_reason": finish_reason,
        "response_sha256": sha256_text(content) if content else None,
        "response_chars": len(content),
        "usage": usage,
        "error": error,
    }


def summarize(results: list[dict[str, Any]]) -> dict[str, Any]:
    latencies = [result["latency_ms"] for result in results if result["ok"]]
    total_tokens = [
        result["usage"]["total_tokens"]
        for result in results
        if isinstance(result.get("usage"), dict) and isinstance(result["usage"].get("total_tokens"), int)
    ]
    return {
        "task_count": len(results),
        "ok_count": sum(1 for result in results if result["ok"]),
        "failed_count": sum(1 for result in results if not result["ok"]),
        "mean_latency_ms": round(statistics.mean(latencies), 3) if latencies else None,
        "max_latency_ms": max(latencies) if latencies else None,
        "mean_total_tokens": round(statistics.mean(total_tokens), 3) if total_tokens else None,
        "max_total_tokens": max(total_tokens) if total_tokens else None,
    }


def build_report(args: argparse.Namespace) -> tuple[dict[str, Any], int]:
    task_set = load_json_object(args.task_set, "task set")
    candidate_config = load_json_object(args.candidates, "candidate config")
    tasks = validate_task_set(task_set)
    candidates = validate_candidates(candidate_config)
    candidate_reports = []
    for candidate in candidates:
        results = [
            run_one(candidate, task, args.timeout, args.system_prompt)
            for task in tasks
        ]
        candidate_reports.append(
            {
                "candidate_id": candidate["candidate_id"],
                "model_sha256": sha256_text(candidate["model"]),
                "endpoint": sanitize_base_url(candidate["base_url"]),
                "summary": summarize(results),
                "results": results,
            }
        )
    report = {
        "schema_version": "1.0",
        "report_kind": "radeon_model_candidate_benchmark",
        "recorded_at": args.recorded_at or utc_now(),
        "task_set": {
            "task_set_id": task_set.get("task_set_id"),
            "split": task_set.get("split"),
            "sha256": sha256_file(args.task_set),
            "task_count": len(tasks),
        },
        "candidate_config_sha256": sha256_file(args.candidates),
        "candidate_count": len(candidates),
        "privacy": {
            "stores_response_bodies": False,
            "stores_response_hashes": True,
            "requires_loopback_endpoints": True,
        },
        "candidates": candidate_reports,
    }
    failed = any(candidate["summary"]["failed_count"] for candidate in candidate_reports)
    return report, 2 if failed else 0


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--task-set", type=Path, required=True)
    parser.add_argument("--candidates", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--timeout", type=float, default=30.0)
    parser.add_argument("--recorded-at", help="Override UTC timestamp for reproducible tests.")
    parser.add_argument("--system-prompt", default=DEFAULT_SYSTEM_PROMPT)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        report, exit_code = build_report(args)
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        report = {
            "schema_version": "1.0",
            "report_kind": "radeon_model_candidate_benchmark",
            "recorded_at": args.recorded_at or utc_now(),
            "error": type(exc).__name__,
            "candidates": [],
        }
        exit_code = 2
    body = json.dumps(report, indent=2, sort_keys=True) + "\n"
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(body, encoding="utf-8")
    os.chmod(args.output, 0o600)
    return exit_code


if __name__ == "__main__":
    raise SystemExit(main())
