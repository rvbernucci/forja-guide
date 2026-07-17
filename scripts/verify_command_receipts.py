#!/usr/bin/env python3
"""Verify durable command receipts against their canonical event evidence."""

import hashlib
import json
import pathlib
import sys
from collections import defaultdict


DOMAIN_AGGREGATES = {"run", "attempt", "sprint", "decision"}
MUTATING_TOOLS = {
    "forja.plan_sprint",
    "forja.submit_sprint",
    "forja.approve_decision",
    "forja.reject_decision",
    "forja.cancel_run",
    "forja.resume_run",
}


def canonical_json(value):
    return json.dumps(
        value,
        ensure_ascii=False,
        allow_nan=False,
        sort_keys=True,
        separators=(",", ":"),
    )


def decode_hex(value):
    return bytes.fromhex(value).decode("utf-8")


def load_events(path):
    events = []
    event_ids = set()
    for line_number, line in enumerate(pathlib.Path(path).read_text().splitlines(), 1):
        fields = line.split("\t", 12)
        if len(fields) != 13:
            raise ValueError(
                f"command event row {line_number} has {len(fields)} fields, want 13"
            )
        (
            tenant_id,
            repository_id,
            aggregate_type,
            aggregate_id,
            aggregate_version,
            event_id,
            event_type,
            actor_type_hex,
            actor_id_hex,
            correlation_hex,
            causation_hex,
            idempotency_key_hex,
            payload_hex,
        ) = fields
        if event_id in event_ids:
            raise ValueError(f"duplicate event ID {event_id}")
        event_ids.add(event_id)
        events.append(
            {
                "tenant_id": tenant_id,
                "repository_id": repository_id,
                "aggregate_type": aggregate_type,
                "aggregate_id": aggregate_id,
                "aggregate_version": int(aggregate_version),
                "event_id": event_id,
                "event_type": event_type,
                "actor_type": decode_hex(actor_type_hex),
                "actor_id": decode_hex(actor_id_hex),
                "correlation_id": decode_hex(correlation_hex),
                "causation_id": decode_hex(causation_hex),
                "idempotency_key": decode_hex(idempotency_key_hex),
                "payload": json.loads(decode_hex(payload_hex)),
            }
        )
    return events


def load_receipts(path):
    receipts = []
    identities = set()
    for line_number, line in enumerate(pathlib.Path(path).read_text().splitlines(), 1):
        fields = line.split("\t", 6)
        if len(fields) != 7:
            raise ValueError(
                f"idempotency row {line_number} has {len(fields)} fields, want 7"
            )
        (
            tenant_id,
            scope_hex,
            idempotency_key_hex,
            request_hash,
            status,
            response_hex,
            expires_is_null,
        ) = fields
        if expires_is_null != "t":
            raise ValueError("release receipts must not expire")
        receipt = {
            "tenant_id": tenant_id,
            "scope": decode_hex(scope_hex),
            "idempotency_key": decode_hex(idempotency_key_hex),
            "request_hash": request_hash,
            "status": int(status),
            "response": json.loads(decode_hex(response_hex)),
        }
        identity = (
            receipt["tenant_id"],
            receipt["scope"],
            receipt["idempotency_key"],
        )
        if identity in identities:
            raise ValueError(f"duplicate command receipt identity {identity}")
        identities.add(identity)
        receipts.append(receipt)
    return receipts


def event_identity(event):
    return (
        event["tenant_id"],
        event["repository_id"],
        event["idempotency_key"],
        event["actor_type"],
        event["actor_id"],
        event["correlation_id"],
        event["causation_id"],
    )


def stable_command_identity(event):
    """Return receipt-bound identity, excluding retry-level correlation."""
    return (
        event["tenant_id"],
        event["repository_id"],
        event["idempotency_key"],
        event["actor_type"],
        event["actor_id"],
        event["causation_id"],
    )


def parse_scope(scope):
    parts = scope.split(":")
    expected_lengths = {
        "create_run": 2,
        "transition_run": 3,
        "create_attempt": 3,
        "plan_sprint": 2,
        "submit_sprint": 3,
        "resolve_decision": 3,
        "resume_run": 3,
    }
    if not parts or parts[0] not in expected_lengths:
        raise ValueError(f"unsupported command receipt scope {scope!r}")
    if len(parts) != expected_lengths[parts[0]] or not parts[1]:
        raise ValueError(f"malformed command receipt scope {scope!r}")
    return parts


