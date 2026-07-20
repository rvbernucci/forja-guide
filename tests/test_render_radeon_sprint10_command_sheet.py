"""Tests for the Sprint 10 Radeon operator command sheet renderer."""

from __future__ import annotations

import importlib.util
import sys
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT_PATH = ROOT / "scripts" / "render_radeon_sprint10_command_sheet.py"


def load_module(path: Path, name: str):
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Unable to load {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


SHEET = load_module(SCRIPT_PATH, "render_radeon_sprint10_command_sheet")


class RadeonSprint10CommandSheetTests(unittest.TestCase):
    def test_sheet_contains_ordered_evidence_flow(self) -> None:
        body = render()

        expected = [
            "preflight_radeon_ssh.py 36.150.116.206 31200",
            "wait_radeon_ssh.py 36.150.116.206 31200",
            "python3 scripts/validate_repository.py",
            "prepare_radeon_sprint10_operator_bundle.py",
            "verify_radeon_operator_bundle.py",
            "check_radeon_sprint10_private_inputs.py",
            "run-sprint10-evidence.sh",
            "diagnose_radeon_sprint10_artifacts.py",
            "Bring Back Only The Public Summary",
            "ingest_radeon_sprint10_public_summary.py",
            "verify_sprint10_review_readiness.py",
        ]
        positions = [body.index(item) for item in expected]
        self.assertEqual(sorted(positions), positions)

    def test_sheet_is_public_safe_by_default(self) -> None:
        body = render()

        forbidden = ["FIREWORKS_API_KEY", "AWS_SECRET", "hf_", "password=", "token="]
        for value in forbidden:
            self.assertNotIn(value, body)
        self.assertIn("contains no credentials", body)
        self.assertIn("next_sprint_authorized: false", body)

    def test_sheet_uses_supplied_branch_and_paths(self) -> None:
        body = SHEET.render_sheet(
            host="example.test",
            port="2222",
            repo_url="https://github.com/example/repo",
            branch="feature/demo",
            repo_dir="/workspace/demo",
            evidence_dir="/workspace/evidence",
            snapshot_root="/secure/demo",
        )

        self.assertIn("preflight_radeon_ssh.py example.test 2222", body)
        self.assertIn("wait_radeon_ssh.py example.test 2222", body)
        self.assertIn("git checkout feature/demo", body)
        self.assertIn("git clone https://github.com/example/repo .", body)
        self.assertIn("--snapshot-root /secure/demo", body)
        self.assertIn("/workspace/evidence/radeon-public-summary.json", body)


def render() -> str:
    return SHEET.render_sheet(
        host="36.150.116.206",
        port="31200",
        repo_url=SHEET.DEFAULT_REPO,
        branch=SHEET.DEFAULT_BRANCH,
        repo_dir="/workspace/forja-guide",
        evidence_dir="/workspace/forja-alpha-sprint10-evidence",
        snapshot_root="/secure/forja",
    )


if __name__ == "__main__":
    unittest.main()
