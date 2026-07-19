# Sprint 07 Artifacts, Conversations, and Memory Plan

Status: In progress

The object-storage boundary is governed by
[ADR-0013](../05-decisions/ADR-0013-CONTENT-ADDRESSED-OBJECT-STORAGE.md).
Memory authority is governed by
[ADR-0014](../05-decisions/ADR-0014-GOVERNED-MEMORY-PROMOTION.md).

## Outcome

Preserve large work products and conversation evidence outside PostgreSQL
while keeping lifecycle, provenance, authorization, retention, and memory
promotion canonical and auditable inside PostgreSQL.

## Trust Boundary

- PostgreSQL is authoritative for artifact identity, lifecycle, references,
  retention, conversations, messages, candidates, memories, events, and
  tombstones.
- S3-compatible storage preserves immutable bodies. Object existence alone
  cannot authorize, validate, promote, or resurrect canonical state.
- End users and agents never select an endpoint, bucket, object key, encryption
  key, tenant prefix, or repository prefix.
- Every body is bound to an expected SHA-256 and size before publication and is
  verified after transport.
- Raw messages are immutable evidence. Corrections append a new message rather
  than rewriting history.
- A memory candidate is untrusted input. Only a distinct authenticated
  promotion command may create a durable memory record.
- Tombstones are committed before external deletion or derived-store removal.

## Artifact State Machine

```text
reserved
  -> uploading
  -> verified
  -> active

reserved | uploading | verified -> failed
active -> tombstoned -> purged
active -> superseded -> tombstoned -> purged
```

The database transaction and object-store write form a recoverable saga, not a
distributed transaction. Interruption leaves a journaled state that can be
reconciled without inventing success.

## Memory State Machine

```text
candidate.proposed
  -> candidate.promoted -> memory.active
  -> candidate.rejected
  -> candidate.expired

memory.active
  -> memory.superseded
  -> memory.expired
  -> memory.tombstoned
```

Promotion requires an authenticated `human` or policy-owning `system`
principal with dedicated authority. Agent-authored chat cannot satisfy that
gate.

## Delivery Sequence

### 1. Contracts and persistence boundary

- [ ] Publish strict conversation, message, memory-candidate, memory-record,
  and artifact-bundle-manifest schemas.
- [ ] Preserve the released `artifact.schema.json` 1.0 contract unchanged.
- [ ] Add semantic Go validation for ordering, hashes, lifecycle coupling, and
  duplicate references.
- [ ] Record the PostgreSQL/S3 saga and memory-promotion decisions in ADRs.
- [ ] Define bounded object, manifest, message, and citation limits.

### 2. PostgreSQL authority

- [ ] Add migration 007 with artifact blobs, artifact records, operation
  journal, conversations, messages, content parts, citations, memory
  candidates, memory records, supersession edges, and bundle entries.
- [ ] Enforce tenant and repository ownership through composite keys.
- [ ] Protect immutable messages, content parts, citations, and bundle entries
  from update or delete.
- [ ] Commit aggregate state, append-only event, outbox row, and idempotency
  receipt atomically.
- [ ] Extend schema verification and rollback compatibility checks.

### 3. Content-addressed object storage

- [ ] Add an S3 adapter behind a narrow object-store interface.
- [ ] Derive keys only from the operator-bound prefix and canonical SHA-256.
- [ ] Use conditional writes and provider-side SHA-256 where supported.
- [ ] Re-read every first publication and verify exact bytes before activation.
- [ ] Treat an existing key as idempotent only after size, metadata, and body
  verification.
- [ ] Reconcile interrupted uploads, missing objects, mismatched objects, and
  canonical records left before finalization.

### 4. Conversations and memory

- [ ] Append conversations, messages, ordered content parts, and citations
  without storing raw body bytes in PostgreSQL.
- [ ] Close a conversation through an immutable transcript bundle manifest.
- [ ] Propose memory only from canonical message and artifact references.
- [ ] Promote memory only through dedicated human or policy authority.
- [ ] Implement rejection, supersession, expiry, and tombstoning.
- [ ] Emit projection-safe tombstone events before deletion.

### 5. Retention, manifests, and observability

- [ ] Publish immutable, canonically ordered evidence bundle manifests.
- [ ] Prevent purge while any live canonical reference remains.
- [ ] Add bounded metrics and traces for object operations and memory lifecycle.
- [ ] Exclude raw content, keys, credentials, and storage errors from telemetry.
- [ ] Document backup, restore, reconciliation, and credential rotation.

### 6. Acceptance and closure

- [ ] Archive and restore a multi-object evidence bundle against PostgreSQL and
  a real S3-compatible endpoint.
- [ ] Prove idempotent concurrent publication of identical bytes.
- [ ] Prove hash mismatch, partial upload, missing object, and cross-tenant
  references fail closed.
- [ ] Prove chat cannot promote itself into durable memory.
- [ ] Prove tombstones precede purge and derived deletion requests.
- [ ] Rehearse rollback to the authoritative Sprint 06 commit.
- [ ] Run race, integration, security, and independent full-range reviews.
- [ ] Publish a fail-closed Sprint 07 candidate and close it through the
  two-phase protocol.

## Acceptance Evidence

- Contract fixtures and semantic cross-field tests.
- PostgreSQL migration, concurrency, tenant-isolation, and append-only tests.
- S3 integration tests for conditional create, full-body verification, retry,
  corruption, disappearance, and purge.
- End-to-end conversation-to-candidate-to-promoted-memory evidence.
- Database and object-store backup/restore with reference verification.
- An isolated rollback drill against Sprint 06.

## Out of Scope

- Compiler-derived code lineage belongs to Sprint 08.
- Embeddings, Qdrant collections, and semantic ranking belong to Sprint 09.
- Neo4j projection belongs to Sprint 10.
- Context-pack assembly belongs to Sprint 11.
- Object storage is not a secret manager and is not exposed directly to
  workers or model-generated credentials.

## Rollback

Disable new artifact and memory intake, allow in-flight object operations to
settle or mark them reconciliation-required, and retain every object and
tombstone. Migration 007 may roll back only when its canonical tables, related
events, and idempotency receipts are empty. Once Sprint 07 history exists,
rollback is a forward-repair deployment to the Sprint 07 schema; never delete
objects or receipts to force an older binary to start.
