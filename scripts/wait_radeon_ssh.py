#!/usr/bin/env python3
"""Wait for a Radeon Cloud SSH endpoint to expose an SSH banner."""

from __future__ import annotations

import argparse
import importlib.util
import json
import sys
import time
from dataclasses import asdict
from pathlib import Path
from typing import Callable


ROOT = Path(__file__).resolve().parents[1]
PROBE_SCRIPT = ROOT / "scripts" / "probe_radeon_ssh.py"


def load_probe_module():
    spec = importlib.util.spec_from_file_location("probe_radeon_ssh_for_wait", PROBE_SCRIPT)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Unable to load {PROBE_SCRIPT}")
    module = importlib.util.module_from_spec(spec)
    sys.modules["probe_radeon_ssh_for_wait"] = module
    spec.loader.exec_module(module)
    return module


PROBE = load_probe_module()


def wait_for_ssh(
    *,
    host: str,
    port: int,
    timeout_seconds: float,
    interval_seconds: float,
    probe_timeout_seconds: float,
    now: Callable[[], float] = time.monotonic,
    sleep: Callable[[float], None] = time.sleep,
) -> tuple[dict[str, object], int]:
    if timeout_seconds <= 0:
        raise ValueError("timeout_seconds must be positive")
    if interval_seconds <= 0:
        raise ValueError("interval_seconds must be positive")
    if probe_timeout_seconds <= 0:
        raise ValueError("probe_timeout_seconds must be positive")

    started = now()
    deadline = started + timeout_seconds
    attempts = []
    exit_code = 2

    while True:
        attempt_number = len(attempts) + 1
        result = PROBE.probe(host, port, probe_timeout_seconds)
        attempts.append(
            {
                "attempt": attempt_number,
                "elapsed_seconds": round(max(0.0, now() - started), 3),
                "result": asdict(result),
            }
        )
        if result.status == "ready":
            exit_code = 0
            break
        remaining = deadline - now()
        if remaining <= 0:
            break
        sleep(min(interval_seconds, remaining))

    last = attempts[-1]["result"] if attempts else None
    report = {
        "schema_version": "1.0",
        "report_kind": "radeon_ssh_wait",
        "host": host,
        "port": port,
        "ready": exit_code == 0,
        "attempt_count": len(attempts),
        "timeout_seconds": timeout_seconds,
        "interval_seconds": interval_seconds,
        "probe_timeout_seconds": probe_timeout_seconds,
        "last_result": last,
        "attempts": attempts,
    }
    return report, exit_code


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("host")
    parser.add_argument("port", type=int)
    parser.add_argument("--timeout-seconds", type=float, default=180.0)
    parser.add_argument("--interval-seconds", type=float, default=10.0)
    parser.add_argument("--probe-timeout-seconds", type=float, default=8.0)
    parser.add_argument("--output", type=Path)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        report, exit_code = wait_for_ssh(
            host=args.host,
            port=args.port,
            timeout_seconds=args.timeout_seconds,
            interval_seconds=args.interval_seconds,
            probe_timeout_seconds=args.probe_timeout_seconds,
        )
    except ValueError as exc:
        print(f"Radeon SSH wait rejected: {exc}", file=sys.stderr)
        return 2
    body = json.dumps(report, indent=2, sort_keys=True) + "\n"
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(body, encoding="utf-8")
    else:
        sys.stdout.write(body)
    return exit_code


if __name__ == "__main__":
    raise SystemExit(main())
