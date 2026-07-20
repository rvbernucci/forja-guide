"""Tests for the Sprint 10 immutable review request renderer."""

from __future__ import annotations

import importlib.util
import json
import shutil
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT_PATH = ROOT / "scripts" / "render_sprint10_immutable_review_request.py"


def load_module(path: Path, name: str):
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Unable to load {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


RENDER = load_module(SCRIPT_PATH, "render_sprint10_immutable_review_request")


class Sprint10ImmutableReviewRequestTests(unittest.TestCase):
    def test_request_contains_scope_hashes_and_mechanical_commands(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            evidence_dir = copy_readyish_evidence(Path(tmp))

            body = RENDER.render_request(
                evidence_dir=evidence_dir,
                reviewer="codex-review",
                subject_commit="a" * 40,
                subject_tree="b" * 40,
            )

        self.assertIn("Sprint 10 Immutable Review Request", body)
        self.assertIn("Subject commit: `aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa`", body)
        self.assertIn("Subject tree: `bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb`", body)
        self.assertIn("docs/evidence/sprint-10/closure-candidate.json", body)
        self.assertIn("docs/evidence/sprint-10/radeon-public-summary.json", body)
        self.assertIn("verify_radeon_sprint10_public_summary.py", body)
        self.assertIn("verify_sprint10_review_readiness.py", body)
        self.assertIn("promote_sprint10_close_receipt.py", body)
        self.assertIn("--dry-run", body)
        self.assertIn("does not authorize Sprint 11", body)
        self.assertIn("TODO: `PASS` or `FAIL`", body)

    def test_missing_summary_is_listed_without_authorizing_review(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            evidence_dir = copy_readyish_evidence(Path(tmp))
            (evidence_dir / "radeon-public-summary.json").unlink()

            body = RENDER.render_request(
                evidence_dir=evidence_dir,
                reviewer="codex-review",
                subject_commit="c" * 40,
                subject_tree="d" * 40,
            )

        self.assertIn("MISSING", body)
        self.assertIn("docs/evidence/sprint-10/radeon-public-summary.json", body)
        self.assertIn("Sprint 10\nremains open", body)

    def test_cli_writes_template(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            evidence_dir = copy_readyish_evidence(root)
            output = root / "request.md"

            code = RENDER.main(
                [
                    "--evidence-dir",
                    evidence_dir.as_posix(),
                    "--reviewer",
                    "codex-review",
                    "--subject-commit",
                    "e" * 40,
                    "--subject-tree",
                    "f" * 40,
                    "--output",
                    output.as_posix(),
                ]
            )
            body = output.read_text(encoding="utf-8")

        self.assertEqual(0, code)
        self.assertIn("Sprint 10 Immutable Review Request", body)


def copy_readyish_evidence(root: Path) -> Path:
    destination = root / "docs" / "evidence" / "sprint-10"
    shutil.copytree(ROOT / "docs" / "evidence" / "sprint-10", destination)
    summary = {
        "evidence_version": "1.0",
        "sprint_id": "10",
        "summary_kind": "radeon_sprint10_public_summary",
        "status": "passed",
        "basis_commit": "1" * 40,
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
    (destination / "radeon-public-summary.json").write_text(
        json.dumps(summary),
        encoding="utf-8",
    )
    return destination


if __name__ == "__main__":
    unittest.main()
