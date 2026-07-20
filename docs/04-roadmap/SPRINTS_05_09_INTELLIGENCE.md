# Sprints 05-09: Delivery and Intelligence

## Sprint 05: Isolated Delivery

**Outcome:** produce bounded repository changes with mechanical and independent
validation.

**Status:** Closed by the authoritative
[Sprint 05 receipt](../evidence/sprint-05/close-receipt.json). See the
[Sprint 05 execution plan](SPRINT_05_ISOLATED_DELIVERY_PLAN.md) for boundaries
and residual risk.

### Scope

- [x] Create isolated Git worktrees per attempt and quarantine them on uncertain
  cleanup. Automatic physical deletion remains disabled until process
  quiescence and live-lease proofs are implemented.
- [x] Implement file and artifact write leases with fencing tokens.
- [x] Reject writes outside the declared write set.
- [x] Require every untrusted adapter to prove equivalent OS-enforced writable
  roots before registration.
- [x] Capture base commit, resulting commit, diff hash, and changed paths.
- [x] Implement mechanical validators for tests, formatting, schemas, secrets,
  generated files, and path scope.
- [x] Implement independent validator assignment.
- [x] Prevent the author from being the only validator.
- [x] Produce hash-bound validation evidence bundles and delivery receipts.
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

**Status:** Closed by the authoritative
[Sprint 06 receipt](../evidence/sprint-06/close-receipt.json). See the
[Sprint 06 execution plan](SPRINT_06_OBSERVABILITY_PLAN.md) for boundaries
and residual risk.

### Scope

- [x] Add OpenTelemetry traces across MCP, scheduler, worker, validation, and
  persistence boundaries.
- [x] Export Prometheus metrics with controlled cardinality.
- [x] Emit structured, redacted logs for Loki.
- [x] Build the current factory-health dashboard for runs, workers, failures,
  projection lag, and approvals. Retrieval and cost panels wait for their
  owning subsystems.
- [x] Define a stable failure taxonomy.
- [x] Add alert candidates for stuck runs, expired leases, outbox backlog,
  projection lag, and worker crash loops.
- [x] Keep stdout/stderr bodies out of canonical attempt events; large durable
  artifacts and retention remain owned by Sprint 07.
- [x] Test telemetry behavior during cancellation and partial outages.

### Acceptance

- An operator can trace one task from MCP command to evidence.
- Dashboard metrics explain every non-success terminal state.
- Secret fixtures do not appear in logs or traces.
- Observability failure does not stop canonical state transitions.

## Sprint 07: Artifacts, Conversations, and Memory

**Outcome:** preserve durable work and learning without treating chat history as
truth.

**Status:** Closed by the authoritative
[Sprint 07 receipt](../evidence/sprint-07/close-receipt.json). See the
[Sprint 07 execution plan](SPRINT_07_ARTIFACTS_MEMORY_PLAN.md) for boundaries
and residual risk.

### Scope

- [x] Implement artifact metadata and provenance tables.
- [x] Integrate S3-compatible object storage.
- [x] Verify upload hashes and idempotent object writes.
- [x] Implement conversations, messages, content parts, and citations.
- [x] Separate raw messages, working summaries, memory candidates, and durable
  memory records.
- [x] Add memory promotion, supersession, expiry, and deletion workflows.
- [x] Add retention and tombstone projection events.
- [x] Add tenant-safe object and metadata authorization.
- [x] Add immutable evidence bundle manifests.
- [x] Test restore, missing objects, hash mismatch, and partial upload behavior.

### Acceptance

- Large evidence is stored outside PostgreSQL with verified metadata.
- Deleting a canonical record schedules removal from derived stores.
- A chat statement cannot become durable memory without a promotion event.
- Object and database backup restoration preserves references.

## Sprint 08: Deterministic Indexing

**Outcome:** create canonical repository, symbol, type, schema, and behavior
metadata before semantic indexing.

**Status:** Closed by the authoritative
[Sprint 08 receipt](../evidence/sprint-08/close-receipt.json). See the
[Sprint 08 execution plan](SPRINT_08_DETERMINISTIC_INDEXING_PLAN.md) for
boundaries and residual risk. Sprint 09 is authorized.

