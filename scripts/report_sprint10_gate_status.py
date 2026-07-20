#!/usr/bin/env python3
"""Report Sprint 10 closure gates without promoting or mutating evidence."""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parents[1]
DEFAULT_EVIDENCE_DIR = ROOT / "docs" / "evidence" / "sprint-10"
REAL_GATES = (
    {
        "gate": "real_radeon_runtime_receipt_captured",
        "metric": "real_radeon_runtime_receipts",
        "proof": "Private runtime receipt summarized through radeon-public-summary.json",
        "next_action": "Run scripts/capture_radeon_runtime_receipt.py on the Radeon instance.",
    },
    {
        "gate": "two_local_instruction_candidates_benchmarked_on_radeon",
        "metric": "real_radeon_model_benchmarks",
        "proof": "Private model candidate benchmark summarized through radeon-public-summary.json",
        "next_action": "Serve two loopback instruction endpoints and run scripts/benchmark_radeon_model_candidates.py.",
    },
    {
        "gate": "local_embedding_endpoint_benchmarked_on_radeon",
        "metric": "real_radeon_embedding_benchmarks",
        "proof": "Private embedding benchmark summarized through radeon-public-summary.json",
        "next_action": "Serve one loopback embedding endpoint and run scripts/benchmark_radeon_embedding.py.",
    },
    {
        "gate": "destroy_recreate_recovery_verified",
        "metric": "real_destroy_recreate_recovery_reports",
        "proof": "Private competition-profile recovery report summarized through radeon-public-summary.json",
        "next_action": "Destroy/recreate the Radeon profile, restore source snapshots, and run scripts/verify_competition_profile_recovery.py.",
    },
)


def load_json(path: Path) -> dict[str, Any] | None:
    if not path.is_file():
        return None
    payload = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(payload, dict):
        raise ValueError(f"JSON document must be an object: {path}")
    return payload


