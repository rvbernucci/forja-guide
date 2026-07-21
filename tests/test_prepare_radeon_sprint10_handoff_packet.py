"""Tests for the Sprint 10 Radeon handoff packet generator."""

from __future__ import annotations

import importlib.util
import json
import stat
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT_PATH = ROOT / "scripts" / "prepare_radeon_sprint10_handoff_packet.py"


def load_module(path: Path, name: str):
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Unable to load {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


HANDOFF = load_module(SCRIPT_PATH, "prepare_radeon_sprint10_handoff_packet")


class RadeonSprint10HandoffPacketTests(unittest.TestCase):
    def test_packet_contains_public_safe_operator_files(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            report = prepare(Path(tmp))

            self.assertEqual("prepared", report["status"])
            self.assertTrue(report["public_safe"])
            self.assertFalse(report["collects_evidence"])
            self.assertFalse(report["next_sprint_authorized"])
            names = {Path(item["path"]).name for item in report["files"]}
            self.assertEqual(
                {
                    "quick-start.md",
                    "snapshot-checklist.md",
                    "command-sheet.md",
                    "web-terminal-bootstrap.sh",
                    "web-terminal-evidence.md",
                    "ssh-recovery.md",
                },
                names,
            )

    def test_packet_files_are_public_safe(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            report = prepare(Path(tmp))

            forbidden = ["FIREWORKS_API_KEY", "AWS_SECRET", "hf_", "password=", "token="]
            for item in report["files"]:
                body = Path(item["path"]).read_text(encoding="utf-8")
                for value in forbidden:
                    self.assertNotIn(value, body)
            manifest = json.loads(Path(report["manifest"]["path"]).read_text(encoding="utf-8"))
            self.assertTrue(manifest["public_safe"])
            self.assertFalse(manifest["collects_evidence"])

    def test_packet_permissions_are_restrictive(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            report = prepare(Path(tmp))

            self.assertEqual(0o700, stat.S_IMODE(Path(tmp).stat().st_mode))
            modes = {Path(item["path"]).name: item["mode"] for item in report["files"]}
            self.assertEqual("0o700", modes["web-terminal-bootstrap.sh"])
            self.assertEqual("0o600", modes["quick-start.md"])
            self.assertEqual("0o600", modes["snapshot-checklist.md"])
            self.assertEqual("0o600", modes["command-sheet.md"])
            self.assertEqual("0o600", modes["web-terminal-evidence.md"])
            self.assertEqual("0o600", modes["ssh-recovery.md"])
            self.assertEqual("0o600", report["manifest"]["mode"])

    def test_quick_start_indexes_safe_operator_order(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            report = prepare(Path(tmp))
            quick_start = next(
                Path(item["path"])
                for item in report["files"]
                if Path(item["path"]).name == "quick-start.md"
            )
            body = quick_start.read_text(encoding="utf-8")

            expected = [
                "Read `command-sheet.md`",
                "Run the SSH preflight",
                "copy\n   `web-terminal-bootstrap.sh`",
                "use `snapshot-checklist.md`",
                "Export only `/workspace/forja-alpha-sprint10-evidence/radeon-public-summary.json`",
                "Verify and ingest the public summary",
                "Request immutable review",
            ]
            positions = [body.index(item) for item in expected]
            self.assertEqual(sorted(positions), positions)
            self.assertIn("next_sprint_authorized: false", body)
            self.assertIn("Sprint 11 remains blocked", body)


def prepare(output_dir: Path) -> dict[str, object]:
    return HANDOFF.prepare_packet(
        output_dir=output_dir,
        host="36.150.116.206",
        port="31200",
        repo_url=HANDOFF.DEFAULT_REPO,
        branch=HANDOFF.DEFAULT_BRANCH,
        repo_dir=HANDOFF.DEFAULT_REPO_DIR,
        bundle_dir=HANDOFF.DEFAULT_BUNDLE_DIR,
        evidence_dir=HANDOFF.DEFAULT_EVIDENCE_DIR,
        snapshot_root=HANDOFF.DEFAULT_SNAPSHOT_ROOT,
    )


if __name__ == "__main__":
    unittest.main()
