"""Tests for the Radeon SSH wait helper."""

from __future__ import annotations

import importlib.util
import sys
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT_PATH = ROOT / "scripts" / "wait_radeon_ssh.py"


def load_module(path: Path, name: str):
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Unable to load {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


WAIT = load_module(SCRIPT_PATH, "wait_radeon_ssh")


class FakeClock:
    def __init__(self) -> None:
        self.value = 0.0

    def now(self) -> float:
        return self.value

    def sleep(self, seconds: float) -> None:
        self.value += seconds


class RadeonSSHWaitTests(unittest.TestCase):
    def tearDown(self) -> None:
        WAIT.PROBE.probe = self.original_probe

    def setUp(self) -> None:
        self.original_probe = WAIT.PROBE.probe

    def test_wait_succeeds_after_transient_no_banner(self) -> None:
        clock = FakeClock()
        results = [
            WAIT.PROBE.ProbeResult(
                host="example.test",
                port=22,
                status="connected_no_banner",
                ssh_banner_seen=False,
                detail="tcp_connected_but_no_banner_before_timeout",
            ),
            WAIT.PROBE.ProbeResult(
                host="example.test",
                port=22,
                status="ready",
                ssh_banner_seen=True,
                detail="SSH-2.0-Test",
            ),
        ]

        def fake_probe(host: str, port: int, timeout: float):
            return results.pop(0)

        WAIT.PROBE.probe = fake_probe

        report, exit_code = WAIT.wait_for_ssh(
            host="example.test",
            port=22,
            timeout_seconds=30,
            interval_seconds=5,
            probe_timeout_seconds=2,
            now=clock.now,
            sleep=clock.sleep,
        )

        self.assertEqual(0, exit_code)
        self.assertTrue(report["ready"])
        self.assertEqual(2, report["attempt_count"])
        self.assertEqual("ready", report["last_result"]["status"])
        self.assertIn("operator command sheet", report["next_action"])
        self.assertTrue(report["operator_hints"])

    def test_wait_times_out_without_ready_banner(self) -> None:
        clock = FakeClock()

        def fake_probe(host: str, port: int, timeout: float):
            return WAIT.PROBE.ProbeResult(
                host=host,
                port=port,
                status="connected_no_banner",
                ssh_banner_seen=False,
                detail="tcp_connected_but_no_banner_before_timeout",
            )

        WAIT.PROBE.probe = fake_probe

        report, exit_code = WAIT.wait_for_ssh(
            host="example.test",
            port=22,
            timeout_seconds=11,
            interval_seconds=5,
            probe_timeout_seconds=2,
            now=clock.now,
            sleep=clock.sleep,
        )

        self.assertEqual(2, exit_code)
        self.assertFalse(report["ready"])
        self.assertEqual(4, report["attempt_count"])
        self.assertEqual("connected_no_banner", report["last_result"]["status"])
        self.assertIn("start or install sshd", report["next_action"])
        self.assertTrue(
            any("ps -ef" in hint for hint in report["operator_hints"]),
            report["operator_hints"],
        )

    def test_refused_endpoint_reports_template_hint(self) -> None:
        clock = FakeClock()

        def fake_probe(host: str, port: int, timeout: float):
            return WAIT.PROBE.ProbeResult(
                host=host,
                port=port,
                status="refused",
                ssh_banner_seen=False,
                detail="tcp_connection_refused",
            )

        WAIT.PROBE.probe = fake_probe

        report, exit_code = WAIT.wait_for_ssh(
            host="example.test",
            port=22,
            timeout_seconds=1,
            interval_seconds=5,
            probe_timeout_seconds=2,
            now=clock.now,
            sleep=clock.sleep,
        )

        self.assertEqual(2, exit_code)
        self.assertIn("SSH toggle", report["next_action"])
        self.assertTrue(any("public key" in hint for hint in report["operator_hints"]))

    def test_rejects_invalid_timing(self) -> None:
        with self.assertRaisesRegex(ValueError, "timeout_seconds"):
            WAIT.wait_for_ssh(
                host="example.test",
                port=22,
                timeout_seconds=0,
                interval_seconds=5,
                probe_timeout_seconds=2,
            )


if __name__ == "__main__":
    unittest.main()
