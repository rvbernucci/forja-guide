# Changelog

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
