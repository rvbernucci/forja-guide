"""Tests for integrated competition-profile recovery verification."""

from __future__ import annotations

import importlib.util
import json
import tempfile
import unittest
from pathlib import Path


MODULE_PATH = (
    Path(__file__).resolve().parents[1]
    / "scripts"
    / "verify_competition_profile_recovery.py"
)
SPEC = importlib.util.spec_from_file_location("verify_competition_profile_recovery", MODULE_PATH)
if SPEC is None or SPEC.loader is None:
    raise RuntimeError("Unable to load competition profile verifier")
RECOVERY = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(RECOVERY)


class CompetitionProfileRecoveryTests(unittest.TestCase):
    def test_complete_evidence_bundle_verifies(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            paths = write_evidence_bundle(root)
            args = namespace(root, paths)

            report, exit_code = RECOVERY.build_report(args)

        self.assertEqual(0, exit_code)
        self.assertTrue(report["verified"])
        self.assertTrue(all(item["valid"] for item in report["evidence"].values()))

    def test_readiness_and_candidate_failures_are_reported(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            paths = write_evidence_bundle(root)
            readiness = json.loads(paths["runtime_readiness"].read_text(encoding="utf-8"))
            readiness["policy"]["zero_remote_core_inference_proved"] = False
            paths["runtime_readiness"].write_text(json.dumps(readiness), encoding="utf-8")
            benchmark = json.loads(paths["model_benchmark"].read_text(encoding="utf-8"))
            benchmark["candidate_count"] = 1
            benchmark["candidates"] = benchmark["candidates"][:1]
            paths["model_benchmark"].write_text(json.dumps(benchmark), encoding="utf-8")
            args = namespace(root, paths)

            report, exit_code = RECOVERY.build_report(args)

        self.assertEqual(2, exit_code)
        self.assertFalse(report["verified"])
        self.assertIn(
            "runtime_readiness_zero_remote_not_proved",
            report["evidence"]["runtime_readiness"]["errors"],
        )
        self.assertIn(
            "model_benchmark_candidate_count",
            report["evidence"]["model_benchmark"]["errors"],
        )


def namespace(root: Path, paths: dict[str, Path]) -> object:
    return type(
        "Args",
        (),
        {
            "runtime_receipt": paths["runtime_receipt"],
            "runtime_readiness": paths["runtime_readiness"],
            "source_restore": paths["source_restore"],
            "model_benchmark": paths["model_benchmark"],
            "output": root / "recovery.json",
            "expected_commit": "a" * 40,
            "minimum_model_candidates": 2,
            "recorded_at": "2026-07-20T14:00:00Z",
        },
    )()


def write_evidence_bundle(root: Path) -> dict[str, Path]:
    paths = {
        "runtime_receipt": root / "runtime-receipt.json",
        "runtime_readiness": root / "runtime-readiness.json",
        "source_restore": root / "source-restore.json",
        "model_benchmark": root / "model-benchmark.json",
    }
    write_json(
        paths["runtime_receipt"],
        {
            "receipt_kind": "radeon_runtime_environment",
            "competition_profile": {"core_remote_inference_allowed": False},
            "checks": {
                "gpu_probe_available": True,
                "rocm_command_available": True,
                "torch_rocm_probe_available": True,
                "vllm_probe_available": True,
            },
            "git": {"commit": "a" * 40, "dirty": False},
        },
    )
    write_json(
        paths["runtime_readiness"],
        {
            "report_kind": "radeon_runtime_readiness",
            "ready": True,
            "policy": {
                "core_remote_inference_allowed": False,
                "model_endpoint_loopback": True,
                "embedding_endpoint_loopback": True,
                "zero_remote_core_inference_proved": True,
            },
        },
    )
    write_json(
        paths["source_restore"],
        {
            "report_kind": "forja_alpha_snapshot_restore_verification",
            "verified": True,
            "coverage": {"missing_required_families": []},
        },
    )
    write_json(
        paths["model_benchmark"],
        {
            "report_kind": "radeon_model_candidate_benchmark",
            "candidate_count": 2,
            "privacy": {
                "stores_response_bodies": False,
                "requires_loopback_endpoints": True,
            },
            "candidates": [
                candidate("candidate-a"),
                candidate("candidate-b"),
            ],
        },
    )
    return paths


def candidate(candidate_id: str) -> dict[str, object]:
    return {
        "candidate_id": candidate_id,
        "endpoint": {"loopback": True},
        "summary": {"ok_count": 4, "failed_count": 0},
    }


def write_json(path: Path, payload: object) -> None:
    path.write_text(json.dumps(payload), encoding="utf-8")


if __name__ == "__main__":
    unittest.main()
