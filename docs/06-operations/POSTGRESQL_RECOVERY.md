# PostgreSQL Recovery

Status: Implemented in Sprint 02; governed command and delivery recovery
extended through Sprint 07

PostgreSQL is Forja's canonical operational authority. A recovery is complete
only when the database restores and its event streams pass continuity checks.
Qdrant and Neo4j are derived stores and are rebuilt later from the outbox.
From Sprint 07 onward, a complete recovery point also requires the matching
S3-compatible provider snapshot. See
[Artifact Storage Operations](ARTIFACT_STORAGE_OPERATIONS.md) for the
two-plane backup, restore, reconciliation, retention, and credential-rotation
procedure.

At the execution layer, `Pipeline.Recover` revalidates immutable human request
approval and a live replacement scheduler fence. It never assumes an old
worker process has stopped. Active attempts become recoverable only after the
old fence expires and fenced reconciliation marks them retryable. The
replacement must own the same scheduler resource recorded by the attempt; a
different scheduler resource in the repository cannot adopt it. Terminal
non-success attempts complete idempotent quarantine, lease release, and Run
closure after restart. Succeeded attempts whose Run already became failed
retry the interrupted quarantine/release path without changing that terminal
Run state. A Run already in `cancelling` reaches `cancelled` after process
quiescence and cleanup for every terminal durable Attempt status; replaying the
same recovery after the Run is already `cancelled` preserves that state
idempotently. If takeover finds a retryable Attempt while its
Run is still `preparing`, absence of both worktree and prior quarantine is valid
pre-worker cleanup; the same absence in `running` or later states remains a
fail-closed anomaly. A durable reconciliation marker always overrides that
absence and requires administrative Git reconciliation. For a durably
succeeded attempt without a publication journal, recovery reconstructs the
deterministic commit and loads or regenerates exact validation evidence. Once a
prepared or published journal exists, recovery instead delegates its immutable
receipt and intent to `PublicationService.Recover`; external evidence files are
no longer required because recovery cannot perform a new Git CAS.
Journaled recovery derives the commit identity from the schema-validated
receipt and never runs `commit-tree` without a live delivery fence. Any
detached lease renewal has its own 45-second deadline, and a real renewal error
cannot be hidden by a concurrent heartbeat shutdown. After that detached
delivery renewal, recovery synchronously refreshes the replacement scheduler
fence again before publication may touch its journal or Git ref.
An Attempt that merely reports `cancelled` cannot manufacture cancellation:
without durable `cancelling` or `cancelled` Run authority, recovery quarantines
and releases it as a retryable interruption. A blocked Attempt remains terminal;
governed resume returns its Run to `queued` for a fresh Attempt and immutable
attempt-scoped authorization.
Completed Runs do not bypass that boundary: recovery validates the journaled
authority and canonical receipt, reconstructs the receipt-bound commit,
freshly observes the publication ref, and retries exact lease-set release. The
external verifier also reconstructs
attempt-scoped `delivery.authorized` events, their human provenance, request
digests, governed predecessors, success audits, and idempotency receipts. The
predecessor relation uses the canonical outbox sequence rather than timestamps,
so clock skew cannot invalidate legitimate authority. The verifier also
reapplies the full delivery-request contract instead of trusting a matching
digest alone. Publication is detached from caller cancellation but retains a
dedicated 45-second deadline in both normal and recovery paths.
First journal preparation and Run cancellation serialize on the same locked
Run row. A cancellation cannot cross a `prepared` or `published` journal, and
publication cannot prepare after the Run has entered cancellation.
The same fence rejects every incompatible transition while publication is
`prepared` or `published`; only a published delivery may advance its Run to
`completed`.

## Safety Rules

- Use a dedicated database and least-privilege credentials.
- Keep the connection URL outside shell history where practical.
- Encrypt backup files at rest and control their retention.
- Restore into an isolated database before promoting it.
- Never use the restore script for an in-place production restore.
- Never treat a successful `pg_restore` exit alone as proof of recovery.

## Backup

The backup command writes a temporary file with owner-only permissions,
creates a PostgreSQL custom-format archive, validates its table of contents,
and atomically publishes it without replacing an existing path:

```bash
export FORJA_DATABASE_URL='postgresql://forja@database/forja'
scripts/postgres_backup.sh /secure/backups/forja-2026-07-16.dump
```

