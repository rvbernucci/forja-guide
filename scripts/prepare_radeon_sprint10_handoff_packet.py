#!/usr/bin/env python3
"""Prepare public-safe Sprint 10 Radeon operator handoff files."""

from __future__ import annotations

import argparse
import hashlib
import importlib.util
import json
import stat
import sys
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parents[1]
DEFAULT_BRANCH = "feat/sprint-10-radeon-runtime-v2"
DEFAULT_REPO = "https://github.com/rvbernucci/forja-guide"
DEFAULT_REPO_DIR = "/workspace/forja-guide"
DEFAULT_BUNDLE_DIR = "/workspace/forja-alpha-sprint10-operator-bundle"
DEFAULT_EVIDENCE_DIR = "/workspace/forja-alpha-sprint10-evidence"
DEFAULT_SNAPSHOT_ROOT = "/secure/forja"


def load_module(script_name: str, module_name: str):
    path = ROOT / "scripts" / script_name
    spec = importlib.util.spec_from_file_location(module_name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Unable to load {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[module_name] = module
    spec.loader.exec_module(module)
    return module


COMMAND_SHEET = load_module(
    "render_radeon_sprint10_command_sheet.py",
    "render_radeon_sprint10_command_sheet_for_handoff",
)
WEB_BOOTSTRAP = load_module(
    "render_radeon_sprint10_web_terminal_bootstrap.py",
    "render_radeon_sprint10_web_terminal_bootstrap_for_handoff",
)
WEB_SHEET = load_module(
    "render_radeon_sprint10_web_terminal_sheet.py",
    "render_radeon_sprint10_web_terminal_sheet_for_handoff",
)
SSH_RECOVERY = load_module(
    "render_radeon_ssh_recovery_sheet.py",
    "render_radeon_ssh_recovery_sheet_for_handoff",
)


def sha256_text(body: str) -> str:
    return hashlib.sha256(body.encode("utf-8")).hexdigest()


def write_file(path: Path, body: str, mode: int = 0o600) -> dict[str, Any]:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(body, encoding="utf-8")
    path.chmod(mode)
    return {
        "path": path.as_posix(),
        "sha256": sha256_text(body),
        "size_bytes": len(body.encode("utf-8")),
        "mode": oct(stat.S_IMODE(path.stat().st_mode)),
    }


def prepare_packet(
    *,
    output_dir: Path,
    host: str,
    port: str,
    repo_url: str,
    branch: str,
    repo_dir: str,
    bundle_dir: str,
    evidence_dir: str,
    snapshot_root: str,
) -> dict[str, Any]:
    output_dir.mkdir(parents=True, exist_ok=True)
    output_dir.chmod(0o700)

    command_sheet = COMMAND_SHEET.render_sheet(
        host=host,
        port=port,
        repo_url=repo_url,
        branch=branch,
        repo_dir=repo_dir,
        evidence_dir=evidence_dir,
        snapshot_root=snapshot_root,
    )
    web_bootstrap = WEB_BOOTSTRAP.render_script(
        repo_url=repo_url,
        branch=branch,
        repo_dir=repo_dir,
        bundle_dir=bundle_dir,
        sheet_path=f"{output_dir.as_posix()}/web-terminal-evidence.md",
    )
    web_sheet = WEB_SHEET.render_sheet(
        repo_url=repo_url,
        branch=branch,
        repo_dir=repo_dir,
        bundle_dir=bundle_dir,
        evidence_dir=evidence_dir,
        snapshot_root=snapshot_root,
    )
    recovery_sheet = SSH_RECOVERY.render_sheet(
        wait_report={"last_result": {"status": "unknown"}},
        host=host,
        port=port,
        repo_url=repo_url,
        branch=branch,
        repo_dir=repo_dir,
    )
    quick_start = render_quick_start(
        host=host,
        port=port,
        repo_url=repo_url,
        branch=branch,
        repo_dir=repo_dir,
        bundle_dir=bundle_dir,
        evidence_dir=evidence_dir,
        snapshot_root=snapshot_root,
    )

    files = [
        write_file(output_dir / "quick-start.md", quick_start),
        write_file(output_dir / "command-sheet.md", command_sheet),
        write_file(output_dir / "web-terminal-bootstrap.sh", web_bootstrap, mode=0o700),
        write_file(output_dir / "web-terminal-evidence.md", web_sheet),
        write_file(output_dir / "ssh-recovery.md", recovery_sheet),
    ]
    manifest = {
        "schema_version": "1.0",
        "report_kind": "radeon_sprint10_handoff_packet",
        "status": "prepared",
        "public_safe": True,
        "collects_evidence": False,
        "next_sprint_authorized": False,
        "host": host,
        "port": port,
        "repo_url": repo_url,
        "branch": branch,
        "repo_dir": repo_dir,
        "bundle_dir": bundle_dir,
        "evidence_dir": evidence_dir,
        "snapshot_root": snapshot_root,
        "files": files,
        "next_action": "Run web-terminal-bootstrap.sh inside Radeon if SSH remains unavailable; then follow web-terminal-evidence.md.",
    }
    manifest_body = json.dumps(manifest, indent=2, sort_keys=True) + "\n"
    manifest_path = output_dir / "manifest.json"
    manifest_path.write_text(manifest_body, encoding="utf-8")
    manifest_path.chmod(0o600)
    manifest["manifest"] = {
        "path": manifest_path.as_posix(),
        "sha256": sha256_text(manifest_body),
        "size_bytes": len(manifest_body.encode("utf-8")),
        "mode": oct(stat.S_IMODE(manifest_path.stat().st_mode)),
    }
    return manifest


def render_quick_start(
    *,
    host: str,
    port: str,
    repo_url: str,
    branch: str,
    repo_dir: str,
    bundle_dir: str,
    evidence_dir: str,
    snapshot_root: str,
) -> str:
    return f"""# Sprint 10 Radeon Handoff Quick Start

Status: public-safe operator index. This file only points to the prepared
handoff materials; it does not collect evidence, close Sprint 10, or authorize
Sprint 11.

## Boundary

- Host/port target: `{host}:{port}`
- Repository: `{repo_url}`
- Branch: `{branch}`
- Radeon checkout: `{repo_dir}`
- Private operator bundle: `{bundle_dir}`
- Private evidence directory: `{evidence_dir}`
- Private snapshot root: `{snapshot_root}`

## Recommended Order

1. Read `command-sheet.md` on the workstation.
2. Run the SSH preflight from `command-sheet.md`.
3. If SSH is ready, follow the workstation/Radeon command sequence in
   `command-sheet.md`.
4. If SSH is not ready but Jupyter/OpenCode web terminal works, copy
   `web-terminal-bootstrap.sh` into the Radeon terminal and run it there.
5. After bootstrap, follow the rendered `web-terminal-evidence.md` on Radeon.
6. Export only `{evidence_dir}/radeon-public-summary.json` back to the
   workstation.
7. Verify and ingest the public summary from the workstation.
8. Request immutable review, then use the Sprint 10 promotion checklist.

## Do Not Export Or Commit

- Raw runtime receipts, command logs, prompts, model outputs, embeddings,
  vectors, source bodies, private source snapshots, model weights, caches,
  tokens, credentials, or filled candidate files.
- Any close receipt before independent review has passed.

## Success Shape

The first target is `ready_for_independent_review: true` while
`next_sprint_authorized: false`. Sprint 11 remains blocked until the reviewed
candidate is promoted to a protocol v2 `close-receipt.json`.
"""


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", type=Path, default=Path("/tmp/forja-radeon-sprint10-handoff"))
    parser.add_argument("--host", default="<radeon-host>")
    parser.add_argument("--port", default="<radeon-port>")
    parser.add_argument("--repo-url", default=DEFAULT_REPO)
    parser.add_argument("--branch", default=DEFAULT_BRANCH)
    parser.add_argument("--repo-dir", default=DEFAULT_REPO_DIR)
    parser.add_argument("--bundle-dir", default=DEFAULT_BUNDLE_DIR)
    parser.add_argument("--evidence-dir", default=DEFAULT_EVIDENCE_DIR)
    parser.add_argument("--snapshot-root", default=DEFAULT_SNAPSHOT_ROOT)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    report = prepare_packet(
        output_dir=args.output_dir,
        host=args.host,
        port=args.port,
        repo_url=args.repo_url,
        branch=args.branch,
        repo_dir=args.repo_dir,
        bundle_dir=args.bundle_dir,
        evidence_dir=args.evidence_dir,
        snapshot_root=args.snapshot_root,
    )
    sys.stdout.write(json.dumps(report, indent=2, sort_keys=True) + "\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
