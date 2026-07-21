"""Tests for Sprint 10 public-code metrics refresh."""

from __future__ import annotations

import importlib.util
import json
import shutil
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT_PATH = ROOT / "scripts" / "refresh_sprint10_public_metrics.py"


def load_module(path: Path, name: str):
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Unable to load {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


REFRESH = load_module(SCRIPT_PATH, "refresh_sprint10_public_metrics")


class Sprint10PublicMetricsRefreshTests(unittest.TestCase):
    def test_refresh_updates_public_counts_and_preserves_real_gate_zeroes(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            evidence_dir = Path(directory) / "sprint-10"
            shutil.copytree(ROOT / "docs" / "evidence" / "sprint-10", evidence_dir)

            report = REFRESH.refresh(
                evidence_dir=evidence_dir,
                basis_commit="a" * 40,
                recorded_at="2026-07-20T19:00:00Z",
                python_tests=106,
                markdown_files=92,
                json_schemas=38,
            )
            metrics = json.loads((evidence_dir / "metrics-summary.json").read_text())
            test_report = json.loads((evidence_dir / "test-report.json").read_text())

        self.assertTrue(report["real_gates_preserved_zero"])
        self.assertEqual(106, metrics["metrics"]["python_tests"])
        self.assertEqual(92, metrics["metrics"]["markdown_files_validated"])
        self.assertEqual(0, metrics["metrics"]["real_radeon_runtime_receipts"])
        self.assertIn("106 Python tests", test_report["tests"][0]["summary"])

    def test_refresh_refuses_to_touch_metrics_after_real_gate_changes(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            evidence_dir = Path(directory) / "sprint-10"
            shutil.copytree(ROOT / "docs" / "evidence" / "sprint-10", evidence_dir)
            metrics_path = evidence_dir / "metrics-summary.json"
            metrics = json.loads(metrics_path.read_text())
            metrics["metrics"]["real_radeon_runtime_receipts"] = 1
            metrics_path.write_text(json.dumps(metrics), encoding="utf-8")

            with self.assertRaisesRegex(ValueError, "real gate changed"):
                REFRESH.refresh(
                    evidence_dir=evidence_dir,
                    basis_commit="a" * 40,
                    recorded_at="2026-07-20T19:00:00Z",
                    python_tests=106,
                    markdown_files=92,
                    json_schemas=38,
                )

    def test_replace_python_count_requires_marker(self) -> None:
        with self.assertRaisesRegex(ValueError, "marker"):
            REFRESH.replace_python_test_count("No count here.", 106)


if __name__ == "__main__":
    unittest.main()
