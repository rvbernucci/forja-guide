"""Tests for ingesting public-safe Radeon Sprint 10 summaries."""

from __future__ import annotations

import importlib.util
import json
import shutil
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT_PATH = ROOT / "scripts" / "ingest_radeon_sprint10_public_summary.py"


def load_module(path: Path, name: str):
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Unable to load {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


INGEST = load_module(SCRIPT_PATH, "ingest_radeon_sprint10_public_summary")


class RadeonSprint10PublicSummaryIngestTests(unittest.TestCase):
    def test_valid_summary_is_ingested_without_authorizing_sprint_11(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            evidence_dir = copy_sprint10_evidence(root)
            summary = write_summary(root, status="passed")

            report, exit_code = INGEST.ingest_summary(
                summary_path=summary,
                evidence_dir=evidence_dir,
                recorded_at="2026-07-20T19:30:00Z",
                dry_run=False,
            )

            candidate = json.loads((evidence_dir / "closure-candidate.json").read_text())
            readiness = INGEST.READINESS.verify(evidence_dir)[0]

        self.assertEqual(0, exit_code)
        self.assertEqual("ready_for_independent_review", report["status"])
        self.assertFalse(report["next_sprint_authorized"])
        self.assertTrue(readiness["ready_for_independent_review"])
        self.assertFalse(readiness["next_sprint_authorized"])
        self.assertEqual("candidate", candidate["status"])
        self.assertFalse(candidate["authoritative"])
        self.assertIsNone(candidate["next_sprint_authorized"])

    def test_dry_run_does_not_write_summary(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            evidence_dir = copy_sprint10_evidence(root)
            summary = write_summary(root, status="passed")

            report, exit_code = INGEST.ingest_summary(
                summary_path=summary,
                evidence_dir=evidence_dir,
                recorded_at="2026-07-20T19:30:00Z",
                dry_run=True,
            )

        self.assertEqual(0, exit_code)
        self.assertTrue(report["dry_run"])
        self.assertFalse((evidence_dir / "radeon-public-summary.json").exists())
        self.assertEqual("not_run_in_dry_run", report["readiness"]["ready_for_independent_review"])

    def test_invalid_summary_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            evidence_dir = copy_sprint10_evidence(root)
            summary = write_summary(root, status="partial_or_failed")

            with self.assertRaisesRegex(ValueError, "status must be passed"):
                INGEST.ingest_summary(
                    summary_path=summary,
                    evidence_dir=evidence_dir,
                    recorded_at="2026-07-20T19:30:00Z",
                    dry_run=False,
                )


def copy_sprint10_evidence(root: Path) -> Path:
    destination = root / "sprint-10"
    shutil.copytree(ROOT / "docs" / "evidence" / "sprint-10", destination)
    return destination


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
                "basis_commit": "b" * 40,
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
