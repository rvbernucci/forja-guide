#!/usr/bin/env bash
set -euo pipefail

if [[ "$#" -ne 0 ]]; then
  echo "usage: FORJA_DATABASE_URL=<database-url> postgres_verify.sh" >&2
  exit 2
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$root/scripts/postgres_connection.sh"
forja_prepare_postgres_connection
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

psql "$FORJA_PG_SAFE_URL" \
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

psql "$FORJA_PG_SAFE_URL" \
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

psql "$FORJA_PG_SAFE_URL" \
  --no-psqlrc \
  --set=ON_ERROR_STOP=1 \
  --tuples-only \
  --no-align \
  --field-separator=$'\t' \
  --command="
    -- Nullability is already part of the table signature. PostgreSQL 18 also
    -- exposes NOT NULL constraints here, unlike earlier supported releases.
    SELECT c.conrelid::regclass::text || ':' || c.conname || ':' || c.contype::text,
           pg_get_constraintdef(c.oid, true)
    FROM pg_constraint AS c
    JOIN pg_namespace AS n ON n.oid=c.connamespace
    WHERE n.nspname='forja' AND c.contype <> 'n'::\"char\"
    ORDER BY 1;
  " >"$work/constraints.txt"

psql "$FORJA_PG_SAFE_URL" \
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

psql "$FORJA_PG_SAFE_URL" \
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

psql "$FORJA_PG_SAFE_URL" \
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
  psql "$FORJA_PG_SAFE_URL" \
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

psql "$FORJA_PG_SAFE_URL" \
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

psql "$FORJA_PG_SAFE_URL" \
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

psql "$FORJA_PG_SAFE_URL" \
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
	           COALESCE((extract(epoch FROM a.started_at) * 1000000)::bigint, -1),
	           COALESCE((extract(epoch FROM a.finished_at) * 1000000)::bigint, -1)
    FROM forja.attempts AS a
    JOIN forja.runs AS r
      ON r.tenant_id=a.tenant_id AND r.run_id=a.run_id
    ORDER BY a.tenant_id, r.repository_id, a.attempt_id;
  " >"$work/attempts.tsv"

psql "$FORJA_PG_SAFE_URL" \
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

psql "$FORJA_PG_SAFE_URL" \
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

psql "$FORJA_PG_SAFE_URL" \
  --no-psqlrc \
  --set=ON_ERROR_STOP=1 \
  --tuples-only \
  --no-align \
  --field-separator=$'\t' \
  --command="
    SELECT e.tenant_id::text,
           e.repository_id::text,
           e.aggregate_type,
           e.aggregate_id,
           e.aggregate_version,
           e.event_id,
           o.outbox_id,
           e.event_type,
           (extract(epoch FROM e.occurred_at) * 1000000)::bigint,
           encode(convert_to(e.actor_type, 'UTF8'), 'hex'),
           encode(convert_to(e.actor_id, 'UTF8'), 'hex'),
           encode(convert_to(e.correlation_id, 'UTF8'), 'hex'),
           encode(convert_to(COALESCE(e.causation_id, ''), 'UTF8'), 'hex'),
           encode(convert_to(e.idempotency_key, 'UTF8'), 'hex'),
           encode(convert_to(e.payload::text, 'UTF8'), 'hex')
    FROM forja.events AS e
    JOIN forja.outbox AS o ON o.event_id=e.event_id
    WHERE e.aggregate_type IN (
      'run', 'attempt', 'sprint', 'decision', 'approval', 'audit', 'projection',
      'artifact', 'artifact_operation', 'artifact_manifest', 'conversation',
      'message', 'memory_candidate', 'memory'
    )
    ORDER BY o.outbox_id;
  " >"$work/command-events.tsv"

psql "$FORJA_PG_SAFE_URL" \
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

psql "$FORJA_PG_SAFE_URL" \
  --no-psqlrc \
  --set=ON_ERROR_STOP=1 \
  --tuples-only \
  --no-align \
  --field-separator=$'\t' \
  --command="
    SELECT sp.tenant_id::text,
           sp.repository_id::text,
           'sprint_' || sp.sprint_id::text,
           sp.sequence_number,
           encode(convert_to(sp.title, 'UTF8'), 'hex'),
           encode(convert_to(sp.objective, 'UTF8'), 'hex'),
           sp.status,
           sp.version,
           sp.run_id,
           encode(convert_to(COALESCE((
             SELECT d.decision_id
             FROM forja.decisions AS d
             WHERE d.tenant_id=sp.tenant_id
               AND d.repository_id=sp.repository_id
               AND d.sprint_id=sp.sprint_id
               AND d.status='pending'
             ORDER BY d.created_at, d.decision_id
             LIMIT 1
           ), ''), 'UTF8'), 'hex'),
           (extract(epoch FROM sp.created_at) * 1000000)::bigint,
           (extract(epoch FROM sp.updated_at) * 1000000)::bigint
    FROM forja.sprints AS sp
    ORDER BY sp.tenant_id, sp.repository_id, sp.sprint_id;
  " >"$work/sprints.tsv"

