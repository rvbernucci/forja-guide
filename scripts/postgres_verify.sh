#!/usr/bin/env bash
set -euo pipefail

if [[ "$#" -ne 1 ]]; then
  echo "usage: postgres_verify.sh <database-url>" >&2
  exit 2
fi

database_url="$1"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
umask 077

python3 - "$root/internal/postgres/migrations" >"$work/expected.tsv" <<'PY'
import hashlib
import pathlib
import sys

migrations = pathlib.Path(sys.argv[1])
for up in sorted(migrations.glob("*.up.sql")):
    prefix, remainder = up.name.split("_", 1)
    name = remainder.removesuffix(".up.sql")
    down = migrations / f"{prefix}_{name}.down.sql"
    if not down.is_file():
        raise SystemExit(f"missing down migration for {up.name}")
    digest = hashlib.sha256(up.read_bytes() + b"\0" + down.read_bytes()).hexdigest()
    print(int(prefix), name, digest, sep="\t")
PY

psql "$database_url" \
  --no-psqlrc \
  --set=ON_ERROR_STOP=1 \
  --tuples-only \
  --no-align \
  --field-separator=$'\t' \
  --command="
    SELECT version, name, checksum
    FROM forja.schema_migrations
    ORDER BY version;
  " >"$work/actual.tsv"

if ! diff \
  --unified \
  --label expected-migrations \
  --label restored-migrations \
  "$work/expected.tsv" \
  "$work/actual.tsv"; then
  echo "restored migration ledger does not match this release" >&2
  exit 1
fi

psql "$database_url" \
  --no-psqlrc \
  --set=ON_ERROR_STOP=1 \
  --tuples-only \
  --no-align \
  --field-separator=$'\t' \
  --command="
    SELECT table_name,
           string_agg(
             column_name || ':' || udt_name || ':' || is_nullable || ':' ||
             is_identity || ':' || COALESCE(identity_generation, '') || ':' ||
             COALESCE(column_default, ''),
             ',' ORDER BY ordinal_position
           )
    FROM information_schema.columns
    WHERE table_schema='forja'
    GROUP BY table_name
    ORDER BY table_name;
  " >"$work/tables.tsv"

psql "$database_url" \
  --no-psqlrc \
  --set=ON_ERROR_STOP=1 \
  --tuples-only \
  --no-align \
  --field-separator=$'\t' \
  --command="
    SELECT c.conrelid::regclass::text || ':' || c.conname || ':' || c.contype::text,
           pg_get_constraintdef(c.oid, true)
    FROM pg_constraint AS c
    JOIN pg_namespace AS n ON n.oid=c.connamespace
    WHERE n.nspname='forja'
    ORDER BY 1;
  " >"$work/constraints.txt"

psql "$database_url" \
  --no-psqlrc \
  --set=ON_ERROR_STOP=1 \
  --tuples-only \
  --no-align \
  --field-separator=$'\t' \
  --command="
    SELECT schemaname || '.' || tablename || ':' || indexname, indexdef
    FROM pg_indexes
    WHERE schemaname='forja'
    ORDER BY 1;
  " >"$work/indexes.txt"

psql "$database_url" \
  --no-psqlrc \
  --set=ON_ERROR_STOP=1 \
  --tuples-only \
  --no-align \
  --field-separator=$'\t' \
  --command="
    SELECT t.tgrelid::regclass::text || ':' || t.tgname,
           t.tgenabled::text || ':' ||
           t.tgtype::text || ':' ||
           l.lanname || ':' ||
           p.prosecdef::text || ':' ||
           p.provolatile::text || ':' ||
           encode(convert_to(p.prosrc, 'UTF8'), 'hex') || ':' ||
           encode(
             convert_to(pg_get_triggerdef(t.oid, true), 'UTF8'),
             'hex'
           )
    FROM pg_trigger AS t
    JOIN pg_class AS c ON c.oid=t.tgrelid
    JOIN pg_namespace AS n ON n.oid=c.relnamespace
    JOIN pg_proc AS p ON p.oid=t.tgfoid
    JOIN pg_language AS l ON l.oid=p.prolang
    WHERE n.nspname='forja' AND NOT t.tgisinternal
    ORDER BY 1;
  " >"$work/triggers.txt"

