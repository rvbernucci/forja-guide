#!/usr/bin/env python3
"""Reconstruct governed aggregates and compare them with canonical rows."""

import datetime
import pathlib
import re
import sys
from collections import defaultdict

from verify_command_receipts import decode_hex, load_events


SPRINT_ID = re.compile(
    r"^sprint_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$"
)
DECISION_ID = re.compile(
    r"^decision_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$"
)
RUN_ID = re.compile(
    r"^run_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$"
)
SPRINT_STATUSES = {
    "proposed",
    "awaiting_approval",
    "approved",
    "rejected",
    "cancelling",
}
DECISION_STATUSES = {"pending", "approved", "rejected"}
RISK_CLASSES = {"low", "medium", "high", "critical"}


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


def read_rows(path, field_count, kind):
    rows = []
    for line_number, line in enumerate(pathlib.Path(path).read_text().splitlines(), 1):
        fields = line.split("\t")
        if len(fields) != field_count:
            raise ValueError(
                f"{kind} row {line_number} has {len(fields)} fields, want {field_count}"
            )
        rows.append(fields)
    return rows


def load_sprints(path):
    sprints = {}
    for fields in read_rows(path, 12, "Sprint"):
        (
            tenant_id,
            repository_id,
            sprint_id,
            sequence_number,
            title_hex,
            objective_hex,
            status,
            version,
            run_id,
            pending_hex,
            created_us,
            updated_us,
        ) = fields
        key = (tenant_id, repository_id, sprint_id)
        if key in sprints:
            raise ValueError(f"duplicate canonical Sprint {key}")
        pending = decode_hex(pending_hex) or None
        row = {
            "sprint_id": sprint_id,
            "schema_version": "1.0",
            "sequence_number": int(sequence_number),
            "title": decode_hex(title_hex),
            "objective": decode_hex(objective_hex),
            "status": status,
            "version": int(version),
            "run_id": run_id,
            "pending_decision_id": pending,
            "created_us": int(created_us),
            "updated_us": int(updated_us),
        }
        validate_sprint(row, "canonical Sprint")
        sprints[key] = row
    return sprints


def load_decisions(path):
    decisions = {}
    for fields in read_rows(path, 15, "decision"):
        (
            tenant_id,
            repository_id,
            decision_id,
            sprint_id,
            run_id,
            action_hex,
            risk_hex,
            status,
            version,
            requested_hex,
            decided_hex,
            reason_hex,
            created_us,
            updated_us,
            decided_us,
        ) = fields
        key = (tenant_id, repository_id, decision_id)
        if key in decisions:
            raise ValueError(f"duplicate canonical decision {key}")
        decided_by = decode_hex(decided_hex) or None
        reason = decode_hex(reason_hex) or None
        decided_at = int(decided_us)
        row = {
            "decision_id": decision_id,
            "schema_version": "1.0",
            "sprint_id": sprint_id,
            "run_id": run_id,
            "action": decode_hex(action_hex),
            "risk_class": decode_hex(risk_hex),
            "status": status,
            "version": int(version),
            "requested_by": decode_hex(requested_hex),
            "decided_by": decided_by,
            "reason": reason,
            "created_us": int(created_us),
            "updated_us": int(updated_us),
            "decided_us": None if decided_at == -1 else decided_at,
        }
        validate_decision(row, "canonical decision")
        decisions[key] = row
    return decisions


