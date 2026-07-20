# Changelog

## Unreleased

- Add the Forja Alpha local web experience foundation with an embedded
  three-pane research interface, strict local-inference endpoint policy,
  bounded research-plan API, and explicit capability readiness.

- Preserve valid pre-upgrade blocked-run resume events during projection replay
  without reopening their retired runtime transition.
- Recover prepared and published journals from their immutable receipt and a
  fresh Git ref observation even after external validation files disappear.
- Give durable Git reconciliation markers precedence over missing-worktree
  inference so unresolved metadata can never release delivery authority.
- Keep Runs non-resumable when durable Attempt finalization fails, and record
  the immutable attempt-scoped human delivery authorization boundary in
  ADR-0011.
- Preserve legacy blocked-run transition verification, reject the new queued
  edge before Sprint 04 rollback, and fsync quarantine marker directory entries.
- Refresh replacement scheduler authority after detached recovery lease renewal
  and before publication can mutate its journal or Git ref.
- Recheck approver separation when consuming persisted authority and fsync both
  sides of quarantine moves before reporting durable cleanup.
- Retire worktrees and exact lease sets before exposing terminal worker states,
  and make cancellation recovery replay-safe for every durable Attempt outcome.
- Keep pre-start failures non-resumable until fenced Attempt reconciliation,
  and retain delivery leases until rejected preparation bytes are quarantined.
- Recheck Run and Attempt lifecycle authority for legacy prepared publication
  journals, and keep quarantine control markers outside repository-owned bytes.
- Pass every authorized artifact scope into the worker sandbox, require
  quarantine proof after worker start, and sync the full metadata parent chain.
- Bind recovered validation evidence to its exact Attempt-derived validation
  ID and apply publication fences to governed Run resumes.
- Refresh scheduler authority immediately before live publication and sync
  reconciliation markers through their actual rooted parent namespace.

- Serialize incremental migrations against command and standalone audit writers
  before any migration-generated outbox records can be allocated.
- Acquire the command-side migration barrier before aggregate locks, including
  the lease-fenced attempt path, to prevent lock-order inversion.
- Fail incremental migration fast when previous-version writers or projection
  rebuilds are active, requiring deployment quiescence instead of risking a
  mixed-version lock cycle.
- Accept only contract-valid, identity-bound Sprint cancellation evidence from
  the generic Run transition path during receipt and recovery verification.
- Queue a fresh attempt after blocked decisions, require durable Run authority
  for cancellation, bind loaded evidence identities to their approved request,
  and serialize Sprint 05 rollback checks with command writers.
- Keep caller-interrupted result commits retryable and reject explicit null
  optionals from delivery-authorization archive verification.
- Align operational archive replay with governed blocked-run resumes to a fresh
  queued attempt.
- Refresh recovery scheduler authority synchronously before Git mutation and
  require a durable request-bound marker for quarantine replay.

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
- Versioned worker task, report, and result contracts with offline cross-schema
  validation.
- A bounded `forja-worker` Codex CLI supervisor with process-tree cancellation,
  sanitized environments, runtime budgets, structured events, and reproducible
  Linux builds.
- Fenced attempt start, finish, idempotent replay, and restart reconciliation
  with secret-safe canonical events.
- A governed execution pipeline that binds an approved Run to its exact fenced
  attempt, real worker process, isolated Git commit, independent validation,
  durable publication receipt, and terminal Run state.
- Immutable human approval of the complete delivery authority envelope,
  continuous scheduler/delivery lease renewal, canonical pre-launch failure
  records, and restart recovery from persisted validation evidence and the
  publication journal.
- Attempt-scoped delivery authorization streams, semantic archive verification
  for their receipts, completed-publication Git revalidation, idempotent lease
  cleanup, and canonical normalization of worker result/error contradictions.
- A hash-pinned Sprint 05 rollback rehearsal that reverses migrations 006-004
  and starts the authoritative Sprint 04 daemon against migration 003.
- Fail-closed rollback preflight for persisted delivery authorizations, bounded
  publication recovery, and outbox-sequenced authorization reconstruction that
  remains correct under application/database clock skew.
- Caller-cancellation-safe bounded publication, race-safe heartbeat shutdown,
  and complete independent delivery-request validation during archive replay.
- Run-locked publication/cancellation serialization, scheduler-resource-bound
  recovery, restart closure for terminal non-success attempts, pre-rebuild
  conflict settlement, and authorization leases held through commit.
- Pre-worktree synchronous authority refresh, cleanup recovery for succeeded
  attempts behind failed Runs, and audited delivery-authorization replays.
- Worker-boundary heartbeat settlement before attempt persistence, followed by
  synchronous dual-authority refresh and heartbeat restart before Git mutation.
- Restart completion for already-cancelling Runs, stage-aware pre-worktree
  cleanup, and retryable classification of commit failures caused by lease loss.
- Journal-only commit reconstruction without unfenced Git mutation, bounded
  detached recovery renewal, and shutdown-race-safe heartbeat error retention.
- Never-created retry binding recovery and a publication fence that blocks all
  incompatible Run transitions from `prepared` through `published`.
- Content-addressed S3-compatible artifact publication with conditional writes,
  complete SHA-256 re-verification, PostgreSQL saga recovery, and retention.
- Immutable evidence manifests, append-only conversations and citations, and
  human- or policy-authorized durable memory promotion with supersession.
- Content-free operational counts for artifact reconciliation, integrity
  failures, tombstones, memory candidates, and active memories.

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
