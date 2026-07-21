"""Tests for Radeon runtime readiness verification."""

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
    / "verify_radeon_runtime_readiness.py"
)
SPEC = importlib.util.spec_from_file_location("verify_radeon_runtime_readiness", MODULE_PATH)
if SPEC is None or SPEC.loader is None:
    raise RuntimeError("Unable to load Radeon runtime readiness module")
READINESS = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(READINESS)


def write_receipt(path: Path) -> None:
    """Write the minimum valid receipt needed by the readiness verifier."""
    path.write_text(
        json.dumps(
            {
                "schema_version": "1.0",
                "receipt_kind": "radeon_runtime_environment",
                "competition_profile": {
                    "core_remote_inference_allowed": False,
                },
                "checks": {
                    "rocm_command_available": True,
                    "gpu_probe_available": True,
                    "torch_rocm_probe_available": True,
                    "vllm_probe_available": True,
                },
            }
        ),
        encoding="utf-8",
    )


class RuntimeReadinessTests(unittest.TestCase):
    def test_loopback_model_and_embedding_endpoints_mark_runtime_ready(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            receipt = Path(directory) / "receipt.json"
            write_receipt(receipt)
            args = type(
                "Args",
                (),
                {
                    "receipt": receipt,
                    "model_base_url": "http://127.0.0.1:8000",
                    "embedding_base_url": "http://127.0.0.1:8001",
                    "embedding_model": "local-embedding",
                    "timeout": 2.0,
                    "recorded_at": "2026-07-20T14:00:00Z",
                    "require_endpoints": True,
                },
            )()

            def fake_probe_json(
                *,
                method: str,
                url: str,
                body: dict[str, object] | None = None,
                timeout: float,
            ) -> tuple[int, dict[str, object], None]:
                if method == "GET":
                    self.assertEqual("http://127.0.0.1:8000/v1/models", url)
                    return 200, {"data": [{"id": "local-model"}]}, None
                self.assertEqual("http://127.0.0.1:8001/v1/embeddings", url)
                self.assertEqual({"model": "local-embedding", "input": "forja runtime smoke"}, body)
                return 200, {"data": [{"embedding": [0.1, 0.2, 0.3]}]}, None

            with patch.object(READINESS, "probe_json", side_effect=fake_probe_json):
                report, exit_code = READINESS.build_report(args)

        self.assertEqual(0, exit_code)
        self.assertTrue(report["ready"])
        self.assertTrue(report["policy"]["zero_remote_core_inference_proved"])
        self.assertEqual(3, report["probes"]["embedding"]["embedding_dimensions"])

    def test_remote_endpoint_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            receipt = Path(directory) / "receipt.json"
            write_receipt(receipt)
            args = type(
                "Args",
                (),
                {
                    "receipt": receipt,
                    "model_base_url": "https://api.example.com",
                    "embedding_base_url": "http://127.0.0.1:65534",
                    "embedding_model": "local-embedding",
                    "timeout": 0.1,
                    "recorded_at": "2026-07-20T14:00:00Z",
                    "require_endpoints": True,
                },
            )()

            report, exit_code = READINESS.build_report(args)

        self.assertEqual(2, exit_code)
        self.assertFalse(report["ready"])
        self.assertFalse(report["endpoints"]["model"]["loopback"])
        self.assertEqual("non_loopback_host", report["endpoints"]["model"]["error"])

    def test_require_endpoints_fails_when_no_endpoint_is_configured(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            receipt = Path(directory) / "receipt.json"
            write_receipt(receipt)
            args = type(
                "Args",
                (),
                {
                    "receipt": receipt,
                    "model_base_url": None,
                    "embedding_base_url": None,
                    "embedding_model": "local-embedding",
                    "timeout": 0.1,
                    "recorded_at": "2026-07-20T14:00:00Z",
                    "require_endpoints": True,
                },
            )()

            report, exit_code = READINESS.build_report(args)

        self.assertEqual(2, exit_code)
        self.assertFalse(report["ready"])
        self.assertEqual("missing", report["endpoints"]["model"]["error"])


if __name__ == "__main__":
    unittest.main()