def gate_status(evidence_dir: Path) -> tuple[dict[str, Any], int]:
    candidate = load_json(evidence_dir / "closure-candidate.json")
    metrics = load_json(evidence_dir / "metrics-summary.json")
    validation = load_json(evidence_dir / "validation-report.json")
    summary = load_json(evidence_dir / "radeon-public-summary.json")
    errors: list[str] = []
    gates: list[dict[str, Any]] = []

    if candidate is None:
        errors.append("missing_closure_candidate")
        candidate = {}
    if metrics is None:
        errors.append("missing_metrics_summary")
        metrics = {}
    if validation is None:
        errors.append("missing_validation_report")
        validation = {}

    acceptance = candidate.get("acceptance")
    if not isinstance(acceptance, dict):
        acceptance = {}
        errors.append("invalid_candidate_acceptance")
    metric_values = metrics.get("metrics")
    if not isinstance(metric_values, dict):
        metric_values = {}
        errors.append("invalid_metrics")

    for item in REAL_GATES:
        candidate_value = acceptance.get(item["gate"])
        metric_value = metric_values.get(item["metric"])
        complete = candidate_value is True and metric_value == 1
        gates.append(
            {
                "gate": item["gate"],
                "complete": complete,
                "candidate_value": candidate_value,
                "metric": item["metric"],
                "metric_value": metric_value,
                "proof": item["proof"],
                "next_action": None if complete else item["next_action"],
            }
        )

    definition = candidate.get("definition_of_done")
    if not isinstance(definition, dict):
        definition = {}
        errors.append("invalid_definition_of_done")
    validation_rows = validation.get("validation")
    if not isinstance(validation_rows, list):
        validation_rows = []
        errors.append("invalid_validation_rows")

    public_gates = {
        "candidate_is_fail_closed": (
            candidate.get("status") == "candidate"
            and candidate.get("authoritative") is False
            and candidate.get("next_sprint_authorized") is None
        ),
        "independent_validation_not_recorded": (
            definition.get("independent_validation_recorded") is False
        ),
        "local_inference_proof_ready": any(
            isinstance(row, dict)
            and row.get("gate") == "local inference proof"
            and row.get("result") == "ready_for_independent_review"
            for row in validation_rows
        ),
        "public_summary_passed": (
            isinstance(summary, dict)
            and summary.get("status") == "passed"
            and summary.get("private_recovery_verified") is True
        ),
        "rollback_demonstrated": definition.get("rollback_demonstrated") is True,
    }
    ready_for_review = (
        not errors
        and all(gate["complete"] for gate in gates)
        and all(public_gates.values())
    )
    report = {
        "schema_version": "1.0",
        "report_kind": "sprint10_gate_status",
        "sprint_id": "10",
        "evidence_dir": evidence_dir.as_posix(),
        "ready_for_independent_review": ready_for_review,
        "next_sprint_authorized": False,
        "public_gates": public_gates,
        "real_radeon_gates": gates,
        "errors": errors,
        "next_commands": [
            "python3 scripts/prepare_radeon_sprint10_handoff_packet.py --host <host> --port <port> --output-dir /tmp/forja-radeon-sprint10-handoff",
            "python3 scripts/render_radeon_sprint10_command_sheet.py --host <host> --port <port> --output /tmp/sprint10-radeon-command-sheet.md",
            "python3 scripts/preflight_radeon_ssh.py <host> <port> --timeout-seconds 180 --interval-seconds 10 --wait-output /tmp/forja-radeon-ssh-wait.json --recovery-output /tmp/forja-radeon-ssh-recovery.md --repo-url https://github.com/rvbernucci/forja-guide --branch feat/sprint-10-radeon-runtime-v2 --repo-dir /workspace/forja-guide --output /tmp/forja-radeon-ssh-preflight.json",
            "python3 scripts/wait_radeon_ssh.py <host> <port> --timeout-seconds 180 --interval-seconds 10",
            "python3 scripts/render_radeon_ssh_recovery_sheet.py --wait-report /tmp/forja-radeon-ssh-wait.json --host <host> --port <port> --repo-url https://github.com/rvbernucci/forja-guide --branch feat/sprint-10-radeon-runtime-v2 --repo-dir /workspace/forja-guide --output /tmp/forja-radeon-ssh-recovery.md",
            "python3 scripts/render_radeon_sprint10_web_terminal_bootstrap.py --repo-url https://github.com/rvbernucci/forja-guide --branch feat/sprint-10-radeon-runtime-v2 --repo-dir /workspace/forja-guide --output /tmp/forja-radeon-web-terminal-bootstrap.sh",
            "python3 scripts/render_radeon_sprint10_web_terminal_sheet.py --repo-url https://github.com/rvbernucci/forja-guide --branch feat/sprint-10-radeon-runtime-v2 --repo-dir /workspace/forja-guide --output /tmp/forja-radeon-web-terminal-evidence.md",
            "python3 scripts/prepare_radeon_sprint10_operator_bundle.py",
            "python3 scripts/verify_radeon_operator_bundle.py --bundle-dir /workspace/forja-alpha-sprint10-operator-bundle --allow-placeholders",
            "python3 scripts/verify_radeon_operator_bundle.py --bundle-dir /workspace/forja-alpha-sprint10-operator-bundle",
            "python3 scripts/check_radeon_sprint10_private_inputs.py --snapshot-root /secure/forja --model-candidates /secure/forja/radeon-model-candidates.json ...",
            "python3 scripts/run_radeon_sprint10_evidence.py --evidence-dir /workspace/forja-alpha-sprint10-evidence ...",
            "python3 scripts/diagnose_radeon_sprint10_artifacts.py --evidence-dir /workspace/forja-alpha-sprint10-evidence",
            "python3 scripts/ingest_radeon_sprint10_public_summary.py --summary /workspace/forja-alpha-sprint10-evidence/radeon-public-summary.json",
            "python3 scripts/verify_sprint10_review_readiness.py --evidence-dir docs/evidence/sprint-10",
        ],
    }
    return report, 0 if ready_for_review else 2


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--evidence-dir", type=Path, default=DEFAULT_EVIDENCE_DIR)
    parser.add_argument("--output", type=Path)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        report, exit_code = gate_status(args.evidence_dir)
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        print(f"Sprint 10 gate report failed: {exc}", file=sys.stderr)
        return 2
    body = json.dumps(report, indent=2, sort_keys=True) + "\n"
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(body, encoding="utf-8")
    else:
        sys.stdout.write(body)
    return exit_code


if __name__ == "__main__":
    raise SystemExit(main())
