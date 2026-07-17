"""Tests for the dependency-free public repository validator."""

from __future__ import annotations

import hashlib
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

    def test_new_legacy_receipt_is_rejected_for_legacy_sprint_id(self) -> None:
        """Only the exact historical legacy receipts are grandfathered."""
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            sprint = root / "docs" / "evidence" / "sprint-02"
            sprint.mkdir(parents=True)
            for filename in VALIDATOR.EVIDENCE_FILES:
                payload = {
                    "evidence_version": "1.0",
                    "sprint_id": "02",
                    "status": (
                        "closed" if filename == "close-receipt.json" else "ok"
                    ),
                }
                if filename == "close-receipt.json":
                    payload["next_sprint_authorized"] = "99"
                (sprint / filename).write_text(
                    json.dumps(payload),
                    encoding="utf-8",
                )

            errors: list[str] = []
            with patch.object(VALIDATOR, "ROOT", root):
                VALIDATOR.validate_evidence_sets(errors)

            self.assertIn(
                "unrecognized legacy Sprint close receipt: "
                "docs/evidence/sprint-02/close-receipt.json",
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
                "unrecognized legacy Sprint close receipt: "
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

    def test_canonical_sprint_successor_is_exact_and_terminal(self) -> None:
        """Receipts authorize only the next planned Sprint; Sprint 14 is terminal."""
        self.assertEqual((True, "04"), VALIDATOR.canonical_sprint_successor("03"))
        self.assertEqual((True, "14"), VALIDATOR.canonical_sprint_successor("13"))
        self.assertEqual((True, None), VALIDATOR.canonical_sprint_successor("14"))
        self.assertEqual((False, None), VALIDATOR.canonical_sprint_successor("15"))
        for invalid in ("3", "003", "\u0660\u0663", "\uff10\uff13", "not-numeric"):
            with self.subTest(invalid=invalid):
                self.assertEqual(
                    (False, None),
                    VALIDATOR.canonical_sprint_successor(invalid),
                )
                self.assertIsNone(VALIDATOR.sprint_roadmap_path(invalid))

    def test_receipt_promotion_preserves_reviewed_candidate_content(self) -> None:
        """Attestation may add review fields but cannot rewrite reviewed evidence."""
        review_path = "docs/evidence/sprint-03/reviews/immutable.md"
        candidate = {
            "evidence_version": "1.0",
            "sprint_id": "03",
            "name": "MCP Control Surface",
            "status": "candidate",
            "closure_protocol_version": "2.0",
            "authoritative": False,
            "basis_commit": "a" * 40,
            "definition_of_done": {
                "implementation_committed": True,
                "independent_validation_recorded": False,
            },
            "acceptance": {"governed_lifecycle": True},
            "evidence_files": ["plan.json"],
            "supporting_artifacts": ["reviews/closure.md"],
            "next_sprint_authorized": None,
            "recorded_at": "2026-07-17T21:06:37Z",
        }
        review = {
            "result": "passed",
            "reviewed_commit": "b" * 40,
            "artifact_path": review_path,
            "artifact_sha256": "c" * 64,
        }
        receipt = {
            **candidate,
            "status": "closed",
            "authoritative": True,
            "definition_of_done": {
                "implementation_committed": True,
                "independent_validation_recorded": True,
            },
            "supporting_artifacts": ["reviews/closure.md", review_path],
            "next_sprint_authorized": "04",
            "candidate_recorded_at": candidate["recorded_at"],
            "reviewed_candidate_commit": "b" * 40,
            "immutable_review": review,
            "closed_at": "2026-07-17T22:10:00Z",
        }
        del receipt["recorded_at"]

        self.assertTrue(
            VALIDATOR.receipt_preserves_candidate(candidate, receipt, review_path)
        )
        receipt["acceptance"] = {"governed_lifecycle": False}
        self.assertFalse(
            VALIDATOR.receipt_preserves_candidate(candidate, receipt, review_path)
        )
        receipt["acceptance"] = {"governed_lifecycle": 1}
        self.assertFalse(
            VALIDATOR.receipt_preserves_candidate(candidate, receipt, review_path)
        )

    def test_receipt_promotion_preserves_json_numbers_losslessly(self) -> None:
        """Reviewed numeric tokens cannot be rewritten through float rounding."""
        review_path = "docs/evidence/sprint-03/reviews/immutable.md"
        candidate = VALIDATOR.load_lossless_json(
            """{
              "sprint_id":"03",
              "status":"candidate",
              "authoritative":false,
              "definition_of_done":{"independent_validation_recorded":false},
              "supporting_artifacts":[],
              "next_sprint_authorized":null,
              "recorded_at":"2026-07-17T21:06:37Z",
              "score":1.0000000000000001,
              "underflow":1e-324
            }"""
        )
        receipt = VALIDATOR.load_lossless_json(
            """{
              "sprint_id":"03",
              "status":"closed",
              "authoritative":true,
              "definition_of_done":{"independent_validation_recorded":true},
              "supporting_artifacts":["docs/evidence/sprint-03/reviews/immutable.md"],
              "next_sprint_authorized":"04",
              "candidate_recorded_at":"2026-07-17T21:06:37Z",
              "score":1.0,
              "underflow":0.0,
              "reviewed_candidate_commit":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
              "immutable_review":{},
              "closed_at":"2026-07-17T22:10:00Z"
            }"""
        )
        self.assertIsInstance(candidate, dict)
        self.assertIsInstance(receipt, dict)
        self.assertFalse(
            VALIDATOR.receipt_preserves_candidate(candidate, receipt, review_path)
        )

    def test_exact_two_phase_attestation_is_accepted(self) -> None:
        """A minimal direct-child promotion passes every protocol-v2 guard."""
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
            sprint = root / "docs/evidence/sprint-03"
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
            roadmap = root / "docs/04-roadmap"
            roadmap.mkdir(parents=True)
            master = roadmap / "MASTER_DEVELOPMENT_PLAN.md"
            detail = roadmap / "SPRINTS_00_04_FOUNDATION.md"
            master.write_text("candidate\n", encoding="utf-8")
            detail.write_text("candidate\n", encoding="utf-8")
            candidate = {
                "evidence_version": "1.0",
                "sprint_id": "03",
                "name": "MCP Control Surface",
                "status": "candidate",
                "closure_protocol_version": "2.0",
                "authoritative": False,
                "definition_of_done": {
                    "implementation_committed": True,
                    "independent_validation_recorded": False,
                },
                "acceptance": {"governed_lifecycle": True},
                "evidence_files": ["plan.json"],
                "supporting_artifacts": [],
                "next_sprint_authorized": None,
                "recorded_at": "2026-07-17T21:06:37Z",
            }
            candidate_path = sprint / VALIDATOR.CLOSURE_CANDIDATE_FILE
            candidate_path.write_text(json.dumps(candidate), encoding="utf-8")
            subprocess.run(
                ["git", "-C", str(root), "add", "."],
                check=True,
            )
            subprocess.run(
                ["git", "-C", str(root), "commit", "-m", "candidate"],
                check=True,
                capture_output=True,
            )
            candidate_commit = subprocess.run(
                ["git", "-C", str(root), "rev-parse", "HEAD"],
                check=True,
                capture_output=True,
                text=True,
            ).stdout.strip()

            review_path = "docs/evidence/sprint-03/reviews/immutable.md"
            review_file = root / review_path
            review_file.parent.mkdir()
            review_file.write_text("No findings.\n", encoding="utf-8")
            review_hash = hashlib.sha256(review_file.read_bytes()).hexdigest()
            review = {
                "result": "passed",
                "reviewed_commit": candidate_commit,
                "artifact_path": review_path,
                "artifact_sha256": review_hash,
            }
            receipt = {
                **candidate,
                "status": "closed",
                "authoritative": True,
                "definition_of_done": {
                    "implementation_committed": True,
                    "independent_validation_recorded": True,
                },
                "supporting_artifacts": [review_path],
                "next_sprint_authorized": "04",
                "candidate_recorded_at": candidate["recorded_at"],
                "reviewed_candidate_commit": candidate_commit,
                "immutable_review": review,
                "closed_at": "2026-07-17T22:10:00Z",
            }
            del receipt["recorded_at"]
            candidate_path.unlink()
            (sprint / "close-receipt.json").write_text(
                json.dumps(receipt),
                encoding="utf-8",
            )
            master.write_text("closed\n", encoding="utf-8")
            detail.write_text("closed\n", encoding="utf-8")
            subprocess.run(
                ["git", "-C", str(root), "add", "-A"],
                check=True,
            )
            subprocess.run(
                ["git", "-C", str(root), "commit", "-m", "attestation"],
                check=True,
                capture_output=True,
            )

            errors: list[str] = []
            environment = {
                "FORJA_ENFORCE_TRUSTED_MAIN": "1",
                "FORJA_TRUSTED_MAIN_SHA": candidate_commit,
            }
            with patch.object(VALIDATOR, "ROOT", root), patch.dict(
                os.environ,
                environment,
            ):
                VALIDATOR.validate_evidence_sets(errors)

            self.assertEqual([], errors)

    def test_v2_receipt_rejects_arbitrary_successor(self) -> None:
        """A non-empty but noncanonical successor must not authorize work."""
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path = root / "docs/evidence/sprint-03/close-receipt.json"
            path.parent.mkdir(parents=True)
            receipt = {
                "authoritative": True,
                "reviewed_candidate_commit": "a" * 40,
                "immutable_review": {
                    "result": "passed",
                    "reviewed_commit": "a" * 40,
                    "artifact_path": (
                        "docs/evidence/sprint-03/reviews/immutable.md"
                    ),
                    "artifact_sha256": "b" * 64,
                },
                "next_sprint_authorized": "99",
                "closed_at": "2026-07-17T22:10:00Z",
            }
            errors: list[str] = []
            with patch.object(VALIDATOR, "ROOT", root):
                VALIDATOR.validate_v2_close_receipt(receipt, path, errors)

            self.assertEqual(
                [
                    "Sprint v2 close receipt is not review-bound: "
                    "docs/evidence/sprint-03/close-receipt.json"
                ],
                errors,
            )

    def test_v2_receipt_fails_closed_without_git_history(self) -> None:
        """An archive cannot self-assert an authoritative protocol-v2 receipt."""
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path = root / "docs/evidence/sprint-03/close-receipt.json"
            path.parent.mkdir(parents=True)
            receipt = {
                "authoritative": True,
                "reviewed_candidate_commit": "a" * 40,
                "immutable_review": {
                    "result": "passed",
                    "reviewed_commit": "a" * 40,
                    "artifact_path": (
                        "docs/evidence/sprint-03/reviews/immutable.md"
                    ),
                    "artifact_sha256": "b" * 64,
                },
                "next_sprint_authorized": "04",
                "closed_at": "2026-07-17T22:10:00Z",
            }
            errors: list[str] = []
            with patch.object(VALIDATOR, "ROOT", root):
                VALIDATOR.validate_v2_close_receipt(receipt, path, errors)

            self.assertEqual(
                [
                    "Sprint v2 close receipt requires complete Git history: "
                    "docs/evidence/sprint-03/close-receipt.json"
                ],
                errors,
            )

    def test_shallow_repository_is_not_complete_history(self) -> None:
        """A shallow checkout cannot establish irreversible closure history."""
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "source"
            shallow = root / "shallow"
            subprocess.run(
                ["git", "init", "--initial-branch=main", str(source)],
                check=True,
                capture_output=True,
            )
            subprocess.run(
                ["git", "-C", str(source), "config", "user.name", "test"],
                check=True,
            )
            subprocess.run(
                ["git", "-C", str(source), "config", "user.email", "test@test"],
                check=True,
            )
            (source / "first").write_text("first\n", encoding="utf-8")
            subprocess.run(
                ["git", "-C", str(source), "add", "first"],
                check=True,
            )
            subprocess.run(
                ["git", "-C", str(source), "commit", "-m", "first"],
                check=True,
                capture_output=True,
            )
            (source / "second").write_text("second\n", encoding="utf-8")
            subprocess.run(
                ["git", "-C", str(source), "add", "second"],
                check=True,
            )
            subprocess.run(
                ["git", "-C", str(source), "commit", "-m", "second"],
                check=True,
                capture_output=True,
            )
            subprocess.run(
                ["git", "clone", "--depth", "1", f"file://{source}", str(shallow)],
                check=True,
                capture_output=True,
            )

            with patch.object(VALIDATOR, "ROOT", shallow):
                self.assertFalse(VALIDATOR.repository_has_complete_history())

    def test_closed_sprint_cannot_return_to_candidate_state(self) -> None:
        """Once introduced, an authoritative receipt cannot be replaced by a candidate."""
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
            sprint = root / "docs/evidence/sprint-03"
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
            candidate = {
                "evidence_version": "1.0",
                "sprint_id": "03",
                "status": "candidate",
                "closure_protocol_version": "2.0",
                "authoritative": False,
                "next_sprint_authorized": None,
            }
            candidate_path = sprint / VALIDATOR.CLOSURE_CANDIDATE_FILE
            receipt_path = sprint / "close-receipt.json"
            candidate_path.write_text(json.dumps(candidate), encoding="utf-8")
            subprocess.run(
                ["git", "-C", str(root), "add", "."],
                check=True,
            )
            subprocess.run(
                ["git", "-C", str(root), "commit", "-m", "candidate"],
                check=True,
                capture_output=True,
            )
            candidate_path.unlink()
            receipt_path.write_text("{}", encoding="utf-8")
            subprocess.run(
                ["git", "-C", str(root), "add", "-A"],
                check=True,
            )
            subprocess.run(
                ["git", "-C", str(root), "commit", "-m", "close"],
                check=True,
                capture_output=True,
            )
            receipt_path.unlink()
            candidate_path.write_text(json.dumps(candidate), encoding="utf-8")
            subprocess.run(
                ["git", "-C", str(root), "add", "-A"],
                check=True,
            )
            subprocess.run(
                ["git", "-C", str(root), "commit", "-m", "reopen"],
                check=True,
                capture_output=True,
            )

            errors: list[str] = []
            with patch.object(VALIDATOR, "ROOT", root):
                VALIDATOR.validate_evidence_sets(errors)

            self.assertIn(
                "closed Sprint cannot return to candidate state: "
                "docs/evidence/sprint-03/closure-candidate.json",
                errors,
            )

    def test_reintroduced_receipt_cannot_hide_a_reopened_sprint(self) -> None:
        """A receipt-to-candidate-to-receipt history remains invalid at final tip."""
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
            sprint = root / "docs/evidence/sprint-03"
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
            roadmaps = root / "docs/04-roadmap"
            roadmaps.mkdir(parents=True)
            master = roadmaps / "MASTER_DEVELOPMENT_PLAN.md"
            detail = roadmaps / "SPRINTS_00_04_FOUNDATION.md"
            master.write_text("old close\n", encoding="utf-8")
            detail.write_text("old close\n", encoding="utf-8")
            receipt_path = sprint / "close-receipt.json"
            receipt_path.write_text("{}", encoding="utf-8")
            subprocess.run(["git", "-C", str(root), "add", "."], check=True)
            subprocess.run(
                ["git", "-C", str(root), "commit", "-m", "old close"],
                check=True,
                capture_output=True,
            )

            candidate = {
                "evidence_version": "1.0",
                "sprint_id": "03",
                "status": "candidate",
                "closure_protocol_version": "2.0",
                "authoritative": False,
                "definition_of_done": {
                    "independent_validation_recorded": False,
                },
                "supporting_artifacts": [],
                "next_sprint_authorized": None,
                "recorded_at": "2026-07-17T21:06:37Z",
            }
            receipt_path.unlink()
            candidate_path = sprint / VALIDATOR.CLOSURE_CANDIDATE_FILE
            candidate_path.write_text(json.dumps(candidate), encoding="utf-8")
            master.write_text("candidate\n", encoding="utf-8")
            detail.write_text("candidate\n", encoding="utf-8")
            subprocess.run(["git", "-C", str(root), "add", "-A"], check=True)
            subprocess.run(
                ["git", "-C", str(root), "commit", "-m", "reopen"],
                check=True,
                capture_output=True,
            )
            candidate_commit = subprocess.run(
                ["git", "-C", str(root), "rev-parse", "HEAD"],
                check=True,
                capture_output=True,
                text=True,
            ).stdout.strip()

            review_path = "docs/evidence/sprint-03/reviews/immutable.md"
            review_file = root / review_path
            review_file.parent.mkdir()
            review_file.write_text("No findings.\n", encoding="utf-8")
            review = {
                "result": "passed",
                "reviewed_commit": candidate_commit,
                "artifact_path": review_path,
                "artifact_sha256": hashlib.sha256(review_file.read_bytes()).hexdigest(),
            }
            receipt = {
                **candidate,
                "status": "closed",
                "authoritative": True,
                "definition_of_done": {
                    "independent_validation_recorded": True,
                },
                "supporting_artifacts": [review_path],
                "next_sprint_authorized": "04",
                "candidate_recorded_at": candidate["recorded_at"],
                "reviewed_candidate_commit": candidate_commit,
                "immutable_review": review,
                "closed_at": "2026-07-17T22:10:00Z",
            }
            del receipt["recorded_at"]
            candidate_path.unlink()
            receipt_path.write_text(json.dumps(receipt), encoding="utf-8")
            master.write_text("closed\n", encoding="utf-8")
            detail.write_text("closed\n", encoding="utf-8")
            subprocess.run(["git", "-C", str(root), "add", "-A"], check=True)
            subprocess.run(
                ["git", "-C", str(root), "commit", "-m", "reclose"],
                check=True,
                capture_output=True,
            )

            errors: list[str] = []
            environment = {
                "FORJA_ENFORCE_TRUSTED_MAIN": "1",
                "FORJA_TRUSTED_MAIN_SHA": subprocess.run(
                    ["git", "-C", str(root), "rev-parse", "HEAD"],
                    check=True,
                    capture_output=True,
                    text=True,
                ).stdout.strip(),
            }
            with patch.object(VALIDATOR, "ROOT", root), patch.dict(
                os.environ,
                environment,
            ):
                VALIDATOR.validate_evidence_sets(errors)

            self.assertIn(
                "v2 close receipt has multiple introductions: "
                "docs/evidence/sprint-03/close-receipt.json",
                errors,
            )

    def test_merged_receipt_history_cannot_hide_reintroduction(self) -> None:
        """Full merge history exposes an add/delete hidden by path simplification."""
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
            sprint = root / "docs/evidence/sprint-03"
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
            roadmaps = root / "docs/04-roadmap"
            roadmaps.mkdir(parents=True)
            master = roadmaps / "MASTER_DEVELOPMENT_PLAN.md"
            detail = roadmaps / "SPRINTS_00_04_FOUNDATION.md"
            master.write_text("candidate\n", encoding="utf-8")
            detail.write_text("candidate\n", encoding="utf-8")
            candidate = {
                "evidence_version": "1.0",
                "sprint_id": "03",
                "status": "candidate",
                "closure_protocol_version": "2.0",
                "authoritative": False,
                "definition_of_done": {
                    "independent_validation_recorded": False,
                },
                "supporting_artifacts": [],
                "next_sprint_authorized": None,
                "recorded_at": "2026-07-17T21:06:37Z",
            }
            candidate_path = sprint / VALIDATOR.CLOSURE_CANDIDATE_FILE
            receipt_path = sprint / "close-receipt.json"
            candidate_path.write_text(json.dumps(candidate), encoding="utf-8")
            subprocess.run(["git", "-C", str(root), "add", "."], check=True)
            subprocess.run(
                ["git", "-C", str(root), "commit", "-m", "candidate"],
                check=True,
                capture_output=True,
            )

            subprocess.run(
                ["git", "-C", str(root), "switch", "-c", "side"],
                check=True,
                capture_output=True,
            )
            receipt_path.write_text("{}", encoding="utf-8")
            subprocess.run(
                ["git", "-C", str(root), "add", str(receipt_path)],
                check=True,
            )
            subprocess.run(
                ["git", "-C", str(root), "commit", "-m", "hidden add"],
                check=True,
                capture_output=True,
            )
            receipt_path.unlink()
            subprocess.run(
                ["git", "-C", str(root), "add", "-A"],
                check=True,
            )
            subprocess.run(
                ["git", "-C", str(root), "commit", "-m", "hidden delete"],
                check=True,
                capture_output=True,
            )
            subprocess.run(
                ["git", "-C", str(root), "switch", "main"],
                check=True,
                capture_output=True,
            )
            (root / "main-marker").write_text("main\n", encoding="utf-8")
            subprocess.run(
                ["git", "-C", str(root), "add", "main-marker"],
                check=True,
            )
            subprocess.run(
                ["git", "-C", str(root), "commit", "-m", "main advance"],
                check=True,
                capture_output=True,
            )
            subprocess.run(
                ["git", "-C", str(root), "merge", "--no-ff", "side", "-m", "merge"],
                check=True,
                capture_output=True,
            )
            candidate_commit = subprocess.run(
                ["git", "-C", str(root), "rev-parse", "HEAD"],
                check=True,
                capture_output=True,
                text=True,
            ).stdout.strip()

            review_path = "docs/evidence/sprint-03/reviews/immutable.md"
            review_file = root / review_path
            review_file.parent.mkdir()
            review_file.write_text("No findings.\n", encoding="utf-8")
            review = {
                "result": "passed",
                "reviewed_commit": candidate_commit,
                "artifact_path": review_path,
                "artifact_sha256": hashlib.sha256(review_file.read_bytes()).hexdigest(),
            }
            receipt = {
                **candidate,
                "status": "closed",
                "authoritative": True,
                "definition_of_done": {
                    "independent_validation_recorded": True,
                },
                "supporting_artifacts": [review_path],
                "next_sprint_authorized": "04",
                "candidate_recorded_at": candidate["recorded_at"],
                "reviewed_candidate_commit": candidate_commit,
                "immutable_review": review,
                "closed_at": "2026-07-17T22:10:00Z",
            }
            del receipt["recorded_at"]
            candidate_path.unlink()
            receipt_path.write_text(json.dumps(receipt), encoding="utf-8")
            master.write_text("closed\n", encoding="utf-8")
            detail.write_text("closed\n", encoding="utf-8")
            subprocess.run(["git", "-C", str(root), "add", "-A"], check=True)
            subprocess.run(
                ["git", "-C", str(root), "commit", "-m", "attest"],
                check=True,
                capture_output=True,
            )
            trusted_main = subprocess.run(
                ["git", "-C", str(root), "rev-parse", "HEAD"],
                check=True,
                capture_output=True,
                text=True,
            ).stdout.strip()

            errors: list[str] = []
            with patch.object(VALIDATOR, "ROOT", root), patch.dict(
                os.environ,
                {
                    "FORJA_ENFORCE_TRUSTED_MAIN": "1",
                    "FORJA_TRUSTED_MAIN_SHA": trusted_main,
                },
            ):
                VALIDATOR.validate_evidence_sets(errors)

            self.assertIn(
                "v2 close receipt has multiple introductions: "
                "docs/evidence/sprint-03/close-receipt.json",
                errors,
            )

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
