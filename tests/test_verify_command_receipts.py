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


class DeliveryAuthorizationEvidenceTests(unittest.TestCase):
    """Require the latest pre-approval execution state to remain queued."""

    tenant_id = "00000000-0000-4000-8000-000000000001"
    repository_id = "00000000-0000-4000-8000-000000000002"
    delivery_id = "delivery_00000000-0000-4000-8000-000000000003"
    attempt_id = "attempt_00000000-0000-4000-8000-000000000004"
    run_id = "run_00000000-0000-4000-8000-000000000005"
    approved_at = "2026-07-18T12:00:00Z"

    def request(self) -> dict:
        """Return one canonical delivery request."""
        return {
            "delivery_id": self.delivery_id,
            "tenant_id": "tenant_" + self.tenant_id,
            "repository_id": "repo_" + self.repository_id,
            "task_id": "task_00000000-0000-4000-8000-000000000006",
            "attempt_id": self.attempt_id,
            "run_id": self.run_id,
            "schema_version": "1.1",
            "repository_path": "/tmp/repository",
            "worktree_root": "/tmp/worktrees",
            "base_commit": "a" * 40,
            "publication_ref": "refs/forja/deliveries/" + self.delivery_id,
            "publication_previous_commit": None,
            "author_id": "worker-author",
            "validator_id": "independent-validator",
            "role": "implementer",
            "objective": "Deliver one governed change",
            "read_scopes": ["."],
            "write_scopes": ["internal/generated"],
            "artifact_scopes": ["evidence"],
            "evidence_scope": "evidence",
            "attempt_ordinal": 1,
            "worker_budgets": {
                "wall_clock_ms": 1000,
                "inactivity_ms": 500,
                "max_output_bytes": 4096,
                "cancellation_grace_ms": 100,
                "max_retries": 1,
            },
            "mechanical_validator_ids": ["unit"],
            "lease_ttl_ms": 60000,
        }

    def evidence(self) -> tuple[dict, dict, list[dict], list[str]]:
        """Return a valid authorization and its governed predecessors."""
        request = self.request()
        digest = hashlib.sha256(
            VERIFIER.go_json_bytes(VERIFIER.ordered_delivery_request(request))
        ).hexdigest()
        approved_us = VERIFIER.parse_utc_us(self.approved_at)
        authorization = {
            "request": request,
            "request_sha256": digest,
            "approved_by": "independent-human",
            "approved_at": self.approved_at,
        }
        event = {
            "tenant_id": self.tenant_id,
            "repository_id": self.repository_id,
            "aggregate_type": "approval",
            "aggregate_id": self.delivery_id + ":" + self.attempt_id,
            "aggregate_version": 1,
            "outbox_id": 40,
            "event_type": "delivery.authorized",
            "occurred_us": approved_us,
            "actor_type": "human",
            "actor_id": "independent-human",
            "payload": authorization,
        }
        events = [
            {
                "tenant_id": self.tenant_id,
                "repository_id": self.repository_id,
                "aggregate_type": "decision",
                "aggregate_id": "decision-1",
                "aggregate_version": 2,
                "outbox_id": 10,
                "event_type": "decision.approved",
                "occurred_us": approved_us - 3,
                "payload": {
                    "run_id": self.run_id,
                    "action": "submit_sprint",
                    "status": "approved",
                },
            },
            {
                "tenant_id": self.tenant_id,
                "repository_id": self.repository_id,
                "aggregate_type": "run",
                "aggregate_id": self.run_id,
                "aggregate_version": 3,
                "outbox_id": 20,
                "event_type": "run.transitioned",
                "occurred_us": approved_us - 2,
                "payload": {
                    "run_id": self.run_id,
                    "objective": request["objective"],
                    "state": "queued",
                },
            },
            {
                "tenant_id": self.tenant_id,
                "repository_id": self.repository_id,
                "aggregate_type": "attempt",
                "aggregate_id": self.attempt_id,
                "aggregate_version": 1,
                "outbox_id": 30,
                "event_type": "attempt.created",
                "occurred_us": approved_us - 1,
                "payload": {
                    "attempt_id": self.attempt_id,
                    "run_id": self.run_id,
                    "ordinal": 1,
                    "status": "queued",
                },
            },
            event,
        ]
        scope = ["authorize_delivery", self.repository_id, self.delivery_id, self.attempt_id]
        return event, authorization, events, scope

    def test_accepts_latest_queued_run_and_attempt(self) -> None:
        """The canonical queued predecessor state remains valid."""
        event, authorization, events, scope = self.evidence()
        VERIFIER.validate_delivery_authorization(event, authorization, events, scope)

    def test_rejects_run_that_advanced_before_authorization(self) -> None:
        """An old queued Run snapshot cannot authorize a running Run."""
        event, authorization, events, scope = self.evidence()
        events.insert(-1, {
            "tenant_id": self.tenant_id,
            "repository_id": self.repository_id,
            "aggregate_type": "run",
            "aggregate_id": self.run_id,
            "aggregate_version": 4,
            "outbox_id": 35,
            "event_type": "run.transitioned",
            "occurred_us": event["occurred_us"] - 1,
            "payload": {
                "run_id": self.run_id,
                "objective": authorization["request"]["objective"],
                "state": "running",
            },
        })
        with self.assertRaisesRegex(ValueError, "stale execution authority"):
            VERIFIER.validate_delivery_authorization(event, authorization, events, scope)

    def test_rejects_attempt_that_advanced_before_authorization(self) -> None:
        """An old creation event cannot conceal a started attempt."""
        event, authorization, events, scope = self.evidence()
        events.insert(-1, {
            "tenant_id": self.tenant_id,
            "repository_id": self.repository_id,
            "aggregate_type": "attempt",
            "aggregate_id": self.attempt_id,
            "aggregate_version": 2,
            "outbox_id": 35,
            "event_type": "attempt.started",
            "occurred_us": event["occurred_us"] - 1,
            "payload": {
                "attempt": {
                    "attempt_id": self.attempt_id,
                    "run_id": self.run_id,
                    "ordinal": 1,
                    "status": "running",
                }
            },
        })
        with self.assertRaisesRegex(ValueError, "stale execution authority"):
            VERIFIER.validate_delivery_authorization(event, authorization, events, scope)

    def test_accepts_clock_skew_when_outbox_order_proves_predecessors(self) -> None:
        """Wall-clock skew cannot invalidate monotonic committed ordering."""
        event, authorization, events, scope = self.evidence()
        for predecessor in events[:-1]:
            predecessor["occurred_us"] = event["occurred_us"] + 100
        VERIFIER.validate_delivery_authorization(event, authorization, events, scope)

    def test_rejects_schema_consistent_but_semantically_invalid_requests(self) -> None:
        """A matching digest cannot legitimize malformed execution authority."""
        corruptions = {
            "task identity": lambda request: request.__setitem__("task_id", "task-invalid"),
            "repository root": lambda request: request.__setitem__("repository_path", "/"),
            "scope traversal": lambda request: request.__setitem__("write_scopes", ["../escape"]),
            "worker budget": lambda request: request["worker_budgets"].__setitem__("wall_clock_ms", 99),
            "lease horizon": lambda request: request.__setitem__("lease_ttl_ms", -1),
            "retry budget": lambda request: request.__setitem__("attempt_ordinal", 3),
            "null context reference": lambda request: request.__setitem__(
                "context_pack_ref", None
            ),
            "null model": lambda request: request.__setitem__("model", None),
            "null token budget": lambda request: request["worker_budgets"].__setitem__(
                "max_tokens", None
            ),
            "null command budget": lambda request: request["worker_budgets"].__setitem__(
                "max_commands", None
            ),
        }
        for name, corrupt in corruptions.items():
            with self.subTest(name=name):
                event, authorization, events, scope = self.evidence()
                corrupt(authorization["request"])
                ordered = VERIFIER.ordered_delivery_request(authorization["request"])
                authorization["request_sha256"] = hashlib.sha256(
                    VERIFIER.go_json_bytes(ordered)
                ).hexdigest()
                with self.assertRaisesRegex(ValueError, "delivery authorization"):
                    VERIFIER.validate_delivery_authorization(
                        event, authorization, events, scope
                    )
