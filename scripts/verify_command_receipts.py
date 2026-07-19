#!/usr/bin/env python3
"""Verify durable command receipts against their canonical event evidence."""

import datetime
import hashlib
import json
import pathlib
import posixpath
import re
import sys
from collections import defaultdict


DOMAIN_AGGREGATES = {"run", "attempt", "sprint", "decision", "approval"}
KNOWLEDGE_AGGREGATES = {
    "artifact",
    "artifact_manifest",
    "conversation",
    "memory",
    "memory_candidate",
    "message",
}
RECEIPT_BOUND_ARTIFACT_OPERATION_EVENTS = {
    "artifact.publication_activated",
    "artifact.publication_reconciled",
    "artifact.publication_reconciliation_failed",
}
MUTATING_TOOLS = {
    "forja.plan_sprint",
    "forja.submit_sprint",
    "forja.approve_decision",
    "forja.reject_decision",
    "forja.cancel_run",
    "forja.resume_run",
    "forja.authorize_delivery",
}
SPRINT_ID = re.compile(
    r"^sprint_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$"
)
RUN_ID = re.compile(
    r"^run_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$"
)
DELIVERY_ID = re.compile(
    r"^delivery_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$"
)
ATTEMPT_ID = re.compile(
    r"^attempt_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$"
)
TASK_ID = re.compile(
    r"^task_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$"
)
TENANT_ID = re.compile(
    r"^tenant_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$"
)
REPOSITORY_ID = re.compile(
    r"^repo_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$"
)
COMMIT_ID = re.compile(r"^[0-9a-f]{40}$")
CONTEXT_REF = re.compile(r"^(artifact|context)_[A-Za-z0-9_-]+$")
VALIDATOR_ID = re.compile(r"^[a-z0-9][a-z0-9._-]{0,119}$")


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


def parse_utc_us(value):
    if not isinstance(value, str) or not value.endswith("Z"):
        raise ValueError("timestamp must be an RFC3339 UTC string")
    parsed = datetime.datetime.fromisoformat(value[:-1] + "+00:00")
    if parsed.utcoffset() != datetime.timedelta(0):
        raise ValueError("timestamp must use UTC")
    elapsed = parsed - datetime.datetime(1970, 1, 1, tzinfo=datetime.timezone.utc)
    return (
        elapsed.days * 86400 * 1000000
        + elapsed.seconds * 1000000
        + elapsed.microseconds
    )


