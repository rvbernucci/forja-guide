#!/usr/bin/env python3
"""Capture a sanitized AMD Radeon Cloud runtime receipt for Sprint 10."""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import os
import platform
import subprocess
import sys
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parents[1]
SENSITIVE_ENV_KEYS = (
    "FORJA_DATABASE_URL",
    "FORJA_QDRANT_API_KEY",
    "FORJA_S3_BUCKET",
    "FORJA_S3_ENDPOINT",
    "AWS_REGION",
    "AWS_ACCESS_KEY_ID",
    "AWS_SECRET_ACCESS_KEY",
    "AWS_SESSION_TOKEN",
    "HF_TOKEN",
    "HUGGING_FACE_HUB_TOKEN",
)


def utc_now() -> str:
    """Return second-precision UTC for stable public evidence."""
    return dt.datetime.now(dt.UTC).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def digest(value: str) -> str:
    """Hash command output without needing to preserve the full body."""
    return hashlib.sha256(value.encode("utf-8", errors="replace")).hexdigest()


def excerpt(value: str, limit: int = 1200) -> str:
    """Keep receipts small and avoid accidentally turning logs into artifacts."""
    cleaned = "\n".join(line.rstrip() for line in value.splitlines()[:24]).strip()
    if len(cleaned) <= limit:
        return cleaned
    return cleaned[: limit - 15].rstrip() + "\n...[truncated]"


def run_command(args: list[str], timeout: float = 8.0) -> dict[str, Any]:
    """Run one bounded probe and return content-limited evidence."""
    try:
        result = subprocess.run(
            args,
            cwd=ROOT,
            check=False,
            capture_output=True,
            text=True,
            timeout=timeout,
        )
    except FileNotFoundError:
        return {
            "available": False,
            "exit_code": None,
            "timed_out": False,
            "stdout_sha256": None,
            "stderr_sha256": None,
            "stdout_excerpt": "",
        }
    except subprocess.TimeoutExpired as exc:
        stdout = exc.stdout if isinstance(exc.stdout, str) else ""
        stderr = exc.stderr if isinstance(exc.stderr, str) else ""
        return {
            "available": True,
            "exit_code": None,
            "timed_out": True,
            "stdout_sha256": digest(stdout),
            "stderr_sha256": digest(stderr),
            "stdout_excerpt": excerpt(stdout),
        }
    return {
        "available": True,
        "exit_code": result.returncode,
        "timed_out": False,
        "stdout_sha256": digest(result.stdout),
        "stderr_sha256": digest(result.stderr),
        "stdout_excerpt": excerpt(result.stdout),
    }


def git_metadata() -> dict[str, Any]:
    """Capture source identity without requiring Git to be installed."""
    commit = run_command(["git", "rev-parse", "HEAD"])
    dirty = run_command(["git", "status", "--short"])
    commit_value = commit["stdout_excerpt"].strip()
    if commit["exit_code"] != 0 or len(commit_value) != 40:
        commit_value = None
    dirty_value: bool | None
    if dirty["exit_code"] == 0:
        dirty_value = bool(dirty["stdout_excerpt"].strip())
    else:
        dirty_value = None
    return {"commit": commit_value, "dirty": dirty_value}


def collect_receipt(args: argparse.Namespace) -> dict[str, Any]:
    """Build the sanitized runtime receipt."""
    commands = {
        "uname": run_command(["uname", "-a"]),
        "python": run_command([sys.executable, "--version"]),
        "rocm_smi": run_command(["rocm-smi", "--showproductname", "--showdriverversion"]),
        "rocminfo": run_command(["rocminfo"], timeout=12.0),
        "torch_rocm": run_command(
            [
                sys.executable,
                "-c",
                (
                    "import json, torch; "
                    "print(json.dumps({"
                    "'torch': torch.__version__, "
                    "'hip': getattr(torch.version, 'hip', None), "
                    "'cuda_available': torch.cuda.is_available(), "
                    "'device_count': torch.cuda.device_count(), "
                    "'device_name': torch.cuda.get_device_name(0) if torch.cuda.is_available() else None"
                    "}, sort_keys=True))"
                ),
            ],
            timeout=12.0,
        ),
        "vllm": run_command(
            [
                sys.executable,
                "-c",
                "import importlib.metadata as m; print(m.version('vllm'))",
            ]
        ),
    }
    return {
        "schema_version": "1.0",
        "receipt_kind": "radeon_runtime_environment",
        "recorded_at": args.recorded_at or utc_now(),
        "competition_profile": {
            "event": "AMD AI DevMaster Hackathon 2026-07",
            "track": "Track 2: Agentic AI",
            "core_remote_inference_allowed": False,
            "recommended_base_image": args.base_image,
            "storage_profile": args.storage_profile,
            "ssh_profile": args.ssh_profile,
        },
        "host": {
            "system": platform.system(),
            "machine": platform.machine(),
            "python_version": platform.python_version(),
        },
        "git": git_metadata(),
        "environment_presence": {key: key in os.environ for key in SENSITIVE_ENV_KEYS},
        "commands": commands,
        "checks": {
            "rocm_command_available": commands["rocm_smi"]["available"] or commands["rocminfo"]["available"],
            "gpu_probe_available": commands["rocm_smi"]["exit_code"] == 0 or commands["rocminfo"]["exit_code"] == 0,
            "torch_rocm_probe_available": commands["torch_rocm"]["exit_code"] == 0,
            "vllm_probe_available": commands["vllm"]["exit_code"] == 0,
        },
    }


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, help="Write receipt JSON to this path.")
    parser.add_argument("--recorded-at", help="Override UTC timestamp for reproducible tests.")
    parser.add_argument(
        "--base-image",
        default="GH-proxy-stable (amd-oneclick-base:git-proxy-test-20260528-1125)",
        help="Radeon Cloud template base image label.",
    )
    parser.add_argument(
        "--storage-profile",
        choices=("persistent_pvc", "local_ssd_ephemeral", "unknown"),
        default="persistent_pvc",
    )
    parser.add_argument(
        "--ssh-profile",
        choices=("enabled", "disabled", "unknown"),
        default="enabled",
    )
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    payload = collect_receipt(args)
    body = json.dumps(payload, indent=2, sort_keys=True) + "\n"
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(body, encoding="utf-8")
        os.chmod(args.output, 0o600)
    else:
        sys.stdout.write(body)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
