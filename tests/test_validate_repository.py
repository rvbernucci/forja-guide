"""Tests for the dependency-free public repository validator."""

from __future__ import annotations

import importlib.util
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch


MODULE_PATH = (
    Path(__file__).resolve().parents[1] / "scripts" / "validate_repository.py"
)
SPEC = importlib.util.spec_from_file_location("validate_repository", MODULE_PATH)
if SPEC is None or SPEC.loader is None:
    raise RuntimeError("Unable to load repository validator")
VALIDATOR = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(VALIDATOR)


class EvidenceValidationTests(unittest.TestCase):
    """Exercise fail-closed behavior for malformed Sprint evidence."""

    def test_non_object_evidence_is_reported_without_crashing(self) -> None:
        """A valid JSON list must be rejected as an invalid evidence document."""
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            sprint = root / "docs" / "evidence" / "sprint-00"
            sprint.mkdir(parents=True)
            for filename in VALIDATOR.EVIDENCE_FILES:
                payload: object = {
                    "evidence_version": "1.0",
                    "sprint_id": "00",
                    "status": "closed" if filename == "close-receipt.json" else "ok",
                }
                if filename == "plan.json":
                    payload = []
                (sprint / filename).write_text(
                    json.dumps(payload),
                    encoding="utf-8",
                )

            errors: list[str] = []
            with patch.object(VALIDATOR, "ROOT", root):
                VALIDATOR.validate_evidence_sets(errors)

            self.assertIn(
                "evidence document must be a JSON object: "
                "docs/evidence/sprint-00/plan.json",
                errors,
            )

    def test_incomplete_evidence_set_reports_every_missing_file(self) -> None:
        """An evidence directory must contain all seven mandatory documents."""
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            sprint = root / "docs" / "evidence" / "sprint-01"
            sprint.mkdir(parents=True)
            (sprint / "plan.json").write_text(
                json.dumps(
                    {
                        "evidence_version": "1.0",
                        "sprint_id": "01",
                    }
                ),
                encoding="utf-8",
            )

            errors: list[str] = []
            with patch.object(VALIDATOR, "ROOT", root):
                VALIDATOR.validate_evidence_sets(errors)

            missing_errors = [
                error for error in errors if "is missing" in error
            ]
            self.assertEqual(6, len(missing_errors))

    def test_invalid_basis_commit_is_rejected(self) -> None:
        """Evidence commit references must use immutable full SHA-1 values."""
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            sprint = root / "docs" / "evidence" / "sprint-01"
            sprint.mkdir(parents=True)
            for filename in VALIDATOR.EVIDENCE_FILES:
                payload = {
                    "evidence_version": "1.0",
                    "sprint_id": "01",
                    "basis_commit": "not-a-commit",
                    "status": (
                        "closed" if filename == "close-receipt.json" else "ok"
                    ),
                }
                (sprint / filename).write_text(
                    json.dumps(payload),
                    encoding="utf-8",
                )

            errors: list[str] = []
            with patch.object(VALIDATOR, "ROOT", root):
                VALIDATOR.validate_evidence_sets(errors)

            invalid = [
                error for error in errors if "invalid basis_commit" in error
            ]
            self.assertEqual(7, len(invalid))

    def test_evidence_artifact_digest_is_enforced(self) -> None:
        """Hash-pinned review and security artifacts must remain byte exact."""
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            sprint = root / "docs" / "evidence" / "sprint-02"
            artifact = sprint / "reviews" / "review.md"
            artifact.parent.mkdir(parents=True)
            artifact.write_text("reviewed\n", encoding="utf-8")
            for filename in VALIDATOR.EVIDENCE_FILES:
                payload = {
                    "evidence_version": "1.0",
                    "sprint_id": "02",
                    "status": (
                        "closed" if filename == "close-receipt.json" else "ok"
                    ),
                }
                if filename == "validation-report.json":
                    payload["validator"] = {
                        "artifact_path": (
                            "docs/evidence/sprint-02/reviews/review.md"
                        ),
                        "artifact_sha256": "0" * 64,
                    }
                (sprint / filename).write_text(
                    json.dumps(payload),
                    encoding="utf-8",
                )

            errors: list[str] = []
            with patch.object(VALIDATOR, "ROOT", root):
                VALIDATOR.validate_evidence_sets(errors)

            self.assertEqual(
                1,
                sum("artifact digest mismatch" in error for error in errors),
            )


if __name__ == "__main__":
    unittest.main()
