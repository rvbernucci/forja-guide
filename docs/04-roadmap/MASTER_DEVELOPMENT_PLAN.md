# Master Development Plan

Status: Active. Sprints 00-09 are closed. The authoritative Sprint 09
protocol-v2 receipt records the immutable review and authorizes Sprint 10.
Gates A and B are achieved; Sprint 10 remains subject to its own readiness,
quality, security, and evidence gates.

## Objective

Deliver the neutral Forja runtime and its first bounded product vertical, Forja
Alpha. Alpha must turn a financial-research question into a point-in-time,
source-grounded, locally inferred, tool-verified memo with restart safety,
bounded authority, complete auditability, and measured product quality.

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
| 10 | Canonical Financial Data and Local Runtime | Point-in-time Magnificent Seven data plus verified Radeon/ROCm inference |
| 11 | Deterministic Finance Tools and Evidence Fabric | Typed analytics plus canonical Qdrant/Neo4j context |
| 12 | Governed Alpha Agent and Research Workspace | Planning, tools, RAG, memory, permissions, verification, and web UX |
| 13 | Evaluation, ROCm Optimization, and Safety Closure | Held-out quality gates and reproducible Radeon performance |
| 14 | Pilot, Release, and AMD Submission | Clean Forja Alpha release and complete Track 2 submission |

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

### Gate C: Local Context Intelligence

After Sprint 11:

- exact, semantic, and graph retrieval are evaluated separately;
- core language-model and embedding inference execute locally on AMD
  Radeon/ROCm in the competition profile;
- context packs cite canonical sources;
- stale projections degrade safely;
- token budgets are enforced;
- semantic candidates never become authority automatically.

### Gate C2: Alpha Product Completeness

After Sprint 12:

- financial sources are point-in-time and raw-to-claim traceable;
- deterministic tools own material accounting and statistical calculations;
- the local model plans, invokes bounded tools, and composes verified memos;
- multi-turn memory remains governed, scoped, exportable, and deletable;
- the complete primary scenario is operable from the web interface.

### Gate D: Product and Submission Readiness

After Sprint 14:

- security and chaos suites pass;
- SLOs have measured baselines;
- backup and restore are demonstrated;
- the primary financial-research pilot and its degraded paths complete;
- the AMD Track 2 source, specification, demo, and presentation reproduce from
  a clean Radeon Cloud environment;
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
- [Sprints 10-14: Forja Alpha production](SPRINTS_10_14_PRODUCTION.md)
