# Documentation

The documentation is organized by decision depth rather than by implementation
language.

## Reading Order

1. [Project vision](01-vision/PROJECT_VISION.md)
2. [System architecture](02-architecture/SYSTEM_ARCHITECTURE.md)
3. [Data architecture](02-architecture/DATA_ARCHITECTURE.md)
4. [Context Broker](02-architecture/CONTEXT_BROKER.md)
5. [Runtime and MCP](02-architecture/RUNTIME_AND_MCP.md)
6. [Contract model](03-contracts/CONTRACTS.md)
7. [MCP control API](03-contracts/MCP_CONTROL_API.md)
8. [Worker execution contract](03-contracts/WORKER_EXECUTION.md)
9. [Isolated delivery contract](03-contracts/ISOLATED_DELIVERY.md)
10. [Master development plan](04-roadmap/MASTER_DEVELOPMENT_PLAN.md)
11. [Architecture decisions](05-decisions/)
12. [Evaluation strategy](07-evaluations/EVALUATION_STRATEGY.md)
13. [Retrieval evaluation protocol](07-evaluations/RETRIEVAL_EVALUATION_PROTOCOL.md)
14. [PostgreSQL recovery](06-operations/POSTGRESQL_RECOVERY.md)
15. [Observability operations](06-operations/OBSERVABILITY_OPERATIONS.md)
16. [Artifact storage operations](06-operations/ARTIFACT_STORAGE_OPERATIONS.md)
17. [Deterministic indexing operations](06-operations/DETERMINISTIC_INDEXING_OPERATIONS.md)
18. [Qdrant recovery runbook](06-operations/QDRANT_RECOVERY_RUNBOOK.md)
19. [Sprint evidence](evidence/)

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
