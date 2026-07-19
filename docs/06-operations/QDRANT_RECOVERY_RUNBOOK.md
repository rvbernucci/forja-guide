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
5. Re-register or resume the `qdrant.retrieval` projector with the same
   configuration hash. Its independent delivery ledger replays canonical
   history. Stable point IDs make duplicate upserts idempotent.
6. Compare the durable checkpoint with the maximum published outbox prefix and
   verify sampled point IDs against active PostgreSQL symbol/source hashes.
7. Only after verification, use `CutoverQdrantCollection` to direct the
   serving alias to the rebuilt physical collection. It verifies the green
   physical contract, records the prior alias observation, performs one atomic
   Qdrant alias update, and reads the alias back. Preserve the previous
   collection during the observation window.
8. Only after the alias read-back succeeds, call
   `ActivateRetrievalGeneration` for the matching registered PostgreSQL
   generation. It drains the prior active generation transactionally. Do not
   retire the prior generation while it remains the guarded rollback target.
9. If the observed serving behavior requires rollback, call
   `RollbackQdrantCollection` with the recorded prior target. It first proves
   the alias still points to the expected green collection; it refuses to
   overwrite a newer operator cutover. Read the alias back after rollback,
   reactivate the recorded prior PostgreSQL generation, and record the result
   in the operator ticket.

## Failure Handling

- If Qdrant upsert or deletion fails after canonical state changed, do not
  manually restore a vector. The failed delivery retries; the resolver rejects
  stale or tombstoned PostgreSQL receipts meanwhile.
- If Qdrant query or canonical resolution is unavailable, `QueryService`
  returns a bounded degraded receipt with no accepted context, rather than
  broadening scope or trusting cached payloads.
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