def load_events(path):
    events = []
    event_ids = set()
    outbox_ids = set()
    for line_number, line in enumerate(pathlib.Path(path).read_text().splitlines(), 1):
        fields = line.split("\t", 14)
        if len(fields) != 15:
            raise ValueError(
                f"command event row {line_number} has {len(fields)} fields, want 15"
            )
        (
            tenant_id,
            repository_id,
            aggregate_type,
            aggregate_id,
            aggregate_version,
            event_id,
            outbox_id,
            event_type,
            occurred_us,
            actor_type_hex,
            actor_id_hex,
            correlation_hex,
            causation_hex,
            idempotency_key_hex,
            payload_hex,
        ) = fields
        if event_id in event_ids:
            raise ValueError(f"duplicate event ID {event_id}")
        outbox_id = int(outbox_id)
        if outbox_id < 1 or outbox_id in outbox_ids:
            raise ValueError(f"invalid or duplicate outbox ID {outbox_id}")
        event_ids.add(event_id)
        outbox_ids.add(outbox_id)
        events.append(
            {
                "tenant_id": tenant_id,
                "repository_id": repository_id,
                "aggregate_type": aggregate_type,
                "aggregate_id": aggregate_id,
                "aggregate_version": int(aggregate_version),
                "event_id": event_id,
                "outbox_id": outbox_id,
                "event_type": event_type,
                "occurred_us": int(occurred_us),
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
        "start_attempt": 3,
        "finish_attempt": 3,
        "reconcile_attempts": 3,
        "plan_sprint": 2,
        "submit_sprint": 3,
        "resolve_decision": 3,
        "resume_run": 3,
        "authorize_delivery": 4,
        "artifact_publication": 3,
        "conversation_create": 3,
        "message_append": 3,
        "artifact_manifest_create": 3,
        "conversation_close": 3,
        "conversation_tombstone": 3,
        "memory_candidate_propose": 3,
        "memory_promote": 3,
        "memory_candidate_resolve": 3,
        "memory_transition": 4,
        "artifact_reconcile_complete": 3,
        "artifact_reconcile_fail": 4,
        "artifact_tombstone": 3,
        "artifact_object_purge": 4,
    }
    if not parts or parts[0] not in expected_lengths:
        raise ValueError(f"unsupported command receipt scope {scope!r}")
    if len(parts) != expected_lengths[parts[0]] or not parts[1]:
        raise ValueError(f"malformed command receipt scope {scope!r}")
    return parts


KNOWLEDGE_COMMANDS = {
    "conversation_create",
    "message_append",
    "artifact_manifest_create",
    "conversation_close",
    "conversation_tombstone",
    "memory_candidate_propose",
    "memory_promote",
    "memory_candidate_resolve",
    "memory_transition",
    "artifact_reconcile_complete",
    "artifact_reconcile_fail",
    "artifact_tombstone",
    "artifact_object_purge",
}


def ordered_fields(value, fields, optional=()):
    if not isinstance(value, dict):
        raise ValueError("ordered contract value must be an object")
    allowed = set(fields)
    if not set(value).issubset(allowed) or any(
        field not in value for field in fields if field not in optional
    ):
        raise ValueError("ordered contract value has unexpected fields")
    return {field: value[field] for field in fields if field in value}


def ordered_provenance(value):
    return ordered_fields(
        value,
        ["source_type", "source_refs", "source_commit"],
        {"source_commit"},
    )


def ordered_content_part(value):
    return ordered_fields(
        value,
        [
            "part_id", "ordinal", "kind", "artifact_id", "content_hash",
            "media_type", "size_bytes",
        ],
    )


def ordered_citation(value):
    ordered = ordered_fields(
        value,
        [
            "citation_id", "ordinal", "source_artifact_id",
            "source_content_hash", "locator",
        ],
    )
    ordered["locator"] = ordered_fields(ordered["locator"], ["kind", "value"])
    return ordered


def ordered_conversation(value):
    return ordered_fields(
        value,
        [
            "conversation_id", "schema_version", "tenant_id", "repository_id",
            "status", "version", "retention_class", "created_by",
            "transcript_artifact_id", "transcript_manifest_id", "created_at",
            "updated_at", "closed_at", "tombstoned_at",
        ],
        {
            "transcript_artifact_id", "transcript_manifest_id", "closed_at",
            "tombstoned_at",
        },
    )


def ordered_manifest(value):
    ordered = ordered_fields(
        value,
        [
            "manifest_id", "schema_version", "tenant_id", "repository_id",
            "family", "entries", "total_size_bytes", "source_refs",
            "created_by", "created_at",
        ],
    )
    ordered["entries"] = [
        ordered_fields(
            entry,
            ["logical_path", "artifact_id", "content_hash", "size_bytes", "media_type"],
        )
        for entry in ordered["entries"]
    ]
    return ordered


def ordered_candidate(value):
    return ordered_fields(
        value,
        [
            "candidate_id", "schema_version", "tenant_id", "repository_id",
            "conversation_id", "source_message_ids", "kind",
            "proposed_artifact_id", "proposed_content_hash", "status", "version",
            "proposed_by", "proposed_at", "expires_at", "memory_id",
            "resolved_by", "resolution_reason", "resolved_at",
        ],
        {
            "expires_at", "memory_id", "resolved_by", "resolution_reason",
            "resolved_at",
        },
    )


def ordered_memory(value):
    return ordered_fields(
        value,
        [
            "memory_id", "schema_version", "tenant_id", "repository_id",
            "source_candidate_id", "kind", "status", "version",
            "content_artifact_id", "content_hash", "authority_class",
            "promoted_by", "promotion_reason", "promoted_at", "expires_at",
            "supersedes", "superseded_by", "superseded_at", "expired_at",
            "tombstoned_at",
        ],
        {"superseded_by", "superseded_at", "expired_at", "tombstoned_at"},
    )


def ordered_artifact_intent(value):
    ordered = ordered_fields(
        value,
        [
            "OperationID", "ArtifactID", "RunID", "Kind", "ContentHash",
            "SizeBytes", "MediaType", "CreatedBy", "Provenance", "Metadata",
        ],
    )
    ordered["Provenance"] = ordered_provenance(ordered["Provenance"])
    metadata = ordered["Metadata"]
    if isinstance(metadata, dict):
        ordered["Metadata"] = {key: metadata[key] for key in sorted(metadata)}
    return ordered


def find_event_type(events, aggregate_type, event_type, aggregate_id=None):
    matches = [
        event for event in events
        if event["aggregate_type"] == aggregate_type
        and event["event_type"] == event_type
        and (aggregate_id is None or event["aggregate_id"] == aggregate_id)
    ]
    if len(matches) != 1:
        raise ValueError(
            f"expected one {aggregate_type}/{event_type} event, found {len(matches)}"
        )
    return matches[0]


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


def find_nested_attempt_event(events, event_type, attempt):
    wanted = canonical_json(attempt)
    matches = [
        event
        for event in events
        if event["aggregate_type"] == "attempt"
        and event["event_type"] == event_type
        and isinstance(event["payload"], dict)
        and canonical_json(event["payload"].get("attempt")) == wanted
        and event["aggregate_id"] == attempt.get("attempt_id")
    ]
    if len(matches) != 1:
        raise ValueError(f"expected one attempt/{event_type} event, found {len(matches)}")
    return matches[0]


def worker_result_hash_parts(result):
    usage = result["usage"]
    exit_code = "null" if result["exit_code"] is None else str(result["exit_code"])
    report_sha256 = result["report_sha256"]
    if not re.fullmatch(r"[0-9a-f]{64}", report_sha256):
        raise ValueError("finish_attempt event has invalid report_sha256")
    return [
        result["task_id"],
        result["adapter"],
        result["status"],
        str(result["retryable"]).lower(),
        result["termination_reason"],
        result["started_at"],
        result["finished_at"],
        str(result["duration_ms"]),
        exit_code,
        result["stdout_sha256"],
        result["stderr_sha256"],
        report_sha256,
        str(usage["input_tokens"]),
        str(usage["cached_input_tokens"]),
        str(usage["output_tokens"]),
        str(usage["tool_calls"]),
        canonical_json(result["evidence_refs"]),
    ]


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


def go_json_bytes(value):
    """Encode the approved request exactly like Go encoding/json on its struct."""
    encoded = json.dumps(
        value,
        ensure_ascii=False,
        allow_nan=False,
        separators=(",", ":"),
    )
    return (
        encoded.replace("<", "\\u003c")
        .replace(">", "\\u003e")
        .replace("&", "\\u0026")
        .replace("\u2028", "\\u2028")
        .replace("\u2029", "\\u2029")
        .encode("utf-8")
    )


def ordered_delivery_request(request):
    required = [
        "delivery_id", "tenant_id", "repository_id", "task_id", "attempt_id",
        "run_id", "schema_version", "repository_path", "worktree_root",
        "base_commit", "publication_ref", "publication_previous_commit",
        "author_id", "validator_id", "role", "objective", "read_scopes",
        "write_scopes", "artifact_scopes", "evidence_scope", "attempt_ordinal",
        "worker_budgets", "mechanical_validator_ids", "lease_ttl_ms",
    ]
    optional = ["context_pack_ref", "model"]
    if not isinstance(request, dict) or set(request) != set(required) | {
        field for field in optional if field in request
    }:
        raise ValueError("delivery authorization request has invalid fields")
    ordered = {}
    for field in required[:20]:
        ordered[field] = request[field]
    if "context_pack_ref" in request:
        ordered["context_pack_ref"] = request["context_pack_ref"]
    ordered["attempt_ordinal"] = request["attempt_ordinal"]
    if "model" in request:
        ordered["model"] = request["model"]
    budgets = request["worker_budgets"]
    budget_required = [
        "wall_clock_ms", "inactivity_ms", "max_output_bytes",
        "cancellation_grace_ms", "max_retries",
    ]
    budget_optional = ["max_tokens", "max_commands"]
    if not isinstance(budgets, dict) or set(budgets) != set(budget_required) | {
        field for field in budget_optional if field in budgets
    }:
        raise ValueError("delivery authorization has invalid worker budgets")
    ordered_budgets = {field: budgets[field] for field in budget_required}
    for field in budget_optional:
        if field in budgets:
            ordered_budgets[field] = budgets[field]
    ordered["worker_budgets"] = ordered_budgets
    ordered["mechanical_validator_ids"] = request["mechanical_validator_ids"]
    ordered["lease_ttl_ms"] = request["lease_ttl_ms"]
    return ordered


def is_integer(value):
    return isinstance(value, int) and not isinstance(value, bool)


def validate_absolute_root(value, label):
    if (
        not isinstance(value, str)
        or not 1 <= len(value) <= 4096
        or value == "/"
        or "\\" in value
        or "\x00" in value
        or not value.startswith("/")
        or value.startswith("//")
        or posixpath.normpath(value) != value
    ):
        raise ValueError(f"delivery authorization has invalid {label}")


def scope_covers(value, scopes):
    return any(value == scope or value.startswith(scope + "/") for scope in scopes)


def validate_scopes(values, label, allow_root):
    if (
        not isinstance(values, list)
        or not 1 <= len(values) <= 256
        or values != sorted(values)
        or len(values) != len(set(values))
    ):
        raise ValueError(f"delivery authorization has invalid {label}")
    for index, value in enumerate(values):
        if (
            not isinstance(value, str)
            or not 1 <= len(value) <= 1024
            or "\\" in value
            or "\x00" in value
            or value.startswith("/")
            or posixpath.normpath(value) != value
            or value == ".."
            or value.startswith("../")
            or (value == "." and not allow_root)
            or scope_covers(value, values[:index])
        ):
            raise ValueError(f"delivery authorization has invalid {label}")
    if allow_root and "." in values and values != ["."]:
        raise ValueError("delivery authorization has invalid root read scope")


def validate_delivery_request_contract(request):
    string_patterns = {
        "delivery_id": DELIVERY_ID,
        "tenant_id": TENANT_ID,
        "repository_id": REPOSITORY_ID,
        "task_id": TASK_ID,
        "attempt_id": ATTEMPT_ID,
        "run_id": RUN_ID,
        "base_commit": COMMIT_ID,
    }
    if any(
        not isinstance(request[field], str)
        or not pattern.fullmatch(request[field])
        for field, pattern in string_patterns.items()
    ):
        raise ValueError("delivery authorization has invalid contract identity")
    if request["schema_version"] != "1.1":
        raise ValueError("delivery authorization has invalid schema version")
    validate_absolute_root(request["repository_path"], "repository path")
    validate_absolute_root(request["worktree_root"], "worktree root")
    repository = request["repository_path"]
    worktrees = request["worktree_root"]
    if (
        repository == worktrees
        or repository.startswith(worktrees + "/")
        or worktrees.startswith(repository + "/")
    ):
        raise ValueError("delivery authorization roots overlap")
    previous = request["publication_previous_commit"]
    if previous is not None and (
        not isinstance(previous, str) or not COMMIT_ID.fullmatch(previous)
    ):
        raise ValueError("delivery authorization has invalid previous commit")
    if request["publication_ref"] != "refs/forja/deliveries/" + request["delivery_id"]:
        raise ValueError("delivery authorization has invalid publication ref")
    for field in ("author_id", "validator_id"):
        if not isinstance(request[field], str) or not 1 <= len(request[field]) <= 160:
            raise ValueError(f"delivery authorization has invalid {field}")
    if request["author_id"] == request["validator_id"]:
        raise ValueError("delivery authorization author and validator must differ")
    if request["role"] not in {"implementer", "tester"}:
        raise ValueError("delivery authorization has invalid role")
    if not isinstance(request["objective"], str) or not 3 <= len(request["objective"]) <= 8000:
        raise ValueError("delivery authorization has invalid objective")
    if "context_pack_ref" in request and (
        not isinstance(request["context_pack_ref"], str)
        or not CONTEXT_REF.fullmatch(request["context_pack_ref"])
    ):
        raise ValueError("delivery authorization has invalid context pack ref")
    if "model" in request and (
        not isinstance(request["model"], str)
        or not 1 <= len(request["model"]) <= 200
    ):
        raise ValueError("delivery authorization has invalid model")

    budgets = request["worker_budgets"]
    bounds = {
        "wall_clock_ms": (100, 86400000),
        "inactivity_ms": (100, 3600000),
        "max_output_bytes": (1024, 16777216),
        "cancellation_grace_ms": (10, 30000),
        "max_retries": (0, 10),
    }
    if any(
        not is_integer(budgets[field])
        or not minimum <= budgets[field] <= maximum
        for field, (minimum, maximum) in bounds.items()
    ):
        raise ValueError("delivery authorization has invalid worker budget values")
    for field, maximum in (("max_tokens", None), ("max_commands", 10000)):
        if field in budgets:
            value = budgets[field]
            if (
                not is_integer(value)
                or value < 1
                or (maximum is not None and value > maximum)
            ):
                raise ValueError(f"delivery authorization has invalid {field}")
    ordinal = request["attempt_ordinal"]
    if not is_integer(ordinal) or ordinal < 1 or ordinal > budgets["max_retries"] + 1:
        raise ValueError("delivery authorization has invalid attempt ordinal")
    lease_ttl = request["lease_ttl_ms"]
    if (
        not is_integer(lease_ttl)
        or not 60000 <= lease_ttl <= 86400000
        or lease_ttl <= budgets["wall_clock_ms"] + budgets["cancellation_grace_ms"]
    ):
        raise ValueError("delivery authorization has invalid lease TTL")

    validate_scopes(request["read_scopes"], "read scopes", True)
    validate_scopes(request["write_scopes"], "write scopes", False)
    validate_scopes(request["artifact_scopes"], "artifact scopes", False)
    writable = sorted(request["write_scopes"] + request["artifact_scopes"])
    validate_scopes(writable, "combined writable scopes", False)
    evidence = request["evidence_scope"]
    validate_scopes([evidence], "evidence scope", False)
    if not scope_covers(evidence, request["artifact_scopes"]):
        raise ValueError("delivery authorization evidence scope is not covered")
    validators = request["mechanical_validator_ids"]
    if (
        not isinstance(validators, list)
        or not 1 <= len(validators) <= 64
        or validators != sorted(validators)
        or len(validators) != len(set(validators))
        or any(not isinstance(value, str) or not VALIDATOR_ID.fullmatch(value) for value in validators)
    ):
        raise ValueError("delivery authorization has invalid mechanical validators")


def validate_delivery_authorization(event, authorization, events, scope_parts):
    require_object(
        authorization,
        {"request", "request_sha256", "approved_by", "approved_at"},
        "delivery authorization",
    )
    request = ordered_delivery_request(authorization["request"])
    validate_delivery_request_contract(request)
    repository_id, delivery_id, attempt_id = scope_parts[1:]
    expected_digest = hashlib.sha256(go_json_bytes(request)).hexdigest()
    id_patterns = {
        "delivery_id": DELIVERY_ID,
        "attempt_id": ATTEMPT_ID,
        "run_id": RUN_ID,
    }
    if any(
        not isinstance(request[field], str)
        or not pattern.fullmatch(request[field])
        for field, pattern in id_patterns.items()
    ):
        raise ValueError("delivery authorization has an invalid identity")
    if (
        event["aggregate_version"] != 1
        or event["aggregate_id"] != delivery_id + ":" + attempt_id
        or request["delivery_id"] != delivery_id
        or request["attempt_id"] != attempt_id
        or request["repository_id"] != "repo_" + repository_id
        or request["tenant_id"] != "tenant_" + event["tenant_id"]
        or event["repository_id"] != repository_id
        or request["schema_version"] != "1.1"
        or request["publication_ref"] != "refs/forja/deliveries/" + delivery_id
        or authorization["request_sha256"] != expected_digest
        or event["actor_type"] != "human"
        or event["actor_id"] != authorization["approved_by"]
        or event["actor_id"] in {request["author_id"], request["validator_id"]}
        or request["author_id"] == request["validator_id"]
    ):
        raise ValueError("delivery authorization disagrees with its authority envelope")
    approved_us = parse_utc_us(authorization["approved_at"])
    if approved_us != event["occurred_us"]:
        raise ValueError("delivery authorization timestamp differs from its event")
    if (
        type(request["attempt_ordinal"]) is not int
        or request["attempt_ordinal"] < 1
        or not isinstance(request["objective"], str)
        or not 3 <= len(request["objective"]) <= 8000
    ):
        raise ValueError("delivery authorization has invalid execution authority")

    prior = [
        candidate for candidate in events
        if candidate["tenant_id"] == event["tenant_id"]
        and candidate["repository_id"] == event["repository_id"]
        and candidate["outbox_id"] < event["outbox_id"]
    ]
    approved_decisions = [
        candidate for candidate in prior
        if candidate["aggregate_type"] == "decision"
        and candidate["event_type"] == "decision.approved"
        and isinstance(candidate["payload"], dict)
        and candidate["payload"].get("run_id") == request["run_id"]
        and candidate["payload"].get("action") == "submit_sprint"
        and candidate["payload"].get("status") == "approved"
    ]
    attempts = [
        candidate for candidate in prior
        if candidate["aggregate_type"] == "attempt"
        and candidate["event_type"] == "attempt.created"
        and candidate["aggregate_id"] == request["attempt_id"]
        and isinstance(candidate["payload"], dict)
        and candidate["payload"].get("run_id") == request["run_id"]
        and candidate["payload"].get("ordinal") == request["attempt_ordinal"]
        and candidate["payload"].get("status") == "queued"
    ]
    run_events = [
        candidate for candidate in prior
        if candidate["aggregate_type"] == "run"
        and candidate["aggregate_id"] == request["run_id"]
        and isinstance(candidate["payload"], dict)
    ]
    attempt_events = [
        candidate for candidate in prior
        if candidate["aggregate_type"] == "attempt"
        and candidate["aggregate_id"] == request["attempt_id"]
        and isinstance(candidate["payload"], dict)
    ]
    if (
        len(approved_decisions) != 1
        or len(attempts) != 1
        or not run_events
        or not attempt_events
    ):
        raise ValueError("delivery authorization lacks prior governed execution authority")
    latest_run = max(run_events, key=lambda candidate: candidate["aggregate_version"])
    latest_attempt = max(
        attempt_events, key=lambda candidate: candidate["aggregate_version"]
    )
    run_snapshot = latest_run["payload"]
    attempt_payload = latest_attempt["payload"]
    attempt_snapshot = attempt_payload.get("attempt", attempt_payload)
    if (
        not isinstance(attempt_snapshot, dict)
        or run_snapshot.get("run_id") != request["run_id"]
        or run_snapshot.get("objective") != request["objective"]
        or run_snapshot.get("state") != "queued"
        or attempt_snapshot.get("attempt_id") != request["attempt_id"]
        or attempt_snapshot.get("run_id") != request["run_id"]
        or attempt_snapshot.get("ordinal") != request["attempt_ordinal"]
        or attempt_snapshot.get("status") != "queued"
    ):
        raise ValueError("delivery authorization used stale execution authority")


def validate_sprint_cancellation_event(event, run_id):
    payload = event["payload"]
    required = {
        "sprint_id",
        "schema_version",
        "sequence_number",
        "title",
        "objective",
        "status",
        "version",
        "run_id",
        "created_at",
        "updated_at",
    }
    if not isinstance(payload, dict) or set(payload) != required:
        raise ValueError(
            "Sprint cancellation payload fields differ from the Sprint contract"
        )
    if not isinstance(payload["sprint_id"], str) or not SPRINT_ID.fullmatch(
        payload["sprint_id"]
    ):
        raise ValueError("Sprint cancellation payload has an invalid Sprint ID")
    if payload["sprint_id"] != event["aggregate_id"]:
        raise ValueError("Sprint cancellation payload disagrees with its aggregate ID")
    if payload["schema_version"] != "1.0":
        raise ValueError("Sprint cancellation payload has an unsupported schema version")
    if type(payload["sequence_number"]) is not int or payload["sequence_number"] < 0:
        raise ValueError("Sprint cancellation payload has an invalid sequence number")
    if not isinstance(payload["title"], str) or not 1 <= len(payload["title"]) <= 500:
        raise ValueError("Sprint cancellation payload has an invalid title")
    if not isinstance(payload["objective"], str) or not 3 <= len(
        payload["objective"]
    ) <= 8000:
        raise ValueError("Sprint cancellation payload has an invalid objective")
    if type(payload["version"]) is not int or payload["version"] < 1:
        raise ValueError("Sprint cancellation payload has an invalid version")
    if payload["version"] != event["aggregate_version"]:
        raise ValueError("Sprint cancellation payload disagrees with its aggregate version")
    if not isinstance(payload["run_id"], str) or not RUN_ID.fullmatch(
        payload["run_id"]
    ):
        raise ValueError("Sprint cancellation payload has an invalid Run ID")
    if payload["run_id"] != run_id:
        raise ValueError("Sprint cancellation payload disagrees with the Run receipt")
    if payload["status"] != "cancelling":
        raise ValueError("Sprint cancellation payload is not cancelling")
    timestamps = []
    for field in ("created_at", "updated_at"):
        value = payload[field]
        if not isinstance(value, str) or not value.endswith("Z"):
            raise ValueError("Sprint cancellation payload has a non-UTC timestamp")
        try:
            parsed = datetime.datetime.fromisoformat(value[:-1] + "+00:00")
        except ValueError as error:
            raise ValueError(
                "Sprint cancellation payload has an invalid timestamp"
            ) from error
        if parsed.utcoffset() != datetime.timedelta(0):
            raise ValueError("Sprint cancellation payload has a non-UTC timestamp")
        timestamps.append(parsed)
    if timestamps[1] < timestamps[0]:
        raise ValueError("Sprint cancellation payload moves backward in time")


def verify_artifact_publication_receipt(receipt, candidates, scope_parts):
    artifact_id = scope_parts[2]
    response = receipt["response"]
    primary = find_event(
        candidates, "artifact", "artifact.activated", response, aggregate_id=artifact_id
    )
    publication = find_event_type(
        candidates,
        "artifact_operation",
        "artifact.publication_activated",
    )
    payload = publication["payload"]
    require_object(payload, {"Intent", "State", "Version", "CreatedAt", "UpdatedAt"}, "artifact publication")
    intent = ordered_artifact_intent(payload["Intent"])
    if (
        intent["ArtifactID"] != artifact_id
        or publication["aggregate_id"] != intent["OperationID"]
        or response.get("artifact_id") != artifact_id
    ):
        raise ValueError("artifact publication scope differs from its canonical events")
    verify_response(receipt, 201, response)
    verify_hash(
        receipt,
        primary,
        ["artifact_publication", go_json_bytes(intent).decode("utf-8")],
    )
    return {
        "identity": event_identity(primary),
        "stable_identity": stable_command_identity(primary),
        "tool_name": "",
        "domain_event_ids": {primary["event_id"], publication["event_id"]},
        "audit_event_ids": set(),
    }


def verify_knowledge_receipt(receipt, candidates, scope_parts):
    command = scope_parts[0]
    response = receipt["response"]
    repository_id = scope_parts[1]
    domain_events = []

    if command == "conversation_create":
        primary = find_event(
            candidates, "conversation", "conversation.created", response,
            aggregate_id=scope_parts[2],
        )
        status = 201
        parts = [
            response["conversation_id"], response["retention_class"],
            response["created_by"],
        ]
    elif command == "message_append":
        primary = find_event(
            candidates, "message", "message.appended", response,
            aggregate_id=scope_parts[2],
        )
        companion = find_event_type(
            candidates, "conversation", "conversation.message_appended",
            response["conversation_id"],
        )
        domain_events.append(companion)
        draft = {
            "MessageID": response["message_id"],
            "ConversationID": response["conversation_id"],
            "Role": response["role"],
            "AuthorID": response["author_id"],
            "SupersedesMessageID": response.get("supersedes_message_id"),
            "ContentParts": [ordered_content_part(part) for part in response["content_parts"]],
            "Citations": None if response["citations"] is None else [
                ordered_citation(citation) for citation in response["citations"]
            ],
        }
        status = 201
        parts = [go_json_bytes(draft).decode("utf-8")]
    elif command == "artifact_manifest_create":
        primary = find_event(
            candidates, "artifact_manifest", "artifact_manifest.created", response,
            aggregate_id=scope_parts[2],
        )
        status = 201
        parts = [go_json_bytes(ordered_manifest(response)).decode("utf-8")]
    elif command == "conversation_close":
        primary = find_event(
            candidates, "conversation", "conversation.closed", response,
            aggregate_id=scope_parts[2],
        )
        status = 200
        parts = [
            response["conversation_id"], str(response["version"] - 1),
            response["transcript_artifact_id"], response["transcript_manifest_id"],
        ]
    elif command == "conversation_tombstone":
        primary = find_event(
            candidates, "conversation", "conversation.tombstoned", response,
            aggregate_id=scope_parts[2],
        )
        status = 200
        parts = [response["conversation_id"], str(response["version"] - 1)]
    elif command == "memory_candidate_propose":
        primary = find_event(
            candidates, "memory_candidate", "memory_candidate.proposed", response,
            aggregate_id=scope_parts[2],
        )
        status = 201
        parts = [go_json_bytes(ordered_candidate(response)).decode("utf-8")]
    elif command == "memory_promote":
        primary = find_event(
            candidates, "memory", "memory.promoted", response,
            aggregate_id=scope_parts[2],
        )
        promoted_candidate = find_event_type(
            candidates, "memory_candidate", "memory_candidate.promoted",
            response["source_candidate_id"],
        )
        domain_events.append(promoted_candidate)
        domain_events.extend(
            event for event in candidates
            if event["aggregate_type"] == "memory"
            and event["event_type"] == "memory.superseded"
        )
        command_value = {
            "Memory": ordered_memory(response),
            "ExpectedCandidateVersion": promoted_candidate["payload"]["version"] - 1,
        }
        status = 201
        parts = [go_json_bytes(command_value).decode("utf-8")]
    elif command == "memory_candidate_resolve":
        event_type = "memory_candidate." + response["status"]
        primary = find_event(
            candidates, "memory_candidate", event_type, response,
            aggregate_id=scope_parts[2],
        )
        status = 200
        parts = [
            response["candidate_id"], str(response["version"] - 1),
            response["status"], response["resolution_reason"],
        ]
    elif command == "memory_transition":
        primary = find_event(
            candidates, "memory", "memory." + scope_parts[3], response,
            aggregate_id=scope_parts[2],
        )
        status = 200
        parts = [response["memory_id"], str(response["version"] - 1), scope_parts[3]]
    elif command == "artifact_reconcile_complete":
        primary = find_event(
            candidates, "artifact", "artifact.activated", response,
            aggregate_id=response["artifact_id"],
        )
        publication = find_event_type(
            candidates, "artifact_operation", "artifact.publication_reconciled",
            scope_parts[2],
        )
        domain_events.append(publication)
        status = 200
        parts = [scope_parts[2]]
    elif command == "artifact_reconcile_fail":
        primary = find_event_type(
            candidates,
            "artifact_operation",
            "artifact.publication_reconciliation_failed",
            scope_parts[2],
        )
        require_object(primary["payload"], {"publication", "failure_class"}, "artifact reconciliation failure")
        if primary["payload"]["failure_class"] != scope_parts[3]:
            raise ValueError("artifact reconciliation failure class differs from scope")
        if canonical_json(primary["payload"]["publication"]) != canonical_json(response):
            raise ValueError("artifact reconciliation failure response differs from event")
        status = 200
        parts = [scope_parts[2], scope_parts[3]]
    elif command == "artifact_tombstone":
        matches = [
            event for event in candidates
            if event["aggregate_type"] == "artifact"
            and event["event_type"] == "artifact.tombstoned"
            and isinstance(event["payload"], dict)
            and canonical_json(event["payload"].get("artifact")) == canonical_json(response)
            and event["aggregate_id"] == scope_parts[2]
        ]
        if len(matches) != 1 or matches[0]["payload"].get("deleted") is not False:
            raise ValueError("artifact tombstone lacks its exact pre-delete event")
        primary = matches[0]
        status = 200
        parts = [scope_parts[2], str(primary["aggregate_version"] - 1)]
    elif command == "artifact_object_purge":
        primary = find_event_type(
            candidates, "artifact", "artifact.object_purged",
        )
        content_hash = "sha256:" + scope_parts[3]
        if primary["payload"] != {"content_hash": content_hash, "deleted": True}:
            raise ValueError("artifact purge event differs from its scope")
        status = 200
        parts = [content_hash]
    else:
        raise AssertionError(f"unreachable knowledge command {command}")

    if not primary["repository_id"] == repository_id:
        raise ValueError("knowledge receipt repository differs from event")
    verify_response(receipt, status, response)
    verify_hash(receipt, primary, ["knowledge", *parts])
    domain_events.append(primary)
    return {
        "identity": event_identity(primary),
        "stable_identity": stable_command_identity(primary),
        "tool_name": "",
        "domain_event_ids": {event["event_id"] for event in domain_events},
        "audit_event_ids": set(),
    }


def verify_receipt(receipt, events):
    scope_parts = parse_scope(receipt["scope"])
    command = scope_parts[0]
    repository_id = scope_parts[1]
    response = receipt["response"]
    candidates = [
        event
        for event in events
        if event["tenant_id"] == receipt["tenant_id"]
        and event["repository_id"] == repository_id
        and event["idempotency_key"] == receipt["idempotency_key"]
    ]
    if command == "artifact_publication":
        if not candidates:
            raise ValueError(f"receipt {receipt['scope']} has no canonical command events")
        return verify_artifact_publication_receipt(receipt, candidates, scope_parts)
    if command in KNOWLEDGE_COMMANDS:
        if not candidates:
            raise ValueError(f"receipt {receipt['scope']} has no canonical command events")
        return verify_knowledge_receipt(receipt, candidates, scope_parts)
    empty_reconciliation = (
        command == "reconcile_attempts"
        and isinstance(response, dict)
        and response.get("attempts") == []
    )
    if not candidates and not empty_reconciliation:
        raise ValueError(f"receipt {receipt['scope']} has no canonical command events")

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
        for cancellation_event in cancellation_events:
            validate_sprint_cancellation_event(cancellation_event, run_id)
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
    elif command == "start_attempt":
        attempt_id = scope_parts[2]
        primary = find_nested_attempt_event(candidates, "attempt.started", response)
        domain_events.append(primary)
        if response.get("attempt_id") != attempt_id or response.get("status") != "running":
            raise ValueError("start_attempt receipt disagrees with its attempt")
        status = 201
        hash_parts = [
            receipt["scope"],
            str(response["version"] - 1),
            response["worker_id"],
            str(response["fencing_token"]),
        ]
        expected_response = response
    elif command == "finish_attempt":
        attempt_id = scope_parts[2]
        primary = find_nested_attempt_event(candidates, "attempt.finished", response)
        domain_events.append(primary)
        result = primary["payload"].get("result")
        if not isinstance(result, dict) or response.get("attempt_id") != attempt_id:
            raise ValueError("finish_attempt receipt lacks its safe worker result")
        status = 201
        hash_parts = [receipt["scope"], str(response["version"] - 1)]
        hash_parts.extend(worker_result_hash_parts(result))
        hash_parts.extend([response["worker_id"], str(response["fencing_token"])])
        expected_response = response
    elif command == "reconcile_attempts":
        require_object(
            response,
            {"attempts", "authority", "command"},
            "reconcile_attempts response",
        )
        attempts = response["attempts"]
        authority = response["authority"]
        command_snapshot = response["command"]
        if not isinstance(attempts, list):
            raise ValueError("reconcile_attempts attempts must be a list")
        require_object(
            authority,
            {
                "tenant_id",
                "repository_id",
                "resource_type",
                "resource_id",
                "owner_id",
                "fencing_token",
            },
            "reconcile_attempts authority",
        )
        require_object(
            command_snapshot,
            {
                "tenant_id",
                "repository_id",
                "idempotency_key",
                "actor_type",
                "actor_id",
                "correlation_id",
                "causation_id",
            },
            "reconcile_attempts command",
        )
        repository_id = scope_parts[1]
        resource_id = scope_parts[2]
        if (
            authority["tenant_id"] != receipt["tenant_id"]
            or authority["repository_id"] != repository_id
            or authority["resource_type"] != "scheduler"
            or authority["resource_id"] != resource_id
            or not isinstance(authority["owner_id"], str)
            or not authority["owner_id"]
            or type(authority["fencing_token"]) is not int
            or authority["fencing_token"] < 1
        ):
            raise ValueError("reconcile_attempts authority disagrees with its scope")
        if (
            command_snapshot["tenant_id"] != receipt["tenant_id"]
            or command_snapshot["repository_id"] != repository_id
            or command_snapshot["idempotency_key"] != receipt["idempotency_key"]
            or command_snapshot["actor_type"] not in {"human", "agent", "worker", "system"}
            or any(
                not isinstance(command_snapshot[field], str)
                or not command_snapshot[field]
                for field in ("actor_id", "correlation_id")
            )
            or not isinstance(command_snapshot["causation_id"], str)
        ):
            raise ValueError("reconcile_attempts command provenance is invalid")
        synthetic_primary = {
            "tenant_id": command_snapshot["tenant_id"],
            "repository_id": command_snapshot["repository_id"],
            "idempotency_key": command_snapshot["idempotency_key"],
            "actor_type": command_snapshot["actor_type"],
            "actor_id": command_snapshot["actor_id"],
            "correlation_id": command_snapshot["correlation_id"],
            "causation_id": command_snapshot["causation_id"],
        }
        recovered = []
        for attempt in attempts:
            event = find_nested_attempt_event(candidates, "attempt.reconciled", attempt)
            recovered.append(event)
        primary = recovered[0] if recovered else synthetic_primary
        if any(
            event_identity(event) != event_identity(synthetic_primary)
            for event in recovered
        ):
            raise ValueError("reconcile_attempts events disagree with command provenance")
        if any(
            canonical_json(event["payload"].get("reconciled_by"))
            != canonical_json(authority)
            for event in recovered
        ):
            raise ValueError("reconcile_attempts events disagree with recovery authority")
        domain_events.extend(recovered)
        status = 200
        hash_parts = [
            receipt["scope"],
            authority["owner_id"],
            str(authority["fencing_token"]),
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
    elif command == "authorize_delivery":
        delivery_id = scope_parts[2]
        primary = find_event(
            candidates,
            "approval",
            "delivery.authorized",
            response,
            aggregate_id=delivery_id + ":" + scope_parts[3],
        )
        validate_delivery_authorization(primary, response, events, scope_parts)
        domain_events.append(primary)
        status = 200
        hash_parts = [receipt["scope"], response["request_sha256"]]
        expected_response = response
        tool_name = "forja.authorize_delivery"
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


def requires_command_receipt(event):
    """Identify canonical events that must be consumed by one durable receipt."""
    if event["aggregate_type"] in DOMAIN_AGGREGATES | KNOWLEDGE_AGGREGATES:
        return True
    return (
        event["aggregate_type"] == "artifact_operation"
        and event["event_type"] in RECEIPT_BOUND_ARTIFACT_OPERATION_EVENTS
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
        if requires_command_receipt(event):
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
