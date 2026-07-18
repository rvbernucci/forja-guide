# Local Development

Status: Sprint 03 control surface and Sprint 04 worker supervisor implemented

## Current Repository

The repository contains the executable durable Go kernel, governed MCP control
surface, bounded one-shot worker supervisor, architecture, planning, canonical
schemas, and quality automation.

Validate it with:

```bash
make validate
```

The full gate runs formatting checks, module verification, `go vet`, unit and
race tests, reproducible cross-builds, process-level kernel and MCP smoke tests,
and public repository validation.

## Worker Supervisor

Run the offline one-shot smoke test:

```bash
make smoke-worker
```

Execute a real canonical task with `forja-worker --task ... --result ...`.
See [Worker Operations](WORKER_OPERATIONS.md) for task preparation, exit codes,
cancellation, recovery, security, and rollback. The public contract is in
[Worker Execution](../03-contracts/WORKER_EXECUTION.md).

## MCP Control Surface

Build and run the stdio server with an explicit local principal:

```bash
export FORJA_MCP_ACTOR_ID='codex-co-architect'
go run ./cmd/forja-mcp
```

Use `FORJA_DATABASE_URL` to preserve Sprints, decisions, Runs, events, outbox,
and idempotency receipts across MCP process restarts. Without it, the process
prints an explicit ephemeral-state warning to standard error.

Durable MCP startup verifies the complete migration ledger and semantic schema
manifest before serving stdio. With auto-migration disabled, a missing or
drifted schema therefore fails startup instead of failing on the first tool
call.

Register a built binary with Codex:

```bash
go build -trimpath -o "$HOME/.local/bin/forja-mcp" ./cmd/forja-mcp
codex mcp add forja \
  --env FORJA_MCP_ACTOR_ID=codex-co-architect \
  -- "$HOME/.local/bin/forja-mcp"
```

See the [MCP control API](../03-contracts/MCP_CONTROL_API.md) for all tools,
permissions, command identity fields, and the remote HTTP security boundary.

## Kernel

Prerequisite: Go 1.26.5.

Start the daemon:

```bash
export FORJA_HTTP_BEARER_TOKEN="$(openssl rand -hex 32)"
export FORJA_HTTP_ACTOR_TYPE='human'
export FORJA_HTTP_ACTOR_ID='local-operator'
go run ./cmd/forjad --listen 127.0.0.1:8080
```

The CLI reads the same `FORJA_HTTP_BEARER_TOKEN` from its environment and sends
it as a bearer credential. The secret has no flag or JSON-file equivalent, is
never printed, and must be delivered through an approved secret boundary.
`forjad` accepts only a numeric loopback listen IP while it serves plaintext
HTTP; the CLI requires HTTPS for hostnames and non-loopback endpoints.

This starts explicit ephemeral mode. To preserve commands across restarts,
create a PostgreSQL database and provide its URL:

```bash
export FORJA_DATABASE_URL='postgres:///forja?host=/tmp'
go run ./cmd/forjad --listen 127.0.0.1:8080
```

Embedded migrations run by default under a PostgreSQL advisory lock. Set
`FORJA_DATABASE_AUTO_MIGRATE=false` only when a deployment pipeline applies
migrations separately.

Incremental migrations require a quiescent writer window. Their relation lock
barrier uses `NOWAIT`, and their projection-watermark lock uses a bounded lock
timeout. Startup fails immediately when an older process, projection rebuild,
or command transaction is active. Drain writers and retry; do not loop
migrations while the previous release is still accepting work.
See [ADR-0007](../05-decisions/ADR-0007-FAIL-FAST-INCREMENTAL-MIGRATIONS.md)
for the complete lock protocol and compatibility boundary.

Create and inspect a synthetic run:

```bash
go run ./cmd/forja run create \
  --endpoint http://127.0.0.1:8080 \
  --idempotency-key local-create-0001 \
  --objective "Build a governed Sprint"

go run ./cmd/forja run get \
  --endpoint http://127.0.0.1:8080 \
  --id run_REPLACE_WITH_CREATED_ID
```

Transition it with optimistic concurrency:

```bash
go run ./cmd/forja run transition \
  --endpoint http://127.0.0.1:8080 \
  --idempotency-key local-transition-0001 \
  --id run_REPLACE_WITH_CREATED_ID \
  --expected-version 1 \
  --to awaiting_approval
```

## Current Durable Prerequisites

- Go 1.26.5;
- PostgreSQL 18;
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
| `FORJA_HTTP_BEARER_TOKEN` | Required 32-4096 byte daemon and CLI bearer secret; environment only |
| `FORJA_HTTP_ACTOR_ID` | Required stable audit identity mapped to the HTTP credential |
| `FORJA_HTTP_ACTOR_TYPE` | HTTP principal type; defaults to `human` |
| `FORJA_MCP_ACTOR_ID` | Required authenticated identity for stdio MCP |
| `FORJA_MCP_ACTOR_TYPE` | Capability profile: `agent` (default, no decide/resume), `worker` (read only), or explicitly trusted `human`/`system` (all control capabilities) |
| `FORJA_ENDPOINT` | CLI daemon endpoint |
| `FORJA_TIMEOUT` | CLI request deadline |
| `CODEX_HOME` | Deployment-owned Codex authentication root passed only to the Codex adapter |

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
