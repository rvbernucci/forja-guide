"""Tests for Sprint 10 Radeon artifact diagnosis."""

from __future__ import annotations

import importlib.util
import json
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT_PATH = ROOT / "scripts" / "diagnose_radeon_sprint10_artifacts.py"


def load_module(path: Path, name: str):
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Unable to load {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


DIAGNOSE = load_module(SCRIPT_PATH, "diagnose_radeon_sprint10_artifacts")


class RadeonSprint10ArtifactDiagnosisTests(unittest.TestCase):
    def test_empty_evidence_dir_blocks_at_runtime_receipt(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            report, exit_code = DIAGNOSE.diagnose(Path(directory))

        self.assertEqual(2, exit_code)
        self.assertFalse(report["public_summary_ready"])
        self.assertEqual("blocked_at_runtime_receipt", report["stage"])
        self.assertFalse(report["next_sprint_authorized"])

    def test_complete_public_summary_is_ready_to_ingest(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            evidence_dir = Path(directory)
            write_complete_artifacts(evidence_dir)

            report, exit_code = DIAGNOSE.diagnose(evidence_dir)

        self.assertEqual(0, exit_code)
        self.assertTrue(report["public_summary_ready"])
        self.assertEqual("ready_to_ingest_public_summary", report["stage"])
        self.assertIn("ingest_radeon_sprint10_public_summary.py", report["next_action"])
        self.assertTrue(all(item["valid"] for item in report["artifacts"]))

    def test_invalid_middle_artifact_points_to_next_action(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            evidence_dir = Path(directory)
            write_json(evidence_dir / DIAGNOSE.RUNNER.REPORT_NAMES["runtime_receipt"], {"status": "passed"})
            write_json(
                evidence_dir / DIAGNOSE.RUNNER.REPORT_NAMES["runtime_readiness"],
                {"ready": False, "errors": ["endpoint_missing"]},
            )

            report, exit_code = DIAGNOSE.diagnose(evidence_dir)

        self.assertEqual(2, exit_code)
        self.assertEqual("blocked_at_runtime_readiness", report["stage"])
        self.assertIn("Start loopback model", report["next_action"])


def write_complete_artifacts(evidence_dir: Path) -> None:
    write_json(evidence_dir / DIAGNOSE.RUNNER.REPORT_NAMES["runtime_receipt"], {"status": "passed"})
    write_json(evidence_dir / DIAGNOSE.RUNNER.REPORT_NAMES["runtime_readiness"], {"ready": True})
    write_json(evidence_dir / DIAGNOSE.RUNNER.REPORT_NAMES["source_restore"], {"verified": True})
    write_json(evidence_dir / DIAGNOSE.RUNNER.REPORT_NAMES["model_benchmark"], {"valid": True})
    write_json(evidence_dir / DIAGNOSE.RUNNER.REPORT_NAMES["embedding_benchmark"], {"valid": True})
    write_json(evidence_dir / DIAGNOSE.RUNNER.REPORT_NAMES["recovery"], {"verified": True})
    write_json(
        evidence_dir / DIAGNOSE.RUNNER.REPORT_NAMES["public_summary"],
        {
            "evidence_version": "1.0",
            "sprint_id": "10",
            "summary_kind": "radeon_sprint10_public_summary",
            "status": "passed",
            "basis_commit": "d" * 40,
            "private_recovery_verified": True,
            "counts": {
                "required_evidence_items": 5,
                "valid_evidence_items": 5,
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
        },
    )


def write_json(path: Path, payload: dict[str, object]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(payload), encoding="utf-8")


if __name__ == "__main__":
    unittest.main()
