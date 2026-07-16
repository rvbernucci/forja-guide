# Roadmap

Forja will be developed through five capability stages:

| Stage | Sprints | Outcome |
| --- | --- | --- |
| Foundation | 00-02 | Clean repository, Go kernel, contracts, and PostgreSQL event store |
| Execution | 03-05 | MCP control surface, Codex worker pool, worktrees, leases, and validation |
| Intelligence | 06-09 | Artifacts, deterministic lineage, Qdrant retrieval, and Neo4j projections |
| Governance | 10-12 | Context Broker, approvals, resilience, observability, and security |
| Production | 13-14 | Evaluation harness, pilot, release gates, and operational readiness |

The detailed plan is maintained in
[`docs/04-roadmap/MASTER_DEVELOPMENT_PLAN.md`](docs/04-roadmap/MASTER_DEVELOPMENT_PLAN.md).

## Release Policy

- `0.x`: architecture, contracts, and experimental runtime.
- `1.0`: one complete governed Sprint can run from intent to validated evidence
  with restart safety and no manual state repair.
- `2.0`: graph and retrieval context serving meet measured quality budgets.
- `3.0`: multi-tenant operation, policy isolation, and production SLOs.

