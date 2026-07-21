#!/usr/bin/env python3
"""Run the Sprint 10 Radeon evidence collection sequence.

The script is intentionally boring: it calls the smaller audited scripts in the
same order an operator should run them on a fresh Radeon Cloud instance. In
`--dry-run` mode it writes the exact plan without executing commands.
"""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import subprocess
import sys
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parents[1]
DEFAULT_EVIDENCE_DIR = Path("/workspace/forja-alpha-sprint10-evidence")
REPORT_NAMES = {
    "runtime_receipt": "forja-radeon-runtime-receipt.json",
    "runtime_readiness": "forja-radeon-runtime-readiness.json",
    "source_restore": "forja-alpha-source-restore-report.json",
    "model_benchmark": "forja-radeon-model-candidate-report.json",
    "embedding_benchmark": "forja-radeon-embedding-benchmark.json",
    "recovery": "forja-alpha-competition-profile-recovery.json",
    "public_summary": "radeon-public-summary.json",
}


def utc_now() -> str:
    return dt.datetime.now(dt.UTC).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def resolve_commit(explicit: str | None) -> str:
    if explicit:
        return explicit
    result = subprocess.run(
        ["git", "rev-parse", "HEAD"],
        cwd=ROOT,
        check=False,
        capture_output=True,
        text=True,
        timeout=8,
    )
    commit = result.stdout.strip()
    if result.returncode != 0 or len(commit) != 40:
        raise RuntimeError("unable to resolve expected commit; pass --expected-commit")
    return commit


def command_step(step_id: str, description: str, argv: list[str], output: Path) -> dict[str, Any]:
    return {
        "step_id": step_id,
        "description": description,
        "argv": argv,
        "output": str(output),
    }


def build_plan(args: argparse.Namespace) -> dict[str, Any]:
    evidence_dir = args.evidence_dir
    paths = {name: evidence_dir / filename for name, filename in REPORT_NAMES.items()}
    expected_commit = resolve_commit(args.expected_commit)
    python = sys.executable
    steps = []
    if args.build_source_manifest:
        build_manifest_argv = [
            python,
            "scripts/build_alpha_snapshot_manifest.py",
            "--snapshot-root",
            str(args.snapshot_root),
            "--output",
            str(args.source_manifest),
        ]
        for spec in args.required_snapshot:
            build_manifest_argv.extend(["--required-snapshot", spec])
        for spec in args.optional_snapshot:
            build_manifest_argv.extend(["--optional-snapshot", spec])
        steps.append(
            command_step(
                "source_manifest_build",
                "Build the source snapshot manifest from restored private files.",
                build_manifest_argv,
                args.source_manifest,
            )
        )
    steps.extend([
        command_step(
            "runtime_receipt",
            "Capture sanitized Radeon, ROCm, Python, vLLM, torch, and Git runtime evidence.",
            [
                python,
                "scripts/capture_radeon_runtime_receipt.py",
                "--output",
                str(paths["runtime_receipt"]),
                "--base-image",
                args.base_image,
                "--storage-profile",
                args.storage_profile,
                "--ssh-profile",
                args.ssh_profile,
            ],
            paths["runtime_receipt"],
        ),
        command_step(
            "runtime_readiness",
            "Prove loopback-only model and embedding endpoints plus zero remote core inference.",
            [
                python,
                "scripts/verify_radeon_runtime_readiness.py",
                "--receipt",
                str(paths["runtime_receipt"]),
                "--model-base-url",
                args.model_base_url,
                "--embedding-base-url",
                args.embedding_base_url,
                "--embedding-model",
                args.embedding_model,
                "--require-endpoints",
                "--output",
                str(paths["runtime_readiness"]),
            ],
            paths["runtime_readiness"],
        ),
        command_step(
            "source_restore",
            "Verify restored private source snapshots against the hash-pinned manifest.",
            [
                python,
                "scripts/verify_alpha_snapshot_manifest.py",
                "--manifest",
                str(args.source_manifest),
                "--snapshot-root",
                str(args.snapshot_root),
                "--output",
                str(paths["source_restore"]),
            ],
            paths["source_restore"],
        ),
        command_step(
            "model_benchmark",
            "Benchmark loopback local instruction-model candidates on the public smoke task set.",
            [
                python,
                "scripts/benchmark_radeon_model_candidates.py",
                "--task-set",
                str(args.model_task_set),
                "--candidates",
                str(args.model_candidates),
                "--output",
                str(paths["model_benchmark"]),
            ],
            paths["model_benchmark"],
        ),
        command_step(
            "embedding_benchmark",
            "Benchmark the loopback local embedding endpoint without storing input text or vectors.",
            [
                python,
                "scripts/benchmark_radeon_embedding.py",
                "--input-set",
                str(args.embedding_input_set),
                "--base-url",
                args.embedding_base_url,
                "--model",
                args.embedding_model,
                "--output",
                str(paths["embedding_benchmark"]),
            ],
            paths["embedding_benchmark"],
        ),
        command_step(
            "competition_profile_recovery",
            "Bind every Sprint 10 evidence report into the recovery gate.",
            [
                python,
                "scripts/verify_competition_profile_recovery.py",
                "--runtime-receipt",
                str(paths["runtime_receipt"]),
                "--runtime-readiness",
                str(paths["runtime_readiness"]),
                "--source-restore",
                str(paths["source_restore"]),
                "--model-benchmark",
                str(paths["model_benchmark"]),
                "--embedding-benchmark",
                str(paths["embedding_benchmark"]),
                "--expected-commit",
                expected_commit,
                "--output",
                str(paths["recovery"]),
            ],
            paths["recovery"],
        ),
        command_step(
            "public_summary",
            "Summarize the private recovery report into a public-safe Sprint 10 package artifact.",
            [
                python,
                "scripts/summarize_radeon_sprint10_evidence.py",
                "--recovery",
                str(paths["recovery"]),
                "--output",
                str(paths["public_summary"]),
            ],
            paths["public_summary"],
        ),
    ])
    return {
        "schema_version": "1.0",
        "plan_kind": "radeon_sprint10_evidence_sequence",
        "recorded_at": args.recorded_at or utc_now(),
        "dry_run": args.dry_run,
        "expected_commit": expected_commit,
        "evidence_dir": str(evidence_dir),
        "policy": {
            "core_remote_inference_allowed": False,
            "requires_loopback_model_endpoint": True,
            "requires_loopback_embedding_endpoint": True,
            "raw_artifacts_outside_git": True,
        },
        "inputs": {
            "build_source_manifest": args.build_source_manifest,
            "required_snapshots": list(args.required_snapshot),
            "optional_snapshots": list(args.optional_snapshot),
            "source_manifest": str(args.source_manifest),
            "snapshot_root": str(args.snapshot_root),
            "model_task_set": str(args.model_task_set),
            "model_candidates": str(args.model_candidates),
            "embedding_input_set": str(args.embedding_input_set),
        },
        "outputs": {name: str(path) for name, path in paths.items()},
        "steps": steps,
    }


