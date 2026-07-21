"""Tests for applying public Radeon Sprint 10 evidence summaries."""

from __future__ import annotations

import importlib.util
import json
import shutil
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
MODULE_PATH = ROOT / "scripts" / "apply_radeon_sprint10_public_summary.py"
SPEC = importlib.util.spec_from_file_location("apply_radeon_sprint10_public_summary", MODULE_PATH)
if SPEC is None or SPEC.loader is None:
    raise RuntimeError("Unable to load Sprint 10 evidence applier")
APPLIER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(APPLIER)


class RadeonSprint10EvidenceApplierTests(unittest.TestCase):
    def test_verified_summary_updates_candidate_without_authorizing_next_sprint(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            evidence_dir = copy_sprint10_evidence(Path(directory))
            summary = write_summary(Path(directory), status="passed")

            updates = APPLIER.build_updates(
                APPLIER.load_json(summary),
                evidence_dir,
                "2026-07-20T18:25:00Z",
            )

        candidate = updates["closure-candidate.json"]
        metrics = updates["metrics-summary.json"]
        validation = updates["validation-report.json"]
        self.assertEqual("candidate", candidate["status"])
        self.assertFalse(candidate["authoritative"])
        self.assertIsNone(candidate["next_sprint_authorized"])
        self.assertFalse(candidate["definition_of_done"]["independent_validation_recorded"])
        self.assertTrue(candidate["definition_of_done"]["rollback_demonstrated"])
        self.assertTrue(candidate["acceptance"]["destroy_recreate_recovery_verified"])
        self.assertEqual(1, metrics["metrics"]["real_destroy_recreate_recovery_reports"])
        self.assertEqual("ready_for_independent_review", validation["status"])

    def test_unverified_summary_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            summary = write_summary(Path(directory), status="partial_or_failed")

            with self.assertRaises(ValueError) as context:
                APPLIER.validate_summary(APPLIER.load_json(summary))

        self.assertIn("status must be passed", str(context.exception))

    def test_candidate_that_authorizes_next_sprint_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            evidence_dir = copy_sprint10_evidence(Path(directory))
            candidate_path = evidence_dir / "closure-candidate.json"
            candidate = json.loads(candidate_path.read_text(encoding="utf-8"))
            candidate["next_sprint_authorized"] = "11"
            candidate_path.write_text(json.dumps(candidate), encoding="utf-8")
            summary = write_summary(Path(directory), status="passed")

            with self.assertRaises(ValueError) as context:
                APPLIER.build_updates(
                    APPLIER.load_json(summary),
                    evidence_dir,
                    "2026-07-20T18:25:00Z",
                )

        self.assertIn("already authorizes", str(context.exception))


def copy_sprint10_evidence(root: Path) -> Path:
    destination = root / "sprint-10"
    shutil.copytree(ROOT / "docs" / "evidence" / "sprint-10", destination)
    return destination


def write_summary(root: Path, *, status: str) -> Path:
    path = root / "summary.json"
    passed = status == "passed"
    path.write_text(
        json.dumps(
            {
                "evidence_version": "1.0",
                "sprint_id": "10",
                "summary_kind": "radeon_sprint10_public_summary",
                "status": status,
                "basis_commit": "a" * 40,
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
            }
        ),
        encoding="utf-8",
    )
    return path


if __name__ == "__main__":
    unittest.main()
