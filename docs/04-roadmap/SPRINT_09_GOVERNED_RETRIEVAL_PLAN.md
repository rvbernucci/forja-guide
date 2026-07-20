# Sprint 09 Governed Retrieval Plan

Status: Implementation complete. Closure state is controlled by the mutually
exclusive candidate or close receipt under `docs/evidence/sprint-09`. Private
quality evaluation and production activation are explicitly transferred to
Sprint 10; this document does not claim those measurements were executed.

The retrieval boundary is governed by
[ADR-0002](../05-decisions/ADR-0002-POSTGRES-SYSTEM-OF-RECORD.md),
[ADR-0003](../05-decisions/ADR-0003-DERIVED-INTELLIGENCE-STORES.md), and
[ADR-0015](../05-decisions/ADR-0015-GOVERNED-HYBRID-RETRIEVAL.md),
[ADR-0016](../05-decisions/ADR-0016-BEDROCK-TITAN-EMBEDDING-PROVIDER.md),
[ADR-0017](../05-decisions/ADR-0017-GOVERNED-MEMORY-RETRIEVAL-BODIES.md), and
[ADR-0018](../05-decisions/ADR-0018-GOVERNED-INCIDENT-RETRIEVAL.md).

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
- The selected implementation provider is Bedrock Titan Text Embeddings v2
  through the AWS SDK for Go v2. Its production identity is an AWS workload
  role; legacy Coolify bearer credentials are outside the Forja adapter.
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
graph_node_ids           future Sprint 11 join hints only
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
- [x] Build the generic deterministic card boundary plus canonical symbol,
  test, decision, memory, and incident adapters. A test card is emitted only for a
  symbol canonically marked as a test and resolves only while that flag remains
  true. Decisions and memories are re-derived from PostgreSQL. A memory also
  requires an active exact artifact/object binding, an integrity-verified
  provider version, and the bounded redacted body contract in
  [ADR-0017](../05-decisions/ADR-0017-GOVERNED-MEMORY-RETRIEVAL-BODIES.md).
  Incident cards are derived exclusively from the matching immutable terminal
  attempt event; they include only safe classification, identifiers, and
  evidence hashes under
  [ADR-0018](../05-decisions/ADR-0018-GOVERNED-INCIDENT-RETRIEVAL.md).
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
  required indexes, verifies the green physical generation, switches one alias
  atomically, reads the alias target back, and supports guarded rollback.
- [x] Require TLS for non-loopback endpoints and obtain API keys only from an
  environment or operator secret boundary.
- [x] Upsert points idempotently from stable IDs and source hashes. The writer
  waits for Qdrant acknowledgement before its PostgreSQL delivery can advance.
- [x] Project superseded snapshot tombstones and physically delete retired
  points only after the canonical lifecycle receipt is durable. A failed
  delete retries while the canonical resolver remains fail-closed.
- [x] Verify physical collection generation, vector dimensions, strict filtering,
  and payload schema before an operator enables projection work.
- [x] Verify the serving alias target after cutover and before a guarded
  rollback. PostgreSQL records registration, activation, draining, and safe
  retirement of generations.
- [x] Implement automated Qdrant blue-green build verification, atomic alias
  switch, observation, and rollback coverage. Public exact-basis CI runs the
  integration test against digest-pinned Qdrant v1.18.2: it creates two
  physical generations, verifies first alias creation and atomic
  delete-and-create replacement, verifies both targets, rolls back with
  compare-before-switch protection, and deletes its temporary state.

### 4. Governed hybrid retrieval

- [x] Apply mandatory access and lifecycle filters to dense and sparse queries
  before either rank list is produced.
- [x] Fuse bounded dense and sparse rankings with explainable weighted
  reciprocal rank fusion.
- [x] Execute both Qdrant rank paths, treat their payloads as untrusted, and
  return a bounded degraded receipt when either path is unavailable.
- [x] Define and test the fail-closed canonical resolver boundary for identity,
  source hash, source commit, lifecycle, scope, and duplicate checks.
- [x] Resolve symbol, test, decision, memory, and incident candidates against canonical
  PostgreSQL identity, source hash, source commit where applicable, lifecycle,
  and repository authority. Test cards additionally require the canonical
  `is_test` flag; memory cards independently re-read their exact authorized
  object version and rebuild the redacted card before acceptance. Incident cards
  independently re-read the exact attempt and immutable event before acceptance.
- [x] Reject stale, missing, cross-scope, hash-mismatched, or duplicate-identity
  candidates and expose bounded rejection reasons in a receipt.
- [x] Return bounded, scope-authorized canonical entity-ID alternatives for a
  genuine ambiguity while rejecting every ambiguous result as context.
- [x] Degrade to explicit bounded gaps when either Qdrant rank path or the
  canonical resolver is unavailable. Before any rank path runs, the runtime
  now reads the dedicated Qdrant projector backlog from PostgreSQL: any
  non-zero lag returns a `stale` degraded receipt with no accepted context,
  and an unavailable projector status returns an `unknown` degraded receipt.
  Canonical exact-lookup fallback remains a future optional availability
  optimization.