psql "$database_url" \
  --no-psqlrc \
  --set=ON_ERROR_STOP=1 \
  --tuples-only \
  --no-align \
  --field-separator=$'\t' \
  --command="
    SELECT t.tgrelid::regclass::text,
           t.tgname,
           t.tgenabled::text,
           p.pronamespace::regnamespace::text || '.' || p.proname,
           t.tgtype,
           l.lanname,
           p.prosecdef,
           p.provolatile::text,
           encode(convert_to(p.prosrc, 'UTF8'), 'hex'),
           encode(convert_to(pg_get_triggerdef(t.oid, true), 'UTF8'), 'hex')
    FROM pg_trigger AS t
    JOIN pg_proc AS p ON p.oid=t.tgfoid
    JOIN pg_language AS l ON l.oid=p.prolang
    WHERE t.tgrelid='forja.events'::regclass
      AND t.tgname='events_are_append_only'
      AND NOT t.tgisinternal;
  " >"$work/trigger.tsv"

authority="$(
  psql "$database_url" \
    --no-psqlrc \
    --set=ON_ERROR_STOP=1 \
    --tuples-only \
    --no-align \
    --command="
      SELECT EXISTS (
        SELECT 1
        FROM forja.tenants AS t
        JOIN forja.repositories AS r ON r.tenant_id=t.tenant_id
        WHERE t.tenant_id='00000000-0000-4000-8000-000000000001'
          AND r.repository_id='00000000-0000-4000-8000-000000000002'
      );
    "
)"

psql "$database_url" \
  --no-psqlrc \
  --set=ON_ERROR_STOP=1 \
  --tuples-only \
  --no-align \
  --field-separator=$'\t' \
  --command="
    SELECT tenant_id::text,
           repository_id::text,
           aggregate_id,
           aggregate_version,
           event_id,
           event_type,
           (extract(epoch FROM occurred_at) * 1000000)::bigint,
           encode(convert_to(payload::text, 'UTF8'), 'hex')
    FROM forja.events
    WHERE aggregate_type='run'
    ORDER BY tenant_id, repository_id, aggregate_id, aggregate_version;
  " >"$work/run-events.tsv"

psql "$database_url" \
  --no-psqlrc \
  --set=ON_ERROR_STOP=1 \
  --tuples-only \
  --no-align \
  --field-separator=$'\t' \
  --command="
    SELECT tenant_id::text,
           repository_id::text,
           run_id,
           encode(convert_to(objective, 'UTF8'), 'hex'),
           state,
           version,
           (extract(epoch FROM created_at) * 1000000)::bigint,
           (extract(epoch FROM updated_at) * 1000000)::bigint
    FROM forja.runs
    ORDER BY tenant_id, repository_id, run_id;
  " >"$work/runs.tsv"

psql "$database_url" \
  --no-psqlrc \
  --set=ON_ERROR_STOP=1 \
  --tuples-only \
  --no-align \
  --field-separator=$'\t' \
  --command="
    SELECT a.tenant_id::text,
           r.repository_id::text,
           a.attempt_id,
           a.run_id,
	           a.ordinal,
	           encode(convert_to(a.status, 'UTF8'), 'hex'),
	           encode(convert_to(a.lease_resource_type, 'UTF8'), 'hex'),
	           encode(convert_to(a.lease_resource_id, 'UTF8'), 'hex'),
	           encode(convert_to(a.worker_id, 'UTF8'), 'hex'),
           a.fencing_token,
           a.version,
           (extract(epoch FROM a.created_at) * 1000000)::bigint,
           (extract(epoch FROM a.updated_at) * 1000000)::bigint,
           a.started_at IS NULL,
           a.finished_at IS NULL
    FROM forja.attempts AS a
    JOIN forja.runs AS r
      ON r.tenant_id=a.tenant_id AND r.run_id=a.run_id
    ORDER BY a.tenant_id, r.repository_id, a.attempt_id;
  " >"$work/attempts.tsv"

