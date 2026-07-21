"""Tests for public Forja Alpha source-family contract validation."""

from __future__ import annotations

import importlib.util
import json
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT_PATH = ROOT / "scripts" / "validate_alpha_source_contracts.py"


def load_module(path: Path, name: str):
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Unable to load {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


VALIDATOR = load_module(SCRIPT_PATH, "validate_alpha_source_contracts")


class AlphaSourceContractValidationTests(unittest.TestCase):
    def test_public_contract_file_is_ready(self) -> None:
        report, exit_code = VALIDATOR.validate_contracts(VALIDATOR.DEFAULT_CONTRACTS)

        self.assertEqual(0, exit_code)
        self.assertTrue(report["ready"])
        self.assertEqual([], report["errors"])
        self.assertEqual(
            sorted(VALIDATOR.PRIVATE_INPUTS.REQUIRED_SNAPSHOTS),
            report["sprint10_required_families"],
        )

    def test_mismatched_required_snapshot_fails(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "contracts.json"
            payload = load_contract_payload()
            for family in payload["families"]:
                if family["source_family"] == "market":
                    family["expected_snapshot_path"] = "market/MSFT-adjusted.csv"
            path.write_text(json.dumps(payload), encoding="utf-8")

            report, exit_code = VALIDATOR.validate_contracts(path)

        self.assertEqual(2, exit_code)
        self.assertFalse(report["ready"])
        self.assertIn("sprint10_required_snapshot_mismatch", report["errors"])

    def test_duplicate_family_fails(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "contracts.json"
            payload = load_contract_payload()
            payload["families"].append(dict(payload["families"][0]))
            path.write_text(json.dumps(payload), encoding="utf-8")

            report, exit_code = VALIDATOR.validate_contracts(path)

        self.assertEqual(2, exit_code)
        self.assertIn("family_fred_duplicate", report["errors"])

    def test_missing_required_receipt_fields_fail(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "contracts.json"
            payload = load_contract_payload()
            payload["families"][0]["required_receipt_fields"] = []
            path.write_text(json.dumps(payload), encoding="utf-8")

            report, exit_code = VALIDATOR.validate_contracts(path)

        self.assertEqual(2, exit_code)
        self.assertIn("family_fred_required_receipt_fields", report["errors"])


def load_contract_payload() -> dict[str, object]:
    return json.loads(VALIDATOR.DEFAULT_CONTRACTS.read_text(encoding="utf-8"))


if __name__ == "__main__":
    unittest.main()