### 5. Runtime, observability, and operations

- [x] Add a Bedrock Titan v2 embedding adapter using the AWS SDK for Go v2,
  standard AWS credential chain, bounded request/response handling, and
  fail-closed provider output validation. The opt-in live compatibility probe
  verifies the selected model and a real 1024-dimension vector without printing
  card text or vector values. Production activation still requires a
  workload-role deployment and private evaluation evidence.
- [x] Add bounded `forja-retrieval project-once` and `forja-retrieval query`
  commands. Both require a bounded deadline and private output file; database,
  Qdrant, AWS region, and secret configuration are read only from their
  environment boundaries. The command never migrates PostgreSQL, creates a
  Qdrant collection, accepts credentials as flags, or prints query text.
- [x] Add low-cardinality metrics and traces for latency, checkpoint lag, and
  collection generation. Candidate, resolution, and delivery outcomes are
  instrumented already; generation metrics expose only bounded lifecycle
  counts, never generation IDs or collection names.
- [x] Keep query text, vectors, entity names, paths, and payload bodies out of
  retrieval metrics and traces.
- [x] Publish a version-pinned local Qdrant profile and a recovery runbook.
- [x] Implement and mechanically verify safe shutdown, bounded deadlines, retry,
  and full rebuild after deleting the derived collection. Search and each
  projection delivery now have
  bounded 5-second/15-second defaults (maximum 30 seconds) with timeout tests;
  cooperative shutdown leaves an in-flight delivery unacknowledged for lease
  recovery rather than writing through a cancelled context; fenced retry,
  dead-letter repair, and canonical fail-closed ledger reset/replay are covered
  by PostgreSQL integration tests. An opt-in operational drill can delete a
  disposable Qdrant collection, reset canonical provenance and the delivery
  ledger, recreate the collection, and verify replay. Its execution is not a
  Sprint 09 closure claim unless a separate immutable receipt is recorded.

### 6. Evaluation and closure

- [x] Publish the public synthetic corpus, outcome fixtures, strict private
  plan/capture contracts, schema-validated offline scoring CLI, and
  deterministic scorer without exposing private labels to the runtime.
- [x] Implement exact lexical-only, dense-only, unweighted RRF, and weighted
  RRF execution through the governed runtime.
- [x] Transfer private tuning, holdout, OOD, leakage, stale, and adversarial
  corpus execution plus Recall@K, Precision@K, MRR, nDCG, entity-resolution,
  latency, and freshness measurement to Sprint 10.
- [x] Keep production activation disabled until Sprint 10 proves comparative
  lexical/dense benefit and accepts a versioned policy.
- [x] Prove public unauthorized, stale, malformed, and cross-scope fixtures
  return zero accepted canonical results.
- [x] Run unit, race, integration, security, reproducibility, rollback, and
  full-range implementation checks recorded by the Sprint evidence package.
- [x] Publish a fail-closed Sprint 09 closure candidate for immutable review.

### Closure Scope Amendment

Sprint 09 closes the implemented governed retrieval boundary: contracts,
canonical cards, independent projection delivery, Qdrant lifecycle, hybrid
candidate execution, canonical resolution, runtime commands, observability,
recovery, public fixtures, and private evaluation tooling. It does not close
production retrieval quality or provider activation.

Sprint 10 owns the private corpus, real four-baseline comparison, policy
selection, local Radeon embedding provider, ROCm inference deployment, and
quality/latency/freshness gates. Until Sprint 10 accepts those results, Forja
must remain in exact/source-backed or explicitly degraded mode.

### Current Pre-Closure Evidence

This section is non-authoritative progress evidence, not a closure candidate.
It cannot authorize Sprint 10.

- On implementation commits `ecc0fcd`, `fb408b9`, `cb2503d`, `19cdfcf`, and
  `93c899a`, `make validate` passed locally: Go module verification, `go vet`,
  the full Go unit and race suites, reproducible `linux/amd64` and
  `linux/arm64` builds, kernel/MCP/worker smoke tests, 55 Python tests, and
  repository validation. The later runs validate source-bound versus
  global-card scope handling, reject global projections with an unexpected
  source commit, prove that the lexical-only and dense-only baseline policies
  do not invoke their disabled retrieval paths, and align the canonical
  PostgreSQL integration fixtures with immutable index evidence.
- On `40a2326`, the same full validation suite passed after the runtime began
  gating retrieval on its dedicated Qdrant projector backlog. A disposable
  PostgreSQL drill proved that an inactive projector is unavailable, a
  published delivery reports zero lag, and a new fan-out delivery reports a
  positive lag. Unit tests prove that stale or unavailable freshness prevents
  any embedder or Qdrant request from running.
- `go run ./cmd/forja-retrieval-eval` scores both a single frozen ranking
  capture and an immutable four-baseline comparison. The public synthetic
  comparison verifies that lexical-only and dense-only can retain distinct
  quality profiles while every public stale and cross-tenant case is rejected.
  Its capture contract now requires per-case bounded latency and projection
  lag, records accepted-versus-resolved entity counts, and reports stale,
  cross-tenant, and unauthorized rejection evidence separately. This makes the
  remaining private evaluation capable of measuring every listed Sprint 09
  retrieval criterion without exposing its corpus to the runtime.
