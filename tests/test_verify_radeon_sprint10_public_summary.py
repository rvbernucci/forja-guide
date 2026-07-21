"""Tests for the read-only Radeon Sprint 10 public summary verifier."""

from __future__ import annotations

import importlib.util
import json
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT_PATH = ROOT / "scripts" / "verify_radeon_sprint10_public_summary.py"


def load_module(path: Path, name: str):
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Unable to load {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


VERIFY = load_module(SCRIPT_PATH, "verify_radeon_sprint10_public_summary")


class RadeonSprint10PublicSummaryVerifierTests(unittest.TestCase):
    def test_valid_summary_is_ready_to_ingest(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            summary = write_summary(Path(tmp), status="passed")

            report, exit_code = VERIFY.verify_summary(summary)

        self.assertEqual(0, exit_code)
        self.assertTrue(report["ready_to_ingest"])
        self.assertFalse(report["next_sprint_authorized"])
        self.assertEqual("Run ingest_radeon_sprint10_public_summary.py --dry-run first.", report["next_action"])
        self.assertEqual(5, report["counts"]["valid_evidence_items"])

    def test_invalid_summary_reports_error_without_throwing(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            summary = write_summary(Path(tmp), status="partial_or_failed")

            report, exit_code = VERIFY.verify_summary(summary)

        self.assertEqual(2, exit_code)
        self.assertFalse(report["ready_to_ingest"])
        self.assertIn("status must be passed", report["errors"][0])
        self.assertEqual("Fix or regenerate radeon-public-summary.json on the Radeon instance.", report["next_action"])

    def test_verifier_does_not_mutate_evidence_package(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            summary = write_summary(root, status="passed")
            evidence_summary = ROOT / "docs" / "evidence" / "sprint-10" / "radeon-public-summary.json"

            report, exit_code = VERIFY.verify_summary(summary)

        self.assertEqual(0, exit_code)
        self.assertTrue(report["ready_to_ingest"])
        self.assertFalse(evidence_summary.exists())


def write_summary(root: Path, *, status: str) -> Path:
    path = root / "radeon-public-summary.json"
    passed = status == "passed"
    path.write_text(
        json.dumps(
            {
                "evidence_version": "1.0",
                "sprint_id": "10",
                "summary_kind": "radeon_sprint10_public_summary",
                "status": status,
                "basis_commit": "c" * 40,
                "private_recovery_verified": passed,
                "counts": {
                    "required_evidence_items": 5,
                    "valid_evidence_items": 5 if passed else 4,
                    "missing_evidence_items": 0,
                    "unexpected_evidence_items": 0,
                },
                "policy": {
                    "raw_artifacts_outside_git": True,
                    "stores_private_logs": False,
                    "stores_model_outputs": False,
                    "stores_vectors": False,
                    "stores_credentials": False,
                },
                "evidence": [],
            }
        ),
        encoding="utf-8",
    )
    return path


if __name__ == "__main__":
    unittest.main()
