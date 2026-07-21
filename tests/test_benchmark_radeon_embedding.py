"""Tests for local Radeon embedding benchmark reporting."""

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
    / "benchmark_radeon_embedding.py"
)
SPEC = importlib.util.spec_from_file_location("benchmark_radeon_embedding", MODULE_PATH)
if SPEC is None or SPEC.loader is None:
    raise RuntimeError("Unable to load Radeon embedding benchmark module")
BENCH = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(BENCH)


class RadeonEmbeddingBenchmarkTests(unittest.TestCase):
    def test_embedding_benchmark_records_hashes_not_vectors_or_text(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            input_set = write_input_set(root)
            args = namespace(root, input_set, "http://127.0.0.1:8001")

            with patch.object(BENCH, "post_embedding", side_effect=healthy_embedding):
                report, exit_code = BENCH.build_report(args)

        serialized = json.dumps(report)
        self.assertEqual(0, exit_code)
        self.assertTrue(report["verified"])
        self.assertEqual(3, report["summary"]["embedding_dimensions"])
        self.assertTrue(report["privacy"]["stores_hashes"])
        self.assertFalse(report["privacy"]["stores_vectors"])
        self.assertNotIn("[0.1, 0.2, 0.3]", serialized)
        self.assertNotIn("first private input", serialized)

    def test_inconsistent_dimensions_fail_closed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            input_set = write_input_set(root)
            args = namespace(root, input_set, "http://127.0.0.1:8001")

            with patch.object(BENCH, "post_embedding", side_effect=inconsistent_embedding):
                report, exit_code = BENCH.build_report(args)

        self.assertEqual(2, exit_code)
        self.assertFalse(report["verified"])
        self.assertFalse(report["summary"]["consistent_dimensions"])

    def test_remote_endpoint_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            input_set = write_input_set(root)
            args = namespace(root, input_set, "https://api.example.com")

            with self.assertRaises(ValueError) as context:
                BENCH.build_report(args)

        self.assertIn("loopback", str(context.exception))


def healthy_embedding(
    url: str,
    model: str,
    text: str,
    timeout: float,
) -> tuple[int, dict[str, object], None]:
    return 200, {"data": [{"embedding": [0.1, 0.2, 0.3]}]}, None


def inconsistent_embedding(
    url: str,
    model: str,
    text: str,
    timeout: float,
) -> tuple[int, dict[str, object], None]:
    if "second" in text:
        return 200, {"data": [{"embedding": [0.1, 0.2]}]}, None
    return 200, {"data": [{"embedding": [0.1, 0.2, 0.3]}]}, None


def namespace(root: Path, input_set: Path, base_url: str) -> object:
    return type(
        "Args",
        (),
        {
            "input_set": input_set,
            "base_url": base_url,
            "model": "local-embedding-model",
            "output": root / "report.json",
            "timeout": 2.0,
            "recorded_at": "2026-07-20T14:00:00Z",
        },
    )()


def write_input_set(root: Path) -> Path:
    path = root / "inputs.json"
    path.write_text(
        json.dumps(
            {
                "schema_version": "1.0",
                "input_set_id": "test_inputs",
                "split": "public",
                "inputs": [
                    {
                        "input_id": "i1",
                        "category": "filing",
                        "text": "first private input",
                    },
                    {
                        "input_id": "i2",
                        "category": "risk",
                        "text": "second private input",
                    },
                ],
            }
        ),
        encoding="utf-8",
    )
    return path


if __name__ == "__main__":
    unittest.main()
