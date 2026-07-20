"""Tests for Sprint 10 gate status reporting."""

from __future__ import annotations

import importlib.util
import json
import shutil
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT_PATH = ROOT / "scripts" / "report_sprint10_gate_status.py"


def load_module(path: Path, name: str):
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Unable to load {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


REPORT = load_module(SCRIPT_PATH, "report_sprint10_gate_status")


class Sprint10GateStatusReportTests(unittest.TestCase):
    def test_current_candidate_reports_real_gates_incomplete(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            evidence_dir = Path(directory) / "sprint-10"
            shutil.copytree(ROOT / "docs" / "evidence" / "sprint-10", evidence_dir)

            report, exit_code = REPORT.gate_status(evidence_dir)

        self.assertEqual(2, exit_code)
        self.assertFalse(report["ready_for_independent_review"])
        self.assertFalse(report["next_sprint_authorized"])
        self.assertEqual(4, len(report["real_radeon_gates"]))
        self.assertTrue(all(not gate["complete"] for gate in report["real_radeon_gates"]))
        self.assertEqual(
            "python3 scripts/prepare_radeon_sprint10_handoff_packet.py --host <host> --port <port> --output-dir /tmp/forja-radeon-sprint10-handoff",
            report["next_commands"][0],
        )
        self.assertEqual(
            "python3 scripts/render_radeon_sprint10_snapshot_checklist.py --output /tmp/forja-radeon-snapshot-checklist.md",
            report["next_commands"][1],
        )
        self.assertEqual(
            "python3 scripts/render_radeon_sprint10_command_sheet.py --host <host> --port <port> --output /tmp/sprint10-radeon-command-sheet.md",
            report["next_commands"][2],
        )
        self.assertEqual(
            "python3 scripts/preflight_radeon_ssh.py <host> <port> --timeout-seconds 180 --interval-seconds 10 --wait-output /tmp/forja-radeon-ssh-wait.json --recovery-output /tmp/forja-radeon-ssh-recovery.md --repo-url https://github.com/rvbernucci/forja-guide --branch feat/sprint-10-radeon-runtime-v2 --repo-dir /workspace/forja-guide --output /tmp/forja-radeon-ssh-preflight.json",
            report["next_commands"][3],
        )
        self.assertEqual(
            "python3 scripts/wait_radeon_ssh.py <host> <port> --timeout-seconds 180 --interval-seconds 10",
            report["next_commands"][4],
        )
        self.assertEqual(
            "python3 scripts/render_radeon_ssh_recovery_sheet.py --wait-report /tmp/forja-radeon-ssh-wait.json --host <host> --port <port> --repo-url https://github.com/rvbernucci/forja-guide --branch feat/sprint-10-radeon-runtime-v2 --repo-dir /workspace/forja-guide --output /tmp/forja-radeon-ssh-recovery.md",
            report["next_commands"][5],
        )
        self.assertEqual(
            "python3 scripts/render_radeon_sprint10_web_terminal_bootstrap.py --repo-url https://github.com/rvbernucci/forja-guide --branch feat/sprint-10-radeon-runtime-v2 --repo-dir /workspace/forja-guide --output /tmp/forja-radeon-web-terminal-bootstrap.sh",
            report["next_commands"][6],
        )
        self.assertEqual(
            "python3 scripts/render_radeon_sprint10_web_terminal_sheet.py --repo-url https://github.com/rvbernucci/forja-guide --branch feat/sprint-10-radeon-runtime-v2 --repo-dir /workspace/forja-guide --output /tmp/forja-radeon-web-terminal-evidence.md",
            report["next_commands"][7],
        )
        self.assertLess(
            report["next_commands"].index(
                "python3 scripts/prepare_radeon_sprint10_handoff_packet.py --host <host> --port <port> --output-dir /tmp/forja-radeon-sprint10-handoff"
            ),
            report["next_commands"].index(
                "python3 scripts/render_radeon_sprint10_snapshot_checklist.py --output /tmp/forja-radeon-snapshot-checklist.md"
            ),
        )
        self.assertLess(
            report["next_commands"].index(
                "python3 scripts/render_radeon_sprint10_snapshot_checklist.py --output /tmp/forja-radeon-snapshot-checklist.md"
            ),
            report["next_commands"].index(
                "python3 scripts/render_radeon_sprint10_command_sheet.py --host <host> --port <port> --output /tmp/sprint10-radeon-command-sheet.md"
            ),
        )
        self.assertLess(
            report["next_commands"].index(
                "python3 scripts/render_radeon_ssh_recovery_sheet.py --wait-report /tmp/forja-radeon-ssh-wait.json --host <host> --port <port> --repo-url https://github.com/rvbernucci/forja-guide --branch feat/sprint-10-radeon-runtime-v2 --repo-dir /workspace/forja-guide --output /tmp/forja-radeon-ssh-recovery.md"
            ),
            report["next_commands"].index("python3 scripts/prepare_radeon_sprint10_operator_bundle.py"),
        )
        self.assertLess(
            report["next_commands"].index(
                "python3 scripts/render_radeon_sprint10_web_terminal_bootstrap.py --repo-url https://github.com/rvbernucci/forja-guide --branch feat/sprint-10-radeon-runtime-v2 --repo-dir /workspace/forja-guide --output /tmp/forja-radeon-web-terminal-bootstrap.sh"
            ),
            report["next_commands"].index(
                "python3 scripts/render_radeon_sprint10_web_terminal_sheet.py --repo-url https://github.com/rvbernucci/forja-guide --branch feat/sprint-10-radeon-runtime-v2 --repo-dir /workspace/forja-guide --output /tmp/forja-radeon-web-terminal-evidence.md"
            ),
        )
        self.assertLess(
            report["next_commands"].index(
                "python3 scripts/render_radeon_sprint10_web_terminal_sheet.py --repo-url https://github.com/rvbernucci/forja-guide --branch feat/sprint-10-radeon-runtime-v2 --repo-dir /workspace/forja-guide --output /tmp/forja-radeon-web-terminal-evidence.md"
            ),
            report["next_commands"].index("python3 scripts/prepare_radeon_sprint10_operator_bundle.py"),
        )
        self.assertIn(
            "python3 scripts/check_radeon_sprint10_private_inputs.py --snapshot-root /secure/forja --model-candidates /secure/forja/radeon-model-candidates.json ...",
            report["next_commands"],
        )
        self.assertLess(
            report["next_commands"].index(
                "python3 scripts/check_radeon_sprint10_private_inputs.py --snapshot-root /secure/forja --model-candidates /secure/forja/radeon-model-candidates.json ..."
            ),
            report["next_commands"].index(
                "python3 scripts/run_radeon_sprint10_evidence.py --evidence-dir /workspace/forja-alpha-sprint10-evidence ..."
            ),
        )
        self.assertIn(
            "python3 scripts/ingest_radeon_sprint10_public_summary.py --summary /workspace/forja-alpha-sprint10-evidence/radeon-public-summary.json",
            report["next_commands"],
        )
        self.assertLess(
            report["next_commands"].index(
                "python3 scripts/diagnose_radeon_sprint10_artifacts.py --evidence-dir /workspace/forja-alpha-sprint10-evidence"
            ),
            report["next_commands"].index(
                "python3 scripts/verify_radeon_sprint10_public_summary.py --summary /workspace/forja-alpha-sprint10-evidence/radeon-public-summary.json"
            ),
        )
        self.assertLess(
            report["next_commands"].index(
                "python3 scripts/verify_radeon_sprint10_public_summary.py --summary /workspace/forja-alpha-sprint10-evidence/radeon-public-summary.json"
            ),
            report["next_commands"].index(
                "python3 scripts/ingest_radeon_sprint10_public_summary.py --summary /workspace/forja-alpha-sprint10-evidence/radeon-public-summary.json --dry-run"
            ),
        )
        self.assertLess(
            report["next_commands"].index(
                "python3 scripts/ingest_radeon_sprint10_public_summary.py --summary /workspace/forja-alpha-sprint10-evidence/radeon-public-summary.json --dry-run"
            ),
            report["next_commands"].index(
                "python3 scripts/ingest_radeon_sprint10_public_summary.py --summary /workspace/forja-alpha-sprint10-evidence/radeon-public-summary.json"
            ),
        )
        self.assertLess(
            report["next_commands"].index(
                "python3 scripts/verify_sprint10_review_readiness.py --evidence-dir docs/evidence/sprint-10"
            ),
            report["next_commands"].index(
                "python3 scripts/render_sprint10_immutable_review_request.py --reviewer <reviewer-id> --output docs/evidence/sprint-10/reviews/immutable-candidate-review.md"
            ),
        )
        self.assertLess(
            report["next_commands"].index(
                "python3 scripts/render_sprint10_immutable_review_request.py --reviewer <reviewer-id> --output docs/evidence/sprint-10/reviews/immutable-candidate-review.md"
            ),
            report["next_commands"].index(
                "python3 scripts/render_sprint10_promotion_checklist.py --reviewer <reviewer-id> --reviewed-candidate-commit <40-char-candidate-commit> --output /tmp/sprint10-promotion-checklist.md"
            ),
        )

    def test_ready_package_reports_ready_without_authorizing_next_sprint(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            evidence_dir = Path(directory) / "sprint-10"
            shutil.copytree(ROOT / "docs" / "evidence" / "sprint-10", evidence_dir)
            make_ready(evidence_dir)

            report, exit_code = REPORT.gate_status(evidence_dir)

        self.assertEqual(0, exit_code)
        self.assertTrue(report["ready_for_independent_review"])
        self.assertFalse(report["next_sprint_authorized"])
        self.assertTrue(all(gate["complete"] for gate in report["real_radeon_gates"]))

    def test_metric_without_candidate_acceptance_is_not_complete(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            evidence_dir = Path(directory) / "sprint-10"
            shutil.copytree(ROOT / "docs" / "evidence" / "sprint-10", evidence_dir)
            metrics_path = evidence_dir / "metrics-summary.json"
            metrics = json.loads(metrics_path.read_text(encoding="utf-8"))
            metrics["metrics"]["real_radeon_runtime_receipts"] = 1
            metrics_path.write_text(json.dumps(metrics), encoding="utf-8")

            report, exit_code = REPORT.gate_status(evidence_dir)

        self.assertEqual(2, exit_code)
        runtime_gate = report["real_radeon_gates"][0]
        self.assertFalse(runtime_gate["complete"])
        self.assertEqual(1, runtime_gate["metric_value"])
        self.assertFalse(runtime_gate["candidate_value"])


def make_ready(evidence_dir: Path) -> None:
    candidate_path = evidence_dir / "closure-candidate.json"
    candidate = json.loads(candidate_path.read_text(encoding="utf-8"))
    candidate["definition_of_done"]["rollback_demonstrated"] = True
    for item in REPORT.REAL_GATES:
        candidate["acceptance"][item["gate"]] = True
    candidate_path.write_text(json.dumps(candidate), encoding="utf-8")

    metrics_path = evidence_dir / "metrics-summary.json"
    metrics = json.loads(metrics_path.read_text(encoding="utf-8"))
    for item in REPORT.REAL_GATES:
        metrics["metrics"][item["metric"]] = 1
    metrics_path.write_text(json.dumps(metrics), encoding="utf-8")

    validation_path = evidence_dir / "validation-report.json"
    validation = json.loads(validation_path.read_text(encoding="utf-8"))
    for row in validation["validation"]:
        if row.get("gate") == "local inference proof":
            row["result"] = "ready_for_independent_review"
    validation_path.write_text(json.dumps(validation), encoding="utf-8")

    summary_path = evidence_dir / "radeon-public-summary.json"
    summary_path.write_text(
        json.dumps(
            {
                "summary_kind": "radeon_sprint10_public_summary",
                "status": "passed",
                "private_recovery_verified": True,
            }
        ),
        encoding="utf-8",
    )


if __name__ == "__main__":
    unittest.main()
