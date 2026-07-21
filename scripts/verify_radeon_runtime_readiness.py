#!/usr/bin/env python3
"""Verify Radeon runtime readiness from sanitized receipt and local endpoints."""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import os
import socket
import sys
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from typing import Any


LOOPBACK_HOSTS = {"localhost"}
REQUIRED_RECEIPT_CHECKS = (
    "rocm_command_available",
    "gpu_probe_available",
    "torch_rocm_probe_available",
    "vllm_probe_available",
)


def utc_now() -> str:
    """Return second-precision UTC for stable public evidence."""
    return dt.datetime.now(dt.UTC).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def sha256_file(path: Path) -> str:
    """Hash evidence without copying private payloads into readiness reports."""
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def load_json(path: Path) -> dict[str, Any]:
    """Load a JSON object from disk."""
    payload = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(payload, dict):
        raise ValueError(f"JSON document is not an object: {path}")
    return payload


def host_is_loopback(hostname: str | None) -> bool:
    """Return true only for explicit loopback hostnames or IP addresses."""
    if not hostname:
        return False
    candidate = hostname.strip().lower().rstrip(".")
    if candidate in LOOPBACK_HOSTS:
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


def sanitize_base_url(value: str | None) -> dict[str, Any]:
    """Parse and classify a base URL without retaining secrets or query strings."""
    if not value:
        return {
            "configured": False,
            "loopback": False,
            "scheme": None,
            "host_class": None,
            "path": None,
            "error": "missing",
        }
    parsed = urllib.parse.urlparse(value)
    if parsed.scheme not in {"http", "https"}:
        return {
            "configured": True,
            "loopback": False,
            "scheme": parsed.scheme or None,
            "host_class": "invalid",
            "path": parsed.path or "/",
            "error": "unsupported_scheme",
        }
    loopback = host_is_loopback(parsed.hostname)
    return {
        "configured": True,
        "loopback": loopback,
        "scheme": parsed.scheme,
        "host_class": "loopback" if loopback else "remote",
        "path": parsed.path or "/",
        "error": None if loopback else "non_loopback_host",
    }


def openai_path(base_url: str, suffix: str) -> str:
    """Build an OpenAI-compatible endpoint URL from a base or /v1 URL."""
    parsed = urllib.parse.urlparse(base_url)
    path = parsed.path.rstrip("/")
    if path.endswith("/v1"):
        next_path = f"{path}/{suffix.lstrip('/')}"
    else:
        next_path = f"{path}/v1/{suffix.lstrip('/')}"
    return urllib.parse.urlunparse(
        (parsed.scheme, parsed.netloc, next_path, "", "", "")
    )


