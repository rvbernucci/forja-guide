# Sprint 08 Deterministic Indexing Plan

Status: In progress

The indexing boundary is governed by
[ADR-0005](../05-decisions/ADR-0005-DETERMINISTIC-CODE-LINEAGE.md).
Canonical and derived-store authority remain governed by
[ADR-0002](../05-decisions/ADR-0002-POSTGRES-SYSTEM-OF-RECORD.md) and
[ADR-0003](../05-decisions/ADR-0003-DERIVED-INTELLIGENCE-STORES.md).

## Outcome

Produce byte-stable repository, file, symbol, and relation metadata from a
pinned Git commit using declared compiler or parser evidence. Persist identity,
lifecycle, evidence, and invalidation state canonically in PostgreSQL while
keeping large immutable snapshots in the governed artifact store.

## Trust Boundary

- PostgreSQL is authoritative for snapshot identity, lifecycle, source commit,
  adapter version, file hashes, symbol identity, relation evidence, deltas, and
  invalidation state.
- Git object IDs and exact source bytes establish the input boundary. The
  indexer may not read uncommitted files when indexing a committed snapshot.
- Language adapters are untrusted producers. Their output must satisfy the
  common schema, semantic validation, repository scope, source hashes, and
  deterministic-ID rules before canonical publication.
- A relation is authoritative only when tied to declared compiler, parser,
  schema, test, or runtime evidence. Semantic similarity is not accepted by
  this Sprint.
- Toolchain name, version, configuration digest, and execution result are part
  of provenance. A changed toolchain cannot silently replay an older result.
- Object storage holds large immutable snapshot bodies. Object existence never
  establishes canonical index state.
- Qdrant and Neo4j remain absent from the write path. Their later projections
  must be rebuildable from PostgreSQL and canonical artifacts.

## Snapshot State Machine

```text
proposed -> extracting -> validated -> active
    |           |            |
    +-----------+------------+-> failed

active -> superseded
active -> invalidated
```

Only one active snapshot may exist for the same tenant, repository, commit,
configuration digest, and adapter set. Replay is valid only when the complete
request digest matches.

## Identity Rules

- Repository snapshots bind tenant, repository, commit, tree, configuration,
  and ordered adapter descriptors.
- File IDs bind repository, commit, normalized repository-relative path, and
  exact source hash.
- Symbol IDs bind the file ID, language, kind, qualified name, declaration
  range, and signature digest.
- Relation IDs bind source, relation kind, target, evidence class, locator, and
  evidence digest.
- Canonical JSON uses sorted object keys, deterministic arrays, UTF-8, and no
  generated timestamps in identity material.
- Repository paths are slash-normalized, relative, free of traversal, and
  case-preserving. Case-colliding paths fail closed.

## Evidence Classes

```text
candidate_static
  -> confirmed_static
  -> confirmed_behavioral
  -> runtime_observed
```

`candidate_semantic` remains a future discovery-only class and is rejected by
canonical Sprint 08 publication.

## Delivery Sequence

### 1. Contracts and canonicalization

- [ ] Publish strict `RepositorySnapshot`, `FileCard`, `SymbolCard`, and
  `RelationEvidence` JSON schemas.
- [ ] Add Go contract types and semantic validation for hashes, paths, ranges,
  evidence classes, bounded collections, and cross-reference closure.
- [ ] Implement canonical ordering, byte-stable serialization, and stable ID
  generation with golden vectors.
- [ ] Define adapter manifests, supported language versions, and deterministic
  capability declarations.

### 2. Git boundary and change sets

- [ ] Read committed blobs and trees through bounded Git commands without
  consulting the mutable worktree.
- [ ] Validate commit reachability and reject submodule, symlink, traversal,
  case-collision, oversized-file, and unsupported-encoding ambiguity safely.
- [ ] Generate deterministic added, modified, deleted, and renamed path sets.
- [ ] Bind every source card to its Git blob ID and SHA-256 body hash.

