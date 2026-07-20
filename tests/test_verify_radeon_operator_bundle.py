"""Tests for Sprint 10 Radeon operator bundle verification."""

from __future__ import annotations

import importlib.util
import json
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
PREPARE_PATH = ROOT / "scripts" / "prepare_radeon_sprint10_operator_bundle.py"
VERIFY_PATH = ROOT / "scripts" / "verify_radeon_operator_bundle.py"


def load_module(path: Path, name: str):
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Unable to load {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


PREPARE = load_module(PREPARE_PATH, "prepare_radeon_sprint10_operator_bundle_for_verify")
VERIFY = load_module(VERIFY_PATH, "verify_radeon_operator_bundle")


class RadeonOperatorBundleVerificationTests(unittest.TestCase):
    def test_fresh_template_is_valid_only_when_placeholders_are_allowed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            bundle_dir = Path(directory) / "bundle"
            PREPARE.prepare_bundle(
                output_dir=bundle_dir,
                model_base_url="http://127.0.0.1:8000/v1",
                second_model_base_url="http://127.0.0.1:8001/v1",
                embedding_base_url="http://127.0.0.1:8081/v1",
                embedding_model="<local-embedding-model-id>",
            )

            strict_report, strict_code = VERIFY.verify_bundle(bundle_dir)
            template_report, template_code = VERIFY.verify_bundle(
                bundle_dir,
                allow_placeholders=True,
            )

        self.assertEqual(2, strict_code)
        self.assertIn("placeholder_radeon-model-candidates.template.json", strict_report["errors"])
        self.assertEqual(0, template_code)
        self.assertTrue(template_report["ready_to_run"])

    def test_filled_bundle_passes_strict_validation(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            bundle_dir = Path(directory) / "bundle"
            PREPARE.prepare_bundle(
                output_dir=bundle_dir,
                model_base_url="http://127.0.0.1:8000/v1",
                second_model_base_url="http://127.0.0.1:8001/v1",
                embedding_base_url="http://127.0.0.1:8081/v1",
                embedding_model="local-embedder",
            )
            fill_candidate_template(bundle_dir)
            replace_in_file(bundle_dir / "sprint10-env.template.sh", "<local-embedding-model-id>", "local-embedder")

            report, exit_code = VERIFY.verify_bundle(bundle_dir)

        self.assertEqual(0, exit_code)
        self.assertTrue(report["ready_to_run"])
        self.assertEqual([], report["errors"])

    def test_remote_candidate_endpoint_fails(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            bundle_dir = Path(directory) / "bundle"
            PREPARE.prepare_bundle(
                output_dir=bundle_dir,
                model_base_url="http://127.0.0.1:8000/v1",
                second_model_base_url="http://127.0.0.1:8001/v1",
                embedding_base_url="http://127.0.0.1:8081/v1",
                embedding_model="local-embedder",
            )
            fill_candidate_template(bundle_dir)
            path = bundle_dir / "radeon-model-candidates.template.json"
            payload = json.loads(path.read_text(encoding="utf-8"))
            payload["candidates"][1]["base_url"] = "https://api.example.com/v1"
            path.write_text(json.dumps(payload), encoding="utf-8")

            report, exit_code = VERIFY.verify_bundle(bundle_dir)

        self.assertEqual(2, exit_code)
        self.assertIn("candidate_1_base_url", report["errors"])


def fill_candidate_template(bundle_dir: Path) -> None:
    path = bundle_dir / "radeon-model-candidates.template.json"
    payload = json.loads(path.read_text(encoding="utf-8"))
    payload["candidates"][0]["model"] = "local-model-a"
    payload["candidates"][0]["quantization"] = "fp16"
    payload["candidates"][1]["model"] = "local-model-b"
    payload["candidates"][1]["quantization"] = "int4"
    path.write_text(json.dumps(payload), encoding="utf-8")


def replace_in_file(path: Path, old: str, new: str) -> None:
    path.write_text(path.read_text(encoding="utf-8").replace(old, new), encoding="utf-8")


if __name__ == "__main__":
    unittest.main()