def probe_json(
    *,
    method: str,
    url: str,
    body: dict[str, Any] | None = None,
    timeout: float,
) -> tuple[int | None, dict[str, Any] | None, str | None]:
    """Probe one local JSON endpoint with bounded output."""
    data = None
    headers = {"Accept": "application/json"}
    if body is not None:
        data = json.dumps(body).encode("utf-8")
        headers["Content-Type"] = "application/json"
    request = urllib.request.Request(url, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            raw = response.read(1024 * 1024)
            payload = json.loads(raw.decode("utf-8"))
            return response.status, payload, None
    except urllib.error.HTTPError as exc:
        return exc.code, None, f"http_{exc.code}"
    except urllib.error.URLError as exc:
        reason = getattr(exc, "reason", exc)
        return None, None, type(reason).__name__
    except (TimeoutError, json.JSONDecodeError, OSError) as exc:
        return None, None, type(exc).__name__


def probe_model_endpoint(base_url: str, timeout: float) -> dict[str, Any]:
    """Probe /v1/models and record only readiness-safe details."""
    status, payload, error = probe_json(
        method="GET",
        url=openai_path(base_url, "models"),
        timeout=timeout,
    )
    model_count = 0
    if isinstance(payload, dict) and isinstance(payload.get("data"), list):
        model_count = len(payload["data"])
    return {
        "configured": True,
        "status_code": status,
        "ok": status == 200 and model_count > 0,
        "model_count": model_count,
        "error": error,
    }


def probe_embedding_endpoint(base_url: str, model: str, timeout: float) -> dict[str, Any]:
    """Probe /v1/embeddings and validate the returned vector shape."""
    status, payload, error = probe_json(
        method="POST",
        url=openai_path(base_url, "embeddings"),
        body={"model": model, "input": "forja runtime smoke"},
        timeout=timeout,
    )
    dimensions = 0
    if isinstance(payload, dict):
        data = payload.get("data")
        if isinstance(data, list) and data:
            embedding = data[0].get("embedding") if isinstance(data[0], dict) else None
            if isinstance(embedding, list) and all(isinstance(item, (int, float)) for item in embedding):
                dimensions = len(embedding)
    return {
        "configured": True,
        "status_code": status,
        "ok": status == 200 and dimensions > 0,
        "embedding_dimensions": dimensions,
        "error": error,
    }


def validate_receipt(receipt: dict[str, Any]) -> dict[str, Any]:
    """Validate the minimum receipt fields required for Sprint 10 readiness."""
    checks = receipt.get("checks")
    profile = receipt.get("competition_profile")
    errors: list[str] = []
    if receipt.get("schema_version") != "1.0":
        errors.append("schema_version")
    if receipt.get("receipt_kind") != "radeon_runtime_environment":
        errors.append("receipt_kind")
    if not isinstance(profile, dict):
        errors.append("competition_profile")
    elif profile.get("core_remote_inference_allowed") is not False:
        errors.append("core_remote_inference_allowed")
    if not isinstance(checks, dict):
        errors.append("checks")
        checks = {}
    missing = [name for name in REQUIRED_RECEIPT_CHECKS if name not in checks]
    false_checks = [name for name in REQUIRED_RECEIPT_CHECKS if checks.get(name) is False]
    errors.extend(f"missing_check:{name}" for name in missing)
    return {
        "valid": not errors,
        "errors": errors,
        "required_checks": {name: checks.get(name) for name in REQUIRED_RECEIPT_CHECKS},
        "false_checks": false_checks,
    }


def build_report(args: argparse.Namespace) -> tuple[dict[str, Any], int]:
    """Build readiness report and return the recommended process exit code."""
    receipt = load_json(args.receipt)
    receipt_validation = validate_receipt(receipt)
    model_url = args.model_base_url or os.environ.get("FORJA_ALPHA_MODEL_BASE_URL")
    embedding_url = args.embedding_base_url or os.environ.get("FORJA_ALPHA_EMBEDDING_BASE_URL")
    model_boundary = sanitize_base_url(model_url)
    embedding_boundary = sanitize_base_url(embedding_url)

    model_probe: dict[str, Any] = {"configured": False, "ok": False, "error": "missing"}
    embedding_probe: dict[str, Any] = {"configured": False, "ok": False, "error": "missing"}
    if model_url and model_boundary["loopback"]:
        model_probe = probe_model_endpoint(model_url, args.timeout)
    if embedding_url and embedding_boundary["loopback"]:
        embedding_probe = probe_embedding_endpoint(embedding_url, args.embedding_model, args.timeout)

    endpoints_configured = bool(model_boundary["configured"] and embedding_boundary["configured"])
    endpoints_loopback = bool(model_boundary["loopback"] and embedding_boundary["loopback"])
    probes_ok = bool(model_probe["ok"] and embedding_probe["ok"])
    zero_remote_core_inference_proved = bool(
        receipt_validation["valid"]
        and endpoints_configured
        and endpoints_loopback
        and probes_ok
    )
    ready = zero_remote_core_inference_proved and not receipt_validation["false_checks"]
    exit_code = 0
    if not receipt_validation["valid"] or not endpoints_loopback:
        exit_code = 2
    if args.require_endpoints and not zero_remote_core_inference_proved:
        exit_code = 2

    report = {
        "schema_version": "1.0",
        "report_kind": "radeon_runtime_readiness",
        "recorded_at": args.recorded_at or utc_now(),
        "receipt": {
            "path": args.receipt.name,
            "sha256": sha256_file(args.receipt),
            "valid": receipt_validation["valid"],
            "errors": receipt_validation["errors"],
            "required_checks": receipt_validation["required_checks"],
            "false_checks": receipt_validation["false_checks"],
        },
        "policy": {
            "core_remote_inference_allowed": False,
            "model_endpoint_loopback": model_boundary["loopback"],
            "embedding_endpoint_loopback": embedding_boundary["loopback"],
            "zero_remote_core_inference_proved": zero_remote_core_inference_proved,
        },
        "endpoints": {
            "model": model_boundary,
            "embedding": embedding_boundary,
        },
        "probes": {
            "model": model_probe,
            "embedding": embedding_probe,
        },
        "ready": ready,
    }
    return report, exit_code


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--receipt", type=Path, required=True, help="Sanitized Radeon runtime receipt JSON.")
    parser.add_argument("--output", type=Path, help="Write readiness report JSON to this path.")
    parser.add_argument("--model-base-url", help="Loopback OpenAI-compatible model base URL.")
    parser.add_argument("--embedding-base-url", help="Loopback OpenAI-compatible embedding base URL.")
    parser.add_argument("--embedding-model", default="forja-local-embedding-smoke")
    parser.add_argument("--timeout", type=float, default=5.0)
    parser.add_argument("--recorded-at", help="Override UTC timestamp for reproducible tests.")
    parser.add_argument(
        "--require-endpoints",
        action="store_true",
        help="Fail unless both loopback endpoints are configured and probe successfully.",
    )
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        report, exit_code = build_report(args)
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        report = {
            "schema_version": "1.0",
            "report_kind": "radeon_runtime_readiness",
            "recorded_at": args.recorded_at or utc_now(),
            "ready": False,
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
