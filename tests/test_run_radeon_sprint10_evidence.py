"""Tests for the Sprint 10 Radeon evidence runner."""

from __future__ import annotations

import importlib.util
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch


MODULE_PATH = (
    Path(__file__).resolve().parents[1]
    / "scripts"
    / "run_radeon_sprint10_evidence.py"
)
SPEC = importlib.util.spec_from_file_location("run_radeon_sprint10_evidence", MODULE_PATH)
if SPEC is None or SPEC.loader is None:
    raise RuntimeError("Unable to load Sprint 10 evidence runner")
RUNNER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(RUNNER)


class Sprint10EvidenceRunnerTests(unittest.TestCase):
    def test_dry_run_plan_includes_all_required_steps_and_outputs(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            args = namespace(root)
            with patch.object(RUNNER, "resolve_commit", return_value="a" * 40):
                plan = RUNNER.build_plan(args)

        self.assertEqual("radeon_sprint10_evidence_sequence", plan["plan_kind"])
        self.assertTrue(plan["policy"]["requires_loopback_model_endpoint"])
        self.assertTrue(plan["policy"]["requires_loopback_embedding_endpoint"])
        self.assertEqual("a" * 40, plan["expected_commit"])
        self.assertEqual(
            [
                "runtime_receipt",
                "runtime_readiness",
                "source_restore",
                "model_benchmark",
                "embedding_benchmark",
                "competition_profile_recovery",
            ],
            [step["step_id"] for step in plan["steps"]],
        )
        self.assertIn("embedding_benchmark", plan["outputs"])
        self.assertIn("--embedding-benchmark", plan["steps"][-1]["argv"])
        self.assertIn("--require-endpoints", plan["steps"][1]["argv"])

    def test_execution_stops_on_first_failed_step(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            args = namespace(root)
            with patch.object(RUNNER, "resolve_commit", return_value="b" * 40):
                plan = RUNNER.build_plan(args)

        calls: list[str] = []

        def fake_run_step(step: dict[str, object]) -> dict[str, object]:
            calls.append(str(step["step_id"]))
            return {
                "step_id": step["step_id"],
                "started_at": "2026-07-20T00:00:00Z",
                "finished_at": "2026-07-20T00:00:01Z",
                "exit_code": 2,
                "ok": False,
                "stdout_tail": "",
                "stderr_tail": "failed",
            }

        with patch.object(RUNNER, "run_step", side_effect=fake_run_step):
            report, exit_code = RUNNER.execute_plan(plan)

        self.assertEqual(2, exit_code)
        self.assertFalse(report["execution"]["ok"])
        self.assertEqual(["runtime_receipt"], calls)


def namespace(root: Path) -> object:
    return type(
        "Args",
        (),
        {
            "evidence_dir": root / "evidence",
            "source_manifest": root / "manifest.json",
            "snapshot_root": root / "snapshots",
            "model_task_set": Path("internal/alpha/testdata/radeon_model_selection_public_v1.json"),
            "model_candidates": root / "model-candidates.json",
            "embedding_input_set": Path("internal/alpha/testdata/radeon_embedding_public_v1.json"),
            "model_base_url": "http://127.0.0.1:8000",
            "embedding_base_url": "http://127.0.0.1:8001",
            "embedding_model": "local-embedding",
            "expected_commit": "a" * 40,
            "output_plan": None,
            "execution_report": None,
            "recorded_at": "2026-07-20T00:00:00Z",
            "dry_run": True,
            "base_image": "GH-proxy-stable",
            "storage_profile": "persistent_pvc",
            "ssh_profile": "enabled",
        },
    )()


if __name__ == "__main__":
    unittest.main()
