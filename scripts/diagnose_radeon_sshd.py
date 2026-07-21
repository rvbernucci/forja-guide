#!/usr/bin/env python3
"""Diagnose sshd readiness from inside a Radeon web terminal."""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
from pathlib import Path
from typing import Any


def run_command(argv: list[str], timeout: float = 5.0) -> dict[str, Any]:
    try:
        result = subprocess.run(
            argv,
            check=False,
            capture_output=True,
            text=True,
            timeout=timeout,
        )
    except FileNotFoundError:
        return {"available": False, "exit_code": None, "stdout": "", "stderr": "", "timed_out": False}
    except subprocess.TimeoutExpired as exc:
        return {
            "available": True,
            "exit_code": None,
            "stdout": exc.stdout if isinstance(exc.stdout, str) else "",
            "stderr": exc.stderr if isinstance(exc.stderr, str) else "",
            "timed_out": True,
        }
    return {
        "available": True,
        "exit_code": result.returncode,
        "stdout": result.stdout.strip(),
        "stderr": result.stderr.strip(),
        "timed_out": False,
    }


def file_exists(path: str) -> bool:
    return Path(path).exists()


def host_keys_present() -> bool:
    return any(Path("/etc/ssh").glob("ssh_host_*_key"))


def listening_on_22(ss_report: dict[str, Any]) -> bool:
    return ss_report["exit_code"] == 0 and ":22 " in ss_report["stdout"]


def process_running(process_report: dict[str, Any]) -> bool:
    output = process_report["stdout"]
    return process_report["exit_code"] == 0 and bool(output.strip())


def command_ok(report: dict[str, Any]) -> bool:
    return report["available"] and report["exit_code"] == 0


def diagnose() -> tuple[dict[str, Any], int]:
    commands = {
        "whoami": run_command(["whoami"]),
        "id": run_command(["id"]),
        "sshd_path": run_command(["sh", "-lc", "command -v sshd"]),
        "sshd_process": run_command(["sh", "-lc", "ps -ef | grep '[s]shd'"]),
        "ss_path": run_command(["sh", "-lc", "command -v ss"]),
        "port_22": run_command(["sh", "-lc", "ss -ltnp | grep ':22 '"]),
        "systemctl": run_command(["sh", "-lc", "command -v systemctl"]),
        "service": run_command(["sh", "-lc", "command -v service"]),
    }
    checks = {
        "running_as_root": os.geteuid() == 0 if hasattr(os, "geteuid") else None,
        "sshd_installed": command_ok(commands["sshd_path"]),
        "sshd_process_running": process_running(commands["sshd_process"]),
        "ss_available": command_ok(commands["ss_path"]),
        "port_22_listening": listening_on_22(commands["port_22"]),
        "run_sshd_dir_exists": file_exists("/run/sshd"),
        "host_keys_present": host_keys_present(),
        "systemctl_available": command_ok(commands["systemctl"]),
        "service_available": command_ok(commands["service"]),
    }
    ready = bool(checks["sshd_installed"] and checks["sshd_process_running"] and checks["port_22_listening"])
    report = {
        "schema_version": "1.0",
        "report_kind": "radeon_sshd_diagnosis",
        "ready": ready,
        "checks": checks,
        "next_action": next_action(checks),
        "suggested_commands": suggested_commands(checks),
        "next_sprint_authorized": False,
        "commands": sanitize_commands(commands),
    }
    return report, 0 if ready else 2


def next_action(checks: dict[str, Any]) -> str:
    if checks["sshd_installed"] and checks["sshd_process_running"] and checks["port_22_listening"]:
        return "Return to the workstation and rerun preflight_radeon_ssh.py."
    if not checks["sshd_installed"]:
        return "Install openssh-server and diagnostic network tools in the Radeon web terminal, then rerun this diagnosis."
    if not checks["ss_available"]:
        return "Install iproute2 so the diagnosis can confirm that sshd is listening on port 22."
    if not checks["run_sshd_dir_exists"]:
        return "Create /run/sshd, then start sshd and rerun this diagnosis."
    if not checks["host_keys_present"]:
        return "Generate OpenSSH host keys with ssh-keygen -A, then start sshd."
    if not checks["sshd_process_running"]:
        return "Start sshd using systemctl, service, or /usr/sbin/sshd."
    if not checks["port_22_listening"]:
        return "sshd is running but not listening on port 22; inspect sshd_config and process logs."
    return "Inspect sshd manually, then rerun this diagnosis."


def suggested_commands(checks: dict[str, Any]) -> list[str]:
    commands: list[str] = []
    if not checks["sshd_installed"]:
        commands.extend(
            [
                "apt-get update",
                "DEBIAN_FRONTEND=noninteractive apt-get install -y openssh-server iproute2 procps",
            ]
        )
    elif not checks["ss_available"]:
        commands.append("DEBIAN_FRONTEND=noninteractive apt-get install -y iproute2")
    if not checks["run_sshd_dir_exists"]:
        commands.extend(["mkdir -p /run/sshd", "chmod 755 /run/sshd"])
    if not checks["host_keys_present"]:
        commands.append("ssh-keygen -A")
    if checks["sshd_installed"] and not checks["sshd_process_running"]:
        commands.append("systemctl restart ssh || service ssh restart || /usr/sbin/sshd")
    commands.append("python3 scripts/diagnose_radeon_sshd.py --output /workspace/forja-radeon-sshd-diagnosis.json")
    return commands


def sanitize_commands(commands: dict[str, dict[str, Any]]) -> dict[str, dict[str, Any]]:
    sanitized: dict[str, dict[str, Any]] = {}
    for key, value in commands.items():
        sanitized[key] = {
            "available": value["available"],
            "exit_code": value["exit_code"],
            "timed_out": value["timed_out"],
            "stdout_excerpt": value["stdout"][:500],
            "stderr_excerpt": value["stderr"][:500],
        }
    return sanitized


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        report, exit_code = diagnose()
    except OSError as exc:
        report = {
            "schema_version": "1.0",
            "report_kind": "radeon_sshd_diagnosis",
            "ready": False,
            "error": type(exc).__name__,
            "message": str(exc),
            "next_sprint_authorized": False,
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