def validate_sprint(value, source):
    required = {
        "sprint_id",
        "schema_version",
        "sequence_number",
        "title",
        "objective",
        "status",
        "version",
        "run_id",
        "pending_decision_id",
        "created_us",
        "updated_us",
    }
    if set(value) != required:
        raise ValueError(f"{source} fields differ from the Sprint contract")
    if not isinstance(value["sprint_id"], str) or not SPRINT_ID.fullmatch(value["sprint_id"]):
        raise ValueError(f"{source} has an invalid Sprint ID")
    if value["schema_version"] != "1.0":
        raise ValueError(f"{source} has an unsupported schema version")
    if type(value["sequence_number"]) is not int or value["sequence_number"] < 0:
        raise ValueError(f"{source} has an invalid sequence number")
    if not isinstance(value["title"], str) or not 1 <= len(value["title"]) <= 500:
        raise ValueError(f"{source} has an invalid title")
    if not isinstance(value["objective"], str) or not 3 <= len(value["objective"]) <= 8000:
        raise ValueError(f"{source} has an invalid objective")
    if value["status"] not in SPRINT_STATUSES:
        raise ValueError(f"{source} has an invalid status")
    if type(value["version"]) is not int or value["version"] < 1:
        raise ValueError(f"{source} has an invalid version")
    if not isinstance(value["run_id"], str) or not RUN_ID.fullmatch(value["run_id"]):
        raise ValueError(f"{source} has an invalid Run ID")
    pending = value["pending_decision_id"]
    if pending is not None and (
        not isinstance(pending, str) or not DECISION_ID.fullmatch(pending)
    ):
        raise ValueError(f"{source} has an invalid pending decision ID")
    if type(value["created_us"]) is not int or type(value["updated_us"]) is not int:
        raise ValueError(f"{source} has an invalid timestamp")
    if value["updated_us"] < value["created_us"]:
        raise ValueError(f"{source} moves backward in time")
    if value["status"] == "awaiting_approval" and pending is None:
        raise ValueError(f"{source} awaiting approval has no pending decision")
    if value["status"] != "awaiting_approval" and pending is not None:
        raise ValueError(f"{source} exposes a pending decision in status {value['status']}")


def validate_decision(value, source):
    required = {
        "decision_id",
        "schema_version",
        "sprint_id",
        "run_id",
        "action",
        "risk_class",
        "status",
        "version",
        "requested_by",
        "decided_by",
        "reason",
        "created_us",
        "updated_us",
        "decided_us",
    }
    if set(value) != required:
        raise ValueError(f"{source} fields differ from the decision contract")
    if not isinstance(value["decision_id"], str) or not DECISION_ID.fullmatch(value["decision_id"]):
        raise ValueError(f"{source} has an invalid decision ID")
    if value["schema_version"] != "1.0":
        raise ValueError(f"{source} has an unsupported schema version")
    if not isinstance(value["sprint_id"], str) or not SPRINT_ID.fullmatch(value["sprint_id"]):
        raise ValueError(f"{source} has an invalid Sprint ID")
    if not isinstance(value["run_id"], str) or not RUN_ID.fullmatch(value["run_id"]):
        raise ValueError(f"{source} has an invalid Run ID")
    if value["action"] != "submit_sprint" or value["risk_class"] not in RISK_CLASSES:
        raise ValueError(f"{source} has an invalid action or risk class")
    if value["status"] not in DECISION_STATUSES:
        raise ValueError(f"{source} has an invalid status")
    if type(value["version"]) is not int or value["version"] < 1:
        raise ValueError(f"{source} has an invalid version")
    if not isinstance(value["requested_by"], str) or not 1 <= len(value["requested_by"]) <= 160:
        raise ValueError(f"{source} has an invalid requester")
    if type(value["created_us"]) is not int or type(value["updated_us"]) is not int:
        raise ValueError(f"{source} has an invalid timestamp")
    if value["updated_us"] < value["created_us"]:
        raise ValueError(f"{source} moves backward in time")
    resolved = value["status"] in {"approved", "rejected"}
    if resolved:
        if not isinstance(value["decided_by"], str) or not 1 <= len(value["decided_by"]) <= 160:
            raise ValueError(f"{source} has an invalid decider")
        if not isinstance(value["reason"], str) or not 3 <= len(value["reason"]) <= 2000:
            raise ValueError(f"{source} has an invalid reason")
        if type(value["decided_us"]) is not int or value["decided_us"] < value["created_us"]:
            raise ValueError(f"{source} has an invalid decision time")
    elif any(value[field] is not None for field in ("decided_by", "reason", "decided_us")):
        raise ValueError(f"{source} pending decision contains resolution fields")


