"""Tests for the Sprint 10 final promotion checklist renderer."""

from __future__ import annotations

import importlib.util
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT_PATH = ROOT / "scripts" / "render_sprint10_promotion_checklist.py"


def load_module(path: Path, name: str):
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Unable to load {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


CHECKLIST = load_module(SCRIPT_PATH, "render_sprint10_promotion_checklist")


class Sprint10PromotionChecklistTests(unittest.TestCase):
    def test_checklist_contains_safe_promotion_sequence(self) -> None:
        body = render()

        expected = [
            "verify_sprint10_review_readiness.py",
            "Dry Run",
            "--dry-run",
            "Write The Close Receipt",
            "--output docs/evidence/sprint-10/close-receipt.json",
            "git rm docs/evidence/sprint-10/closure-candidate.json",
            "python3 scripts/validate_repository.py",
            "make validate",
            "Only after that final promotion commit may Sprint 11 start",
        ]
        positions = [body.index(item) for item in expected]
        self.assertEqual(sorted(positions), positions)

    def test_checklist_is_not_a_closure_receipt(self) -> None:
        body = render()

        self.assertIn("operator checklist, not a closure receipt", body)
        self.assertIn("does not authorize Sprint 11", body)
        self.assertIn("must not include implementation changes", body)

    def test_cli_writes_output_file(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            output = Path(tmp) / "promotion.md"
            code = CHECKLIST.main(
                [
                    "--reviewer",
                    "codex-review",
                    "--reviewed-candidate-commit",
                    "b" * 40,
                    "--output",
                    output.as_posix(),
                ]
            )
            body = output.read_text(encoding="utf-8")

        self.assertEqual(0, code)
        self.assertIn("Sprint 10 Final Promotion Checklist", body)
        self.assertIn("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", body)


def render() -> str:
    return CHECKLIST.render_checklist(
        reviewer="codex-review",
        reviewed_candidate_commit="a" * 40,
        review_artifact=CHECKLIST.DEFAULT_REVIEW,
        close_receipt=CHECKLIST.DEFAULT_CLOSE,
        closure_candidate=CHECKLIST.DEFAULT_CANDIDATE,
    )


if __name__ == "__main__":
    unittest.main()
