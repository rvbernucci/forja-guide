# Sprint 09 Governed Retrieval Plan

Status: In progress. Authorized by the authoritative
[Sprint 08 receipt](../evidence/sprint-08/close-receipt.json).

The retrieval boundary is governed by
[ADR-0002](../05-decisions/ADR-0002-POSTGRES-SYSTEM-OF-RECORD.md),
[ADR-0003](../05-decisions/ADR-0003-DERIVED-INTELLIGENCE-STORES.md), and
[ADR-0015](../05-decisions/ADR-0015-GOVERNED-HYBRID-RETRIEVAL.md).

## Outcome

Discover relevant canonical entities through exact lexical and dense semantic
retrieval without allowing similarity, projection state, or a derived-store
payload to establish identity, authority, freshness, or access.

## Trust Boundary

- PostgreSQL remains authoritative for tenant, repository, source lifecycle,
  stable entity identity, source hash, source commit, and projection progress.
- Qdrant contains replaceable points. Every result is an untrusted candidate
  until it resolves to current canonical PostgreSQL state.
- Hard tenant, repository, lifecycle, authority, commit, language, and kind
  filters are constructed by trusted code and sent with every retrieval path.
- Query text, embeddings, sparse terms, Qdrant responses, payloads, scores, and
  model metadata are untrusted data with explicit size and shape limits.
- Embedding providers receive only the governed textual card or query. Provider
  output must match the configured model, version, dimensions, and finite-value
  contract before publication or search.
- Projection writes consume canonical outbox history through independent,
  fenced per-projector delivery state. They never participate in the source
  transaction and cannot mark the canonical event globally authoritative.
- A stale, ambiguous, malformed, unreachable, or partially rebuilt projection
  fails closed or returns an explicit degraded result. It never broadens scope.

## Retrieval Point

Each point binds:

```text
point_id                 deterministic from collection generation + entity
tenant_id                mandatory filter and canonical ownership
repository_id            mandatory filter and canonical ownership
entity_id                stable canonical identity
artifact_family          symbol, decision, test, memory, or incident
source_commit            exact committed source boundary when applicable
source_hash              exact canonical content digest
status                   active lifecycle only for normal retrieval
authority_class          source authority, never inferred from similarity
stale                     explicit projection state
language / symbol_kind   optional hard filters
proof_refs               canonical evidence references
graph_node_ids           future Sprint 10 join hints only
embedding_model          configured provider identity
embedding_version        immutable model/prompt contract version
embedding_dimensions     exact dense-vector dimensions
embedded_at              projection provenance, not freshness authority
```

The projected text is a deterministic card, not an arbitrary raw-file chunk.
Raw code bodies are excluded by default. Symbol cards include qualified name,
kind, signature, repository path, declaration location, flags, and bounded
documentation evidence.

## Collection Topology

- One shared logical retrieval collection is addressed through a stable alias.
- Versioned physical collections contain named `dense` and `sparse` vectors.
- Filtered fields receive payload indexes before point ingestion.
- Strict mode rejects unindexed filtering and unbounded query shapes.
- A collection generation binds schema, embedding model, version, dimensions,
  sparse encoder version, distance metric, and payload layout.
- Re-embedding builds a green collection from canonical sources, verifies
  counts and sampled source hashes, then atomically switches the alias.
- The old collection remains available for bounded rollback until the new
  generation passes its observation window.

## Delivery Sequence

### 1. Public contracts and deterministic cards

- [x] Publish strict retrieval point, query, and result JSON schemas.
- [x] Add Go contract types and semantic validation for scopes, filters,
  vectors, ranks, finite scores, model descriptors, and bounded collections.
- [x] Build the generic deterministic card boundary and the first canonical
  symbol-card adapter; decision, test, memory, and incident source adapters
  remain pending with their owning canonical models.
- [x] Generate stable point IDs and byte-stable card text from canonical input.
- [x] Implement a versioned deterministic lexical encoder for sparse vectors.

### 2. Independent projection delivery

- [x] Add migration 009 for projector registrations, fenced per-projector
  deliveries, retrieval generation records, and canonical point provenance.
- [x] Fan out future canonical outbox rows to every active projector in the
  same PostgreSQL transaction that inserts the outbox row.
- [x] Backfill a newly registered projector from existing canonical outbox
  history under the shared watermark barrier.
- [x] Claim, retry, dead-letter, and complete deliveries with database-time
  leases and fencing tokens.
- [x] Advance checkpoints only across a contiguous completed prefix; a failed
  or missing delivery must prevent a false freshness claim.

### 3. Qdrant adapter and collection lifecycle

- [x] Use the version-pinned official Qdrant Go client.
- [x] Create protocol plans for named dense and sparse vectors plus indexed
  filter payloads. The operator adapter creates physical collections, applies
  required indexes, and switches one alias atomically; verification and
  rollback wiring remain pending.
