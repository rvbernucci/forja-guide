"""Tests for public Forja Alpha deterministic-tool contract validation."""

from __future__ import annotations

import importlib.util
import json
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT_PATH = ROOT / "scripts" / "validate_alpha_tool_contracts.py"


def load_module(path: Path, name: str):
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Unable to load {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


VALIDATOR = load_module(SCRIPT_PATH, "validate_alpha_tool_contracts")


class AlphaToolContractValidationTests(unittest.TestCase):
    def test_public_tool_contract_file_is_ready(self) -> None:
        report, exit_code = VALIDATOR.validate_contracts(VALIDATOR.DEFAULT_CONTRACTS)

        self.assertEqual(0, exit_code)
        self.assertTrue(report["ready"])
        self.assertEqual([], report["errors"])
        self.assertEqual(sorted(VALIDATOR.REQUIRED_TOOLS), report["tools"])

    def test_missing_tool_fails(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "tools.json"
            payload = load_contract_payload()
            payload["tools"] = [
                tool for tool in payload["tools"] if tool["tool_name"] != "holdings.compare"
            ]
            path.write_text(json.dumps(payload), encoding="utf-8")

            report, exit_code = VALIDATOR.validate_contracts(path)

        self.assertEqual(2, exit_code)
        self.assertIn("missing_required_tools:holdings.compare", report["errors"])

    def test_receipt_fields_must_match_contract(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "tools.json"
            payload = load_contract_payload()
            payload["receipt_contract"]["required_fields"].remove("evidence_refs")
            path.write_text(json.dumps(payload), encoding="utf-8")

            report, exit_code = VALIDATOR.validate_contracts(path)

        self.assertEqual(2, exit_code)
        self.assertIn("receipt_required_fields", report["errors"])

    def test_duplicate_tool_fails(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "tools.json"
            payload = load_contract_payload()
            payload["tools"].append(dict(payload["tools"][0]))
            path.write_text(json.dumps(payload), encoding="utf-8")

            report, exit_code = VALIDATOR.validate_contracts(path)

        self.assertEqual(2, exit_code)
        self.assertIn("tool_evidence.pack_duplicate", report["errors"])


def load_contract_payload() -> dict[str, object]:
    return json.loads(VALIDATOR.DEFAULT_CONTRACTS.read_text(encoding="utf-8"))


if __name__ == "__main__":
    unittest.main()