psql "$database_url" \
  --no-psqlrc \
  --set=ON_ERROR_STOP=1 \
  --tuples-only \
  --no-align \
  --field-separator=$'\t' \
  --command="
    SELECT tenant_id::text,
           repository_id::text,
           aggregate_id,
           aggregate_version,
           event_id,
           event_type,
           (extract(epoch FROM occurred_at) * 1000000)::bigint,
           encode(convert_to(payload::text, 'UTF8'), 'hex')
    FROM forja.events
    WHERE aggregate_type='attempt'
    ORDER BY tenant_id, repository_id, aggregate_id, aggregate_version;
  " >"$work/attempt-events.tsv"

psql "$database_url" \
  --no-psqlrc \
  --set=ON_ERROR_STOP=1 \
  --tuples-only \
  --no-align \
  --field-separator=$'\t' \
  --command="
    SELECT count(*) FILTER (WHERE o.event_id IS NULL),
           count(*) FILTER (
             WHERE o.event_id IS NOT NULL
               AND (o.tenant_id, o.repository_id)
                   IS DISTINCT FROM (e.tenant_id, e.repository_id)
           )
    FROM forja.events AS e
    LEFT JOIN forja.outbox AS o ON o.event_id=e.event_id;
  " >"$work/event-outbox-integrity.tsv"

psql "$database_url" \
  --no-psqlrc \
  --set=ON_ERROR_STOP=1 \
  --tuples-only \
  --no-align \
  --field-separator=$'\t' \
  --command="
    SELECT tenant_id::text,
           repository_id::text,
           aggregate_type,
           aggregate_id,
           event_type,
           encode(convert_to(actor_type, 'UTF8'), 'hex'),
           encode(convert_to(actor_id, 'UTF8'), 'hex'),
           encode(convert_to(COALESCE(causation_id, ''), 'UTF8'), 'hex'),
           encode(convert_to(idempotency_key, 'UTF8'), 'hex'),
           encode(convert_to(payload::text, 'UTF8'), 'hex')
    FROM forja.events
    WHERE aggregate_type IN ('run', 'attempt')
    ORDER BY tenant_id, repository_id, aggregate_type, aggregate_id,
             aggregate_version;
  " >"$work/command-events.tsv"

psql "$database_url" \
  --no-psqlrc \
  --set=ON_ERROR_STOP=1 \
  --tuples-only \
  --no-align \
  --field-separator=$'\t' \
  --command="
    SELECT tenant_id::text,
           encode(convert_to(scope, 'UTF8'), 'hex'),
           encode(convert_to(idempotency_key, 'UTF8'), 'hex'),
           encode(request_hash, 'hex'),
           response_status,
           encode(convert_to(response_body::text, 'UTF8'), 'hex'),
           expires_at IS NULL
    FROM forja.idempotency_keys
    ORDER BY tenant_id, scope, idempotency_key;
  " >"$work/idempotency.tsv"

python3 - \
  "$root/internal/postgres/schema_manifest.json" \
  "$work/tables.tsv" \
  "$work/constraints.txt" \
  "$work/indexes.txt" \
  "$work/triggers.txt" \
  "$work/trigger.tsv" \
  "$work/run-events.tsv" \
  "$work/runs.tsv" \
  "$work/attempts.tsv" \
  "$work/attempt-events.tsv" \
  "$work/event-outbox-integrity.tsv" \
  "$work/command-events.tsv" \
  "$work/idempotency.tsv" \
  "$authority" <<'PY'
import datetime
import hashlib
import json
import pathlib
import re
import sys

(
    manifest_path,
    tables_path,
    constraints_path,
    indexes_path,
    triggers_path,
    trigger_path,
    run_events_path,
    runs_path,
    attempts_path,
    attempt_events_path,
    event_outbox_integrity_path,
    command_events_path,
    idempotency_path,
    authority,
) = sys.argv[1:]
manifest = json.loads(pathlib.Path(manifest_path).read_text())