psql "$FORJA_PG_SAFE_URL" \
  --no-psqlrc \
  --set=ON_ERROR_STOP=1 \
  --tuples-only \
  --no-align \
  --field-separator=$'\t' \
  --command="
    SELECT d.tenant_id::text,
           d.repository_id::text,
           d.decision_id,
           'sprint_' || d.sprint_id::text,
           d.run_id,
           encode(convert_to(d.action, 'UTF8'), 'hex'),
           encode(convert_to(d.risk_class, 'UTF8'), 'hex'),
           d.status,
           d.version,
           encode(convert_to(d.requested_by, 'UTF8'), 'hex'),
           encode(convert_to(COALESCE(d.decided_by, ''), 'UTF8'), 'hex'),
           encode(convert_to(COALESCE(d.reason, ''), 'UTF8'), 'hex'),
           (extract(epoch FROM d.created_at) * 1000000)::bigint,
           (extract(epoch FROM d.updated_at) * 1000000)::bigint,
           COALESCE((extract(epoch FROM d.decided_at) * 1000000)::bigint, -1)
    FROM forja.decisions AS d
    ORDER BY d.tenant_id, d.repository_id, d.decision_id;
  " >"$work/decisions.tsv"

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
        "queued",
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
        started_us,
        finished_us,
    ) = line.split("\t", 14)
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
        "updated_us": int(updated_us),
        "started_us": int(started_us),
        "finished_us": int(finished_us),
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
    payload = json.loads(bytes.fromhex(payload_hex).decode())

    if event_type == "attempt.created":
        if key in attempt_events or int(aggregate_version) != 1:
            raise SystemExit(f"attempt {aggregate_id} has an invalid creation event")
        attempt = payload
        required = {
            "attempt_id", "run_id", "ordinal", "status", "lease_resource_type",
            "lease_resource_id", "worker_id", "fencing_token", "version",
            "created_at"
        }
    else:
        if key not in attempt_events or not isinstance(payload, dict):
            raise SystemExit(f"attempt event {event_id} has no creation predecessor")
        if event_type in {"attempt.started", "attempt.finished"}:
            required_payload = {"attempt"}
            if event_type == "attempt.finished":
                required_payload.add("result")
            if set(payload) != required_payload:
                raise SystemExit(f"attempt event {event_id} has an invalid envelope")
        elif event_type == "attempt.reconciled":
            if set(payload) != {"attempt", "reconciled_by"}:
                raise SystemExit(f"attempt event {event_id} has an invalid envelope")
            authority = payload["reconciled_by"]
            if not isinstance(authority, dict) or set(authority) != {
                "tenant_id", "repository_id", "resource_type", "resource_id",
                "owner_id", "fencing_token"
            } or authority["resource_type"] != "scheduler" \
               or authority["tenant_id"] != tenant_id \
               or authority["repository_id"] != repository_id \
               or type(authority["fencing_token"]) is not int \
               or authority["fencing_token"] < 1:
                raise SystemExit(f"attempt event {event_id} has invalid recovery authority")
        else:
            raise SystemExit(f"attempt event {event_id} has unsupported type {event_type}")
        attempt = payload["attempt"]
        required = {
            "attempt_id", "run_id", "ordinal", "status", "lease_resource_type",
            "lease_resource_id", "worker_id", "fencing_token", "version",
            "created_at", "updated_at"
        }
        if attempt.get("started_at") is not None:
            required.add("started_at")
        if attempt.get("finished_at") is not None:
            required.add("finished_at")

    if (
        not isinstance(attempt, dict)
        or set(attempt) != required
        or not isinstance(attempt.get("attempt_id"), str)
        or not attempt_id_pattern.fullmatch(attempt["attempt_id"])
        or attempt["attempt_id"] != aggregate_id
        or not isinstance(attempt.get("run_id"), str)
        or not run_id_pattern.fullmatch(attempt["run_id"])
        or type(attempt.get("ordinal")) is not int
        or attempt["ordinal"] < 1
        or not isinstance(attempt.get("status"), str)
        or not 1 <= len(attempt["status"]) <= 100
        or attempt.get("lease_resource_type") != "scheduler"
        or not isinstance(attempt.get("lease_resource_id"), str)
        or not 1 <= len(attempt["lease_resource_id"]) <= 500
        or not isinstance(attempt.get("worker_id"), str)
        or not 1 <= len(attempt["worker_id"]) <= 500
        or type(attempt.get("fencing_token")) is not int
        or attempt["fencing_token"] < 1
        or type(attempt.get("version")) is not int
        or attempt["version"] != int(aggregate_version)
    ):
        raise SystemExit(f"attempt event {event_id} has an invalid contract")
    try:
        created = parse_utc(attempt["created_at"])
        updated = parse_utc(attempt.get("updated_at", attempt["created_at"]))
        started = parse_utc(attempt["started_at"]) if "started_at" in attempt else None
        finished = parse_utc(attempt["finished_at"]) if "finished_at" in attempt else None
    except (TypeError, ValueError) as error:
        raise SystemExit(
            f"attempt event {event_id} has an invalid timestamp: {error}"
        )
    epoch = datetime.datetime(1970, 1, 1, tzinfo=datetime.timezone.utc)
    def micros(value):
        if value is None:
            return -1
        elapsed = value - epoch
        return elapsed.days * 86400 * 1000000 + elapsed.seconds * 1000000 + elapsed.microseconds

    payload_created_us = micros(created)
    payload_updated_us = micros(updated)
    previous = attempt_events.get(key)
    if previous is not None:
        if attempt["version"] != previous["version"] + 1:
            raise SystemExit(f"attempt event {event_id} skips an aggregate version")
        immutable = {
            "attempt_id", "run_id", "ordinal", "lease_resource_type",
            "lease_resource_id", "worker_id", "fencing_token", "created_us"
        }
        current_immutable = {
            "attempt_id": attempt["attempt_id"], "run_id": attempt["run_id"],
            "ordinal": attempt["ordinal"],
            "lease_resource_type": attempt["lease_resource_type"],
            "lease_resource_id": attempt["lease_resource_id"],
            "worker_id": attempt["worker_id"],
            "fencing_token": attempt["fencing_token"],
            "created_us": payload_created_us,
        }
        if any(previous[field] != current_immutable[field] for field in immutable):
            raise SystemExit(f"attempt event {event_id} mutates immutable authority")
        if event_type == "attempt.started" and not (
            previous["status"] == "queued" and attempt["status"] == "running"
            and started is not None and finished is None
        ):
            raise SystemExit(f"attempt event {event_id} has an invalid start transition")
        if event_type == "attempt.finished" and not (
            previous["status"] == "running"
            and attempt["status"] in {"succeeded", "blocked", "failed_retryable", "failed_terminal", "cancelled"}
            and finished is not None
        ):
            raise SystemExit(f"attempt event {event_id} has an invalid finish transition")
        if event_type == "attempt.reconciled" and not (
            previous["status"] in {"queued", "running"}
            and attempt["status"] == "failed_retryable" and finished is not None
        ):
            raise SystemExit(f"attempt event {event_id} has an invalid recovery transition")
        if event_type == "attempt.finished":
            result = payload["result"]
            expected_result_fields = {
                "task_id", "adapter", "status", "retryable", "termination_reason",
                "started_at", "finished_at", "duration_ms", "exit_code",
                "stdout_sha256", "stderr_sha256", "report_sha256", "usage",
                "evidence_refs"
            }
            if not isinstance(result, dict) or set(result) != expected_result_fields \
               or result["status"] != attempt["status"]:
                raise SystemExit(f"attempt event {event_id} has an invalid safe result")
            if not re.fullmatch(r"[0-9a-f]{64}", result["report_sha256"]):
                raise SystemExit(f"attempt event {event_id} has an invalid report hash")

    attempt_events[key] = {
        "attempt_id": attempt["attempt_id"],
        "run_id": attempt["run_id"],
        "ordinal": attempt["ordinal"],
        "status": attempt["status"],
        "lease_resource_type": attempt["lease_resource_type"],
        "lease_resource_id": attempt["lease_resource_id"],
        "worker_id": attempt["worker_id"],
        "fencing_token": attempt["fencing_token"],
        "version": attempt["version"],
        "created_us": payload_created_us,
        "updated_us": payload_updated_us,
        "started_us": micros(started),
        "finished_us": micros(finished),
    }
    if payload_updated_us != int(occurred_us):
        raise SystemExit(f"attempt event {event_id} occurrence time differs")

if attempts != attempt_events:
    raise SystemExit("canonical attempts and replayed attempt streams differ")

outbox_parts = pathlib.Path(event_outbox_integrity_path).read_text().strip().split("\t")
if outbox_parts != ["0", "0"]:
    raise SystemExit(
        "every canonical event must have exactly one matching outbox row: "
        f"observed={outbox_parts}"
    )
PY

python3 "$root/scripts/verify_command_receipts.py" \
  "$work/command-events.tsv" \
  "$work/idempotency.tsv"

python3 "$root/scripts/verify_governed_state.py" \
  "$work/command-events.tsv" \
  "$work/sprints.tsv" \
  "$work/decisions.tsv"

echo "PostgreSQL schema, canonical state, event streams, outbox, and receipts verified"
