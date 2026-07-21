"""Tests for fail-closed Sprint 10 receipt promotion."""

from __future__ import annotations

import importlib.util
import json
import shutil
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
PROMOTER_PATH = ROOT / "scripts" / "promote_sprint10_close_receipt.py"


def load_module(path: Path, name: str):
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Unable to load {path}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


PROMOTER = load_module(PROMOTER_PATH, "promote_sprint10_close_receipt")


class Sprint10CloseReceiptPromotionTests(unittest.TestCase):
    def test_ready_candidate_promotes_to_review_bound_receipt(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            candidate_path, review_path = prepare_ready_candidate(root)

            receipt = PROMOTER.build_receipt(
                candidate_path=candidate_path,
                review_artifact=review_path,
                reviewed_candidate_commit="a" * 40,
                model="codex-independent-review",
                closed_at="2026-07-20T19:00:00Z",
                root=root,
            )

        self.assertEqual("closed", receipt["status"])
        self.assertTrue(receipt["authoritative"])
        self.assertEqual("11", receipt["next_sprint_authorized"])
        self.assertTrue(receipt["definition_of_done"]["independent_validation_recorded"])
        self.assertEqual("a" * 40, receipt["reviewed_candidate_commit"])
        self.assertEqual("passed", receipt["immutable_review"]["result"])
        self.assertEqual(
            "docs/evidence/sprint-10/reviews/immutable-candidate-review.md",
            receipt["immutable_review"]["artifact_path"],
        )
        self.assertIn(
            "docs/evidence/sprint-10/reviews/immutable-candidate-review.md",
            receipt["supporting_artifacts"],
        )
        self.assertIn("candidate_recorded_at", receipt)
        self.assertNotIn("recorded_at", receipt)

    def test_current_unproven_candidate_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            evidence_dir = root / "docs" / "evidence" / "sprint-10"
            shutil.copytree(ROOT / "docs" / "evidence" / "sprint-10", evidence_dir)
            review_path = evidence_dir / "reviews" / "immutable-candidate-review.md"
            review_path.parent.mkdir(parents=True)
            review_path.write_text("Independent review passed.\n", encoding="utf-8")

            with self.assertRaisesRegex(ValueError, "rollback must be demonstrated"):
                PROMOTER.build_receipt(
                    candidate_path=evidence_dir / "closure-candidate.json",
                    review_artifact=review_path,
                    reviewed_candidate_commit="b" * 40,
                    model="codex-independent-review",
                    closed_at="2026-07-20T19:00:00Z",
                    root=root,
                )

    def test_candidate_with_rollback_but_missing_real_gates_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            evidence_dir = root / "docs" / "evidence" / "sprint-10"
            shutil.copytree(ROOT / "docs" / "evidence" / "sprint-10", evidence_dir)
            candidate_path = evidence_dir / "closure-candidate.json"
            candidate = json.loads(candidate_path.read_text(encoding="utf-8"))
            candidate["definition_of_done"]["rollback_demonstrated"] = True
            candidate_path.write_text(json.dumps(candidate), encoding="utf-8")
            review_path = evidence_dir / "reviews" / "immutable-candidate-review.md"
            review_path.parent.mkdir(parents=True)
            review_path.write_text("Independent review passed.\n", encoding="utf-8")

            with self.assertRaisesRegex(ValueError, "real Radeon gates"):
                PROMOTER.build_receipt(
                    candidate_path=candidate_path,
                    review_artifact=review_path,
                    reviewed_candidate_commit="b" * 40,
                    model="codex-independent-review",
                    closed_at="2026-07-20T19:00:00Z",
                    root=root,
                )

    def test_review_artifact_must_remain_inside_sprint_reviews(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            candidate_path, _review_path = prepare_ready_candidate(root)
            bad_review = root / "docs" / "evidence" / "sprint-10" / "outside.md"
            bad_review.write_text("Review.\n", encoding="utf-8")

            with self.assertRaisesRegex(ValueError, "sprint-10/reviews"):
                PROMOTER.build_receipt(
                    candidate_path=candidate_path,
                    review_artifact=bad_review,
                    reviewed_candidate_commit="c" * 40,
                    model="codex-independent-review",
                    closed_at="2026-07-20T19:00:00Z",
                    root=root,
                )

    def test_review_artifact_cannot_be_prelisted(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            candidate_path, review_path = prepare_ready_candidate(root)
            candidate = json.loads(candidate_path.read_text(encoding="utf-8"))
            candidate["supporting_artifacts"].append(
                "docs/evidence/sprint-10/reviews/immutable-candidate-review.md"
            )
            candidate_path.write_text(json.dumps(candidate), encoding="utf-8")

            with self.assertRaisesRegex(ValueError, "already listed"):
                PROMOTER.build_receipt(
                    candidate_path=candidate_path,
                    review_artifact=review_path,
                    reviewed_candidate_commit="d" * 40,
                    model="codex-independent-review",
                    closed_at="2026-07-20T19:00:00Z",
                    root=root,
                )


def prepare_ready_candidate(root: Path) -> tuple[Path, Path]:
    evidence_dir = root / "docs" / "evidence" / "sprint-10"
    shutil.copytree(ROOT / "docs" / "evidence" / "sprint-10", evidence_dir)
    candidate_path = evidence_dir / "closure-candidate.json"
    candidate = json.loads(candidate_path.read_text(encoding="utf-8"))
    candidate["definition_of_done"]["rollback_demonstrated"] = True
    for key in PROMOTER.REAL_ACCEPTANCE_GATES:
        candidate["acceptance"][key] = True
    candidate_path.write_text(json.dumps(candidate), encoding="utf-8")
    review_path = evidence_dir / "reviews" / "immutable-candidate-review.md"
    review_path.parent.mkdir(parents=True)
    review_path.write_text("Independent immutable review passed.\n", encoding="utf-8")
    return candidate_path, review_path


if __name__ == "__main__":
    unittest.main()
