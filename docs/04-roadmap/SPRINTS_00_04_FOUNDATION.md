# Sprints 00-04: Foundation and Execution

## Sprint 00: Public Foundation

**Outcome:** establish a clean public project that clearly separates plans from
implemented behavior.

**Status:** Closed. See the
[Sprint 00 evidence package](../evidence/sprint-00/close-receipt.json).

### Scope

- [x] Create repository structure and reading order.
- [x] Add Apache 2.0 license, governance, contribution, and security policies.
- [x] Document system, data, context, runtime, lineage, security, and
  observability architecture.
- [x] Add initial language-neutral JSON schemas.
- [x] Add automated checks for internal links, JSON validity, private paths, and
  common credential patterns.
- [x] Enable GitHub branch protection and required CI checks.
- [x] Enable GitHub private vulnerability reporting.
- [x] Create the first tagged planning release.

### Acceptance

- `make validate` passes locally and in GitHub Actions.
- A clean clone contains no private source, credentials, generated databases,
  or machine-specific paths.
- README status does not imply that the runtime already exists.

## Sprint 01: Go Kernel

**Outcome:** provide an executable `forjad` daemon and `forja` CLI with contract
validation and a deterministic in-memory state machine.

**Status:** Closed. See the
[Sprint 01 evidence package](../evidence/sprint-01/close-receipt.json).

### Scope

- [x] Initialize the Go module and target supported Go versions.
- [x] Create `cmd/forjad` and `cmd/forja`.
- [x] Define configuration precedence: defaults, file, environment, flags.
- [x] Generate or hand-map Go structs from canonical JSON schemas.
- [x] Implement schema validation at external boundaries.
- [x] Implement typed IDs, clocks, UUID generation, and error taxonomy.
- [x] Implement the run FSM in memory.
- [x] Add graceful shutdown through context cancellation.
- [x] Add structured logging with automatic secret redaction.
- [x] Add unit, race, fuzz, and contract fixture tests.
- [x] Build reproducible `linux/amd64` and `linux/arm64` binaries.

### Acceptance

- Daemon starts and becomes healthy within the defined local budget.
- CLI can create and inspect a synthetic run.
- Invalid transitions and malformed contracts fail closed.
- `go test -race ./...` passes.

## Sprint 02: Durable State

**Outcome:** make state transitions, leases, and projection events durable and
recoverable in PostgreSQL.

**Status:** Closed. See the
[Sprint 02 evidence package](../evidence/sprint-02/close-receipt.json).

### Scope

- [x] Define migrations for tenants, repositories, Sprints, tasks, runs,
  attempts, events, leases, idempotency keys, and outbox entries.
- [x] Use optimistic aggregate versions for state transitions.
- [x] Commit aggregate state and immutable events in one transaction.
- [x] Implement command idempotency and replay-safe responses.
- [x] Implement worker, scheduler, file, and worktree leases.
- [x] Implement lease expiry, renewal, fencing tokens, and takeover.
- [x] Implement transactional outbox claiming with `SKIP LOCKED`.
- [x] Add projection checkpoints and dead-letter state.
- [x] Add backup, restore, migration rollback, and corruption fixtures.
- [x] Add repository interfaces without leaking SQL into policy logic.

### Acceptance

- Kill-and-restart tests preserve valid state.
- Duplicate commands do not create duplicate attempts.
- Two schedulers cannot own the same fenced lease.
- An event replay rebuilds the expected read model.
- Migrations pass clean-database and upgrade-path tests.

## Sprint 03: MCP Control Surface

**Outcome:** allow a Co-architect in an MCP client to create governed plans and
control runs through stable tools.

**Status:** Closed. See the
[Sprint 03 evidence package](../evidence/sprint-03/close-receipt.json).

### Scope

- [x] Integrate the official Go MCP SDK.
- [x] Implement stdio transport for local clients.
- [x] Define the authenticated HTTP transport boundary for later deployment.
- [x] Implement `plan_sprint`, `submit_sprint`, `get_sprint`, and `get_run`.
- [x] Implement `approve_decision`, `reject_decision`, `cancel_run`, and
  `resume_run`.
- [x] Bind approvals to stable pending-decision IDs.
- [x] Add tool input/output schemas and compatibility fixtures.
- [x] Separate conversational explanations from canonical commands.
- [x] Add authorization checks before command persistence.
- [x] Add end-to-end MCP tests using a deterministic fake client.

### Acceptance

- A client can propose, inspect, approve, and cancel a synthetic Sprint.
- Replayed MCP calls are idempotent.
- Free-form chat text cannot bypass a pending approval contract.
- Every tool action emits correlated audit events.

## Sprint 04: Worker Supervisor

**Outcome:** execute Codex CLI workers as bounded, cancellable, and observable
processes.

**Status:** Closed. See the
[Sprint 04 evidence package](../evidence/sprint-04/close-receipt.json). Sprint
05 is authorized.

### Scope

- [x] Define the worker task and result contracts.
- [x] Implement the Codex CLI adapter behind a generic worker interface.
- [x] Create sanitized worker environments.
- [x] Start workers in independent process groups.
- [x] Enforce wall-clock, inactivity, output-size, and retry budgets.
- [x] Stream structured worker events without storing raw secrets.
- [x] Propagate cancellation to the complete process tree.
- [x] Capture exit status, stdout, stderr, usage, and evidence references.
- [x] Implement deterministic fake and fault-injection workers.
- [x] Add crash, timeout, cancellation, and orphan-process tests.

### Acceptance

- Worker cancellation leaves no orphan process.
- Timeout produces a classified retryable or terminal result.
- Supervisor restart reconciles active attempts correctly.
- A worker cannot change scheduler or approval state directly.