tables = {}
for line in pathlib.Path(tables_path).read_text().splitlines():
    table, signature = line.split("\t", 1)
    tables[table] = signature
if tables != manifest["tables"]:
    missing = sorted(set(manifest["tables"]) - set(tables))
    extra = sorted(set(tables) - set(manifest["tables"]))
    drifted = sorted(
        table
        for table in set(tables) & set(manifest["tables"])
        if tables[table] != manifest["tables"][table]
    )
    raise SystemExit(
        f"canonical table signatures differ: missing={missing} "
        f"extra={extra} drifted={drifted}"
    )

def file_sha256(path):
    return hashlib.sha256(pathlib.Path(path).read_bytes()).hexdigest()


if file_sha256(constraints_path) != manifest["constraints_sha256"]:
    raise SystemExit("canonical constraints differ from the release manifest")
if file_sha256(indexes_path) != manifest["indexes_sha256"]:
    raise SystemExit("canonical indexes differ from the release manifest")
if file_sha256(triggers_path) != manifest["triggers_sha256"]:
    raise SystemExit("canonical trigger set differs from the release manifest")

trigger_lines = pathlib.Path(trigger_path).read_text().splitlines()
expected_trigger = manifest["trigger"]
expected_line = "\t".join(
    [
        expected_trigger["relation"],
        expected_trigger["name"],
        expected_trigger["enabled"],
        expected_trigger["function"],
        str(expected_trigger["trigger_type"]),
        expected_trigger["function_language"],
        "t" if expected_trigger["function_security_definer"] else "f",
        expected_trigger["function_volatility"],
    ]
)
if len(trigger_lines) != 1:
    raise SystemExit(
        f"append-only trigger count differs: got={trigger_lines!r}"
    )
trigger_parts = trigger_lines[0].split("\t")
actual_line = "\t".join(trigger_parts[:-2])
source_hash = hashlib.sha256(bytes.fromhex(trigger_parts[-2])).hexdigest()
definition = bytes.fromhex(trigger_parts[-1]).decode()
if (
    actual_line != expected_line
    or source_hash != expected_trigger["function_source_sha256"]
    or definition != expected_trigger["definition"]
):
    raise SystemExit("append-only trigger or function behavior differs")
if authority != "t":
    raise SystemExit("default tenant/repository authority is missing")

states = {
    "draft",
    "awaiting_approval",
    "queued",
    "preparing",
    "running",
    "validating",
    "awaiting_decision",
    "completed",
    "cancelling",
    "cancelled",
    "failed_retryable",
    "failed_terminal",
}
transitions = {
    "draft": {"awaiting_approval", "cancelling"},
    "awaiting_approval": {"queued", "cancelling"},
    "queued": {"preparing", "cancelling"},
    "preparing": {
        "running",
        "cancelling",
        "failed_retryable",
        "failed_terminal",
    },
    "running": {
        "validating",
        "cancelling",
        "failed_retryable",
        "failed_terminal",
    },
    "validating": {
        "awaiting_decision",
        "completed",
        "cancelling",
        "failed_retryable",
        "failed_terminal",
    },
    "awaiting_decision": {
        "running",
        "completed",
        "cancelling",
        "failed_terminal",
    },
    "failed_retryable": {"queued", "cancelling"},
    "cancelling": {"cancelled"},
}
run_id_pattern = re.compile(
    r"^run_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-"
    r"[89ab][0-9a-f]{3}-[0-9a-f]{12}$"
)
attempt_id_pattern = re.compile(r"^attempt_[A-Za-z0-9_-]+$")
required_run_keys = {
    "run_id",
    "schema_version",
    "objective",
    "state",
    "version",
    "created_at",
    "updated_at",
}


def parse_utc(value):
    if not isinstance(value, str):
        raise ValueError("timestamp is not a string")
    parsed = datetime.datetime.fromisoformat(value.replace("Z", "+00:00"))
    if parsed.tzinfo is None or parsed.utcoffset() != datetime.timedelta(0):
        raise ValueError("timestamp is not normalized to UTC")
    return parsed


