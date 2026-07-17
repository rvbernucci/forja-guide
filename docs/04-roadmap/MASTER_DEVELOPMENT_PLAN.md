# Master Development Plan

Status: Active. Sprints 00-03 closed. Sprint 04 is authorized and ready; its
implementation has not started. Gate A remains achieved.

## Objective

Deliver Forja 1.0 as a governed multi-agent factory that can execute one
approved software Sprint from intent to validated evidence with restart safety,
bounded authority, complete auditability, and measured context quality.

## Planning Rules

- Every Sprint must produce executable evidence, not only documentation.
- No Sprint may require an unbounded LLM decision to close.
- Each new store or model must have a failure fallback.
- Derived Qdrant and Neo4j state must be rebuildable.
- Security, cancellation, timeout, and recovery are product behavior.
- Work in progress is limited to one primary implementation Sprint and one
  independent validation lane.

## Definition of Ready

A Sprint is ready when it has:

- a user or operator outcome;
- explicit in-scope and out-of-scope boundaries;
- dependencies identified;
- versioned input and output contracts;
- acceptance evidence;
- rollback or safe failure behavior;
- an assigned author and independent validator.

## Definition of Done

A Sprint is done when:

- implementation and migrations are committed;
- mechanical tests pass;
- independent validation is recorded;
- security and tenant boundaries are checked;
- observability exists for new runtime behavior;
- documentation and ADRs are current;
- rollback has been tested or mechanically demonstrated;
- no undocumented blocker remains.

## Sprint Map

| Sprint | Name | Primary outcome |
| --- | --- | --- |
| 00 | Public Foundation | Clean public repository, governance, contracts, and CI |
| 01 | Go Kernel | Executable daemon and CLI with contract parity |
| 02 | Durable State | PostgreSQL event store, FSM, leases, and outbox |
| 03 | MCP Control Surface | Co-architect can plan, submit, inspect, and decide |
| 04 | Worker Supervisor | Codex CLI processes run with budgets and cancellation |
| 05 | Isolated Delivery | Worktrees, write leases, validation, and evidence |
| 06 | Observability | Metrics, logs, traces, dashboards, and failure taxonomy |
| 07 | Artifacts and Memory | Object storage, conversations, memory, and provenance |
| 08 | Deterministic Indexing | Compiler-backed repository and code lineage |
| 09 | Governed Retrieval | Qdrant hybrid search and canonical entity resolution |
| 10 | Graph Projection | Neo4j lineage, impact paths, and projection recovery |
| 11 | Context Broker | Minimal evidence-backed context packs |
| 12 | Governance and Resilience | Approvals, policy, recovery, security, and chaos tests |
| 13 | Evaluation Harness | Repeatable quality, safety, cost, and OOD evaluations |
| 14 | Production Pilot | End-to-end pilot, SLOs, release, and 1.0 readiness |

## Critical Path

```text
00 -> 01 -> 02 -> 03 -> 04 -> 05
                         |
                         +-> 06
05 -> 07 -> 08 -> 09 -> 10 -> 11 -> 12 -> 13 -> 14
```

Sprints 06 and 07 may overlap after Sprint 05 if they have different authors.
The intelligence pipeline must not block execution-plane reliability work.

## Capability Gates

### Gate A: Executable Kernel

After Sprint 02:

- state survives restart;
- duplicate commands are idempotent;
- transitions are transactional;
- events can rebuild read models.

### Gate B: Governed Execution

After Sprint 05:

- an approved task starts a real worker;
- writes stay inside the declared scope;
- cancellation terminates the process tree;
- evidence is generated and validated;
- recovery does not require manual database editing.

### Gate C: Context Intelligence

After Sprint 11:

- exact, semantic, and graph retrieval are evaluated separately;
- context packs cite canonical sources;
- stale projections degrade safely;
- token budgets are enforced;
- semantic candidates never become authority automatically.

### Gate D: Production Readiness

After Sprint 14:

- security and chaos suites pass;
- SLOs have measured baselines;
- backup and restore are demonstrated;
- one real repository pilot completes successfully;
- the public release accurately describes implemented behavior.

## Evidence Ledger

Every Sprint publishes:

```text
docs/evidence/sprint-XX/
  plan.json
  test-report.json
  validation-report.json
  security-report.json
  rollback-report.json
  metrics-summary.json
  closure-candidate.json  # while immutable review is pending
  close-receipt.json      # after immutable review passes
```

Exactly one closure document may exist at a time. A candidate is fail-closed,
non-authoritative, and cannot authorize the next Sprint. Evidence directories
are introduced with the runtime. Large bodies belong in object storage and are
represented by hash-pinned metadata.

## Detailed Checklists

- [Sprints 00-04: Foundation and execution](SPRINTS_00_04_FOUNDATION.md)
- [Sprints 05-09: Delivery and intelligence](SPRINTS_05_09_INTELLIGENCE.md)
- [Sprints 10-14: Governance and production](SPRINTS_10_14_PRODUCTION.md)
