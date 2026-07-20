# Qdrant Recovery Runbook

Status: Sprint 09 operational procedure. Qdrant is a derived store, never an
authority source or a backup substitute for PostgreSQL, Git, or evidence
artifacts.

## Scope and Preconditions

- Use the pinned local profile at
  [`deploy/retrieval/qdrant.compose.yaml`](../../deploy/retrieval/qdrant.compose.yaml).
- Create a local secret without committing it:

  ```bash
  export QDRANT_API_KEY="$(openssl rand -hex 32)"
  docker compose -f deploy/retrieval/qdrant.compose.yaml up -d
  ```

- The profile binds REST and gRPC only to loopback, requires an API key,
  disables telemetry, drops Linux capabilities, uses the official unprivileged
  image, and mounts only storage/snapshot volumes.
- Production endpoints are outside this profile. They require TLS and an API
  key through `QdrantEndpoint.ClientConfig`; no secret belongs in an artifact,
  card, CLI argument, log, or committed environment file.

The pinned `qdrant/qdrant:v1.18.2-unprivileged` image is the Sprint 09 local
compatibility target. Review the official [Qdrant upgrade guidance](https://qdrant.tech/documentation/operations/upgrades/)
and SDK compatibility before changing it.

## Live Rehearsal

The repository includes opt-in integration tests that create and delete real
Qdrant collections. They never read a key from a command argument: supply the
loopback endpoint and development key through the environment. The second test
also needs an isolated disposable PostgreSQL database through
`FORJA_TEST_DATABASE_URL`.

```bash
export FORJA_QDRANT_LIVE=1
export FORJA_QDRANT_HOST=127.0.0.1
export FORJA_QDRANT_GRPC_PORT=6334
export FORJA_QDRANT_API_KEY='development-only-secret'

go test ./internal/retrieval -run '^TestLiveQdrantBlueGreenQueryAndDelete$' -count=1
FORJA_TEST_DATABASE_URL='postgresql://user@127.0.0.1:5432/forja_test?sslmode=disable' \
  go test ./internal/postgres -run '^TestLiveQdrantDeletionResetAndReplay$' -count=1
```

Run only against a disposable loopback Qdrant/PostgreSQL pair. The drill
creates unique temporary collections, removes them during cleanup, and resets
the PostgreSQL `forja` schema. A non-loopback deployment requires TLS and its
normal secret-management boundary; do not use this development command there.

## Rebuild From Canonical State

1. Stop new retrieval projection workers. Do not stop PostgreSQL or alter the
   canonical outbox, delivery ledger, index snapshots, or artifacts.
2. Record the active generation, serving alias, projector checkpoint, and the
   current Qdrant collection inventory in an operator ticket. Do not put
   document text, vector values, API keys, or payload bodies in the ticket.
3. Delete only the affected physical Qdrant collection or its local volumes.
   Never delete `forja.retrieval_projection_points`, `projection_deliveries`,
   `projection_checkpoints`, or source index evidence as a recovery shortcut.
4. Start Qdrant with the pinned profile. Use `EnsureQdrantCollection` with the
   exact registered generation plan; it creates the physical collection,
   applies mandatory indexes, and verifies generation metadata, dimensions,
   strict filtering, and payload schema.
5. Call `ResetRetrievalProjection` with the registered `qdrant.retrieval`
   configuration hash and matching generation. It refuses inflight work,
   clears canonical point provenance so leftover vectors fail closed, preserves
   dead-letter evidence, reopens every delivery with a new fencing token, and
   resets the independent checkpoint to zero.
6. Resume the `qdrant.retrieval` projector with the same configuration hash and
   generation. Its independent delivery ledger now replays canonical history.
   Stable point IDs make duplicate upserts idempotent.
7. Compare the durable checkpoint with the maximum published outbox prefix and
   verify sampled point IDs against active PostgreSQL symbol/source hashes.
8. Only after verification, use `CutoverQdrantCollection` to direct the
   serving alias to the rebuilt physical collection. It verifies the green
   physical contract, records the prior alias observation, performs one atomic
   Qdrant alias update, and reads the alias back. Preserve the previous
   collection during the observation window.
9. Only after the alias read-back succeeds, call
   `ActivateRetrievalGeneration` for the matching registered PostgreSQL
   generation. It drains the prior active generation transactionally. Do not
   retire the prior generation while it remains the guarded rollback target.
10. If the observed serving behavior requires rollback, call
   `RollbackQdrantCollection` with the recorded prior target. It first proves
   the alias still points to the expected green collection; it refuses to
   overwrite a newer operator cutover. Read the alias back after rollback,
   reactivate the recorded prior PostgreSQL generation, and record the result
   in the operator ticket.

## Bounded Runtime Commands

`forja-retrieval` is the runtime entry point for one delivery batch or one
governed query. It never creates a collection, switches an alias, applies
migrations, or takes credentials through arguments. The collection has already
to be lifecycle-verified by the operator procedure above.

Required environment configuration is `FORJA_DATABASE_URL`,
`FORJA_TENANT_ID`, `FORJA_REPOSITORY_ID`, `FORJA_QDRANT_HOST`,
`FORJA_RETRIEVAL_COLLECTION`, `FORJA_S3_BUCKET`, and `FORJA_S3_REGION`.
Bedrock deployments also require `AWS_REGION`. Radeon competition deployments
set `FORJA_RETRIEVAL_EMBEDDING_PROVIDER=local` plus the loopback local
embedding endpoint, model, version, and dimensions. `FORJA_S3_ENDPOINT` and
`FORJA_S3_PATH_STYLE` select a compatible private S3 endpoint when required.
The object-store capability is used only for bounded, content-addressed,
integrity-verified memory reads; object keys and bodies are never accepted as
command arguments. Set `FORJA_QDRANT_GRPC_PORT` only when it differs from
`6334`. A non-loopback Qdrant host also requires `FORJA_QDRANT_TLS=true` and
`FORJA_QDRANT_API_KEY` from a secret boundary. AWS authentication uses the
standard SDK chain, with a workload role as the production target.

Do not bridge Bedrock access by SSHing into Coolify, inspecting a container, or
copying an application API key into the Forja process. A wrapper may govern a
short-lived, allowlisted re-embedding operation, but AWS credentials must be
resolved directly by the Go SDK from the workload identity. See
[`ADR-0016`](../05-decisions/ADR-0016-BEDROCK-TITAN-EMBEDDING-PROVIDER.md) for
the permission boundary and private activation evidence.

The query is a strict `retrieval-query.schema.json` document in a private
file. The configured tenant, repository, and derived Titan generation must
match it. Results and projection receipts are atomically written with mode
`0600`; no command accepts `-` for input or output.

```bash
go run ./cmd/forja-retrieval project-once \
  --worker-id retrieval-projector-a \
  --batch-size 25 \
  --timeout 20s \
  --output /secure/forja/project-once-receipt.json

go run ./cmd/forja-retrieval query \
  --input /secure/forja/query.json \
  --timeout 20s \
  --output /secure/forja/query-result.json

go run ./cmd/forja-retrieval preflight \
  --timeout 20s \
  --output /secure/forja/retrieval-preflight.json
```

Both operations are capped at 30 seconds. A projection leaves a failed or
interrupted delivery for fenced retry; query failures return no authoritative
context. Do not redirect output to a shared log or pass keys through shell
arguments.

`preflight` is the required operator check before a re-embedding job or a
private baseline capture. It proves only that PostgreSQL reached readiness, the
configured Qdrant collection matches the pinned generation contract, and one
synthetic embedding input returned the configured dimensions. Its private
receipt deliberately excludes AWS identity, credentials, hostnames, collection
names, input text, vector values, and provider responses.

Before Qdrant is queried, `forja-retrieval query` reads the aggregate backlog
for the dedicated `qdrant.retrieval` projector from canonical PostgreSQL. The
result includes only the scalar `projection_lag_events`. A non-zero backlog
returns a bounded degraded result with `projection_freshness: "stale"` and no
accepted context. A missing or inactive projector returns `unknown` freshness
and no accepted context. This prevents a live but lagging vector collection
from being presented as authoritative context.

## Failure Handling

- If Qdrant upsert or deletion fails after canonical state changed, do not
  manually restore a vector. The failed delivery retries; the resolver rejects
  stale or tombstoned PostgreSQL receipts meanwhile.
- If Qdrant query or canonical resolution is unavailable, `QueryService`
  returns a bounded degraded receipt with no accepted context, rather than
  broadening scope or trusting cached payloads.
- If the dedicated retrieval projector has any unpublished delivery, a query
  returns a `projection_lag` degraded receipt. Drain or repair the projector;
  do not bypass this gate by calling Qdrant directly.
- `QueryService` bounds a full retrieval request to five seconds by default;
  `ProjectionWorker` bounds each delivery to fifteen seconds. Operators may
  configure shorter limits but never more than thirty seconds. A timed-out
  query returns a degraded receipt; a timed-out delivery is retained for
  fenced retry and cannot advance its checkpoint.
- If the physical collection verification fails, do not run a projector or
  switch an alias. Build a new generation and investigate the mismatch.
- If Qdrant alias read-back succeeds but PostgreSQL generation activation
  fails, pause retrieval traffic for that alias, preserve both physical
  collections, re-observe the alias, and repair/retry the canonical lifecycle
  transition. Do not guess which generation is serving or retire either one.
- If a delivery reaches the configured retry ceiling, repair the dependency,
  retain the dead-letter evidence, call `RequeueProjectionDelivery`, and replay
  with a new fenced claim. Requeue accepts only `dead` deliveries and resets
  their attempt budget without deleting the original dead-letter record. Never
  advance a checkpoint manually.

## Evidence Required to Close Recovery

- Pinned image reference and client dependency version.
- Collection verification result without secrets or payload bodies.
- Projector configuration hash, checkpoint before/after, count of published and
  dead deliveries, and bounded sampled canonical point IDs.
- Alias target before/after, the observation-window decision, and the operator
  approval of the switch or guarded rollback.
- A successful governed query receipt and an unavailable-Qdrant degraded
  receipt.