def canonical_json(value):
    return json.dumps(
        value,
        ensure_ascii=False,
        allow_nan=False,
        sort_keys=True,
        separators=(",", ":"),
    )


previous_by_stream = {}
for line in pathlib.Path(run_events_path).read_text().splitlines():
    (
        tenant_id,
        repository_id,
        aggregate_id,
        aggregate_version,
        event_id,
        event_type,
        occurred_us,
        payload_hex,
    ) = line.split("\t", 7)
    version = int(aggregate_version)
    run = json.loads(bytes.fromhex(payload_hex).decode())
    if not isinstance(run, dict) or set(run) != required_run_keys:
        raise SystemExit(f"run event {event_id} has an invalid payload contract")
    if (
        not isinstance(run["run_id"], str)
        or not run_id_pattern.fullmatch(run["run_id"])
        or run["run_id"] != aggregate_id
        or run["schema_version"] != "1.0"
        or not isinstance(run["objective"], str)
        or not 3 <= len(run["objective"]) <= 8000
        or not isinstance(run["state"], str)
        or run["state"] not in states
        or type(run["version"]) is not int
        or run["version"] != version
    ):
        raise SystemExit(f"run event {event_id} disagrees with its envelope")
    try:
        created_at = parse_utc(run["created_at"])
        updated_at = parse_utc(run["updated_at"])
    except (TypeError, ValueError) as error:
        raise SystemExit(f"run event {event_id} has invalid timestamps: {error}")
    if updated_at < created_at:
        raise SystemExit(f"run event {event_id} reverses run time")
    epoch = datetime.datetime(1970, 1, 1, tzinfo=datetime.timezone.utc)
    elapsed = updated_at - epoch
    updated_us = (
        elapsed.days * 86400 * 1000000
        + elapsed.seconds * 1000000
        + elapsed.microseconds
    )
    if updated_us != int(occurred_us):
        raise SystemExit(f"run event {event_id} occurrence time differs from payload")
    stream = (tenant_id, repository_id, aggregate_id)
    previous = previous_by_stream.get(stream)
    if previous is None:
        if (
            version != 1
            or event_type != "run.created"
            or run["state"] != "draft"
            or created_at != updated_at
        ):
            raise SystemExit(f"run stream {aggregate_id} has invalid creation")
    else:
        if (
            version != previous["version"] + 1
            or event_type != "run.transitioned"
            or run["objective"] != previous["objective"]
            or created_at != previous["created_at"]
            or updated_at < previous["updated_at"]
            or run["state"] not in transitions.get(previous["state"], set())
        ):
            raise SystemExit(f"run stream {aggregate_id} has an invalid transition")
    previous_by_stream[stream] = {
        "version": version,
        "objective": run["objective"],
        "state": run["state"],
        "created_at": created_at,
        "updated_at": updated_at,
    }

canonical_runs = {}
for line in pathlib.Path(runs_path).read_text().splitlines():
    (
        tenant_id,
        repository_id,
        run_id,
        objective_hex,
        state,
        version,
        created_us,
        updated_us,
    ) = line.split("\t", 7)
    canonical_runs[(tenant_id, repository_id, run_id)] = {
        "version": int(version),
        "objective": bytes.fromhex(objective_hex).decode(),
        "state": state,
        "created_us": int(created_us),
        "updated_us": int(updated_us),
    }

if set(canonical_runs) != set(previous_by_stream):
    raise SystemExit("canonical runs and replayable run streams differ")
