"""Tests for the Radeon SSH preflight wrapper."""

from __future__ import annotations

import argparse
import importlib.util
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT_PATH = ROOT / "scripts" / "preflight_radeon_ssh.py"


def load_module(path: Path, name: str):
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Unable to load {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


PREFLIGHT = load_module(SCRIPT_PATH, "preflight_radeon_ssh")


class RadeonSSHPreflightTests(unittest.TestCase):
    def setUp(self) -> None:
        self.original_wait = PREFLIGHT.WAIT.wait_for_ssh

    def tearDown(self) -> None:
        PREFLIGHT.WAIT.wait_for_ssh = self.original_wait

    def test_ready_endpoint_writes_wait_report_without_recovery_sheet(self) -> None:
        def fake_wait(**kwargs):
            return (
                {
                    "schema_version": "1.0",
                    "report_kind": "radeon_ssh_wait",
                    "ready": True,
                    "last_result": {"status": "ready"},
                    "next_action": "Open SSH and run the Sprint 10 Radeon operator command sheet.",
                },
                0,
            )

        PREFLIGHT.WAIT.wait_for_ssh = fake_wait
        with tempfile.TemporaryDirectory() as directory:
            args = args_for(Path(directory))

            report, exit_code = PREFLIGHT.preflight(args)

            self.assertEqual(0, exit_code)
            self.assertTrue(report["ready"])
            self.assertFalse(report["recovery_rendered"])
            self.assertIsNone(report["recovery_output"])
            self.assertFalse(report["next_sprint_authorized"])
            self.assertTrue(args.wait_output.is_file())
            self.assertFalse(args.recovery_output.exists())

    def test_not_ready_endpoint_writes_recovery_sheet(self) -> None:
        def fake_wait(**kwargs):
            return (
                {
                    "schema_version": "1.0",
                    "report_kind": "radeon_ssh_wait",
                    "ready": False,
                    "last_result": {"status": "connected_no_banner"},
                    "next_action": "TCP is reachable but SSH did not send a banner.",
                },
                2,
            )

        PREFLIGHT.WAIT.wait_for_ssh = fake_wait
        with tempfile.TemporaryDirectory() as directory:
            args = args_for(Path(directory))

            report, exit_code = PREFLIGHT.preflight(args)

            self.assertEqual(2, exit_code)
            self.assertFalse(report["ready"])
            self.assertTrue(report["recovery_rendered"])
            self.assertEqual(args.recovery_output.as_posix(), report["recovery_output"])
            self.assertEqual("connected_no_banner", report["last_status"])
            self.assertEqual("https://github.com/example/repo", report["recovery_repo"]["repo_url"])
            self.assertEqual("feature/demo", report["recovery_repo"]["branch"])
            self.assertEqual("/workspace/demo", report["recovery_repo"]["repo_dir"])
            self.assertFalse(report["next_sprint_authorized"])
            self.assertTrue(args.wait_output.is_file())
            body = args.recovery_output.read_text(encoding="utf-8")
            self.assertIn("Radeon SSH Recovery Sheet", body)
            self.assertIn("git checkout feature/demo", body)


def args_for(directory: Path) -> argparse.Namespace:
    return argparse.Namespace(
        host="example.test",
        port=2222,
        timeout_seconds=1.0,
        interval_seconds=1.0,
        probe_timeout_seconds=1.0,
        wait_output=directory / "wait.json",
        recovery_output=directory / "recovery.md",
        repo_url="https://github.com/example/repo",
        branch="feature/demo",
        repo_dir="/workspace/demo",
        output=None,
    )


if __name__ == "__main__":
    unittest.main()