### 3. Language adapters

- [ ] Implement Go extraction with `go/packages`, `go/types`, and `go/ast`.
- [ ] Implement TypeScript/JavaScript extraction with the TypeScript Compiler
  API and pinned module-resolution behavior.
- [ ] Implement Python structural extraction with the standard AST and a
  declared syntax-version boundary.
- [ ] Normalize declarations, imports, exports, signatures, references, tests,
  routes, schemas, generated markers, diagnostics, and unresolved targets into
  the common contracts.
- [ ] Sandbox adapter process execution with bounded input, output, time,
  environment, and repository scope.

### 4. Canonical persistence and publication

- [ ] Add migration 008 for snapshots, files, symbols, relations, adapter runs,
  deltas, and invalidations using tenant/repository composite authority.
- [ ] Commit validated snapshot metadata, append-only events, outbox records,
  and idempotency receipts atomically.
- [ ] Publish large canonical snapshot payloads through the Sprint 07 artifact
  saga and bind exact artifact hashes in PostgreSQL.
- [ ] Enforce one active equivalent snapshot and deterministic replay under
  concurrent publication.
- [ ] Prevent deletion or mutation of evidence referenced by active snapshots.

### 5. Incremental invalidation and observability

- [ ] Compute the affected region from changed files and proven import,
  reference, schema, test, and route relations.
- [ ] Reuse unchanged cards only after exact source, adapter, configuration,
  and dependency digests match.
- [ ] Emit deterministic entity/relation deltas and projection-safe outbox
  events without writing Qdrant or Neo4j directly.
- [ ] Add bounded metrics and traces for files, symbols, relations,
  diagnostics, cache reuse, invalidations, and adapter failures.
- [ ] Exclude source bodies, qualified names, paths, tool output, and secrets
  from low-cardinality telemetry.

### 6. Acceptance and closure

- [ ] Build golden Go, TypeScript, and Python repositories with cross-version,
  malformed-source, generated-file, and rename fixtures.
- [ ] Prove unchanged commits produce byte-identical cards, IDs, relations,
  deltas, and snapshot artifacts across repeated runs.
- [ ] Prove changed files invalidate only the mechanically justified region.
- [ ] Prove unresolved or ambiguous dynamic relations remain explicit gaps.
- [ ] Prove tenant isolation, concurrent replay, crash recovery, and rollback.
- [ ] Run race, integration, security, reproducibility, and independent
  full-range reviews.
- [ ] Publish a fail-closed Sprint 08 candidate and close it through protocol
  v2 before authorizing Sprint 09.

## Acceptance Evidence

- Contract fixtures, semantic cross-field tests, and stable-ID golden vectors.
- Per-language golden repositories with expected cards and relations.
- Git object-boundary, rename, path, symlink, encoding, and size-limit tests.
- PostgreSQL migration, concurrency, idempotency, tenant-isolation, and
  append-only tests.
- Exact snapshot artifact publication and restore evidence.
- Incremental full-versus-delta equivalence tests.
- Cross-platform deterministic build and serialization evidence.
- An isolated rollback drill against the authoritative Sprint 07 commit.

## Out of Scope

- Embeddings, semantic ranking, Qdrant collections, and retrieval fusion belong
  to Sprint 09.
- Neo4j projection, graph traversal, and impact-path serving belong to Sprint
  10.
- Context-pack selection and token budgeting belong to Sprint 11.
- Runtime traces may later promote behavioral evidence, but this Sprint only
  preserves declared runtime references already present in canonical input.
- Dynamic language behavior is never guessed to improve apparent coverage.

## Rollback

Stop new indexing commands, let bounded adapter processes finish or terminate,
and preserve every active snapshot, artifact, event, and idempotency receipt.
Migration 008 may roll back only when its canonical tables, related events,
artifact references, and receipts are empty. Once Sprint 08 history exists,
rollback is a forward-repair deployment to the Sprint 08 schema; never delete
index evidence to force an older binary to start.
