# Deterministic Indexing Operations

Status: Sprint 08 candidate implementation. Do not treat it as authoritative
until the Sprint 08 protocol-v2 close receipt is present.

## Boundary

`forja-index` reads only committed Git objects, materializes them in a private
temporary directory, runs the source-pinned language adapters, publishes one immutable
snapshot artifact, and atomically activates its exact metadata in PostgreSQL.
It never writes Qdrant or Neo4j directly.

The command supports Go, TypeScript/JavaScript, and Python. TypeScript uses the
lockfile-pinned `@typescript/typescript6` compatibility API because TypeScript
7.0 does not expose the programmatic compiler API.

The bundled adapter executables are trusted release components; their JSON is
untrusted input. Git stdout/stderr, supported-language source encoding,
adapter requests, adapter output, and adapter runtime are bounded and validated.
Environment filtering and a private temporary copy do not form a hostile-code
sandbox, so operators must not register repository-provided or third-party
adapter executables.

## Prerequisites

```bash
npm ci --ignore-scripts --no-audit --no-fund
go build -trimpath -o ./bin/forja-index ./cmd/forja-index
```

Provide PostgreSQL and an S3-compatible object store through the operator
environment. AWS credentials use the standard AWS SDK credential chain and
must never be passed as command arguments.

```bash
export FORJA_DATABASE_URL='postgresql://forja@127.0.0.1:5432/forja?sslmode=disable'
export FORJA_S3_BUCKET='forja-artifacts'
export FORJA_S3_REGION='us-east-1'
export FORJA_S3_ENDPOINT='http://127.0.0.1:9000'
export FORJA_S3_PATH_STYLE='true'
export FORJA_TENANT_ID='00000000-0000-4000-8000-000000000001'
export FORJA_REPOSITORY_ID='00000000-0000-4000-8000-000000000002'
export FORJA_INDEX_ACTOR_ID='operator-indexer'
```

`FORJA_TENANT_ID` and `FORJA_REPOSITORY_ID` must be canonical UUIDv4 values
resolved by the authenticated control plane. `FORJA_INDEX_ACTOR_ID` must be the
authenticated operator or workload identity. Do not derive any of these values
from repository content or caller-supplied command flags.

## Publish A Snapshot

Use one stable idempotency key for every retry of the same logical command.
The revision is resolved to an exact commit before source is read.

```bash
./bin/forja-index \
  --repository /absolute/path/to/repository \
  --revision HEAD \
  --tool-root "$PWD" \
  --idempotency-key index-main-20260719-001 \
  --timeout 10m
```

Success prints the canonical `snapshot_<sha256>` ID. A retry either returns the
same authority or fails on contradictory evidence; it cannot publish a second
active equivalent snapshot.

## Failure Semantics

- Adapter failure publishes no snapshot authority.
- Object-store failure is journaled by the artifact saga and is safe to retry.
- A published object without PostgreSQL activation is not canonical; retrying
  completes activation using the deterministic artifact operation ID.
- PostgreSQL activation writes snapshot rows, cards, relations, adapter runs,
  deltas, invalidations, event, outbox message, and idempotency receipt in one
  transaction.
- A live snapshot prevents its exact artifact from being archived or
  tombstoned.
- Dynamic or unresolved relations remain explicit and never become proven
  edges through similarity.

## Verification

```bash
FORJA_DATABASE_URL="$FORJA_DATABASE_URL" scripts/postgres_verify.sh
go test ./internal/contracts ./internal/indexing ./internal/indexservice
FORJA_TEST_DATABASE_URL="$FORJA_DATABASE_URL" \
  go test ./internal/postgres -run 'Test(IndexPublication|ConcurrentEquivalentIndex|IndexRelationClosure)'
FORJA_TEST_DATABASE_URL="$FORJA_DATABASE_URL" \
  scripts/rehearse_sprint08_indexing.sh
```

The command drill uses a fresh PostgreSQL database and an authenticated,
conditional S3 protocol fixture. It publishes three real Git commits under one
repository authority, proves Go and TypeScript reuse plus Python re-extraction,
then proves that a `go.mod`-only change forces Go re-extraction while Python and
TypeScript remain reusable. Finally, it publishes the same source under a
second repository authority. It verifies four distinct snapshots, authority
isolation, schema, events, outbox, and receipts. Provider-specific
backup/restore remains covered by the separate Sprint 07 MinIO drill.

Prometheus exposes bounded `forja_index_entities_total` and
`forja_index_invalidations_total` series. Paths, symbol names, source bodies,
tool output, and secrets are intentionally absent from labels and spans.

## Rollback

Migration 008 can roll back only before any index snapshot, event, or receipt
exists. After canonical use, stop new indexing work and deploy a forward repair.
Never delete evidence to force an older binary to start.
