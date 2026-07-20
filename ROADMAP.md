# Roadmap

Forja will be developed through five capability stages:

| Stage | Sprints | Outcome |
| --- | --- | --- |
| Foundation | 00-02 | Clean repository, Go kernel, contracts, and PostgreSQL event store |
| Execution | 03-05 | MCP control surface, Codex worker pool, worktrees, leases, and validation |
| Intelligence | 06-09 | Artifacts, deterministic lineage, governed Qdrant retrieval, and evaluation tooling |
| Local Agent | 10-12 | Radeon/ROCm runtime, retrieval evidence, graph-grounded context, and governed private agent |
| Optimization and Release | 13-14 | ROCm optimization, evaluation, pilot, release gates, and AMD Track 2 submission |

The detailed plan is maintained in
[`docs/04-roadmap/MASTER_DEVELOPMENT_PLAN.md`](docs/04-roadmap/MASTER_DEVELOPMENT_PLAN.md).

## Release Policy

- `0.x`: architecture, contracts, and experimental runtime.
- `1.0`: one complete governed Sprint can run from intent to validated evidence
  with restart safety and no manual state repair.
- `2.0`: graph and retrieval context serving meet measured quality budgets.
- `3.0`: multi-tenant operation, policy isolation, and production SLOs.
