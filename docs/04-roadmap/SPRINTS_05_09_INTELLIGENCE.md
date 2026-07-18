# Sprints 05-09: Delivery and Intelligence

## Sprint 05: Isolated Delivery

**Outcome:** produce bounded repository changes with mechanical and independent
validation.

**Status:** In progress. See the
[Sprint 05 execution plan](SPRINT_05_ISOLATED_DELIVERY_PLAN.md).

### Scope

- [ ] Create and remove isolated Git worktrees per attempt. Creation and
  quarantine are implemented; proof-bound physical deletion remains pending.
- [x] Implement file and artifact write leases with fencing tokens.
- [ ] Reject writes outside the declared write set.
- [x] Require every untrusted adapter to prove equivalent OS-enforced writable
  roots before registration.
- [ ] Capture base commit, resulting commit, diff hash, and changed paths.
- [ ] Implement mechanical validators for tests, formatting, schemas, secrets,
  generated files, and path scope.
- [ ] Implement independent validator assignment.
- [ ] Prevent the author from being the only validator.
- [ ] Produce evidence bundles and close receipts.
- [x] Implement retry from clean base and explicit conflict handling.
- [x] Add concurrent-author and stale-lease tests.

### Acceptance

- One synthetic Sprint completes from task to validated patch.
- Conflicting authors cannot write the same artifact simultaneously.
- Out-of-scope writes fail before publication.
- Validation evidence can be reproduced from a clean clone.

## Sprint 06: Observability

**Outcome:** make runtime behavior measurable without relying on terminal
scrollback.

### Scope

- [ ] Add OpenTelemetry traces across MCP, scheduler, worker, validation, and
  persistence boundaries.
- [ ] Export Prometheus metrics with controlled cardinality.
- [ ] Emit structured, redacted logs for Loki.
- [ ] Build dashboards for factory health, runs, workers, failures, retrieval,
  projection lag, approvals, and cost.
- [ ] Define a stable failure taxonomy.
- [ ] Add alert candidates for stuck runs, expired leases, outbox backlog,
  projection lag, and worker crash loops.
- [ ] Store large logs as artifacts rather than database rows.
- [ ] Test telemetry behavior during cancellation and partial outages.

### Acceptance

- An operator can trace one task from MCP command to evidence.
- Dashboard metrics explain every non-success terminal state.
- Secret fixtures do not appear in logs or traces.
- Observability failure does not stop canonical state transitions.

## Sprint 07: Artifacts, Conversations, and Memory

**Outcome:** preserve durable work and learning without treating chat history as
truth.

### Scope

- [ ] Implement artifact metadata and provenance tables.
- [ ] Integrate S3-compatible object storage.
- [ ] Verify upload hashes and idempotent object writes.
- [ ] Implement conversations, messages, content parts, and citations.
- [ ] Separate raw messages, working summaries, memory candidates, and durable
  memory records.
- [ ] Add memory promotion, supersession, expiry, and deletion workflows.
- [ ] Add retention and tombstone projection events.
- [ ] Add tenant-safe object and metadata authorization.
- [ ] Add immutable evidence bundle manifests.
- [ ] Test restore, missing objects, hash mismatch, and partial upload behavior.

### Acceptance

- Large evidence is stored outside PostgreSQL with verified metadata.
- Deleting a canonical record schedules removal from derived stores.
- A chat statement cannot become durable memory without a promotion event.
- Object and database backup restoration preserves references.

## Sprint 08: Deterministic Indexing

**Outcome:** create canonical repository, symbol, type, schema, and behavior
metadata before semantic indexing.

### Scope

- [ ] Define `RepositorySnapshot`, `FileCard`, `SymbolCard`, and
  `RelationEvidence` schemas.
- [ ] Implement Git change-set and stable entity ID generation.
- [ ] Implement the TypeScript Compiler API adapter.
- [ ] Implement the Go `packages/types/ast` adapter.
- [ ] Implement Python AST structural adapter.
- [ ] Extract imports, exports, symbols, signatures, references, tests, routes,
  schemas, and generated-file markers.
- [ ] Add relation confidence and evidence classes.
- [ ] Create incremental invalidation based on commit and source hashes.
- [ ] Store canonical index metadata in PostgreSQL and large snapshots in object
  storage.
- [ ] Add golden repositories and cross-version fixtures.

### Acceptance

- Re-running an unchanged commit produces byte-stable IDs and relations.
- Changed files invalidate only the expected dependency region.
- Semantic inference produces no authoritative relation.
- Golden fixtures measure extraction precision and coverage.

## Sprint 09: Governed Retrieval

**Outcome:** discover relevant artifacts and symbols with hybrid search while
preserving authority and access boundaries.

### Scope

- [ ] Define Qdrant collections, named vectors, payload indexes, and aliases.
- [ ] Produce embeddable symbol, decision, test, memory, and incident cards.
- [ ] Implement dense semantic and sparse lexical retrieval.
- [ ] Apply tenant, repository, status, authority, stale, language, and kind
  filters before ranking.
- [ ] Implement weighted reciprocal rank fusion.
- [ ] Implement canonical entity resolution and ambiguity handling.
- [ ] Record embedding model, version, dimensions, source hash, and generation
  timestamp.
- [ ] Implement outbox-driven idempotent upsert and delete projections.
- [ ] Add collection migration and blue-green re-embedding strategy.
- [ ] Build retrieval recall, precision, freshness, and leakage evaluations.

### Acceptance

- Identifier-heavy queries benefit from lexical retrieval.
- Conceptual queries benefit from dense retrieval.
- Cross-tenant and stale-authority test cases return zero unauthorized results.
- Deleting Qdrant and replaying the outbox rebuilds the expected projection.