### Scope

- [x] Define `RepositorySnapshot`, `FileCard`, `SymbolCard`, and
  `RelationEvidence` schemas.
- [x] Implement Git change-set and stable entity ID generation.
- [x] Implement the TypeScript Compiler API adapter.
- [x] Implement the Go `packages/types/ast` adapter.
- [x] Implement Python AST structural adapter.
- [x] Extract imports, exports, symbols, signatures, references, tests, routes,
  schemas, and generated-file markers.
- [x] Add relation confidence and evidence classes.
- [x] Create incremental invalidation based on commit and source hashes.
- [x] Store canonical index metadata in PostgreSQL and large snapshots in object
  storage.
- [x] Add golden repositories and cross-version fixtures.

### Acceptance

- Re-running an unchanged commit produces byte-stable IDs and relations.
- Changed files invalidate only the expected dependency region.
- Semantic inference produces no authoritative relation.
- Golden fixtures measure extraction precision and coverage.

## Sprint 09: Governed Retrieval

**Outcome:** discover relevant artifacts and symbols with hybrid search while
preserving authority and access boundaries.

**Status:** Implementation complete; the protocol-v2 closure candidate is
published for immutable review. Private quality evaluation and competition-profile activation are
transferred to Sprint 10 and do not become implied Sprint 09 evidence. See the
[Sprint 09 execution plan](SPRINT_09_GOVERNED_RETRIEVAL_PLAN.md).

### Scope

- [x] Define versioned Qdrant collection protocol plans, named vectors,
  mandatory payload indexes, TLS/secret endpoint policy, an idempotent
  official-client point writer, explicit collection/alias operator calls, and
  physical generation/vector/index verification. Alias observation and guarded
  rollback are implemented. Opt-in live integration now covers physical
  blue-green cutover, guarded rollback, collection deletion, and replay.
- [x] Produce embeddable symbol, decision, test, memory, and incident cards.
  Every family rebuilds from its owning canonical source. Incident cards derive
  only from immutable failed-attempt evidence; they are not log cards.
- [x] Implement the dense/sparse request contracts, deterministic sparse
  lexical encoder, Qdrant candidate execution, bounded RRF fusion, degraded
  receipts, and mandatory PostgreSQL resolution. Concrete embedding-provider
  deployment remains pending.
- [x] Apply tenant, repository, status, authority, stale, language, and kind
  filters before ranking.
- [x] Implement weighted reciprocal rank fusion.
- [x] Implement and test canonical-resolution and ambiguity-handling policy,
  including the PostgreSQL resolver adapter for active canonical symbols and
  canonically flagged tests.
- [x] Record embedding model, version, dimensions, source hash, and generation
  timestamp.
- [x] Implement independent, fenced per-projector outbox delivery and backlog
  replay. The symbol Qdrant upsert projector records PostgreSQL provenance
  before acknowledging delivery. Superseded snapshots tombstone canonical
  point receipts before idempotent Qdrant deletion; failed deletes stay
  fail-closed and retry through the delivery ledger.
- [x] Add collection migration and blue-green re-embedding strategy. Immutable
  PostgreSQL generation receipts bind the collection/vector contract; the
  Qdrant operator verifies a green collection, atomically switches the stable
  alias, reads it back, retains the prior generation for observation, and
  permits rollback only when the alias still points to the expected green
  target. Unit and opt-in live drills cover cutover, rollback, deletion, and
  replay.
- [x] Build the public evaluation contracts, scorer, four-baseline capture,
  freshness and leakage fixtures, and transfer execution of the private
  tuning/holdout/OOD corpus to Sprint 10. Production activation remains
  disabled until that evidence passes.
- [x] Publish the complete fail-closed Sprint 09 evidence candidate without
  authorizing Sprint 10.

### Acceptance

- Public fixtures prove distinct lexical and dense paths; private comparative
  benefit remains a Sprint 10 quality gate.
- Cross-tenant and stale-authority test cases return zero unauthorized results.
- Deleting Qdrant and replaying the outbox rebuilds the expected projection.
