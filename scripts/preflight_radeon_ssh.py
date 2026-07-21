#!/usr/bin/env python3
"""Run the Radeon SSH wait and render recovery guidance when needed."""

from __future__ import annotations

import argparse
import importlib.util
import json
import sys
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parents[1]
WAIT_SCRIPT = ROOT / "scripts" / "wait_radeon_ssh.py"
RECOVERY_SCRIPT = ROOT / "scripts" / "render_radeon_ssh_recovery_sheet.py"
DEFAULT_WAIT_OUTPUT = Path("/tmp/forja-radeon-ssh-wait.json")
DEFAULT_RECOVERY_OUTPUT = Path("/tmp/forja-radeon-ssh-recovery.md")


def load_module(path: Path, name: str):
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Unable to load {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


WAIT = load_module(WAIT_SCRIPT, "wait_radeon_ssh_for_preflight")
RECOVERY = load_module(RECOVERY_SCRIPT, "render_radeon_ssh_recovery_sheet_for_preflight")


def write_text(path: Path, body: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(body, encoding="utf-8")


def write_json(path: Path, payload: dict[str, Any]) -> None:
    write_text(path, json.dumps(payload, indent=2, sort_keys=True) + "\n")


def preflight(args: argparse.Namespace) -> tuple[dict[str, Any], int]:
    wait_report, wait_exit = WAIT.wait_for_ssh(
        host=args.host,
        port=args.port,
        timeout_seconds=args.timeout_seconds,
        interval_seconds=args.interval_seconds,
        probe_timeout_seconds=args.probe_timeout_seconds,
    )
    write_json(args.wait_output, wait_report)
    ready = wait_exit == 0
    recovery_rendered = False
    if not ready:
        recovery_body = RECOVERY.render_sheet(
            wait_report=wait_report,
            host=args.host,
            port=str(args.port),
            repo_url=args.repo_url,
            branch=args.branch,
            repo_dir=args.repo_dir,
        )
        write_text(args.recovery_output, recovery_body)
        recovery_rendered = True
    report = {
        "schema_version": "1.0",
        "report_kind": "radeon_ssh_preflight",
        "host": args.host,
        "port": args.port,
        "ready": ready,
        "wait_output": args.wait_output.as_posix(),
        "recovery_rendered": recovery_rendered,
        "recovery_output": args.recovery_output.as_posix() if recovery_rendered else None,
        "recovery_repo": {
            "repo_url": args.repo_url,
            "branch": args.branch,
            "repo_dir": args.repo_dir,
        },
        "last_status": wait_report.get("last_result", {}).get("status")
        if isinstance(wait_report.get("last_result"), dict)
        else None,
        "next_action": wait_report.get("next_action"),
        "next_sprint_authorized": False,
    }
    return report, wait_exit


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("host")
    parser.add_argument("port", type=int)
    parser.add_argument("--timeout-seconds", type=float, default=180.0)
    parser.add_argument("--interval-seconds", type=float, default=10.0)
    parser.add_argument("--probe-timeout-seconds", type=float, default=8.0)
    parser.add_argument("--wait-output", type=Path, default=DEFAULT_WAIT_OUTPUT)
    parser.add_argument("--recovery-output", type=Path, default=DEFAULT_RECOVERY_OUTPUT)
    parser.add_argument("--repo-url", default=RECOVERY.DEFAULT_REPO)
    parser.add_argument("--branch", default=RECOVERY.DEFAULT_BRANCH)
    parser.add_argument("--repo-dir", default=RECOVERY.DEFAULT_REPO_DIR)
    parser.add_argument("--output", type=Path)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        report, exit_code = preflight(args)
    except (OSError, RuntimeError, ValueError, json.JSONDecodeError) as exc:
        report = {
            "schema_version": "1.0",
            "report_kind": "radeon_ssh_preflight",
            "ready": False,
            "error": type(exc).__name__,
            "message": str(exc),
            "next_sprint_authorized": False,
        }
        exit_code = 2
    body = json.dumps(report, indent=2, sort_keys=True) + "\n"
    if args.output:
        write_text(args.output, body)
    else:
        sys.stdout.write(body)
    return exit_code


if __name__ == "__main__":
    raise SystemExit(main())
