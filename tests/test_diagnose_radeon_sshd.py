"""Tests for the Radeon sshd diagnosis helper."""

from __future__ import annotations

import importlib.util
import sys
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT_PATH = ROOT / "scripts" / "diagnose_radeon_sshd.py"


def load_module(path: Path, name: str):
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Unable to load {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


DIAGNOSE = load_module(SCRIPT_PATH, "diagnose_radeon_sshd")


class RadeonSSHDiagnosisTests(unittest.TestCase):
    def setUp(self) -> None:
        self.original_run_command = DIAGNOSE.run_command
        self.original_file_exists = DIAGNOSE.file_exists
        self.original_host_keys_present = DIAGNOSE.host_keys_present

    def tearDown(self) -> None:
        DIAGNOSE.run_command = self.original_run_command
        DIAGNOSE.file_exists = self.original_file_exists
        DIAGNOSE.host_keys_present = self.original_host_keys_present

    def test_ready_when_sshd_process_and_port_are_available(self) -> None:
        configure_fake_host(sshd_installed=True, process=True, port=True, run_dir=True)

        report, exit_code = DIAGNOSE.diagnose()

        self.assertEqual(0, exit_code)
        self.assertTrue(report["ready"])
        self.assertFalse(report["next_sprint_authorized"])
        self.assertIn("rerun preflight_radeon_ssh.py", report["next_action"])

    def test_missing_sshd_recommends_install(self) -> None:
        configure_fake_host(sshd_installed=False, process=False, port=False, run_dir=True)

        report, exit_code = DIAGNOSE.diagnose()

        self.assertEqual(2, exit_code)
        self.assertFalse(report["ready"])
        self.assertEqual("Install openssh-server in the Radeon web terminal, then rerun this diagnosis.", report["next_action"])
        self.assertIn("apt-get install -y openssh-server", report["suggested_commands"])

    def test_missing_runtime_directory_recommends_run_sshd_dir(self) -> None:
        configure_fake_host(sshd_installed=True, process=False, port=False, run_dir=False)

        report, exit_code = DIAGNOSE.diagnose()

        self.assertEqual(2, exit_code)
        self.assertFalse(report["checks"]["run_sshd_dir_exists"])
        self.assertIn("Create /run/sshd", report["next_action"])
        self.assertIn("mkdir -p /run/sshd", report["suggested_commands"])

    def test_installed_but_stopped_recommends_start(self) -> None:
        configure_fake_host(sshd_installed=True, process=False, port=False, run_dir=True)

        report, exit_code = DIAGNOSE.diagnose()

        self.assertEqual(2, exit_code)
        self.assertIn("Start sshd", report["next_action"])
        self.assertIn("systemctl restart ssh || service ssh restart || /usr/sbin/sshd", report["suggested_commands"])


def configure_fake_host(*, sshd_installed: bool, process: bool, port: bool, run_dir: bool) -> None:
    def fake_run_command(argv: list[str], timeout: float = 5.0):
        command = " ".join(argv)
        if "command -v sshd" in command:
            return report("/usr/sbin/sshd" if sshd_installed else "", 0 if sshd_installed else 1)
        if "ps -ef" in command:
            return report("root 1 0 00:00 ? 00:00:00 sshd: /usr/sbin/sshd -D" if process else "", 0 if process else 1)
        if "ss -ltnp" in command:
            return report("LISTEN 0 4096 0.0.0.0:22 0.0.0.0:* users:((\"sshd\",pid=1,fd=3))" if port else "", 0 if port else 1)
        if "command -v systemctl" in command:
            return report("/usr/bin/systemctl", 0)
        if "command -v service" in command:
            return report("/usr/sbin/service", 0)
        return report("root", 0)

    def fake_file_exists(path: str) -> bool:
        if path == "/run/sshd":
            return run_dir
        return True

    DIAGNOSE.run_command = fake_run_command
    DIAGNOSE.file_exists = fake_file_exists
    DIAGNOSE.host_keys_present = lambda: True


def report(stdout: str, exit_code: int) -> dict[str, object]:
    return {"available": True, "exit_code": exit_code, "stdout": stdout, "stderr": "", "timed_out": False}


if __name__ == "__main__":
    unittest.main()
