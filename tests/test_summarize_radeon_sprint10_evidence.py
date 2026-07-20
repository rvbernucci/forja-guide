"""Tests for public Radeon Sprint 10 evidence summaries."""

from __future__ import annotations

import importlib.util
import json
import tempfile
import unittest
from pathlib import Path


MODULE_PATH = (
    Path(__file__).resolve().parents[1]
    / "scripts"
    / "summarize_radeon_sprint10_evidence.py"
)
SPEC = importlib.util.spec_from_file_location("summarize_radeon_sprint10_evidence", MODULE_PATH)
if SPEC is None or SPEC.loader is None:
    raise RuntimeError("Unable to load Sprint 10 public summary module")
SUMMARY = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(SUMMARY)


class RadeonSprint10SummaryTests(unittest.TestCase):
    def test_verified_recovery_produces_public_pass_summary(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            recovery = write_recovery(root, verified=True)
            args = namespace(root, recovery)

            summary, exit_code = SUMMARY.build_summary(args)

        self.assertEqual(0, exit_code)
        self.assertEqual("passed", summary["status"])
        self.assertTrue(summary["private_recovery_verified"])
        self.assertEqual(5, summary["counts"]["valid_evidence_items"])
        self.assertTrue(summary["policy"]["raw_artifacts_outside_git"])
        self.assertFalse(summary["policy"]["stores_vectors"])
        self.assertNotIn("path", json.dumps(summary))

    def test_missing_evidence_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            recovery = write_recovery(root, verified=True)
            payload = json.loads(recovery.read_text(encoding="utf-8"))
            payload["evidence"].pop("model_benchmark")
            recovery.write_text(json.dumps(payload), encoding="utf-8")
            args = namespace(root, recovery)

            summary, exit_code = SUMMARY.build_summary(args)

        self.assertEqual(2, exit_code)
        self.assertEqual("partial_or_failed", summary["status"])
        self.assertIn("model_benchmark", summary["missing_evidence"])
        self.assertIn("missing_evidence", summary["errors"])

    def test_invalid_evidence_hash_is_reported_without_private_path(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            recovery = write_recovery(root, verified=True)
            payload = json.loads(recovery.read_text(encoding="utf-8"))
            payload["evidence"]["runtime_receipt"]["sha256"] = "not-a-hash"
            recovery.write_text(json.dumps(payload), encoding="utf-8")
            args = namespace(root, recovery)

            summary, exit_code = SUMMARY.build_summary(args)

        self.assertEqual(2, exit_code)
        receipt = next(item for item in summary["evidence"] if item["name"] == "runtime_receipt")
        self.assertIsNone(receipt["sha256"])
        self.assertIn("invalid_sha256", receipt["errors"])
        self.assertNotIn("/workspace", json.dumps(summary))


def namespace(root: Path, recovery: Path) -> object:
    return type(
        "Args",
        (),
        {
            "recovery": recovery,
            "output": root / "summary.json",
            "recorded_at": "2026-07-20T18:20:00Z",
        },
    )()


def write_recovery(root: Path, *, verified: bool) -> Path:
    evidence = {}
    for index, name in enumerate(sorted(SUMMARY.EXPECTED_EVIDENCE_KEYS)):
        evidence[name] = {
            "path": f"private-{name}.json",
            "sha256": f"{index + 1:064x}",
            "valid": verified,
            "errors": [] if verified else [f"{name}_failed"],
        }
    path = root / "recovery.json"
    path.write_text(
        json.dumps(
            {
                "schema_version": "1.0",
                "report_kind": "forja_alpha_competition_profile_recovery",
                "recorded_at": "2026-07-20T18:20:00Z",
                "expected_commit": "a" * 40,
                "minimum_model_candidates": 2,
                "evidence": evidence,
                "verified": verified,
            }
        ),
        encoding="utf-8",
    )
    return path


if __name__ == "__main__":
    unittest.main()
