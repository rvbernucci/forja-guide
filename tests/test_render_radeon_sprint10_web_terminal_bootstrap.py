"""Tests for the Sprint 10 Radeon web-terminal bootstrap renderer."""

from __future__ import annotations

import importlib.util
import stat
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT_PATH = ROOT / "scripts" / "render_radeon_sprint10_web_terminal_bootstrap.py"


def load_module(path: Path, name: str):
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Unable to load {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


BOOTSTRAP = load_module(SCRIPT_PATH, "render_radeon_sprint10_web_terminal_bootstrap")


class RadeonSprint10WebTerminalBootstrapTests(unittest.TestCase):
    def test_bootstrap_contains_ordered_setup_flow(self) -> None:
        body = render()

        expected = [
            "git clone \"$repo_url\" .",
            "git fetch origin",
            "git checkout \"$branch\"",
            "python3 scripts/validate_repository.py",
            "prepare_radeon_sprint10_operator_bundle.py",
            "verify_radeon_operator_bundle.py",
            "render_radeon_sprint10_web_terminal_sheet.py",
            "Sprint 10 remains open",
        ]
        positions = [body.index(item) for item in expected]
        self.assertEqual(sorted(positions), positions)

    def test_bootstrap_is_public_safe_and_does_not_collect_evidence(self) -> None:
        body = render()

        forbidden = [
            "FIREWORKS_API_KEY",
            "AWS_SECRET",
            "hf_",
            "password=",
            "token=",
            "run-sprint10-evidence.sh",
            "capture_radeon_runtime_receipt.py",
            "benchmark_radeon_model_candidates.py",
        ]
        for value in forbidden:
            self.assertNotIn(value, body)
        self.assertIn("It does not", body)
        self.assertIn("Sprint 11 is still not authorized", body)

    def test_bootstrap_quotes_supplied_paths(self) -> None:
        body = BOOTSTRAP.render_script(
            repo_url="https://github.com/example/repo",
            branch="feature/demo",
            repo_dir="/workspace/demo repo",
            bundle_dir="/workspace/private bundle",
            sheet_path="/workspace/sheet.md",
        )

        self.assertIn("repo_dir='/workspace/demo repo'", body)
        self.assertIn("bundle_dir='/workspace/private bundle'", body)
        self.assertIn("sheet_path=/workspace/sheet.md", body)
        self.assertIn("repo_url=https://github.com/example/repo", body)

    def test_cli_writes_executable_file(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            output = Path(tmp) / "bootstrap.sh"
            code = BOOTSTRAP.main(["--output", output.as_posix()])
            self.assertEqual(0, code)
            self.assertIn("Sprint 10 web-terminal bootstrap", output.read_text(encoding="utf-8"))
            self.assertEqual(0o700, stat.S_IMODE(output.stat().st_mode))


def render() -> str:
    return BOOTSTRAP.render_script(
        repo_url=BOOTSTRAP.DEFAULT_REPO,
        branch=BOOTSTRAP.DEFAULT_BRANCH,
        repo_dir=BOOTSTRAP.DEFAULT_REPO_DIR,
        bundle_dir=BOOTSTRAP.DEFAULT_BUNDLE_DIR,
        sheet_path=BOOTSTRAP.DEFAULT_SHEET_PATH,
    )


if __name__ == "__main__":
    unittest.main()