for stream, replayed in previous_by_stream.items():
    canonical = canonical_runs[stream]
    epoch = datetime.datetime(1970, 1, 1, tzinfo=datetime.timezone.utc)
    created_elapsed = replayed["created_at"] - epoch
    updated_elapsed = replayed["updated_at"] - epoch
    replayed_created_us = (
        created_elapsed.days * 86400 * 1000000
        + created_elapsed.seconds * 1000000
        + created_elapsed.microseconds
    )
    replayed_updated_us = (
        updated_elapsed.days * 86400 * 1000000
        + updated_elapsed.seconds * 1000000
        + updated_elapsed.microseconds
    )
    if canonical != {
        "version": replayed["version"],
        "objective": replayed["objective"],
        "state": replayed["state"],
        "created_us": replayed_created_us,
        "updated_us": replayed_updated_us,
    }:
        raise SystemExit(f"canonical run {stream[2]} differs from event replay")

attempts = {}
for line in pathlib.Path(attempts_path).read_text().splitlines():
    (
        tenant_id,
        repository_id,
        attempt_id,
        run_id,
        ordinal,
        status_hex,
        lease_resource_type_hex,
        lease_resource_id_hex,
        worker_id_hex,
        fencing_token,
        version,
        created_us,
        updated_us,
        started_is_null,
        finished_is_null,
    ) = line.split("\t", 14)
    if started_is_null != "t" or finished_is_null != "t":
        raise SystemExit(f"attempt {attempt_id} has unsupported execution timestamps")
    if created_us != updated_us:
        raise SystemExit(f"attempt {attempt_id} has unsupported update history")
    attempts[(tenant_id, repository_id, attempt_id)] = {
        "attempt_id": attempt_id,
        "run_id": run_id,
        "ordinal": int(ordinal),
        "status": bytes.fromhex(status_hex).decode(),
        "lease_resource_type": bytes.fromhex(lease_resource_type_hex).decode(),
        "lease_resource_id": bytes.fromhex(lease_resource_id_hex).decode(),
        "worker_id": bytes.fromhex(worker_id_hex).decode(),
        "fencing_token": int(fencing_token),
        "version": int(version),
        "created_us": int(created_us),
    }

attempt_events = {}
for line in pathlib.Path(attempt_events_path).read_text().splitlines():
    (
        tenant_id,
        repository_id,
        aggregate_id,
        aggregate_version,
        event_id,
        event_type,
        occurred_us,
        payload_hex,
    ) = line.split("\t", 7)
    key = (tenant_id, repository_id, aggregate_id)
    if key in attempt_events:
        raise SystemExit(f"attempt {aggregate_id} has more than one creation event")
    payload = json.loads(bytes.fromhex(payload_hex).decode())
    if (
        event_type != "attempt.created"
        or int(aggregate_version) != 1
        or not isinstance(payload, dict)
        or set(payload) != {
            "attempt_id", "run_id", "ordinal", "status", "lease_resource_type",
            "lease_resource_id", "worker_id", "fencing_token", "version",
            "created_at"
        }
        or not isinstance(payload["attempt_id"], str)
        or not attempt_id_pattern.fullmatch(payload["attempt_id"])
        or payload["attempt_id"] != aggregate_id
        or not isinstance(payload["run_id"], str)
        or not run_id_pattern.fullmatch(payload["run_id"])
        or type(payload["ordinal"]) is not int
        or payload["ordinal"] < 1
        or not isinstance(payload["status"], str)
        or not 1 <= len(payload["status"]) <= 100
        or payload["lease_resource_type"] != "scheduler"
        or not isinstance(payload["lease_resource_id"], str)
        or not 1 <= len(payload["lease_resource_id"]) <= 500
        or not isinstance(payload["worker_id"], str)
        or not 1 <= len(payload["worker_id"]) <= 500
        or type(payload["fencing_token"]) is not int
        or payload["fencing_token"] < 1
        or type(payload["version"]) is not int
        or payload["version"] != 1
    ):
        raise SystemExit(f"attempt event {event_id} has an invalid contract")
    try:
        created = parse_utc(payload["created_at"])
    except (TypeError, ValueError) as error:
        raise SystemExit(
            f"attempt event {event_id} has an invalid timestamp: {error}"
        )
    elapsed = created - datetime.datetime(
        1970, 1, 1, tzinfo=datetime.timezone.utc
    )
    payload_created_us = (
        elapsed.days * 86400 * 1000000
        + elapsed.seconds * 1000000
        + elapsed.microseconds
    )
    attempt_events[key] = {
        "attempt_id": payload["attempt_id"],
        "run_id": payload["run_id"],
        "ordinal": payload["ordinal"],
        "status": payload["status"],
        "lease_resource_type": payload["lease_resource_type"],
        "lease_resource_id": payload["lease_resource_id"],
        "worker_id": payload["worker_id"],
        "fencing_token": payload["fencing_token"],
        "version": payload["version"],
        "created_us": payload_created_us,
    }
    if payload_created_us != int(occurred_us):
        raise SystemExit(f"attempt event {event_id} occurrence time differs")