def sprint_from_event(event):
    payload = event["payload"]
    if not isinstance(payload, dict):
        raise ValueError(f"Sprint event {event['event_id']} payload is not an object")
    optional = {"pending_decision_id"}
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
    if not required.issubset(payload) or set(payload) - required - optional:
        raise ValueError(f"Sprint event {event['event_id']} payload fields differ")
    value = {
        "sprint_id": payload["sprint_id"],
        "schema_version": payload["schema_version"],
        "sequence_number": payload["sequence_number"],
        "title": payload["title"],
        "objective": payload["objective"],
        "status": payload["status"],
        "version": payload["version"],
        "run_id": payload["run_id"],
        "pending_decision_id": payload.get("pending_decision_id"),
        "created_us": parse_utc_us(payload["created_at"]),
        "updated_us": parse_utc_us(payload["updated_at"]),
    }
    validate_sprint(value, f"Sprint event {event['event_id']}")
    if event["aggregate_id"] != value["sprint_id"] or event["aggregate_version"] != value["version"]:
        raise ValueError(f"Sprint event {event['event_id']} envelope differs from its payload")
    return value


def decision_from_event(event):
    payload = event["payload"]
    if not isinstance(payload, dict):
        raise ValueError(f"decision event {event['event_id']} payload is not an object")
    optional = {"decided_by", "reason", "decided_at"}
    required = {
        "decision_id",
        "schema_version",
        "sprint_id",
        "run_id",
        "action",
        "risk_class",
        "status",
        "version",
        "requested_by",
        "created_at",
        "updated_at",
    }
    if not required.issubset(payload) or set(payload) - required - optional:
        raise ValueError(f"decision event {event['event_id']} payload fields differ")
    value = {
        "decision_id": payload["decision_id"],
        "schema_version": payload["schema_version"],
        "sprint_id": payload["sprint_id"],
        "run_id": payload["run_id"],
        "action": payload["action"],
        "risk_class": payload["risk_class"],
        "status": payload["status"],
        "version": payload["version"],
        "requested_by": payload["requested_by"],
        "decided_by": payload.get("decided_by"),
        "reason": payload.get("reason"),
        "created_us": parse_utc_us(payload["created_at"]),
        "updated_us": parse_utc_us(payload["updated_at"]),
        "decided_us": (
            parse_utc_us(payload["decided_at"])
            if "decided_at" in payload
            else None
        ),
    }
    validate_decision(value, f"decision event {event['event_id']}")
    if event["aggregate_id"] != value["decision_id"] or event["aggregate_version"] != value["version"]:
        raise ValueError(f"decision event {event['event_id']} envelope differs from its payload")
    return value


def verify_sprints(events, canonical):
    streams = defaultdict(list)
    for event in events:
        if event["aggregate_type"] == "sprint":
            streams[(event["tenant_id"], event["repository_id"], event["aggregate_id"])].append(event)
    if set(streams) != set(canonical):
        raise ValueError(
            "canonical Sprint identities and Sprint event streams differ: "
            f"rows_only={sorted(set(canonical) - set(streams))} "
            f"events_only={sorted(set(streams) - set(canonical))}"
        )
    for key, stream in streams.items():
        stream.sort(key=lambda event: event["aggregate_version"])
        previous = None
        for index, event in enumerate(stream):
            current = sprint_from_event(event)
            event_type = event["event_type"]
            if index == 0:
                if event_type == "sprint.planned":
                    if current["version"] != 1 or current["status"] != "proposed":
                        raise ValueError(f"Sprint stream {key} has an invalid planned baseline")
                elif event_type == "sprint.migrated":
                    if event["actor_type"] != "system" or event["actor_id"] != "migration-003":
                        raise ValueError(f"Sprint stream {key} has an untrusted migration baseline")
                else:
                    raise ValueError(f"Sprint stream {key} starts with {event_type}")
            else:
                if current["version"] != previous["version"] + 1:
                    raise ValueError(f"Sprint stream {key} has a version gap")
                immutable = ("sprint_id", "sequence_number", "title", "objective", "run_id", "created_us")
                if any(current[field] != previous[field] for field in immutable):
                    raise ValueError(f"Sprint stream {key} changed immutable identity")
                if current["updated_us"] < previous["updated_us"]:
                    raise ValueError(f"Sprint stream {key} moved backward in time")
                if event_type == "sprint.submitted":
                    valid = previous["status"] == "proposed" and current["status"] == "awaiting_approval"
                elif event_type == "sprint.decision_resolved":
                    valid = previous["status"] == "awaiting_approval" and current["status"] in {"approved", "rejected"}
                elif event_type == "sprint.cancellation_requested":
                    valid = previous["status"] in {"proposed", "approved"} and current["status"] == "cancelling"
                else:
                    valid = False
                if not valid:
                    raise ValueError(f"Sprint stream {key} has invalid transition {event_type}")
            previous = current
        if previous != canonical[key]:
            raise ValueError(f"canonical Sprint {key} differs from its latest event")