def find_event(events, aggregate_type, event_type, payload, aggregate_id=None):
    wanted = canonical_json(payload)
    matches = [
        event
        for event in events
        if event["aggregate_type"] == aggregate_type
        and event["event_type"] == event_type
        and canonical_json(event["payload"]) == wanted
        and (aggregate_id is None or event["aggregate_id"] == aggregate_id)
    ]
    if len(matches) != 1:
        raise ValueError(
            f"expected one {aggregate_type}/{event_type} event, found {len(matches)}"
        )
    return matches[0]


def require_sibling(events, primary, aggregate_type, event_type, payload):
    siblings = [event for event in events if event_identity(event) == event_identity(primary)]
    return find_event(siblings, aggregate_type, event_type, payload)


def require_success_audit(events, primary, tool_name, command_scope):
    matches = []
    for event in events:
        if event_identity(event) != event_identity(primary):
            continue
        payload = event["payload"]
        if (
            event["aggregate_type"] == "audit"
            and event["aggregate_version"] == 1
            and event["event_type"] == "mcp.tool.succeeded"
            and isinstance(payload, dict)
            and payload.get("tool_name") == tool_name
            and payload.get("command_scope") == command_scope
            and payload.get("outcome") == "succeeded"
            and payload.get("replay") is False
        ):
            matches.append(event)
    if len(matches) != 1:
        raise ValueError(
            f"command requires exactly one atomic {tool_name} success audit, "
            f"found {len(matches)}"
        )
    for audit in matches:
        payload = audit["payload"]
        expected = {
            "actor_type": audit["actor_type"],
            "actor_id": audit["actor_id"],
            "correlation_id": audit["correlation_id"],
            "idempotency_key": audit["idempotency_key"],
        }
        for field, value in expected.items():
            if payload.get(field) != value:
                raise ValueError(f"{tool_name} audit payload disagrees on {field}")
        payload_causation = payload.get("causation_id", "")
        if payload_causation != audit["causation_id"]:
            raise ValueError(f"{tool_name} audit payload disagrees on causation_id")
    return matches[0]


def verify_response(receipt, expected_status, expected_response):
    if receipt["status"] != expected_status:
        raise ValueError(
            f"receipt {receipt['scope']} has status {receipt['status']}, "
            f"want {expected_status}"
        )
    if canonical_json(receipt["response"]) != canonical_json(expected_response):
        raise ValueError(
            f"receipt {receipt['scope']} response differs from canonical events"
        )


def verify_hash(receipt, primary, parts, tool_name=""):
    hash_parts = parts + [
        primary["actor_type"],
        primary["actor_id"],
        primary["causation_id"],
    ]
    if tool_name:
        hash_parts.append(tool_name)
    expected = hashlib.sha256("\0".join(hash_parts).encode("utf-8")).hexdigest()
    if receipt["request_hash"] != expected:
        raise ValueError(
            f"receipt {receipt['scope']} request hash differs from canonical command"
        )


def require_object(value, fields, label):
    if not isinstance(value, dict) or set(value) != set(fields):
        raise ValueError(f"{label} must contain exactly {sorted(fields)}")