if attempts != attempt_events:
    raise SystemExit("canonical attempts and attempt creation events differ")

outbox_parts = pathlib.Path(event_outbox_integrity_path).read_text().strip().split("\t")
if outbox_parts != ["0", "0"]:
    raise SystemExit(
        "every canonical event must have exactly one matching outbox row: "
        f"observed={outbox_parts}"
    )

expected_receipts = {}
for line in pathlib.Path(command_events_path).read_text().splitlines():
    (
        tenant_id,
        repository_id,
        aggregate_type,
        aggregate_id,
        event_type,
        actor_type_hex,
        actor_id_hex,
        causation_hex,
        idempotency_key_hex,
        payload_hex,
    ) = line.split("\t", 9)
    actor_type = bytes.fromhex(actor_type_hex).decode()
    actor_id = bytes.fromhex(actor_id_hex).decode()
    causation = bytes.fromhex(causation_hex).decode()
    idempotency_key = bytes.fromhex(idempotency_key_hex).decode()
    payload = json.loads(bytes.fromhex(payload_hex).decode())
    if aggregate_type == "run" and event_type == "run.created":
        scope = f"create_run:{repository_id}"
        status = 201
        parts = ["create_run", repository_id, payload["objective"]]
    elif aggregate_type == "run" and event_type == "run.transitioned":
        scope = f"transition_run:{repository_id}:{aggregate_id}"
        status = 200
        parts = [
            "transition_run",
            aggregate_id,
            str(payload["version"] - 1),
            payload["state"],
        ]
    elif aggregate_type == "attempt" and event_type == "attempt.created":
        scope = f"create_attempt:{repository_id}:{payload['run_id']}"
        status = 201
        parts = [
            scope,
            payload["status"],
            payload["lease_resource_type"],
            payload["lease_resource_id"],
            payload["worker_id"],
            str(payload["fencing_token"]),
        ]
    else:
        raise SystemExit(
            f"unsupported canonical command event {aggregate_type}/{event_type}"
        )
    request_hash = hashlib.sha256(
        "\0".join(parts + [actor_type, actor_id, causation]).encode()
    ).hexdigest()
    key = (tenant_id, scope, idempotency_key)
    if key in expected_receipts:
        raise SystemExit(f"duplicate command receipt identity: {key}")
    expected_receipts[key] = {
        "request_hash": request_hash,
        "status": status,
        "response": canonical_json(payload),
    }

receipts = {}
for line in pathlib.Path(idempotency_path).read_text().splitlines():
    (
        tenant_id,
        scope_hex,
        idempotency_key_hex,
        request_hash,
        status,
        response_hex,
        expires_is_null,
    ) = line.split("\t", 6)
    if expires_is_null != "t":
        raise SystemExit("release receipts must not expire")
    key = (
        tenant_id,
        bytes.fromhex(scope_hex).decode(),
        bytes.fromhex(idempotency_key_hex).decode(),
    )
    receipts[key] = {
        "request_hash": request_hash,
        "status": int(status),
        "response": canonical_json(
            json.loads(bytes.fromhex(response_hex).decode())
        ),
    }

if receipts != expected_receipts:
    raise SystemExit(
        "idempotency receipts differ from canonical command events"
    )
PY

echo "PostgreSQL schema, canonical state, event streams, and outbox verified"