def run_step(step: dict[str, Any]) -> dict[str, Any]:
    started_at = utc_now()
    try:
        result = subprocess.run(
            step["argv"],
            cwd=ROOT,
            check=False,
            capture_output=True,
            text=True,
            timeout=900,
        )
        status = {
            "step_id": step["step_id"],
            "started_at": started_at,
            "finished_at": utc_now(),
            "exit_code": result.returncode,
            "ok": result.returncode == 0,
            "stdout_tail": result.stdout[-1200:],
            "stderr_tail": result.stderr[-1200:],
        }
    except subprocess.TimeoutExpired as exc:
        status = {
            "step_id": step["step_id"],
            "started_at": started_at,
            "finished_at": utc_now(),
            "exit_code": None,
            "ok": False,
            "stdout_tail": (exc.stdout or "")[-1200:] if isinstance(exc.stdout, str) else "",
            "stderr_tail": (exc.stderr or "")[-1200:] if isinstance(exc.stderr, str) else "",
            "error": "timeout",
        }
    return status


def execute_plan(plan: dict[str, Any]) -> tuple[dict[str, Any], int]:
    results = []
    exit_code = 0
    for step in plan["steps"]:
        result = run_step(step)
        results.append(result)
        if not result["ok"]:
            exit_code = 2
            break
    report = {
        **plan,
        "dry_run": False,
        "execution": {
            "ok": exit_code == 0,
            "steps": results,
        },
    }
    return report, exit_code


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--evidence-dir", type=Path, default=DEFAULT_EVIDENCE_DIR)
    parser.add_argument("--source-manifest", type=Path, required=True)
    parser.add_argument("--snapshot-root", type=Path, required=True)
    parser.add_argument("--build-source-manifest", action="store_true")
    parser.add_argument("--required-snapshot", action="append", default=[])
    parser.add_argument("--optional-snapshot", action="append", default=[])
    parser.add_argument(
        "--model-task-set",
        type=Path,
        default=Path("internal/alpha/testdata/radeon_model_selection_public_v1.json"),
    )
    parser.add_argument("--model-candidates", type=Path, required=True)
    parser.add_argument(
        "--embedding-input-set",
        type=Path,
        default=Path("internal/alpha/testdata/radeon_embedding_public_v1.json"),
    )
    parser.add_argument("--model-base-url", required=True)
    parser.add_argument("--embedding-base-url", required=True)
    parser.add_argument("--embedding-model", required=True)
    parser.add_argument("--expected-commit")
    parser.add_argument("--output-plan", type=Path)
    parser.add_argument("--execution-report", type=Path)
    parser.add_argument("--recorded-at")
    parser.add_argument("--dry-run", action="store_true")
    parser.add_argument(
        "--base-image",
        default="GH-proxy-stable (amd-oneclick-base:git-proxy-test-20260528-1125)",
    )
    parser.add_argument(
        "--storage-profile",
        choices=("persistent_pvc", "local_ssd_ephemeral", "unknown"),
        default="persistent_pvc",
    )
    parser.add_argument(
        "--ssh-profile",
        choices=("enabled", "disabled", "unknown"),
        default="enabled",
    )
    return parser.parse_args()


def write_json(path: Path, payload: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    os.chmod(path, 0o600)


def main() -> int:
    args = parse_args()
    plan = build_plan(args)
    if args.output_plan:
        write_json(args.output_plan, plan)
    if args.dry_run:
        sys.stdout.write(json.dumps(plan, indent=2, sort_keys=True) + "\n")
        return 0
    args.evidence_dir.mkdir(parents=True, exist_ok=True)
    report, exit_code = execute_plan(plan)
    report_path = args.execution_report or (args.evidence_dir / "forja-radeon-sprint10-evidence-run.json")
    write_json(report_path, report)
    return exit_code


if __name__ == "__main__":
    raise SystemExit(main())