The script does not print the connection URL and refuses an existing
destination, so a known-good recovery point cannot be silently overwritten.
The credential-bearing URL is accepted only through the environment. Before a
client starts, any embedded password is separated into `PGPASSWORD`; only the
sanitized, password-free URI can appear in a PostgreSQL client argument.
See [ADR-0006](../05-decisions/ADR-0006-POSTGRES-CREDENTIAL-BOUNDARY.md) for
the threat model and trust-boundary decision.

## Restore Drill

Restore into a new, empty staging database:

```bash
export FORJA_DATABASE_URL='postgresql://forja@staging-database/forja_restore'
scripts/postgres_restore.sh /secure/backups/forja-2026-07-16.dump
```

The restore command uses a schema-only archive to refuse any target containing
dumpable user-defined objects, including functions and event triggers. It
restores the archive as one transaction without destructive cleanup, requires
exact version, name, and checksum parity with the release migration files,
verifies all canonical column signatures, defaults, identities, constraints,
and indexes against the same manifest embedded by the runtime, verifies the
complete trigger set and every trigger function body, verifies bootstrap
authority, and semantically replays run event streams. Replay rejects gaps,
invalid payloads, envelope mismatches, changed immutable fields, timestamp
drift, and illegal FSM transitions. Replay retains the historical Sprint 04
`awaiting_decision -> running` edge only for immutable event compatibility;
new runtime commands must resume through `queued`. It also requires replayed runs to equal
canonical rows, every event to have its matching outbox message, every attempt
to equal its complete fencing-authorized creation event, and every idempotency
receipt to match the recomputed command fingerprint, response, and status. A
governed receipt is reconstructed from its complete multi-aggregate event set,
including the Sprint, Decision, Run, and transactionally committed MCP success
audit. The verifier covers planning, submission, approval, rejection,
cancellation, and resume commands and rejects orphan command events, corrupted
composite responses, altered fingerprints, missing atomic audits, or an attempt
to substitute a later replay audit for the original transaction audit. A
failed validation can therefore affect only a disposable staging target, never
replace a populated database.

Retry audits may carry a new correlation ID because correlation identifies an
individual delivery attempt and is intentionally absent from the idempotency
fingerprint. Recovery still requires the same tenant, repository, command
scope, idempotency key, actor, causation, and tool, and it verifies that every
audit payload agrees with its immutable event envelope.

Sprint 03 also reconstructs every canonical Sprint and decision from its
immutable aggregate stream and compares every field and timestamp with the
restored row. Legacy Sprints receive one deterministic `sprint.migrated`
baseline event during upgrade. The migration fails closed when a legacy Sprint
is linked to multiple Runs; silently selecting one would leave the others able
to bypass governed transition checks. It also rejects `awaiting_approval` and
other legacy approval states that have no governed event evidence, rather than
migrating an aggregate with no decision-based recovery path. A proposed legacy
Sprint is accepted only when its existing linked Run is still `draft`; an
already-advanced Run cannot be submitted through the governed lifecycle and is
therefore rejected instead of being stranded after upgrade.

The same non-destructive verification can be run independently:

```bash
export FORJA_DATABASE_URL='postgresql://forja@staging-database/forja_restore'
scripts/postgres_verify.sh
```

After staging restore:

1. Start `forjad` against the restored database.
2. Check `/healthz`, `/readyz`, and `/version`.
3. Inspect representative runs and their event counts.
4. Run the run projection rebuild.
5. Rebuild external projections from pending outbox events.
6. Promote the staging database through the deployment platform's controlled
   database-switch procedure; never rerun the script against production.
7. Record the archive hash, recovery point, duration, and operator in an
   immutable evidence artifact.

## Migration Rollback

Migrations are embedded in `internal/postgres/migrations` as paired
`*.up.sql` and `*.down.sql` files. `postgres.Migrate` serializes writers with a
transaction-scoped advisory lock. `postgres.RollbackLast` rolls back only the
latest known migration and removes its ledger record in the same transaction.
The ledger stores a SHA-256 checksum over each up/down pair. Startup and
rollback reject modified, gapped, or unknown applied history. This prevents an
older binary from starting against a forward-migrated database.

Before checking Sprint 05 delivery-authorization compatibility, rollback
acquires the same command/event/outbox/lease writer barrier used by incremental
upgrade and holds it through the down migration commit. A concurrent command
writer therefore makes rollback fail closed instead of allowing authorization
history to appear after the compatibility snapshot.
The same preflight rejects the Sprint 05 blocked-run resume edge from
`awaiting_decision` to `queued`, because the authoritative Sprint 04 verifier
understands only its historical transition to `running`. Forward verification
continues accepting both historical and current event shapes, while the current
runtime emits only the safer queued transition.