- [x] Require TLS for non-loopback endpoints and obtain API keys only from an
  environment or operator secret boundary.
- [x] Upsert points idempotently from stable IDs and source hashes. The writer
  waits for Qdrant acknowledgement before its PostgreSQL delivery can advance.
- [x] Project superseded snapshot tombstones and physically delete retired
  points only after the canonical lifecycle receipt is durable. A failed
  delete retries while the canonical resolver remains fail-closed.
- [x] Verify physical collection generation, vector dimensions, strict filtering,
  and payload schema before an operator enables projection work.
- [ ] Verify the serving alias target and its observation window before retiring
  a prior generation.
- [ ] Implement build, verify, atomic alias switch, observation, and rollback
  for blue-green re-embedding.

### 4. Governed hybrid retrieval

- [x] Apply mandatory access and lifecycle filters to dense and sparse queries
  before either rank list is produced.
- [x] Fuse bounded dense and sparse rankings with explainable weighted
  reciprocal rank fusion.
- [x] Execute both Qdrant rank paths, treat their payloads as untrusted, and
  return a bounded degraded receipt when either path is unavailable.
- [x] Define and test the fail-closed canonical resolver boundary for identity,
  source hash, source commit, lifecycle, scope, and duplicate checks.
- [x] Resolve symbol candidates against canonical PostgreSQL identity, source
  hash, source commit, lifecycle, and repository authority. Other card
  families remain absent until their canonical adapters exist.
- [x] Reject stale, missing, cross-scope, hash-mismatched, or duplicate-identity
  candidates and expose bounded rejection reasons in a receipt.
- [ ] Return alternatives for genuine ambiguity rather than inventing a link.
- [x] Degrade to explicit bounded gaps when either Qdrant rank path or the
  canonical resolver is unavailable. Canonical exact-lookup fallback remains
  a future optional availability optimization.

### 5. Runtime, observability, and operations

- [ ] Add a bounded one-shot projection and query CLI without embedding secrets
  in process arguments or output.
- [ ] Add low-cardinality metrics and traces for candidates, resolutions,
  rejections, latency, checkpoint lag, projection retries, and collection
  generation.
- [ ] Keep query text, vectors, entity names, paths, and payload bodies out of
  logs, metrics, and traces.
- [x] Publish a version-pinned local Qdrant profile and a recovery runbook.
- [ ] Prove safe shutdown, bounded deadlines, retry, dead-letter repair, and
  full rebuild after deleting the derived collection.

### 6. Evaluation and closure

- [ ] Create tuning, holdout, OOD, leakage, stale, and adversarial fixtures.
- [ ] Compare lexical-only, dense-only, unweighted RRF, and weighted RRF.
- [ ] Measure Recall@K, Precision@K, MRR, nDCG, entity-resolution accuracy,
  stale rejection, cross-tenant leakage, latency, and projection freshness.
- [ ] Prove identifier-heavy queries improve with lexical retrieval and
  conceptual queries improve with dense retrieval.
- [ ] Prove every unauthorized or stale fixture returns zero accepted results.
- [ ] Run unit, race, integration, security, reproducibility, rollback, and
  independent full-range reviews.
- [ ] Publish a fail-closed Sprint 09 closure candidate for immutable review.

## Acceptance Evidence

- JSON Schema fixtures plus semantic cross-field and finite-number tests.
- Stable card, sparse-vector, point-ID, and weighted-RRF golden vectors.
- Qdrant protocol tests proving mandatory filters on both retrieval paths.
- PostgreSQL concurrency tests for registration, fan-out, claims, fencing,
  contiguous checkpoints, replay, and dead letters.
- Real Qdrant integration tests for collection creation, payload indexes,
  upsert, delete, query, alias swap, rollback, and rebuild.
- Cross-tenant, cross-repository, stale-commit, hash-mismatch, tombstone,
  malformed-payload, timeout, and provider-failure tests.
- A retrieval evaluation report with dataset and policy hashes.
- A clean-host rebuild drill starting from PostgreSQL, canonical artifacts,
  and an empty Qdrant instance.

## Out of Scope

- Neo4j projection and graph traversal belong to Sprint 10.
- Context-pack assembly, path ranking, and token budgeting belong to Sprint 11.
- Retrieval candidates never authorize writes or memory promotion.
- Fine-tuning or training an embedding model is not required; model selection
  remains a versioned deployment choice measured by the evaluation harness.
- Qdrant snapshots are optional operational acceleration, not authoritative
  backup. Rebuild from canonical sources remains mandatory.

## Rollback

Stop projection intake, preserve canonical outbox and per-projector delivery
history, and atomically return the stable alias to the last verified collection
generation. If Qdrant is unavailable or corrupt, disable semantic retrieval and
serve only bounded canonical exact lookup with explicit degraded freshness.
Migration 009 may roll back only before projector registrations, deliveries,
generation receipts, or checkpoints exist. After history exists, rollback is a
forward repair that preserves the canonical delivery ledger.
