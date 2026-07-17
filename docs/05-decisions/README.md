# Architecture Decision Records

| ADR | Decision | Status |
| --- | --- | --- |
| [0001](ADR-0001-GO-CONTROL-PLANE.md) | Use Go for the control plane | Accepted |
| [0002](ADR-0002-POSTGRES-SYSTEM-OF-RECORD.md) | Use PostgreSQL as the operational system of record | Accepted |
| [0003](ADR-0003-DERIVED-INTELLIGENCE-STORES.md) | Treat Qdrant and Neo4j as rebuildable projections | Accepted |
| [0004](ADR-0004-STANDARD-MCP-BOUNDARY.md) | Build a Forja MCP server on the standard protocol | Accepted |
| [0005](ADR-0005-DETERMINISTIC-CODE-LINEAGE.md) | Derive code authority before semantic indexing | Accepted |
| [0006](ADR-0006-POSTGRES-CREDENTIAL-BOUNDARY.md) | Keep PostgreSQL credentials out of process arguments | Accepted |
| [0007](ADR-0007-FAIL-FAST-INCREMENTAL-MIGRATIONS.md) | Require quiescence and fail-fast barriers for incremental migrations | Accepted |
| [0008](ADR-0008-FAIL-CLOSED-DAEMON-HTTP-AUTHENTICATION.md) | Authenticate and authorize the daemon HTTP boundary fail-closed | Accepted |

Use zero-padded sequential identifiers. Accepted ADRs are not edited to reverse
their decision; a new ADR supersedes them.
