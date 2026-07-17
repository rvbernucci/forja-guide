"""Unit tests for command receipt evidence validation."""

import copy
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