Production rollbacks should prefer forward repair when a migration has already
served writes. Exercise every down migration in a disposable database before
release. Migration 003 first locks the idempotency command table, which every
command reads before acquiring aggregate locks. Only after active commands
drain does it take the remaining exclusive maintenance locks. This ordering
prevents both parent-table deadlocks and creation of a new decision after the
safety check. It then refuses rollback while any decision is pending. Resolve
or reject every pending decision through the governed
command before retrying; never delete decision rows to force a downgrade. The rollback
removes command receipts whose response bodies depend on Sprint 03 columns,
decision rows, or MCP audit evidence, preventing stale replay after a later
re-upgrade. Generic Sprint 02-compatible transition receipts remain. Legacy Sprints
that require generated Runs during upgrade receive matching `run.created`
events and outbox messages, so projection rebuild remains authoritative.

Incremental forward migration is deliberately not a mixed-version online
operation. Before applying it, stop command intake and projection rebuilds,
allow active transactions to finish, and then run the migration. The migration
first acquires the projection watermark with a bounded lock timeout, then its
complete relation barrier with `NOWAIT`; lock contention fails closed with
PostgreSQL `lock_not_available` (`55P03`). Retry only after confirming the
writer window is quiescent.
Migration 006 additionally requires every lease set to be released before the
upgrade. Stop delivery intake, allow active attempts to finish or cancel them
through the governed path, verify that no `forja.lease_sets` row remains
`active`, and only then retry. The migration deliberately refuses to infer an
original TTL from legacy timestamps.
See [ADR-0007](../05-decisions/ADR-0007-FAIL-FAST-INCREMENTAL-MIGRATIONS.md)
for the authoritative barrier order and writer classes.
Before removing an incompatible governed receipt, the rollback writes an
immutable `idempotency.receipt_invalidated` marker containing its exact command
identity, command scope, command-anchor event, and referenced domain event ID.
Its event and aggregate IDs are recomputed from those fields during recovery.
The command anchor and surviving domain events must also equal the aggregate
version and canonical response payload stored in the receipt. Commands may
therefore reuse actor, correlation, and idempotency values across distinct
scopes without causing one rollback marker to claim another command's event.
Recovery accepts a preserved domain event without a receipt only when one
unambiguous marker names that exact event and proves the deliberate downgrade
action. A receipt consumes only the exact event IDs reconstructed from its
response, never every event sharing reusable metadata. Atomic success
audits also carry `command_scope`, so equal idempotency keys, actors, and
correlations reused legally across different aggregates remain unambiguous.
Receipt-free migration events are accepted only when their event IDs, fixed
migration identity, and deterministic legacy Sprint-to-Run relationship all
recompute exactly; an ordinary caller named `migration-003` receives no
exemption.

Migration 005 is reversible only while `forja.delivery_publications` is empty.
Its down migration first crosses the lease writer barrier and then refuses any
prepared, published, conflicted, or abandoned publication history. If history
exists, stop delivery intake and use forward repair; never delete canonical
receipts merely to run an older schema or binary.

Returning to the authoritative Sprint 04 binary requires reversing migrations
006, 005, and 004 in that order. Reversing only 006 and 005 is insufficient:
Sprint 04 does not contain the lease-set schema introduced by migration 004.
Run `scripts/rehearse_sprint05_rollback.sh` with a disposable
`FORJA_TEST_DATABASE_URL`; it builds the hash-pinned Sprint 04 close commit,
downgrades the database to migration 003, proves that exact daemon reaches
readiness with auto-migration disabled, and then reapplies the current schema.

## Automated Drill

The integration suite uses a disposable database:

```bash
export FORJA_TEST_DATABASE_URL='postgres:///forja_test?host=/tmp'
make test-integration
```

It verifies clean migration, idempotent migration, unknown-version rejection,
pending-decision rollback refusal, safe rollback and re-upgrade, backup/restore,
migration-ledger tampering, structural
and semantic event corruption, durable process restart, fenced command replay,
tenant and repository isolation, semantic schema drift, atomic attempt events,
safe projection watermarks, governed lifecycle backup and restore, governed
receipt/audit corruption, non-overlapping outbox claims, exact Sprint 04 binary
compatibility after a three-migration downgrade, and one approved
Run-to-worker-to-publication execution.
