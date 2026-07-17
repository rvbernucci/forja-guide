# Changelog

## Unreleased

- Serialize incremental migrations against command and standalone audit writers
  before any migration-generated outbox records can be allocated.
- Acquire the command-side migration barrier before aggregate locks, including
  the lease-fenced attempt path, to prevent lock-order inversion.
- Fail incremental migration fast when previous-version writers or projection
  rebuilds are active, requiring deployment quiescence instead of risking a
  mixed-version lock cycle.
- Accept only contract-valid, identity-bound Sprint cancellation evidence from
  the generic Run transition path during receipt and recovery verification.

All notable public changes to Forja Guide are recorded here.

The project follows semantic versioning for published planning and
implementation releases.

## [Unreleased]

### Added

- Experimental `forjad` daemon and `forja` CLI.
- Embedded JSON Schema validation at runtime boundaries.
- Typed run IDs, deterministic clocks, stable error codes, and in-memory FSM.
- Structured JSON logging with automatic secret redaction.
- Graceful shutdown, contract fixtures, fuzz seeds, race tests, and
  reproducible Linux cross-build checks.
- PostgreSQL migrations for canonical planning, run, attempt, event, lease,
  idempotency, outbox, checkpoint, dead-letter, and projection state.
- Atomic run commands with optimistic concurrency and replay-safe responses.
- Fenced worker, scheduler, file, and worktree leases.
- Competing transactional outbox claims with `SKIP LOCKED`.
- Deterministic run projection rebuild and corruption detection.
- Validated PostgreSQL backup/restore and kill/restart acceptance tooling.
- Exact schema readiness, migration drift/forward-version rejection, and
  release-matched restore verification.
- Commit-ordered outbox checkpoint fencing, semantic FSM replay validation, and
  database-enforced tenant relationship boundaries.
- Official Go MCP SDK integration with authenticated stdio transport.
- Eight typed control tools for Sprint planning, submission, decisions,
  inspection, cancellation, and retry resumption.
- Durable decision aggregates, stable pending-decision IDs, correlated tool
  audit events, and compatibility-pinned MCP schemas.
- A fail-closed bearer authentication boundary for later Streamable HTTP
  deployment.

### Changed

- Hardened MCP cancellation against stranded decisions, normalized approval
  reasons, validated read audit identities, and made durable startup fail on
  schema drift.
- Closed generic-transition approval bypasses, made resume retries replayable
  after a lost response, and surfaced failed-action audit outages as retryable
  unavailability.
- Made mutating MCP success audits atomic with domain changes, preserved
  cross-scope denial evidence, and normalized signal-driven MCP shutdown.
- Extended recovery verification across governed multi-aggregate receipts,
  canonical command evidence, and atomic MCP success audits.
- Added canonical Sprint/decision event reconstruction and deterministic legacy
  Sprint migration baselines, with fail-closed rejection of ambiguous legacy
  Sprint-to-Run links.
- Removed decision and resume authority from the default MCP agent profile so a
  co-architect cannot authorize its own execution.
- Reserved retry and awaiting-decision resume transitions for the governed
  resume command at both in-memory and PostgreSQL persistence boundaries.
- Distinguished original success audits from replay audits and made rollback
  receipt invalidations event-specific while preserving command scope.
- Added the Sprint pending-decision invariant to the public schema and rejected
  legacy approval states that cannot be reconstructed from governed evidence.
- Added exact command scopes to atomic audits, quiesced governed writers before
  rollback safety checks, and preserved legal scoped idempotency-key reuse.
- Bound every rollback invalidation to exact command-anchor and domain-event
  IDs, and made recovery consume receipt evidence by event ID rather than by a
  reusable metadata tuple.
- Restricted receipt-free migration evidence to deterministic legacy Sprint
  and generated Run events so callers cannot impersonate the migration actor.
- Ordered destructive rollback behind the idempotency command barrier to avoid
  parent-table deadlocks, and rejected proposed legacy Sprints whose linked Run
  had already advanced beyond `draft`.
- Allowed replay audits to use a fresh retry correlation while preserving every
  receipt-bound identity field, and bound rollback invalidations to the exact
  response payload and aggregate version across reused command identities.
- Mapped HTTP authentication and authorization failures to `401` and `403`
  instead of reporting policy denials as server failures.
- Made migration 003 upgrade legacy Sprint rows safely and restore the Sprint
  02 event constraint during a tested post-use rollback while refusing to
  discard pending decisions. Generated legacy Runs now receive canonical
  creation events, while incompatible command receipts are removed on rollback.

## [0.1.0] - 2026-07-16

### Added

- Public project vision and governance.
- Go control-plane architecture.
- PostgreSQL, object storage, Qdrant, and Neo4j data boundaries.
- MCP runtime and Codex CLI worker design.
- Deterministic code lineage and Context Broker architecture.
- Security and observability architecture.
- Initial language-neutral JSON schemas.
- Fifteen-Sprint implementation roadmap.
- Automated repository quality validation.
- Protected `main` branch and private vulnerability reporting.

[0.1.0]: https://github.com/rvbernucci/forja-guide/releases/tag/v0.1.0
