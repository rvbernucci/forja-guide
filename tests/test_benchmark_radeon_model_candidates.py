"""Tests for local Radeon model candidate benchmark reporting."""

from __future__ import annotations

import importlib.util
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch


MODULE_PATH = (
    Path(__file__).resolve().parents[1]
    / "scripts"
    / "benchmark_radeon_model_candidates.py"
)
SPEC = importlib.util.spec_from_file_location("benchmark_radeon_model_candidates", MODULE_PATH)
if SPEC is None or SPEC.loader is None:
    raise RuntimeError("Unable to load Radeon model benchmark module")
BENCH = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(BENCH)


class RadeonModelBenchmarkTests(unittest.TestCase):
    def test_benchmark_writes_sanitized_candidate_report(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            task_set = write_task_set(root)
            candidates = write_candidates(root, "http://127.0.0.1:8000", "local-model")
            args = namespace(root, task_set, candidates)

            with patch.object(BENCH, "post_json", side_effect=fake_chat_completion):
                report, exit_code = BENCH.build_report(args)

        self.assertEqual(0, exit_code)
        self.assertEqual(1, report["candidate_count"])
        self.assertFalse(report["privacy"]["stores_response_bodies"])
        self.assertEqual(2, report["candidates"][0]["summary"]["ok_count"])
        self.assertNotIn("evidence-grounded answer", json.dumps(report))
        self.assertTrue(report["candidates"][0]["results"][0]["response_sha256"])

    def test_remote_candidate_endpoint_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            task_set = write_task_set(root)
            candidates = write_candidates(root, "https://api.example.com", "remote-model")
            args = namespace(root, task_set, candidates)

            with self.assertRaises(ValueError) as context:
                BENCH.build_report(args)

        self.assertIn("not loopback", str(context.exception))

    def test_candidate_config_requires_schema_version(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            task_set = write_task_set(root)
            candidates = write_candidates(root, "http://127.0.0.1:8000", "local-model")
            payload = json.loads(candidates.read_text(encoding="utf-8"))
            payload.pop("schema_version")
            candidates.write_text(json.dumps(payload), encoding="utf-8")
            args = namespace(root, task_set, candidates)

            with self.assertRaises(ValueError) as context:
                BENCH.build_report(args)

        self.assertIn("schema_version", str(context.exception))

    def test_public_candidate_example_is_accepted_by_validator(self) -> None:
        example = (
            Path(__file__).resolve().parents[1]
            / "internal"
            / "alpha"
            / "testdata"
            / "radeon_model_candidates.example.json"
        )
        payload = json.loads(example.read_text(encoding="utf-8"))

        candidates = BENCH.validate_candidates(payload)

        self.assertEqual(2, len(candidates))
        self.assertTrue(all(candidate["base_url"].startswith("http://127.0.0.1") for candidate in candidates))


def fake_chat_completion(
    url: str,
    payload: dict[str, object],
    timeout: float,
) -> tuple[int, dict[str, object], None]:
    return (
        200,
        {
            "choices": [
                {
                    "finish_reason": "stop",
                    "message": {"content": "evidence-grounded answer"},
                }
            ],
            "usage": {
                "prompt_tokens": 12,
                "completion_tokens": 4,
                "total_tokens": 16,
            },
        },
        None,
    )


def namespace(root: Path, task_set: Path, candidates: Path) -> object:
    return type(
        "Args",
        (),
        {
            "task_set": task_set,
            "candidates": candidates,
            "output": root / "report.json",
            "timeout": 2.0,
            "recorded_at": "2026-07-20T14:00:00Z",
            "system_prompt": BENCH.DEFAULT_SYSTEM_PROMPT,
        },
    )()


def write_task_set(root: Path) -> Path:
    path = root / "tasks.json"
    path.write_text(
        json.dumps(
            {
                "schema_version": "1.0",
                "task_set_id": "test_tasks",
                "split": "public",
                "tasks": [
                    {
                        "task_id": "t1",
                        "category": "planning",
                        "prompt": "Plan one bounded step.",
                        "max_output_tokens": 32,
                    },
                    {
                        "task_id": "t2",
                        "category": "safety",
                        "prompt": "Reject investment advice.",
                        "max_output_tokens": 32,
                    },
                ],
            }
        ),
        encoding="utf-8",
    )
    return path


def write_candidates(root: Path, base_url: str, model: str) -> Path:
    path = root / "candidates.json"
    path.write_text(
        json.dumps(
            {
                "schema_version": "1.0",
                "candidates": [
                    {
                        "candidate_id": "candidate-a",
                        "base_url": base_url,
                        "model": model,
                    }
                ],
            }
        ),
        encoding="utf-8",
    )
    return path


if __name__ == "__main__":
    unittest.main()