def verify_receipt(receipt, events):
    scope_parts = parse_scope(receipt["scope"])
    command = scope_parts[0]
    repository_id = scope_parts[1]
    candidates = [
        event
        for event in events
        if event["tenant_id"] == receipt["tenant_id"]
        and event["repository_id"] == repository_id
        and event["idempotency_key"] == receipt["idempotency_key"]
    ]
    if not candidates:
        raise ValueError(f"receipt {receipt['scope']} has no canonical command events")

    response = receipt["response"]
    tool_name = ""
    domain_events = []
    if command == "create_run":
        primary = find_event(candidates, "run", "run.created", response)
        domain_events.append(primary)
        status = 201
        hash_parts = ["create_run", repository_id, response["objective"]]
        expected_response = response
    elif command == "transition_run":
        run_id = scope_parts[2]
        primary = find_event(
            candidates, "run", "run.transitioned", response, aggregate_id=run_id
        )
        domain_events.append(primary)
        status = 200
        hash_parts = [
            "transition_run",
            run_id,
            str(response["version"] - 1),
            response["state"],
        ]
        expected_response = response
        cancel_audits = [
            event
            for event in candidates
            if event_identity(event) == event_identity(primary)
            and event["aggregate_type"] == "audit"
            and isinstance(event["payload"], dict)
            and event["payload"].get("tool_name") == "forja.cancel_run"
            and event["payload"].get("command_scope") == receipt["scope"]
        ]
        cancellation_events = [
            event
            for event in candidates
            if event_identity(event) == event_identity(primary)
            and event["aggregate_type"] == "sprint"
            and event["event_type"] == "sprint.cancellation_requested"
            and isinstance(event["payload"], dict)
            and event["payload"].get("run_id") == run_id
        ]
        if len(cancellation_events) > 1:
            raise ValueError(
                "transition_run command has ambiguous Sprint cancellation events"
            )
        if cancellation_events and response.get("state") != "cancelling":
            raise ValueError(
                "Sprint cancellation event disagrees with the Run transition state"
            )
        domain_events.extend(cancellation_events)
        if cancel_audits:
            tool_name = "forja.cancel_run"
    elif command == "create_attempt":
        run_id = scope_parts[2]
        primary = find_event(candidates, "attempt", "attempt.created", response)
        domain_events.append(primary)
        if response.get("run_id") != run_id:
            raise ValueError("create_attempt receipt scope disagrees with run_id")
        status = 201
        hash_parts = [
            receipt["scope"],
            response["status"],
            response["lease_resource_type"],
            response["lease_resource_id"],
            response["worker_id"],
            str(response["fencing_token"]),
        ]
        expected_response = response
    elif command == "plan_sprint":
        require_object(response, {"sprint", "run"}, "plan_sprint response")
        sprint = response["sprint"]
        run = response["run"]
        primary = find_event(candidates, "sprint", "sprint.planned", sprint)
        domain_events.extend(
            [primary, require_sibling(candidates, primary, "run", "run.created", run)]
        )
        status = 201
        hash_parts = [
            "plan_sprint",
            repository_id,
            sprint["title"],
            sprint["objective"],
        ]
        expected_response = {"sprint": sprint, "run": run}
        tool_name = "forja.plan_sprint"
    elif command == "submit_sprint":
        require_object(
            response,
            {"sprint", "decision", "run"},
            "submit_sprint response",
        )
        sprint = response["sprint"]
        decision = response["decision"]
        run = response["run"]
        if sprint.get("sprint_id") != scope_parts[2]:
            raise ValueError("submit_sprint receipt scope disagrees with sprint_id")
        primary = find_event(candidates, "sprint", "sprint.submitted", sprint)
        domain_events.extend(
            [
                primary,
                require_sibling(
                    candidates, primary, "decision", "decision.requested", decision
                ),
                require_sibling(candidates, primary, "run", "run.transitioned", run),
            ]
        )
        status = 200
        hash_parts = [
            "submit_sprint",
            sprint["sprint_id"],
            str(sprint["version"] - 1),
            decision["risk_class"],
        ]
        expected_response = {"sprint": sprint, "decision": decision, "run": run}
        tool_name = "forja.submit_sprint"
    elif command == "resolve_decision":
        require_object(
            response,
            {"sprint", "decision", "run"},
            "resolve_decision response",
        )
        sprint = response["sprint"]
        decision = response["decision"]
        run = response["run"]
        if decision.get("decision_id") != scope_parts[2]:
            raise ValueError("resolve_decision receipt scope disagrees with decision_id")
        if decision.get("status") == "approved":
            event_type = "decision.approved"
            approve = "true"
            tool_name = "forja.approve_decision"
        elif decision.get("status") == "rejected":
            event_type = "decision.rejected"
            approve = "false"
            tool_name = "forja.reject_decision"
        else:
            raise ValueError("resolved decision receipt has an unresolved status")
        primary = find_event(candidates, "decision", event_type, decision)
        domain_events.extend(
            [
                primary,
                require_sibling(
                    candidates,
                    primary,
                    "sprint",
                    "sprint.decision_resolved",
                    sprint,
                ),
                require_sibling(candidates, primary, "run", "run.transitioned", run),
            ]
        )
        status = 200
        hash_parts = [
            "resolve_decision",
            decision["decision_id"],
            str(decision["version"] - 1),
            approve,
            decision["reason"],
        ]
        expected_response = {"sprint": sprint, "decision": decision, "run": run}
    elif command == "resume_run":
        run_id = scope_parts[2]
        primary = find_event(
            candidates, "run", "run.transitioned", response, aggregate_id=run_id
        )
        domain_events.append(primary)
        status = 200
        hash_parts = ["resume_run", run_id, str(response["version"] - 1)]
        expected_response = response
        tool_name = "forja.resume_run"
    else:
        raise AssertionError(f"unreachable command {command}")

    verify_response(receipt, status, expected_response)
    audit_events = []
    if tool_name:
        audit_events.append(
            require_success_audit(candidates, primary, tool_name, receipt["scope"])
        )
    verify_hash(receipt, primary, hash_parts, tool_name)
    return {
        "identity": event_identity(primary),
        "stable_identity": stable_command_identity(primary),
        "tool_name": tool_name,
        "domain_event_ids": {event["event_id"] for event in domain_events},
        "audit_event_ids": {event["event_id"] for event in audit_events},
    }


