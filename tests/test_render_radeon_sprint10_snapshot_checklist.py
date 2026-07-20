"""Tests for the Sprint 10 Radeon private snapshot checklist renderer."""

from __future__ import annotations

import importlib.util
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT_PATH = ROOT / "scripts" / "render_radeon_sprint10_snapshot_checklist.py"


def load_module(path: Path, name: str):
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Unable to load {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


CHECKLIST = load_module(SCRIPT_PATH, "render_radeon_sprint10_snapshot_checklist")


class RadeonSprint10SnapshotChecklistTests(unittest.TestCase):
    def test_checklist_uses_private_input_contract(self) -> None:
        required = CHECKLIST.load_private_input_contract()
        body = CHECKLIST.render_checklist(
            snapshot_root="/secure/forja",
            model_candidates="/secure/forja/radeon-model-candidates.json",
            required_snapshots=required,
        )

        for family, logical_path in required.items():
            self.assertIn(f"`{family}`", body)
            self.assertIn(f"`/secure/forja/{logical_path}`", body)
        self.assertIn("check_radeon_sprint10_private_inputs.py", body)
        self.assertIn("build_alpha_snapshot_manifest.py", body)
        self.assertIn("does not collect evidence", body)
        self.assertIn("Only the sanitized `radeon-public-summary.json` leaves", body)

    def test_cli_writes_output(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            output = Path(tmp) / "snapshot-checklist.md"
            code = CHECKLIST.main(["--output", output.as_posix()])
            body = output.read_text(encoding="utf-8")

        self.assertEqual(0, code)
        self.assertIn("Sprint 10 Private Snapshot Checklist", body)
        self.assertIn("missing_required_families", body)


if __name__ == "__main__":
    unittest.main()
