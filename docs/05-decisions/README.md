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
| [0009](ADR-0009-TWO-PHASE-SPRINT-CLOSURE.md) | Bind Sprint closure to a published, independently reviewed candidate | Accepted |
| [0010](ADR-0010-BOUNDED-WORKER-SUPERVISION.md) | Supervise workers through bounded, authority-free process contracts | Accepted |
| [0011](ADR-0011-FENCED-GIT-DELIVERY.md) | Deliver Git changes through fenced leases and reproducible validation | Accepted for Sprint 05 |
| [0012](ADR-0012-FAIL-SOFT-CONTENT-FREE-OBSERVABILITY.md) | Keep observability fail-soft, content-free, and non-authoritative | Accepted for Sprint 06 |
| [0013](ADR-0013-CONTENT-ADDRESSED-OBJECT-STORAGE.md) | Preserve large bodies through a recoverable PostgreSQL/S3 saga | Accepted for Sprint 07 |
| [0014](ADR-0014-GOVERNED-MEMORY-PROMOTION.md) | Require explicit authority before chat becomes durable memory | Accepted for Sprint 07 |
| [0015](ADR-0015-GOVERNED-HYBRID-RETRIEVAL.md) | Discover with hybrid search and resolve against canonical authority | Accepted for Sprint 09 |
| [0016](ADR-0016-BEDROCK-TITAN-EMBEDDING-PROVIDER.md) | Use Bedrock Titan through the AWS Go SDK with workload identity | Accepted for Sprint 09 implementation |
| [0017](ADR-0017-GOVERNED-MEMORY-RETRIEVAL-BODIES.md) | Retrieve memory through authorized derived bodies | Accepted for Sprint 09 implementation |
| [0018](ADR-0018-GOVERNED-INCIDENT-RETRIEVAL.md) | Derive safe incident cards from immutable attempt failures | Accepted for Sprint 09 implementation |
| [0019](ADR-0019-AMD-TRACK2-LOCAL-INFERENCE.md) | Keep AMD Track 2 core inference local on Radeon/ROCm | Accepted for Sprints 10-14 |
| [0020](ADR-0020-BOUNDED-ALPHA-VERTICAL.md) | Bound Forja Alpha as a local-first domain vertical | Accepted for Sprints 10-14 |

Use zero-padded sequential identifiers. Accepted ADRs are not edited to reverse
their decision; a new ADR supersedes them.