- `forja-retrieval capture` now accepts only a private, label-free query plan
  and runs the exact four required baseline policies through the governed
  runtime. It validates the plan and comparison schemas, enforces private-file
  permissions and bounded per-query/whole-run deadlines, captures only
  canonical accepted entity IDs plus scalar latency/freshness telemetry, and
  writes atomically. Its companion private corpus remains a separate operator
  capability. No private capture has yet been run, so this is implementation
  readiness rather than quality evidence.
- `forja-retrieval preflight` now provides the required bounded readiness
  check before a re-embedding job or private capture. It verifies PostgreSQL
  readiness, resolves an optional serving alias to its physical Qdrant target,
  verifies the exact collection generation contract, and requests one
  synthetic 1024-dimension Titan embedding. Its schema-validated private
  receipt excludes credentials, AWS identity, hosts, collection names, text,
  vectors, and provider responses. No workload-role preflight receipt has yet
  been captured.
- Sprint 09 makes no vulnerability-reachability claim because no hash-pinned
  scanner receipt is committed for this candidate.
- The public fixture remains a contract smoke test only. No tuning, holdout,
  OOD, adversarial, production corpus, private label, or provider result is
  represented by these results.
- A sanitized staging probe reached the VPS but its environment checker is
  intentionally `sudo`-gated. No privileged operation or Coolify bearer
  extraction was attempted from this workstation. The opt-in Bedrock
  compatibility probe exists to validate a synthetic 1024-dimension response;
  Sprint 09 makes no provider-execution claim without a committed receipt.
- The repository contains opt-in PostgreSQL and Qdrant integration drills for
  blue-green cutover, alias rollback, deletion, canonical reset, and replay.
  Public CI run `29722787449` on basis commit `8602b217` executes PostgreSQL
  durability and the digest-pinned live Qdrant collection, query, atomic alias
  replacement, rollback, deletion, and cleanup lifecycle. Deployment-host
  replay and production activation remain Sprint 10 concerns.
- The remaining live evidence must use a deployment workload identity and the
  access-controlled private evaluation boundary, not copied runtime
  credentials. Those results are required by Sprint 10 before production or
  competition-profile activation, not represented as completed Sprint 09 work.
- A historical operator note reports that Hostinger Builder Plane dependencies
  were checked through governed role wrappers and that Qdrant and Neo4j were
  ready and loopback-only at that time. No immutable underlying command receipt
  is committed, so it proves neither current readiness nor a verified boundary.
  The note and remaining activation prerequisites are retained in
  [`VPS_RETRIEVAL_RUNTIME_RECEIPT.md`](../06-operations/VPS_RETRIEVAL_RUNTIME_RECEIPT.md).

## Acceptance Evidence

- JSON Schema fixtures plus semantic cross-field and finite-number tests.
- Stable card, sparse-vector, point-ID, and weighted-RRF golden vectors.
- Qdrant protocol tests proving mandatory filters on both retrieval paths.
- PostgreSQL concurrency tests for registration, fan-out, claims, fencing,
  contiguous checkpoints, replay, and dead letters.
- Opt-in real Qdrant integration tests for collection creation, payload
  indexes, upsert, delete, query, alias swap, rollback, and rebuild.
- Cross-tenant, cross-repository, stale-commit, hash-mismatch, tombstone,
  malformed-payload, timeout, and provider-failure tests.
- The schema-validated public synthetic evaluation report with dataset and
  policy hashes; private quality reports are owned by Sprint 10.
- Automated rebuild coverage starting from canonical PostgreSQL history and an
  empty disposable Qdrant instance. A deployment-host drill and its private
  receipt are owned by Sprint 10 activation.

## Out of Scope

- Neo4j projection and graph traversal belong to Sprint 11 after Sprint 10
  establishes the local Radeon runtime and retrieval quality baseline.
- Context-pack assembly, path ranking, and token budgeting belong to Sprint 11.
- Retrieval candidates never authorize writes or memory promotion.
- Fine-tuning or training an embedding model is not required for Sprint 09.
  Sprint 10 selects a local Radeon-compatible embedding model through the
  versioned evaluation harness for the AMD competition profile.
- Qdrant snapshots are optional operational acceleration, not authoritative
  backup. Rebuild from canonical sources remains mandatory.

## Rollback

Stop projection intake, preserve canonical outbox and per-projector delivery
history, and atomically return the stable alias to the last verified collection
generation. If Qdrant is unavailable or corrupt, disable semantic retrieval;
the governed query path returns bounded explicit degradation for unavailable
rank paths. If embedding generation fails, the query fails explicitly and no
result file is written. A canonical exact-lookup fallback is not implemented.
Migration 009 may roll back only before projector registrations, deliveries,
generation receipts, or checkpoints exist. After history exists, rollback is a
forward repair that preserves the canonical delivery ledger.