def marker_hash(event, payload):
    fields = [
        event["tenant_id"],
        event["repository_id"],
        payload["scope"],
        event["idempotency_key"],
        payload["domain_event_id"],
        payload["command_event_id"],
        payload["command_aggregate_type"],
        payload["command_aggregate_id"],
        payload["command_event_type"],
        payload["actor_type"],
        payload["actor_id"],
        payload["correlation_id"],
        payload["causation_id"] or "",
    ]
    return hashlib.md5(  # noqa: S324 - integrity marker, not cryptography
        "\x1f".join(fields).encode("utf-8"), usedforsecurity=False
    ).hexdigest()


def migration_md5(value):
    return hashlib.md5(  # noqa: S324 - PostgreSQL migration compatibility
        value.encode("utf-8"), usedforsecurity=False
    ).hexdigest()


def generated_legacy_run_id(tenant_id, repository_id, sprint_uuid):
    digest = migration_md5(
        f"forja-legacy-sprint-run:{tenant_id}:{repository_id}:{sprint_uuid}"
    )
    return (
        f"run_{digest[0:8]}-{digest[8:12]}-4{digest[13:16]}-"
        f"8{digest[17:20]}-{digest[20:32]}"
    )


def is_generated_migration_event(event, events):
    if (
        event["actor_type"] != "system"
        or event["actor_id"] != "migration-003"
        or event["causation_id"] != ""
    ):
        return False
    tenant_id = event["tenant_id"]
    repository_id = event["repository_id"]
    if (
        event["aggregate_type"] == "sprint"
        and event["event_type"] == "sprint.migrated"
        and event["correlation_id"] == "migration-003-legacy-sprint"
        and event["idempotency_key"] == "migration-003-legacy-sprint"
        and event["aggregate_id"].startswith("sprint_")
    ):
        sprint_uuid = event["aggregate_id"].removeprefix("sprint_")
        expected_event_id = "event_legacy_sprint_" + migration_md5(
            f"{tenant_id}:{repository_id}:{sprint_uuid}"
        )
        return (
            event["event_id"] == expected_event_id
            and isinstance(event["payload"], dict)
            and event["payload"].get("sprint_id") == event["aggregate_id"]
        )
    if not (
        event["aggregate_type"] == "run"
        and event["event_type"] == "run.created"
        and event["correlation_id"] == "migration-003-legacy-run"
        and event["idempotency_key"] == "migration-003-legacy-run"
        and isinstance(event["payload"], dict)
        and event["payload"].get("run_id") == event["aggregate_id"]
    ):
        return False
    sprint_events = [
        candidate
        for candidate in events
        if candidate["tenant_id"] == tenant_id
        and candidate["repository_id"] == repository_id
        and candidate["aggregate_type"] == "sprint"
        and candidate["event_type"] == "sprint.migrated"
        and isinstance(candidate["payload"], dict)
        and candidate["payload"].get("run_id") == event["aggregate_id"]
    ]
    if len(sprint_events) != 1 or not is_generated_migration_event(
        sprint_events[0], events
    ):
        return False
    sprint_uuid = sprint_events[0]["aggregate_id"].removeprefix("sprint_")
    expected_event_id = "event_" + migration_md5(
        f"forja-legacy-sprint-event:{tenant_id}:{repository_id}:{sprint_uuid}"
    )
    return (
        event["aggregate_id"]
        == generated_legacy_run_id(tenant_id, repository_id, sprint_uuid)
        and event["event_id"] == expected_event_id
    )