def verify_decisions(events, canonical):
    streams = defaultdict(list)
    for event in events:
        if event["aggregate_type"] == "decision":
            streams[(event["tenant_id"], event["repository_id"], event["aggregate_id"])].append(event)
    if set(streams) != set(canonical):
        raise ValueError(
            "canonical decision identities and decision event streams differ: "
            f"rows_only={sorted(set(canonical) - set(streams))} "
            f"events_only={sorted(set(streams) - set(canonical))}"
        )
    for key, stream in streams.items():
        stream.sort(key=lambda event: event["aggregate_version"])
        previous = None
        for index, event in enumerate(stream):
            current = decision_from_event(event)
            event_type = event["event_type"]
            if index == 0:
                if event_type != "decision.requested" or current["version"] != 1 or current["status"] != "pending":
                    raise ValueError(f"decision stream {key} has an invalid baseline")
            else:
                if current["version"] != previous["version"] + 1:
                    raise ValueError(f"decision stream {key} has a version gap")
                immutable = (
                    "decision_id", "sprint_id", "run_id", "action", "risk_class",
                    "requested_by", "created_us",
                )
                if any(current[field] != previous[field] for field in immutable):
                    raise ValueError(f"decision stream {key} changed immutable identity")
                expected_type = f"decision.{current['status']}"
                if previous["status"] != "pending" or current["status"] not in {"approved", "rejected"} or event_type != expected_type:
                    raise ValueError(f"decision stream {key} has invalid transition {event_type}")
            previous = current
        if previous != canonical[key]:
            raise ValueError(f"canonical decision {key} differs from its latest event")


def verify_cross_aggregate_links(sprints, decisions):
    pending_by_sprint = {}
    for key, decision in decisions.items():
        sprint_key = (key[0], key[1], decision["sprint_id"])
        sprint = sprints.get(sprint_key)
        if sprint is None or sprint["run_id"] != decision["run_id"]:
            raise ValueError(f"decision {key} does not match its canonical Sprint and Run")
        if decision["status"] == "pending":
            if sprint_key in pending_by_sprint:
                raise ValueError(f"Sprint {sprint_key} has multiple pending decisions")
            pending_by_sprint[sprint_key] = decision["decision_id"]
    for key, sprint in sprints.items():
        if sprint["pending_decision_id"] != pending_by_sprint.get(key):
            raise ValueError(f"Sprint {key} pending decision projection differs")


def main(argv):
    if len(argv) != 4:
        print(
            "usage: verify_governed_state.py EVENTS_TSV SPRINTS_TSV DECISIONS_TSV",
            file=sys.stderr,
        )
        return 2
    events = load_events(argv[1])
    sprints = load_sprints(argv[2])
    decisions = load_decisions(argv[3])
    verify_sprints(events, sprints)
    verify_decisions(events, decisions)
    verify_cross_aggregate_links(sprints, decisions)
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main(sys.argv))
    except (OSError, ValueError) as error:
        print(f"governed state verification failed: {error}", file=sys.stderr)
        raise SystemExit(1)
