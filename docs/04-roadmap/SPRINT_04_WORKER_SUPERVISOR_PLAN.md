# Sprint 04 Worker Supervisor Plan

Status: Implementation candidate complete; closure review pending

## Outcome

Run Codex CLI workers as bounded, cancellable, observable processes without
giving model output scheduler or approval authority.

## Definition of Ready

- Outcome: one approved worker task produces one canonical worker result.
- In scope: public contracts, generic supervisor, Codex adapter, durable attempt
  lifecycle, restart reconciliation, one-shot CLI, tests, and runbooks.
- Out of scope: worktree creation and diff delivery (Sprint 05), multi-run
  scheduling, remote containers, and context retrieval.
- Dependencies: Sprint 03 authoritative receipt, Go kernel, PostgreSQL event
  store, leases, attempts, and run FSM.
- Contracts: `worker-task.schema.json`, `worker-report.schema.json`, and
  `worker-result.schema.json` version 1.0.
- Acceptance evidence: process-tree, timeout, inactivity, output-limit,
  environment, contract, fault-injection, PostgreSQL reconciliation, and
  restart smoke tests.
- Safe failure: fail before launch on invalid contracts; terminate the process
  group on runtime budget violations; reconcile abandoned attempts as
  retryable without trusting stale PIDs.
- Author: Codex implementation lane.
- Independent validator: protected GitHub Actions, CodeRabbit status, and an
  isolated Codex CLI review of the exact public candidate.

## Delivery Sequence

- [x] Compile and test canonical worker contracts.
- [x] Implement invocation adapters and environment policy.
- [x] Implement process groups, cancellation, timers, output bounds, and events.
- [x] Implement Codex JSONL usage parsing and final-report validation.
- [x] Persist attempt start, completion, and restart reconciliation.
- [x] Add the one-shot operational CLI and smoke test.
- [ ] Complete security, rollback, metrics, and validation evidence.
- [ ] Publish a fail-closed closure candidate for immutable review.

## Acceptance

- Worker cancellation leaves no process in the launched process group.
- Wall and inactivity timeouts produce stable retryable classifications.
- A restarted supervisor reconciles abandoned attempts before scheduling.
- A worker receives no Forja control, database, Git, or unrelated host secret.
- Raw output never appears in structured lifecycle events.
- Invalid or blocked final reports cannot be represented as successful work.