def verify(command_events_path, receipts_path):
    events = load_events(command_events_path)
    receipts = load_receipts(receipts_path)
    events_by_id = {event["event_id"]: event for event in events}
    invalidated_event_ids = set()
    for event in events:
        if event["event_type"] != "idempotency.receipt_invalidated":
            continue
        payload = event["payload"]
        expected_fields = {
            "scope",
            "domain_event_id",
            "command_event_id",
            "command_aggregate_type",
            "command_aggregate_id",
            "command_event_type",
            "tenant_id",
            "repository_id",
            "idempotency_key",
            "actor_type",
            "actor_id",
            "correlation_id",
            "causation_id",
        }
        if (
            event["aggregate_type"] != "projection"
            or event["aggregate_version"] != 1
            or event["actor_type"] != "system"
            or event["actor_id"] != "migration-003-rollback"
            or event["correlation_id"] != "migration-003-rollback"
            or not isinstance(payload, dict)
            or set(payload) != expected_fields
            or payload["tenant_id"] != event["tenant_id"]
            or payload["repository_id"] != event["repository_id"]
            or payload["idempotency_key"] != event["idempotency_key"]
        ):
            raise ValueError(
                f"receipt invalidation marker {event['event_id']} is malformed"
            )
        expected_marker_hash = marker_hash(event, payload)
        if (
            event["event_id"]
            != "event_receipt_invalidation_" + expected_marker_hash
            or event["aggregate_id"]
            != "receipt_invalidation_" + expected_marker_hash
        ):
            raise ValueError(
                f"receipt invalidation marker {event['event_id']} has an invalid identity"
            )
        scope_parts = parse_scope(payload["scope"])
        if scope_parts[0] not in {
            "plan_sprint",
            "submit_sprint",
            "resolve_decision",
            "transition_run",
            "resume_run",
        } or scope_parts[1] != event["repository_id"]:
            raise ValueError(
                f"receipt invalidation marker {event['event_id']} has an invalid scope"
            )
        command_name = scope_parts[0]
        if command_name == "plan_sprint":
            anchor_type, anchor_id, anchor_event_types = (
                "sprint",
                None,
                {"sprint.planned"},
            )
        elif command_name == "submit_sprint":
            anchor_type, anchor_id, anchor_event_types = (
                "sprint",
                scope_parts[2],
                {"sprint.submitted"},
            )
        elif command_name == "resolve_decision":
            anchor_type, anchor_id, anchor_event_types = (
                "decision",
                scope_parts[2],
                {"decision.approved", "decision.rejected"},
            )
        else:
            anchor_type, anchor_id, anchor_event_types = (
                "run",
                scope_parts[2],
                {"run.transitioned"},
            )
        if (
            payload["command_aggregate_type"] != anchor_type
            or (anchor_id is not None and payload["command_aggregate_id"] != anchor_id)
            or payload["command_event_type"] not in anchor_event_types
        ):
            raise ValueError(
                f"receipt invalidation marker {event['event_id']} scope disagrees with its command anchor"
            )
        command_event = events_by_id.get(payload["command_event_id"])
        if command_event is None:
            if payload["command_aggregate_type"] != "decision":
                raise ValueError(
                    f"receipt invalidation marker {event['event_id']} references no command anchor"
                )
        elif (
            command_event["aggregate_type"] != payload["command_aggregate_type"]
            or command_event["aggregate_id"] != payload["command_aggregate_id"]
            or command_event["event_type"] != payload["command_event_type"]
        ):
            raise ValueError(
                f"receipt invalidation marker {event['event_id']} disagrees with its command anchor"
            )
        domain_event = events_by_id.get(payload["domain_event_id"])
        if domain_event is None or domain_event["aggregate_type"] not in DOMAIN_AGGREGATES:
            raise ValueError(
                f"receipt invalidation marker {event['event_id']} references no domain event"
            )
        expected_identity = (
            payload["tenant_id"],
            payload["repository_id"],
            payload["idempotency_key"],
            payload["actor_type"],
            payload["actor_id"],
            payload["correlation_id"],
            payload["causation_id"] or "",
        )
        if event_identity(domain_event) != expected_identity:
            raise ValueError(
                f"receipt invalidation marker {event['event_id']} disagrees with its domain event identity"
            )
        if command_event is not None and event_identity(command_event) != expected_identity:
            raise ValueError(
                f"receipt invalidation marker {event['event_id']} disagrees with its command anchor identity"
            )
        allowed_event_types = {
            "plan_sprint": {"sprint.planned", "run.created"},
            "submit_sprint": {"sprint.submitted", "run.transitioned"},
            "resolve_decision": {"sprint.decision_resolved", "run.transitioned"},
            "transition_run": {"sprint.cancellation_requested", "run.transitioned"},
            "resume_run": {"run.transitioned"},
        }
        if domain_event["event_type"] not in allowed_event_types[scope_parts[0]]:
            raise ValueError(
                f"receipt invalidation marker {event['event_id']} scope disagrees with its domain event"
            )
        if domain_event["event_id"] in invalidated_event_ids:
            raise ValueError(
                f"domain event {domain_event['event_id']} has ambiguous receipt invalidations"
            )
        invalidated_event_ids.add(domain_event["event_id"])
    consumed_event_ids = set()
    consumed_audit_event_ids = set()
    receipt_evidence = {}
    for receipt in receipts:
        evidence = verify_receipt(receipt, events)
        consumed_event_ids.update(evidence["domain_event_ids"])
        consumed_audit_event_ids.update(evidence["audit_event_ids"])
        receipt_evidence[
            (receipt["tenant_id"], receipt["scope"], receipt["idempotency_key"])
        ] = evidence

    for event in events:
        if event["aggregate_type"] in DOMAIN_AGGREGATES:
            if is_generated_migration_event(event, events):
                continue
            if (
                event["event_id"] not in consumed_event_ids
                and event["event_id"] not in invalidated_event_ids
            ):
                raise ValueError(
                    f"canonical command event {event['event_id']} has no matching receipt"
                )
        if event["aggregate_type"] == "audit" and isinstance(event["payload"], dict):
            payload = event["payload"]
            if event["event_type"] != "mcp.tool.succeeded" or payload.get(
                "tool_name"
            ) not in MUTATING_TOOLS:
                continue
            scope = payload.get("command_scope")
            evidence = receipt_evidence.get(
                (event["tenant_id"], scope, event["idempotency_key"])
            )
            if evidence is None:
                raise ValueError(
                    f"mutating audit event {event['event_id']} has no matching receipt"
                )
            expected_payload_identity = {
                "actor_type": event["actor_type"],
                "actor_id": event["actor_id"],
                "correlation_id": event["correlation_id"],
                "idempotency_key": event["idempotency_key"],
            }
            if any(
                payload.get(field) != value
                for field, value in expected_payload_identity.items()
            ) or payload.get("causation_id", "") != event["causation_id"]:
                raise ValueError(
                    f"mutating audit event {event['event_id']} payload disagrees with its event identity"
                )
            if payload.get("tool_name") != evidence["tool_name"]:
                raise ValueError(
                    f"mutating audit event {event['event_id']} tool disagrees with its receipt"
                )
            if payload.get("replay") is False:
                if event_identity(event) != evidence["identity"]:
                    raise ValueError(
                        f"atomic audit event {event['event_id']} identity disagrees with its receipt"
                    )
                if event["event_id"] not in consumed_audit_event_ids:
                    raise ValueError(
                        f"atomic audit event {event['event_id']} is not the receipt evidence"
                    )
            elif payload.get("replay") is True:
                if stable_command_identity(event) != evidence["stable_identity"]:
                    raise ValueError(
                        f"replay audit event {event['event_id']} identity disagrees with its receipt"
                    )
            else:
                raise ValueError(
                    f"mutating audit event {event['event_id']} has an invalid replay flag"
                )


def main(argv):
    if len(argv) != 3:
        print(
            "usage: verify_command_receipts.py COMMAND_EVENTS_TSV RECEIPTS_TSV",
            file=sys.stderr,
        )
        return 2
    try:
        verify(argv[1], argv[2])
    except (KeyError, TypeError, ValueError, json.JSONDecodeError) as error:
        print(f"command receipt verification failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
