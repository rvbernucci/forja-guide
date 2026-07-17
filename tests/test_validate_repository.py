"""Tests for the dependency-free public repository validator."""

from __future__ import annotations

import importlib.util
import json
import os
import subprocess
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

    def test_fail_closed_closure_candidate_is_accepted(self) -> None:
        """A candidate can be validated without authorizing the next Sprint."""
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            sprint = root / "docs" / "evidence" / "sprint-03"
            sprint.mkdir(parents=True)
            for filename in VALIDATOR.EVIDENCE_FILES[:-1]:
                (sprint / filename).write_text(
                    json.dumps(
                        {
                            "evidence_version": "1.0",
                            "sprint_id": "03",
                            "status": "ok",
                        }
                    ),
                    encoding="utf-8",
                )
            (sprint / VALIDATOR.CLOSURE_CANDIDATE_FILE).write_text(
                json.dumps(
                    {
                        "evidence_version": "1.0",
                        "sprint_id": "03",
                        "status": "candidate",
                        "closure_protocol_version": "2.0",
                        "authoritative": False,
                        "next_sprint_authorized": None,
                    }
                ),
                encoding="utf-8",
            )

            errors: list[str] = []
            with patch.object(VALIDATOR, "ROOT", root):
                VALIDATOR.validate_evidence_sets(errors)

            self.assertEqual([], errors)

    def test_closure_candidate_cannot_authorize_next_sprint(self) -> None:
        """A mutable candidate must remain explicitly non-authoritative."""
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            sprint = root / "docs" / "evidence" / "sprint-03"
            sprint.mkdir(parents=True)
            for filename in VALIDATOR.EVIDENCE_FILES[:-1]:
                (sprint / filename).write_text(
                    json.dumps(
                        {
                            "evidence_version": "1.0",
                            "sprint_id": "03",
                            "status": "ok",
                        }
                    ),
                    encoding="utf-8",
                )
            (sprint / VALIDATOR.CLOSURE_CANDIDATE_FILE).write_text(
                json.dumps(
                    {
                        "evidence_version": "1.0",
                        "sprint_id": "03",
                        "status": "candidate",
                        "closure_protocol_version": "2.0",
                        "authoritative": False,
                        "next_sprint_authorized": "04",
                    }
                ),
                encoding="utf-8",
            )

            errors: list[str] = []
            with patch.object(VALIDATOR, "ROOT", root):
                VALIDATOR.validate_evidence_sets(errors)

            self.assertEqual(
                1,
                sum("candidate is not fail-closed" in error for error in errors),
            )

    def test_every_closure_candidate_requires_protocol_v2(self) -> None:
        """Legacy receipt compatibility cannot downgrade a new candidate."""
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            sprint = root / "docs" / "evidence" / "sprint-02"
            sprint.mkdir(parents=True)
            for filename in VALIDATOR.EVIDENCE_FILES[:-1]:
                (sprint / filename).write_text(
                    json.dumps(
                        {
                            "evidence_version": "1.0",
                            "sprint_id": "02",
                            "status": "ok",
                        }
                    ),
                    encoding="utf-8",
                )
            (sprint / VALIDATOR.CLOSURE_CANDIDATE_FILE).write_text(
                json.dumps(
                    {
                        "evidence_version": "1.0",
                        "sprint_id": "02",
                        "status": "candidate",
                        "authoritative": False,
                        "next_sprint_authorized": None,
                    }
                ),
                encoding="utf-8",
            )

            errors: list[str] = []
            with patch.object(VALIDATOR, "ROOT", root):
                VALIDATOR.validate_evidence_sets(errors)

            self.assertIn(
                "Sprint closure candidate is not fail-closed: "
                "docs/evidence/sprint-02/closure-candidate.json",
                errors,
            )

    def test_v2_close_receipt_requires_immutable_review_binding(self) -> None:
        """A v2 close receipt cannot authorize work without a passed review."""
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            sprint = root / "docs" / "evidence" / "sprint-03"
            sprint.mkdir(parents=True)
            for filename in VALIDATOR.EVIDENCE_FILES:
                payload: dict[str, object] = {
                    "evidence_version": "1.0",
                    "sprint_id": "03",
                    "status": "ok",
                }
                if filename == "close-receipt.json":
                    payload.update(
                        {
                            "status": "closed",
                            "closure_protocol_version": "2.0",
                            "authoritative": True,
                            "reviewed_candidate_commit": "0" * 40,
                            "next_sprint_authorized": "04",
                            "closed_at": "2026-07-17T21:21:32Z",
                        }
                    )
                (sprint / filename).write_text(
                    json.dumps(payload),
                    encoding="utf-8",
                )

            errors: list[str] = []
            with patch.object(VALIDATOR, "ROOT", root):
                VALIDATOR.validate_evidence_sets(errors)

            self.assertIn(
                "Sprint v2 close receipt is not review-bound: "
                "docs/evidence/sprint-03/close-receipt.json",
                errors,
            )

    def test_sprint03_close_receipt_cannot_downgrade_protocol(self) -> None:
        """Sprint 03 and later cannot fall back to the legacy receipt shape."""
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            sprint = root / "docs" / "evidence" / "sprint-03"
            sprint.mkdir(parents=True)
            for filename in VALIDATOR.EVIDENCE_FILES:
                payload = {
                    "evidence_version": "1.0",
                    "sprint_id": "03",
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

            self.assertIn(
                "Sprint close receipt requires closure protocol 2.0: "
                "docs/evidence/sprint-03/close-receipt.json",
                errors,
            )

    def test_sprint_roadmap_path_covers_every_planned_range(self) -> None:
        """Attestations must update the roadmap that owns their Sprint."""
        expectations = {
            "03": "docs/04-roadmap/SPRINTS_00_04_FOUNDATION.md",
            "05": "docs/04-roadmap/SPRINTS_05_09_INTELLIGENCE.md",
            "09": "docs/04-roadmap/SPRINTS_05_09_INTELLIGENCE.md",
            "10": "docs/04-roadmap/SPRINTS_10_14_PRODUCTION.md",
            "14": "docs/04-roadmap/SPRINTS_10_14_PRODUCTION.md",
        }
        for sprint_id, expected in expectations.items():
            with self.subTest(sprint_id=sprint_id):
                self.assertEqual(
                    expected,
                    VALIDATOR.sprint_roadmap_path(sprint_id),
                )

        self.assertIsNone(VALIDATOR.sprint_roadmap_path("not-numeric"))
        self.assertIsNone(VALIDATOR.sprint_roadmap_path("15"))

    def test_attestation_must_match_trusted_main_topology(self) -> None:
        """Protected CI rejects unpublished and stale-base attestations."""
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            subprocess.run(
                ["git", "init", "--initial-branch=main", str(root)],
                check=True,
                capture_output=True,
            )
            subprocess.run(
                ["git", "-C", str(root), "config", "user.name", "test"],
                check=True,
            )
            subprocess.run(
                ["git", "-C", str(root), "config", "user.email", "test@test"],
                check=True,
            )
            (root / "state").write_text("main\n", encoding="utf-8")
            subprocess.run(
                ["git", "-C", str(root), "add", "state"],
                check=True,
            )
            subprocess.run(
                ["git", "-C", str(root), "commit", "-m", "main"],
                check=True,
                capture_output=True,
            )
            base = subprocess.run(
                ["git", "-C", str(root), "rev-parse", "HEAD"],
                check=True,
                capture_output=True,
                text=True,
            ).stdout.strip()
            subprocess.run(
                ["git", "-C", str(root), "switch", "-c", "candidate"],
                check=True,
                capture_output=True,
            )
            (root / "candidate").write_text("candidate\n", encoding="utf-8")
            subprocess.run(
                ["git", "-C", str(root), "add", "candidate"],
                check=True,
            )
            subprocess.run(
                ["git", "-C", str(root), "commit", "-m", "candidate"],
                check=True,
                capture_output=True,
            )
            candidate = subprocess.run(
                ["git", "-C", str(root), "rev-parse", "HEAD"],
                check=True,
                capture_output=True,
                text=True,
            ).stdout.strip()
            (root / "attestation").write_text("attestation\n", encoding="utf-8")
            subprocess.run(
                ["git", "-C", str(root), "add", "attestation"],
                check=True,
            )
            subprocess.run(
                ["git", "-C", str(root), "commit", "-m", "attestation"],
                check=True,
                capture_output=True,
            )
            attestation = subprocess.run(
                ["git", "-C", str(root), "rev-parse", "HEAD"],
                check=True,
                capture_output=True,
                text=True,
            ).stdout.strip()

            environment = {
                "FORJA_ENFORCE_TRUSTED_MAIN": "1",
                "FORJA_TRUSTED_MAIN_SHA": base,
            }
            with patch.object(VALIDATOR, "ROOT", root), patch.dict(
                os.environ,
                environment,
            ):
                self.assertFalse(
                    VALIDATOR.attestation_matches_trusted_main(
                        candidate,
                        attestation,
                    )
                )

            environment["FORJA_TRUSTED_MAIN_SHA"] = candidate
            with patch.object(VALIDATOR, "ROOT", root), patch.dict(
                os.environ,
                environment,
            ):
                self.assertTrue(
                    VALIDATOR.attestation_matches_trusted_main(
                        candidate,
                        attestation,
                    )
                )

            subprocess.run(
                [
                    "git",
                    "-C",
                    str(root),
                    "switch",
                    "-c",
                    "advanced",
                    candidate,
                ],
                check=True,
                capture_output=True,
            )
            (root / "unrelated").write_text("unrelated\n", encoding="utf-8")
            subprocess.run(
                ["git", "-C", str(root), "add", "unrelated"],
                check=True,
            )
            subprocess.run(
                ["git", "-C", str(root), "commit", "-m", "unrelated"],
                check=True,
                capture_output=True,
            )
            advanced = subprocess.run(
                ["git", "-C", str(root), "rev-parse", "HEAD"],
                check=True,
                capture_output=True,
                text=True,
            ).stdout.strip()
            environment["FORJA_TRUSTED_MAIN_SHA"] = advanced
            with patch.object(VALIDATOR, "ROOT", root), patch.dict(
                os.environ,
                environment,
            ):
                self.assertFalse(
                    VALIDATOR.attestation_matches_trusted_main(
                        candidate,
                        attestation,
                    )
                )

            environment["FORJA_TRUSTED_MAIN_SHA"] = attestation
            with patch.object(VALIDATOR, "ROOT", root), patch.dict(
                os.environ,
                environment,
            ):
                self.assertTrue(
                    VALIDATOR.attestation_matches_trusted_main(
                        candidate,
                        attestation,
                    )
                )

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
