# Documentation

The documentation is organized by decision depth rather than by implementation
language.

## Reading Order

1. [Project vision](01-vision/PROJECT_VISION.md)
2. [System architecture](02-architecture/SYSTEM_ARCHITECTURE.md)
3. [Data architecture](02-architecture/DATA_ARCHITECTURE.md)
4. [Context Broker](02-architecture/CONTEXT_BROKER.md)
5. [Forja Alpha](02-architecture/FORJA_ALPHA.md)
6. [Runtime and MCP](02-architecture/RUNTIME_AND_MCP.md)
7. [Contract model](03-contracts/CONTRACTS.md)
8. [MCP control API](03-contracts/MCP_CONTROL_API.md)
9. [Worker execution contract](03-contracts/WORKER_EXECUTION.md)
10. [Isolated delivery contract](03-contracts/ISOLATED_DELIVERY.md)
11. [Master development plan](04-roadmap/MASTER_DEVELOPMENT_PLAN.md)
12. [Architecture decisions](05-decisions/)
13. [Evaluation strategy](07-evaluations/EVALUATION_STRATEGY.md)
14. [Retrieval evaluation protocol](07-evaluations/RETRIEVAL_EVALUATION_PROTOCOL.md)
15. [Forja Alpha local experience](06-operations/FORJA_ALPHA_LOCAL.md)
16. [PostgreSQL recovery](06-operations/POSTGRESQL_RECOVERY.md)
17. [Observability operations](06-operations/OBSERVABILITY_OPERATIONS.md)
18. [Artifact storage operations](06-operations/ARTIFACT_STORAGE_OPERATIONS.md)
19. [Deterministic indexing operations](06-operations/DETERMINISTIC_INDEXING_OPERATIONS.md)
20. [Qdrant recovery runbook](06-operations/QDRANT_RECOVERY_RUNBOOK.md)
21. [Sprint evidence](evidence/)

## Documentation States

| State | Meaning |
| --- | --- |
| Proposed | Design under discussion |
| Accepted | Architecture decision approved |
| Implemented | Behavior exists and has evidence |
| Superseded | Replaced by a newer decision |
| Historical | Preserved for context only |

Documents must not claim `Implemented` without a reference to tests, code, or an
operational receipt.
