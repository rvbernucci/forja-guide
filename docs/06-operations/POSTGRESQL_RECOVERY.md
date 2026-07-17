# PostgreSQL Recovery

Status: Implemented in Sprint 02; governed command recovery extended in Sprint 03

PostgreSQL is Forja's canonical operational authority. A recovery is complete
only when the database restores and its event streams pass continuity checks.
Qdrant and Neo4j are derived stores and are rebuilt later from the outbox.

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
drift, and illegal FSM transitions. It also requires replayed runs to equal
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
receipt/audit corruption, and non-overlapping outbox claims.
