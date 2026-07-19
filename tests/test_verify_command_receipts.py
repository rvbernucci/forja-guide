"""Unit tests for command receipt evidence validation."""

import copy
import hashlib
import importlib.util
import json
import pathlib
import unittest
from unittest import mock


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


class KnowledgeReceiptEvidenceTests(unittest.TestCase):
    """Bind knowledge receipts to their exact canonical event evidence."""

    tenant_id = "00000000-0000-4000-8000-000000000011"
    repository_id = "00000000-0000-4000-8000-000000000012"
    conversation_id = "conversation_00000000-0000-4000-8000-000000000013"

    def evidence(self) -> tuple[dict, list[dict]]:
        """Return a valid conversation creation receipt and event."""
        response = {
            "conversation_id": self.conversation_id,
            "schema_version": "1.0",
            "tenant_id": "tenant_" + self.tenant_id,
            "repository_id": "repo_" + self.repository_id,
            "status": "active",
            "version": 1,
            "retention_class": "project",
            "created_by": "co-architect",
            "created_at": "2026-07-19T12:00:00Z",
            "updated_at": "2026-07-19T12:00:00Z",
        }
        event = {
            "tenant_id": self.tenant_id,
            "repository_id": self.repository_id,
            "aggregate_type": "conversation",
            "aggregate_id": self.conversation_id,
            "aggregate_version": 1,
            "event_id": "event-knowledge-1",
            "outbox_id": 1,
            "event_type": "conversation.created",
            "occurred_us": VERIFIER.parse_utc_us(response["created_at"]),
            "actor_type": "agent",
            "actor_id": "co-architect",
            "correlation_id": "correlation-knowledge-1",
            "causation_id": "",
            "idempotency_key": "create-conversation-1",
            "payload": response,
        }
        hash_parts = [
            "knowledge",
            self.conversation_id,
            response["retention_class"],
            response["created_by"],
            event["actor_type"],
            event["actor_id"],
            event["causation_id"],
        ]
        receipt = {
            "tenant_id": self.tenant_id,
            "scope": f"conversation_create:{self.repository_id}:{self.conversation_id}",
            "idempotency_key": event["idempotency_key"],
            "request_hash": hashlib.sha256(
                "\0".join(hash_parts).encode("utf-8")
            ).hexdigest(),
            "status": 201,
            "response": response,
        }
        return receipt, [event]

    def test_accepts_exact_knowledge_receipt(self) -> None:
        """The canonical response and request identity form valid evidence."""
        receipt, events = self.evidence()
        evidence = VERIFIER.verify_receipt(receipt, events)
        self.assertEqual(evidence["domain_event_ids"], {"event-knowledge-1"})

    def test_rejects_mutated_response(self) -> None:
        """A receipt cannot rewrite its canonical conversation payload."""
        receipt, events = self.evidence()
        receipt["response"]["retention_class"] = "legal_hold"
        with self.assertRaises(ValueError):
            VERIFIER.verify_receipt(receipt, events)

    def test_rejects_mutated_request_hash(self) -> None:
        """Matching JSON cannot conceal a different command identity."""
        receipt, events = self.evidence()
        receipt["request_hash"] = "0" * 64
        with self.assertRaisesRegex(ValueError, "request hash"):
            VERIFIER.verify_receipt(receipt, events)

    def test_rejects_cross_repository_event(self) -> None:
        """A receipt cannot adopt evidence from another repository."""
        receipt, events = self.evidence()
        events[0]["repository_id"] = "00000000-0000-4000-8000-000000000099"
        with self.assertRaises(ValueError):
            VERIFIER.verify_receipt(receipt, events)

    def test_rejects_orphan_knowledge_event(self) -> None:
        """Every canonical knowledge event must be consumed by a receipt."""
        _, events = self.evidence()
        with mock.patch.object(VERIFIER, "load_events", return_value=events), \
             mock.patch.object(VERIFIER, "load_receipts", return_value=[]):
            with self.assertRaisesRegex(ValueError, "has no matching receipt"):
                VERIFIER.verify("events.tsv", "receipts.tsv")

    def test_ignores_nonterminal_artifact_saga_event(self) -> None:
        """A pre-activation saga event has no completed command receipt yet."""
        _, events = self.evidence()
        events[0]["aggregate_type"] = "artifact_operation"
        events[0]["event_type"] = "artifact.publication_uploading"
        with mock.patch.object(VERIFIER, "load_events", return_value=events), \
             mock.patch.object(VERIFIER, "load_receipts", return_value=[]):
            VERIFIER.verify("events.tsv", "receipts.tsv")


