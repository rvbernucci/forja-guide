"""Tests for Sprint 10 Radeon private input preflight."""

from __future__ import annotations

import argparse
import importlib.util
import json
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT_PATH = ROOT / "scripts" / "check_radeon_sprint10_private_inputs.py"


def load_module(path: Path, name: str):
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Unable to load {path}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


PREFLIGHT = load_module(SCRIPT_PATH, "check_radeon_sprint10_private_inputs")


class RadeonSprint10PrivateInputPreflightTests(unittest.TestCase):
    def test_complete_private_inputs_pass(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "secure"
            write_required_snapshots(root)
            candidates = root / "radeon-model-candidates.json"
            write_candidates(candidates)

            report, exit_code = PREFLIGHT.check_private_inputs(
                args(
                    snapshot_root=root,
                    model_candidates=candidates,
                    embedding_model="local-embedder",
                )
            )

        self.assertEqual(0, exit_code)
        self.assertTrue(report["ready_to_run"])
        self.assertEqual([], report["errors"])
        self.assertEqual(6, len(report["snapshots"]))
        self.assertEqual(["candidate-a", "candidate-b"], report["model_candidates"]["candidate_ids"])

    def test_missing_snapshot_fails_before_gpu_work(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "secure"
            write_required_snapshots(root)
            (root / "market" / "NVDA-adjusted.csv").unlink()
            candidates = root / "radeon-model-candidates.json"
            write_candidates(candidates)

            report, exit_code = PREFLIGHT.check_private_inputs(
                args(
                    snapshot_root=root,
                    model_candidates=candidates,
                    embedding_model="local-embedder",
                )
            )

        self.assertEqual(2, exit_code)
        self.assertFalse(report["ready_to_run"])
        self.assertIn("missing_snapshot_market", report["errors"])

    def test_candidate_placeholders_and_remote_urls_fail(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "secure"
            write_required_snapshots(root)
            candidates = root / "radeon-model-candidates.json"
            write_candidates(
                candidates,
                second_base_url="https://api.example.com/v1",
                first_model="<local-model>",
            )

            report, exit_code = PREFLIGHT.check_private_inputs(
                args(
                    snapshot_root=root,
                    model_candidates=candidates,
                    embedding_model="<local-embedder>",
                )
            )

        self.assertEqual(2, exit_code)
        self.assertIn("model_candidates_placeholder", report["errors"])
        self.assertIn("candidate_0_model", report["errors"])
        self.assertIn("candidate_1_base_url", report["errors"])
        self.assertIn("embedding_model_placeholder", report["errors"])


def args(
    *,
    snapshot_root: Path,
    model_candidates: Path,
    embedding_model: str,
    model_base_url: str = "http://127.0.0.1:8000/v1",
    embedding_base_url: str = "http://127.0.0.1:8081/v1",
) -> argparse.Namespace:
    return argparse.Namespace(
        snapshot_root=snapshot_root,
        model_candidates=model_candidates,
        model_base_url=model_base_url,
        embedding_base_url=embedding_base_url,
        embedding_model=embedding_model,
        output=None,
    )


def write_required_snapshots(root: Path) -> None:
    for family, logical_path in PREFLIGHT.REQUIRED_SNAPSHOTS.items():
        path = root / logical_path
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(f"{family}\n", encoding="utf-8")


def write_candidates(
    path: Path,
    *,
    first_model: str = "local-model-a",
    second_base_url: str = "http://127.0.0.1:8001/v1",
) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    payload = {
        "schema_version": "1.0",
        "candidates": [
            {
                "candidate_id": "candidate-a",
                "base_url": "http://127.0.0.1:8000/v1",
                "model": first_model,
            },
            {
                "candidate_id": "candidate-b",
                "base_url": second_base_url,
                "model": "local-model-b",
            },
        ],
    }
    path.write_text(json.dumps(payload), encoding="utf-8")


if __name__ == "__main__":
    unittest.main()
