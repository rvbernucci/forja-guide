"""Tests for the Sprint 10 Radeon web-terminal evidence sheet renderer."""

from __future__ import annotations

import importlib.util
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT_PATH = ROOT / "scripts" / "render_radeon_sprint10_web_terminal_sheet.py"


def load_module(path: Path, name: str):
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Unable to load {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


SHEET = load_module(SCRIPT_PATH, "render_radeon_sprint10_web_terminal_sheet")


class RadeonSprint10WebTerminalSheetTests(unittest.TestCase):
    def test_sheet_contains_web_terminal_fallback_flow(self) -> None:
        body = render()

        expected = [
            "Prepare The Repository",
            "prepare_radeon_sprint10_operator_bundle.py",
            "verify_radeon_operator_bundle.py",
            "Fill Private Inputs Outside Git",
            "Start Local Endpoints On Loopback",
            "check_radeon_sprint10_private_inputs.py",
            "run-sprint10-evidence.sh",
            "diagnose_radeon_sprint10_artifacts.py",
            "Export Only The Public Summary",
            "ingest_radeon_sprint10_public_summary.py",
            "verify_sprint10_review_readiness.py",
        ]
        positions = [body.index(item) for item in expected]
        self.assertEqual(sorted(positions), positions)

    def test_sheet_is_public_safe_and_fail_closed(self) -> None:
        body = render()

        forbidden = ["FIREWORKS_API_KEY", "AWS_SECRET", "hf_", "password=", "token="]
        for value in forbidden:
            self.assertNotIn(value, body)
        self.assertIn("contains no credentials", body)
        self.assertIn("Do not export private receipts", body)
        self.assertIn("next_sprint_authorized: false", body)

    def test_sheet_uses_supplied_paths(self) -> None:
        body = SHEET.render_sheet(
            repo_url="https://github.com/example/repo",
            branch="feature/demo",
            repo_dir="/workspace/demo",
            bundle_dir="/workspace/bundle",
            evidence_dir="/workspace/evidence",
            snapshot_root="/secure/demo",
        )

        self.assertIn("git clone https://github.com/example/repo .", body)
        self.assertIn("git checkout feature/demo", body)
        self.assertIn("cd /workspace/demo", body)
        self.assertIn("--bundle-dir /workspace/bundle", body)
        self.assertIn("--snapshot-root /secure/demo", body)
        self.assertIn("/workspace/evidence/radeon-public-summary.json", body)

    def test_cli_writes_output_file(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            output = Path(tmp) / "sheet.md"
            code = SHEET.main(["--output", output.as_posix()])
            self.assertEqual(0, code)
            self.assertIn("Sprint 10 Radeon Web-Terminal Evidence Sheet", output.read_text(encoding="utf-8"))


def render() -> str:
    return SHEET.render_sheet(
        repo_url=SHEET.DEFAULT_REPO,
        branch=SHEET.DEFAULT_BRANCH,
        repo_dir=SHEET.DEFAULT_REPO_DIR,
        bundle_dir=SHEET.DEFAULT_BUNDLE_DIR,
        evidence_dir=SHEET.DEFAULT_EVIDENCE_DIR,
        snapshot_root=SHEET.DEFAULT_SNAPSHOT_ROOT,
    )


if __name__ == "__main__":
    unittest.main()