class IndexPublicationReceiptTests(unittest.TestCase):
    """Bind index receipts to the exact validated publication request."""

    tenant_id = "00000000-0000-4000-8000-000000000021"
    repository_id = "00000000-0000-4000-8000-000000000022"
    snapshot_id = "snapshot_" + "a" * 64

    def evidence(self, replay=False) -> tuple[dict, list[dict]]:
        """Return one canonical activation or equivalent-command replay."""
        response = {
            "snapshot_id": self.snapshot_id,
            "schema_version": "1.0",
            "tenant_id": "tenant_" + self.tenant_id,
            "repository_id": "repo_" + self.repository_id,
            "source_commit": "b" * 40,
            "source_tree": "c" * 40,
            "configuration_hash": "sha256:" + "d" * 64,
            "adapter_set_hash": "sha256:" + "e" * 64,
            "adapters": [],
            "status": "active",
            "version": 1,
            "counts": {
                "files": 0,
                "symbols": 0,
                "relations": 0,
                "diagnostics": 0,
            },
            "artifact_id": "artifact_index_receipt",
            "artifact_content_hash": "sha256:" + "f" * 64,
            "created_by": "indexer",
            "created_at": "2026-07-19T12:00:00Z",
            "validated_at": "2026-07-19T12:00:01Z",
        }
        request_snapshot = dict(response)
        request_snapshot.pop("validated_at")
        publication = {
            "bundle": {
                "snapshot": request_snapshot,
                "files": [],
                "symbols": [],
                "relations": [],
            },
            "adapter_runs": [],
            "deltas": [],
            "invalidations": [],
        }
        request_identity = VERIFIER.go_json_bytes(publication).decode("utf-8")
        actor_type = "agent"
        actor_id = "indexer"
        causation_id = "run-index-receipt"
        hash_parts = [
            "knowledge",
            "index_publication",
            request_identity,
            actor_type,
            actor_id,
            causation_id,
        ]
        request_hash = hashlib.sha256(
            "\0".join(hash_parts).encode("utf-8")
        ).hexdigest()
        idempotency_key = "index-replay" if replay else "index-activate"
        event = {
            "tenant_id": self.tenant_id,
            "repository_id": self.repository_id,
            "aggregate_type": "audit" if replay else "index_snapshot",
            "aggregate_id": "index-replay-id" if replay else self.snapshot_id,
            "aggregate_version": 1,
            "event_id": "event-index-replay" if replay else "event-index-activate",
            "outbox_id": 2 if replay else 1,
            "event_type": (
                "index_snapshot.replayed" if replay else "index_snapshot.activated"
            ),
            "occurred_us": VERIFIER.parse_utc_us("2026-07-19T12:00:02Z"),
            "actor_type": actor_type,
            "actor_id": actor_id,
            "correlation_id": "correlation-index-receipt",
            "causation_id": causation_id,
            "idempotency_key": idempotency_key,
            "payload": {
                "snapshot": response,
                "request_identity_json": request_identity,
                "request_sha256": request_hash,
            },
        }
        receipt = {
            "tenant_id": self.tenant_id,
            "scope": f"index_publish:{self.repository_id}:{self.snapshot_id}",
            "idempotency_key": idempotency_key,
            "request_hash": request_hash,
            "status": 200 if replay else 201,
            "response": response,
        }
        return receipt, [event]

    def test_accepts_activation_with_recomputed_request_identity(self) -> None:
        """The activation event proves the full publication command hash."""
        receipt, events = self.evidence()
        evidence = VERIFIER.verify_receipt(receipt, events)
        self.assertEqual(evidence["domain_event_ids"], {"event-index-activate"})

    def test_accepts_equivalent_publication_replay(self) -> None:
        """A different idempotency key has an explicit canonical replay event."""
        receipt, events = self.evidence(replay=True)
        evidence = VERIFIER.verify_receipt(receipt, events)
        self.assertEqual(evidence["domain_event_ids"], {"event-index-replay"})

    def test_rejects_joint_receipt_and_event_hash_tampering(self) -> None:
        """Matching stored hashes cannot replace recomputation from the request."""
        receipt, events = self.evidence()
        receipt["request_hash"] = "0" * 64
        events[0]["payload"]["request_sha256"] = "0" * 64
        with self.assertRaisesRegex(ValueError, "request hash"):
            VERIFIER.verify_receipt(receipt, events)

    def test_rejects_request_snapshot_that_differs_from_response(self) -> None:
        """A receipt cannot pair one publication request with another snapshot."""
        receipt, events = self.evidence()
        publication = json.loads(events[0]["payload"]["request_identity_json"])
        publication["bundle"]["snapshot"]["source_tree"] = "9" * 40
        events[0]["payload"]["request_identity_json"] = VERIFIER.go_json_bytes(
            publication
        ).decode("utf-8")
        with self.assertRaisesRegex(ValueError, "scope differs"):
            VERIFIER.verify_receipt(receipt, events)


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
