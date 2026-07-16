# Local Development

Status: Proposed

## Current Repository

The repository currently contains architecture, planning, schemas, and quality
automation.

Validate it with:

```bash
make validate
```

## Planned Runtime Prerequisites

- Go;
- Docker or a compatible container runtime;
- PostgreSQL;
- S3-compatible object storage;
- Qdrant;
- Neo4j;
- Prometheus;
- Loki;
- Grafana;
- Codex CLI for the initial worker adapter.

## Planned Local Stack

The implementation should provide a version-pinned Compose profile:

```text
postgres
object-storage
qdrant
neo4j
prometheus
loki
grafana
forjad
```

Models and worker CLIs remain optional profiles so control-plane tests can run
deterministically without external inference.

## Configuration

Configuration order:

1. compiled safe defaults;
2. explicit configuration file;
3. environment variables;
4. CLI flags.

No configuration layer may contain committed credentials.

The future `.env.example` will list variable names and synthetic values only.

## Test Layers

```text
unit
contract
database
adapter
integration
end-to-end
fault-injection
security
evaluation
```

Fast deterministic tests run on every pull request. External-model evaluations
run separately with budgets and recorded model versions.

## Clean Repository Rule

Runtime artifacts, model outputs, worktrees, logs, database volumes, and
retrieval indexes stay outside Git. Only small deterministic fixtures and
hash-pinned evidence metadata may be committed.

