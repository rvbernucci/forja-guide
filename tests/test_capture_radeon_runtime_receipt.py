"""Tests for sanitized Radeon runtime receipt capture."""

from __future__ import annotations

import importlib.util
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch


MODULE_PATH = (
    Path(__file__).resolve().parents[1]
    / "scripts"
    / "capture_radeon_runtime_receipt.py"
)
SPEC = importlib.util.spec_from_file_location("capture_radeon_runtime_receipt", MODULE_PATH)
if SPEC is None or SPEC.loader is None:
    raise RuntimeError("Unable to load Radeon runtime receipt module")
RECEIPT = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(RECEIPT)


class RadeonRuntimeReceiptTests(unittest.TestCase):
    def test_missing_command_is_content_limited(self) -> None:
        result = RECEIPT.run_command(["definitely-not-a-forja-command"])

        self.assertFalse(result["available"])
        self.assertIsNone(result["exit_code"])
        self.assertEqual("", result["stdout_excerpt"])

    def test_collect_receipt_records_presence_not_secret_values(self) -> None:
        namespace = type(
            "Args",
            (),
            {
                "recorded_at": "2026-07-20T14:00:00Z",
                "base_image": "GH-proxy-stable",
                "storage_profile": "persistent_pvc",
                "ssh_profile": "enabled",
            },
        )()
        fake_command = {
            "available": True,
            "exit_code": 0,
            "timed_out": False,
            "stdout_sha256": "0" * 64,
            "stderr_sha256": "0" * 64,
            "stdout_excerpt": "ok",
        }

        with patch.dict(RECEIPT.os.environ, {"AWS_SECRET_ACCESS_KEY": "do-not-leak"}):
            with patch.object(RECEIPT, "run_command", return_value=fake_command):
                payload = RECEIPT.collect_receipt(namespace)

        serialized = json.dumps(payload, sort_keys=True)
        self.assertNotIn("do-not-leak", serialized)
        self.assertTrue(payload["environment_presence"]["AWS_SECRET_ACCESS_KEY"])
        self.assertEqual("radeon_runtime_environment", payload["receipt_kind"])

    def test_output_file_is_private_json(self) -> None:
        namespace = type(
            "Args",
            (),
            {
                "recorded_at": "2026-07-20T14:00:00Z",
                "base_image": "GH-proxy-stable",
                "storage_profile": "persistent_pvc",
                "ssh_profile": "enabled",
                "output": None,
            },
        )()
        fake_payload = {
            "schema_version": "1.0",
            "receipt_kind": "radeon_runtime_environment",
        }

        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "receipt.json"
            namespace.output = output
            with patch.object(RECEIPT, "parse_args", return_value=namespace):
                with patch.object(RECEIPT, "collect_receipt", return_value=fake_payload):
                    self.assertEqual(0, RECEIPT.main())

            self.assertEqual(fake_payload, json.loads(output.read_text(encoding="utf-8")))
            self.assertEqual(0o600, output.stat().st_mode & 0o777)


if __name__ == "__main__":
    unittest.main()
