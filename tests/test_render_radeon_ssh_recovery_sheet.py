"""Tests for the Radeon SSH recovery sheet renderer."""

from __future__ import annotations

import importlib.util
import json
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT_PATH = ROOT / "scripts" / "render_radeon_ssh_recovery_sheet.py"


def load_module(path: Path, name: str):
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Unable to load {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


RECOVERY = load_module(SCRIPT_PATH, "render_radeon_ssh_recovery_sheet")


class RadeonSSHRecoverySheetTests(unittest.TestCase):
    def test_sheet_contains_web_terminal_recovery_flow(self) -> None:
        body = RECOVERY.render_sheet(
            wait_report=connected_no_banner_report(),
            host="36.150.116.206",
            port="31200",
        )

        expected = [
            "Observed wait status: `connected_no_banner`",
            "git clone https://github.com/rvbernucci/forja-guide .",
            "git checkout feat/sprint-10-radeon-runtime-v2",
            "diagnose_radeon_sshd.py",
            "command -v sshd",
            "apt-get install -y openssh-server",
            "mkdir -p /run/sshd",
            "ssh-keygen -A",
            "systemctl restart ssh",
            "wait_radeon_ssh.py 36.150.116.206 31200",
            "`\"ready\": true`",
        ]
        for text in expected:
            self.assertIn(text, body)

    def test_sheet_is_public_safe(self) -> None:
        body = RECOVERY.render_sheet(
            wait_report=connected_no_banner_report(),
            host="example.test",
            port="2222",
        )

        for forbidden in ("password=", "token=", "hf_", "AWS_SECRET", "PRIVATE KEY"):
            self.assertNotIn(forbidden, body)
        self.assertIn("contains no credentials", body)

    def test_sheet_uses_supplied_branch_and_paths(self) -> None:
        body = RECOVERY.render_sheet(
            wait_report=connected_no_banner_report(),
            host="example.test",
            port="2222",
            repo_url="https://github.com/example/repo",
            branch="feature/demo",
            repo_dir="/workspace/demo",
        )

        self.assertIn("mkdir -p /workspace/demo", body)
        self.assertIn("git clone https://github.com/example/repo .", body)
        self.assertIn("git checkout feature/demo", body)
        self.assertIn("wait_radeon_ssh.py example.test 2222", body)

    def test_load_wait_report_reads_json_object(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "wait.json"
            path.write_text(json.dumps(connected_no_banner_report()), encoding="utf-8")

            payload = RECOVERY.load_wait_report(path)

        self.assertEqual("connected_no_banner", RECOVERY.wait_status(payload))


def connected_no_banner_report() -> dict[str, object]:
    return {
        "schema_version": "1.0",
        "report_kind": "radeon_ssh_wait",
        "last_result": {
            "status": "connected_no_banner",
            "detail": "tcp_connected_but_no_banner_before_timeout",
        },
    }


if __name__ == "__main__":
    unittest.main()
