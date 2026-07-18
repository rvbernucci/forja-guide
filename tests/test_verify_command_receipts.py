"""Unit tests for command receipt evidence validation."""

import copy
import hashlib
import importlib.util
import pathlib
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location(
    "verify_command_receipts",
    ROOT / "scripts" / "verify_command_receipts.py",
)
VERIFIER = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(VERIFIER)


class SprintCancellationEvidenceTests(unittest.TestCase):
    """Reject cancellation evidence that does not describe its Sprint."""

    run_id = "run_00000000-0000-4000-8000-000000000003"
    sprint_id = "sprint_00000000-0000-4000-8000-000000000004"

    def event(self) -> dict:
        """Return a valid cancellation event envelope and payload."""
        return {
            "aggregate_id": self.sprint_id,
            "aggregate_version": 2,
            "payload": {
                "sprint_id": self.sprint_id,
                "schema_version": "1.0",
                "sequence_number": 3,
                "title": "Cancellation evidence",
                "objective": "Reject corrupted cancellation evidence",
                "status": "cancelling",
                "version": 2,
                "run_id": self.run_id,
                "created_at": "2026-07-17T00:00:00Z",
                "updated_at": "2026-07-17T00:00:01Z",
            },
        }

    def assert_rejected(self, mutate) -> None:
        """Apply one corruption and require fail-closed validation."""
        event = copy.deepcopy(self.event())
        mutate(event)
        with self.assertRaises(ValueError):
            VERIFIER.validate_sprint_cancellation_event(event, self.run_id)

    def test_valid_cancellation_matches_contract_and_envelope(self) -> None:
        """Accept the exact canonical cancellation representation."""
        VERIFIER.validate_sprint_cancellation_event(self.event(), self.run_id)

    def test_rejects_non_cancelling_status(self) -> None:
        """A matching Run ID cannot conceal a stale Sprint status."""
        self.assert_rejected(
            lambda event: event["payload"].__setitem__("status", "approved")
        )

    def test_rejects_payload_sprint_identity_mismatch(self) -> None:
        """Payload identity must equal the immutable event envelope."""
        self.assert_rejected(
            lambda event: event["payload"].__setitem__(
                "sprint_id", "sprint_00000000-0000-4000-8000-000000000005"
            )
        )

    def test_rejects_run_identity_mismatch(self) -> None:
        """Cancellation must belong to the Run named by the receipt scope."""
        self.assert_rejected(
            lambda event: event["payload"].__setitem__(
                "run_id", "run_00000000-0000-4000-8000-000000000006"
            )
        )

    def test_rejects_aggregate_version_mismatch(self) -> None:
        """Payload and event envelope versions must remain identical."""
        self.assert_rejected(
            lambda event: event["payload"].__setitem__("version", 3)
        )

    def test_rejects_pending_decision_field(self) -> None:
        """A cancelling Sprint cannot expose a pending decision."""
        self.assert_rejected(
            lambda event: event["payload"].__setitem__(
                "pending_decision_id",
                "decision_00000000-0000-4000-8000-000000000007",
            )
        )

    def test_rejects_invalid_contract_field_type(self) -> None:
        """Matching identities cannot conceal a malformed Sprint contract."""
        self.assert_rejected(
            lambda event: event["payload"].__setitem__("sequence_number", True)
        )


class EmptyReconciliationReceiptTests(unittest.TestCase):
    """Verify a no-op reconciliation without inventing a domain event."""

    tenant_id = "00000000-0000-4000-8000-000000000001"
    repository_id = "00000000-0000-4000-8000-000000000002"
    scope = f"reconcile_attempts:{repository_id}:scheduler-main"

    def receipt(self) -> dict:
        """Return an independently verifiable empty reconciliation receipt."""
        response = {
            "attempts": [],
            "authority": {
                "tenant_id": self.tenant_id,
                "repository_id": self.repository_id,
                "resource_type": "scheduler",
                "resource_id": "scheduler-main",
                "owner_id": "scheduler-1",
                "fencing_token": 7,
            },
            "command": {
                "tenant_id": self.tenant_id,
                "repository_id": self.repository_id,
                "idempotency_key": "empty-reconcile",
                "actor_type": "system",
                "actor_id": "reconciler",
                "correlation_id": "correlation-empty-reconcile",
                "causation_id": "",
            },
        }
        hash_parts = [
            self.scope,
            "scheduler-1",
            "7",
            "system",
            "reconciler",
            "",
        ]
        return {
            "tenant_id": self.tenant_id,
            "scope": self.scope,
            "idempotency_key": "empty-reconcile",
            "request_hash": hashlib.sha256(
                "\0".join(hash_parts).encode("utf-8")
            ).hexdigest(),
            "status": 200,
            "response": response,
        }

    def test_accepts_hash_bound_empty_reconciliation(self) -> None:
        """The receipt itself carries the proof and command provenance."""
        evidence = VERIFIER.verify_receipt(self.receipt(), [])
        self.assertEqual(evidence["domain_event_ids"], set())

    def test_rejects_scope_authority_mismatch(self) -> None:
        """A no-op receipt cannot substitute another scheduler fence."""
        receipt = self.receipt()
        receipt["response"]["authority"]["resource_id"] = "other-scheduler"
        with self.assertRaises(ValueError):
            VERIFIER.verify_receipt(receipt, [])
