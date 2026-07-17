# PostgreSQL Recovery

Status: Implemented in Sprint 02

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
scripts/postgres_backup.sh \
  "$FORJA_DATABASE_URL" \
  /secure/backups/forja-2026-07-16.dump
```

The script does not print the connection URL and refuses an existing
destination, so a known-good recovery point cannot be silently overwritten.

## Restore Drill

Restore into a new, empty staging database:

```bash
scripts/postgres_restore.sh \
  "$FORJA_RESTORE_DATABASE_URL" \
  /secure/backups/forja-2026-07-16.dump
```

The restore command refuses any target containing user relations. It restores
the archive as one transaction without destructive cleanup, requires exact
version, name, and checksum parity with the release migration files, verifies
all canonical column signatures, defaults, identities, constraints, and indexes
against the same manifest embedded by the runtime, verifies the complete
trigger set and every trigger function body, verifies bootstrap authority, and
semantically replays run event streams. Replay rejects gaps, invalid payloads,
envelope mismatches, changed immutable fields, timestamp drift, and illegal FSM
transitions. It also requires replayed runs to equal canonical rows, every event
to have its matching outbox message, every attempt to equal its complete
fencing-authorized creation event, and every idempotency receipt to match the
recomputed command fingerprint, response, and status. A failed validation can
therefore affect only a disposable staging target, never replace a populated
database.

The same non-destructive verification can be run independently:

```bash
scripts/postgres_verify.sh "$FORJA_RESTORE_DATABASE_URL"
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
release.

## Automated Drill

The integration suite uses a disposable database:

```bash
export FORJA_TEST_DATABASE_URL='postgres:///forja_test?host=/tmp'
make test-integration
```

It verifies clean migration, idempotent migration, unknown-version rejection,
rollback and re-upgrade, backup/restore, migration-ledger tampering, structural
and semantic event corruption, durable process restart, fenced command replay,
tenant and repository isolation, semantic schema drift, atomic attempt events,
safe projection watermarks, and non-overlapping outbox claims.
