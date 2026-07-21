"""Tests for the Radeon SSH endpoint probe."""

from __future__ import annotations

import importlib.util
import sys
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT_PATH = ROOT / "scripts" / "probe_radeon_ssh.py"


def load_module(path: Path, name: str):
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Unable to load {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


PROBE = load_module(SCRIPT_PATH, "probe_radeon_ssh")


class RadeonSSHProbeTests(unittest.TestCase):
    def test_ready_when_ssh_banner_is_seen(self) -> None:
        result = PROBE.classify_banner("127.0.0.1", 22, b"SSH-2.0-Test\r\n")

        self.assertEqual("ready", result.status)
        self.assertTrue(result.ssh_banner_seen)

    def test_unexpected_banner_is_not_ready(self) -> None:
        result = PROBE.classify_banner("127.0.0.1", 22, b"HTTP/1.1 200 OK\r\n")

        self.assertEqual("unexpected_banner", result.status)
        self.assertFalse(result.ssh_banner_seen)


if __name__ == "__main__":
    unittest.main()
