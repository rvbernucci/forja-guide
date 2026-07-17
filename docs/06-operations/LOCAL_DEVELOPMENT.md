# Local Development

Status: Sprint 02 durable kernel implemented; later infrastructure proposed

## Current Repository

The repository contains the executable Sprint 02 Go kernel, architecture,
planning, canonical schemas, and quality automation.

Validate it with:

```bash
make validate
```

The full gate runs formatting checks, module verification, `go vet`, unit and
race tests, reproducible cross-builds, a process-level smoke test, and public
repository validation.

## Kernel

Prerequisite: Go 1.26.5.

Start the daemon:

```bash
go run ./cmd/forjad --listen 127.0.0.1:8080
```

This starts explicit ephemeral mode. To preserve commands across restarts,
create a PostgreSQL database and provide its URL:

```bash
export FORJA_DATABASE_URL='postgres:///forja?host=/tmp'
go run ./cmd/forjad --listen 127.0.0.1:8080
```

Embedded migrations run by default under a PostgreSQL advisory lock. Set
`FORJA_DATABASE_AUTO_MIGRATE=false` only when a deployment pipeline applies
migrations separately.

Create and inspect a synthetic run:

```bash
go run ./cmd/forja run create \
  --endpoint http://127.0.0.1:8080 \
  --objective "Build a governed Sprint"

go run ./cmd/forja run get \
  --endpoint http://127.0.0.1:8080 \
  --id run_REPLACE_WITH_CREATED_ID
```

Transition it with optimistic concurrency:

```bash
go run ./cmd/forja run transition \
  --endpoint http://127.0.0.1:8080 \
  --id run_REPLACE_WITH_CREATED_ID \
  --expected-version 1 \
  --to awaiting_approval
```

## Current Durable Prerequisites

- Go 1.26.5;
- PostgreSQL 14 or newer;
- `pg_dump`, `pg_restore`, and `psql` for recovery verification;
- Python 3.9 or newer and `diff` for release-migration verification.

See the [PostgreSQL recovery runbook](POSTGRESQL_RECOVERY.md).

## Later Runtime Prerequisites

- Docker or a compatible container runtime;
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

Implemented daemon variables are:

| Variable | Purpose |
| --- | --- |
| `FORJA_LISTEN` | Explicit daemon host and port |
| `FORJA_ENVIRONMENT` | Runtime environment label |
| `FORJA_LOG_LEVEL` | `debug`, `info`, `warn`, or `error` |
| `FORJA_SHUTDOWN_TIMEOUT` | Graceful shutdown duration |
| `FORJA_DATABASE_URL` | PostgreSQL connection URL; enables durable mode |
| `FORJA_DATABASE_MAX_CONNECTIONS` | Bounded pool size, default `4` |
| `FORJA_DATABASE_AUTO_MIGRATE` | Apply embedded migrations, default `true` |
| `FORJA_ENDPOINT` | CLI daemon endpoint |
| `FORJA_TIMEOUT` | CLI request deadline |

Daemon precedence is defaults, JSON file, environment, then flags. Unknown
configuration fields fail closed.

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

Run PostgreSQL acceptance tests against a disposable database:

```bash
export FORJA_TEST_DATABASE_URL='postgres:///forja_test?host=/tmp'
make test-integration
```

The suite destroys the `forja` schema in that database. Never point it at a
shared or production database.

## Clean Repository Rule

Runtime artifacts, model outputs, worktrees, logs, database volumes, and
retrieval indexes stay outside Git. Only small deterministic fixtures and
hash-pinned evidence metadata may be committed.
