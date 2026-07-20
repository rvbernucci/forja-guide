"""Tests for Alpha snapshot manifest construction."""

from __future__ import annotations

import importlib.util
import tempfile
import unittest
from pathlib import Path


MODULE_PATH = (
    Path(__file__).resolve().parents[1]
    / "scripts"
    / "build_alpha_snapshot_manifest.py"
)
SPEC = importlib.util.spec_from_file_location("build_alpha_snapshot_manifest", MODULE_PATH)
if SPEC is None or SPEC.loader is None:
    raise RuntimeError("Unable to load Alpha snapshot manifest builder")
BUILDER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(BUILDER)


class AlphaSnapshotManifestBuilderTests(unittest.TestCase):
    def test_builds_complete_manifest_with_hashes_and_sizes(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            required = write_required_snapshots(root)
            args = namespace(root, required)

            manifest, exit_code = BUILDER.build_manifest(args)

        self.assertEqual(0, exit_code)
        self.assertEqual("forja_alpha_source_snapshots", manifest["manifest_kind"])
        self.assertEqual([], manifest["missing_required_families"])
        self.assertEqual(len(BUILDER.REQUIRED_FAMILIES), len(manifest["snapshots"]))
        self.assertTrue(all(len(snapshot["sha256"]) == 64 for snapshot in manifest["snapshots"]))
        self.assertTrue(all(snapshot["size_bytes"] > 0 for snapshot in manifest["snapshots"]))

    def test_missing_required_family_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            required = write_required_snapshots(root)
            required = [item for item in required if not item.startswith("market=")]
            args = namespace(root, required)

            manifest, exit_code = BUILDER.build_manifest(args)

        self.assertEqual(2, exit_code)
        self.assertEqual(["market"], manifest["missing_required_families"])

    def test_unsafe_path_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            args = namespace(root, ["sec_identity=../outside.json"])

            with self.assertRaises(ValueError) as context:
                BUILDER.build_manifest(args)

        self.assertIn("escapes root", str(context.exception))

    def test_duplicate_family_path_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            required = write_required_snapshots(root)
            required.append(required[0])
            args = namespace(root, required)

            manifest, exit_code = BUILDER.build_manifest(args)

        self.assertEqual(2, exit_code)
        self.assertIn("duplicate_snapshots", manifest)


def namespace(root: Path, required: list[str]) -> object:
    return type(
        "Args",
        (),
        {
            "snapshot_root": root,
            "required_snapshot": required,
            "optional_snapshot": [],
            "output": None,
            "recorded_at": "2026-07-20T18:10:00Z",
        },
    )()


def write_required_snapshots(root: Path) -> list[str]:
    specs: list[str] = []
    for family in sorted(BUILDER.REQUIRED_FAMILIES):
        logical_path = f"snapshots/{family}.json"
        path = root / logical_path
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(f"{family}\n", encoding="utf-8")
        specs.append(f"{family}={logical_path}")
    return specs


if __name__ == "__main__":
    unittest.main()
