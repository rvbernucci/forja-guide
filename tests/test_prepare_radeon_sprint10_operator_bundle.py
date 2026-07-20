"""Tests for the private Sprint 10 Radeon operator bundle generator."""

from __future__ import annotations

import importlib.util
import json
import stat
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT_PATH = ROOT / "scripts" / "prepare_radeon_sprint10_operator_bundle.py"


def load_module(path: Path, name: str):
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Unable to load {path}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


BUNDLE = load_module(SCRIPT_PATH, "prepare_radeon_sprint10_operator_bundle")


class RadeonSprint10OperatorBundleTests(unittest.TestCase):
    def test_prepare_bundle_writes_private_templates(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            output_dir = Path(directory) / "bundle"

            report = BUNDLE.prepare_bundle(
                output_dir=output_dir,
                model_base_url="http://127.0.0.1:8000/v1",
                second_model_base_url="http://localhost:8001/v1",
                embedding_base_url="http://127.0.0.1:8081/v1",
                embedding_model="local-embedder",
            )

            candidates = json.loads(
                (output_dir / "radeon-model-candidates.template.json").read_text(
                    encoding="utf-8"
                )
            )
            command_body = (output_dir / "run-sprint10-evidence.sh").read_text(
                encoding="utf-8"
            )

        self.assertEqual("prepared", report["status"])
        self.assertEqual(
            [
                "README.md",
                "radeon-model-candidates.template.json",
                "run-sprint10-evidence.sh",
                "sprint10-env.template.sh",
            ],
            report["files"],
        )
        self.assertEqual("1.0", candidates["schema_version"])
        self.assertEqual(2, len(candidates["candidates"]))
        self.assertEqual("http://localhost:8001/v1", candidates["candidates"][1]["base_url"])
        self.assertIn("check_radeon_sprint10_private_inputs.py \\\n", command_body)
        self.assertLess(
            command_body.index("check_radeon_sprint10_private_inputs.py"),
            command_body.index("capture_radeon_runtime_receipt.py"),
        )

    def test_rejects_remote_urls(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaisesRegex(ValueError, "loopback"):
                BUNDLE.prepare_bundle(
                    output_dir=Path(directory),
                    model_base_url="https://api.example.com/v1",
                    second_model_base_url="http://127.0.0.1:8001/v1",
                    embedding_base_url="http://127.0.0.1:8081/v1",
                    embedding_model="local-embedder",
                )

    def test_private_file_modes_are_restrictive(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            output_dir = Path(directory) / "bundle"
            BUNDLE.prepare_bundle(
                output_dir=output_dir,
                model_base_url="http://127.0.0.1:8000/v1",
                second_model_base_url="http://127.0.0.1:8001/v1",
                embedding_base_url="http://127.0.0.1:8081/v1",
                embedding_model="local-embedder",
            )

            env_mode = stat.S_IMODE((output_dir / "sprint10-env.template.sh").stat().st_mode)
            candidates_mode = stat.S_IMODE(
                (output_dir / "radeon-model-candidates.template.json").stat().st_mode
            )

        self.assertEqual(0o700, env_mode)
        self.assertEqual(0o600, candidates_mode)


if __name__ == "__main__":
    unittest.main()
