"""Tests for Alpha source snapshot restore manifest verification."""

from __future__ import annotations

import hashlib
import importlib.util
import json
import tempfile
import unittest
from pathlib import Path


MODULE_PATH = (
    Path(__file__).resolve().parents[1]
    / "scripts"
    / "verify_alpha_snapshot_manifest.py"
)
SPEC = importlib.util.spec_from_file_location("verify_alpha_snapshot_manifest", MODULE_PATH)
if SPEC is None or SPEC.loader is None:
    raise RuntimeError("Unable to load Alpha snapshot manifest verifier")
VERIFIER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(VERIFIER)


class AlphaSnapshotManifestTests(unittest.TestCase):
    def test_complete_manifest_verifies_all_required_families(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            manifest = write_manifest(root, families=sorted(VERIFIER.REQUIRED_FAMILIES))
            args = namespace(root, manifest)

            report, exit_code = VERIFIER.build_report(args)

        self.assertEqual(0, exit_code)
        self.assertTrue(report["verified"])
        self.assertEqual([], report["coverage"]["missing_required_families"])
        self.assertEqual(len(VERIFIER.REQUIRED_FAMILIES), report["manifest"]["snapshot_count"])

    def test_hash_mismatch_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            manifest = write_manifest(root, families=sorted(VERIFIER.REQUIRED_FAMILIES))
            payload = json.loads(manifest.read_text(encoding="utf-8"))
            payload["snapshots"][0]["sha256"] = "0" * 64
            manifest.write_text(json.dumps(payload), encoding="utf-8")
            args = namespace(root, manifest)

            report, exit_code = VERIFIER.build_report(args)

        self.assertEqual(2, exit_code)
        self.assertFalse(report["verified"])
        self.assertIn("sha256_mismatch", report["results"][0]["errors"])

    def test_path_escape_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            manifest = write_manifest(root, families=sorted(VERIFIER.REQUIRED_FAMILIES))
            payload = json.loads(manifest.read_text(encoding="utf-8"))
            payload["snapshots"][0]["logical_path"] = "../outside.json"
            manifest.write_text(json.dumps(payload), encoding="utf-8")
            args = namespace(root, manifest)

            report, exit_code = VERIFIER.build_report(args)

        self.assertEqual(2, exit_code)
        self.assertFalse(report["verified"])
        self.assertIn("unsafe_logical_path", report["results"][0]["errors"])

    def test_missing_required_family_fails_even_when_files_match(self) -> None:
        families = sorted(VERIFIER.REQUIRED_FAMILIES - {"market"})
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            manifest = write_manifest(root, families=families)
            args = namespace(root, manifest)

            report, exit_code = VERIFIER.build_report(args)

        self.assertEqual(2, exit_code)
        self.assertFalse(report["verified"])
        self.assertEqual(["market"], report["coverage"]["missing_required_families"])


def namespace(root: Path, manifest: Path) -> object:
    return type(
        "Args",
        (),
        {
            "manifest": manifest,
            "snapshot_root": root,
            "output": None,
            "recorded_at": "2026-07-20T14:00:00Z",
        },
    )()


def write_manifest(root: Path, *, families: list[str]) -> Path:
    snapshots: list[dict[str, object]] = []
    for family in families:
        path = root / "snapshots" / f"{family}.json"
        body = (family + "\n").encode("utf-8")
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(body)
        snapshots.append(
            {
                "source_family": family,
                "logical_path": str(path.relative_to(root)),
                "sha256": hashlib.sha256(body).hexdigest(),
                "size_bytes": len(body),
                "media_type": "application/json",
                "required": True,
            }
        )
    manifest = root / "alpha-source-manifest.json"
    manifest.write_text(
        json.dumps(
            {
                "schema_version": "1.0",
                "manifest_kind": "forja_alpha_source_snapshots",
                "generated_at": "2026-07-20T14:00:00Z",
                "snapshots": snapshots,
            }
        ),
        encoding="utf-8",
    )
    return manifest


if __name__ == "__main__":
    unittest.main()
