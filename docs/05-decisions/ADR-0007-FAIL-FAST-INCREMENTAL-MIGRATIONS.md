# ADR-0007: Fail-Fast Incremental Migrations

Status: Accepted

## Context

Incremental migrations can change canonical aggregates and generate immutable
events or outbox records. At the same time, command transactions, standalone
audit writers, and projection rebuilds use those relations and the shared
event watermark.

Updated writers can follow one lock order, but a process from the previous
release cannot retroactively adopt it. Waiting for mixed-version writers is
therefore unsafe: a previous-version command may hold an aggregate while a
migration holds another relation, and a projection rebuild may hold the event
watermark while retaining a pre-migration snapshot. Either overlap can cause a
lock cycle or publish stale projection state.

## Decision

Incremental migration is a quiescent deployment operation, not a
mixed-version online operation.

After validating a non-empty migration prefix and while holding the migration
advisory lock, the migrator:

1. acquires the shared event/outbox watermark advisory lock with a bounded
   PostgreSQL lock timeout;
2. resets the timeout after acquiring the watermark; and
3. acquires `ACCESS EXCLUSIVE` locks on `idempotency_keys`, `sprints`, `runs`,
   `events`, and `outbox` in one `NOWAIT` statement.

Updated command transactions acquire an `ACCESS SHARE` lock on
`idempotency_keys` before their command advisory lock and before any aggregate
lock. Canonical event writers and projection rebuilds acquire the shared
watermark before touching event or outbox state. These compatible paths wait
outside aggregate critical sections once a migration owns the barrier.

Any active previous-version writer, current command, audit writer, event
writer, or projection rebuild causes migration to fail closed with PostgreSQL
`lock_not_available` (`55P03`). The failed transaction is rolled back. The
deployment must stop command intake and rebuilds, drain active transactions,
and retry; it must not loop while the previous release still accepts work.

The relation set is part of the migration protocol. A future canonical writer
or migration-sensitive relation requires this ADR, the barrier, and adversarial
concurrency tests to be updated together.

## Consequences

Positive:

- migrations cannot wait into mixed-version lock cycles;
- projection rebuilds cannot publish snapshots that cross a migration;
- updated writers stop before aggregate locks while migration is active;
- contention has an explicit, observable, retryable failure mode.

Negative:

- incremental upgrades require a short writer outage;
- automatic startup migration can fail until the deployment is quiescent;
- every new canonical writer must participate in the shared protocol.

## Guardrail

Integration tests must prove fail-fast behavior for previous-version writers
and projection watermark holders, ordering for current command and audit
writers, safe retry after drain, and semantic schema validity after migration.

