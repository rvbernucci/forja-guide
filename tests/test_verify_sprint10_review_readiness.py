"""Tests for Sprint 10 independent-review readiness verification."""

from __future__ import annotations

import importlib.util
import json
import shutil
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
APPLIER_PATH = ROOT / "scripts" / "apply_radeon_sprint10_public_summary.py"
VERIFY_PATH = ROOT / "scripts" / "verify_sprint10_review_readiness.py"


def load_module(path: Path, name: str):
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Unable to load {path}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


APPLIER = load_module(APPLIER_PATH, "apply_radeon_sprint10_public_summary")
VERIFY = load_module(VERIFY_PATH, "verify_sprint10_review_readiness")


class Sprint10ReviewReadinessTests(unittest.TestCase):
    def test_ready_package_passes_without_authorizing_next_sprint(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            evidence_dir = prepare_ready_evidence(Path(directory))

            report, exit_code = VERIFY.verify(evidence_dir)

        self.assertEqual(0, exit_code)
        self.assertTrue(report["ready_for_independent_review"])
        self.assertFalse(report["next_sprint_authorized"])
        self.assertEqual([], report["errors"])

    def test_missing_summary_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            evidence_dir = prepare_ready_evidence(Path(directory))
            (evidence_dir / "radeon-public-summary.json").unlink()

            report, exit_code = VERIFY.verify(evidence_dir)

        self.assertEqual(2, exit_code)
        self.assertFalse(report["ready_for_independent_review"])
        self.assertIn("missing_summary", report["errors"])

    def test_candidate_authorizing_next_sprint_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            evidence_dir = prepare_ready_evidence(Path(directory))
            candidate_path = evidence_dir / "closure-candidate.json"
            candidate = json.loads(candidate_path.read_text(encoding="utf-8"))
            candidate["next_sprint_authorized"] = "11"
            candidate_path.write_text(json.dumps(candidate), encoding="utf-8")

            report, exit_code = VERIFY.verify(evidence_dir)

        self.assertEqual(2, exit_code)
        self.assertIn("candidate_authorizes_next_sprint", report["errors"])


def prepare_ready_evidence(root: Path) -> Path:
    evidence_dir = root / "sprint-10"
    shutil.copytree(ROOT / "docs" / "evidence" / "sprint-10", evidence_dir)
    summary = write_summary(evidence_dir)
    updates = APPLIER.build_updates(
        APPLIER.load_json(summary),
        evidence_dir,
        "2026-07-20T18:30:00Z",
    )
    for filename, payload in updates.items():
        (evidence_dir / filename).write_text(json.dumps(payload), encoding="utf-8")
    return evidence_dir


def write_summary(evidence_dir: Path) -> Path:
    path = evidence_dir / "radeon-public-summary.json"
    path.write_text(
        json.dumps(
            {
                "evidence_version": "1.0",
                "sprint_id": "10",
                "summary_kind": "radeon_sprint10_public_summary",
                "status": "passed",
                "basis_commit": "b" * 40,
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
            }
        ),
        encoding="utf-8",
    )
    return path


if __name__ == "__main__":
    unittest.main()
