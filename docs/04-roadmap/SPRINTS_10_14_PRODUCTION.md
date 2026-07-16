# Sprints 10-14: Governance and Production

## Sprint 10: Graph Projection

**Outcome:** serve proven lineage and impact paths through Neo4j without making
the graph a competing source of truth.

### Scope

- [ ] Define node labels, relation types, evidence classes, and uniqueness
  constraints.
- [ ] Project repository, symbol, type, schema, test, document, artifact,
  Sprint, run, and evidence entities.
- [ ] Implement idempotent outbox-driven graph delta application.
- [ ] Store projection source version and hashes on nodes and relations.
- [ ] Implement allowlisted path templates for common engineering questions.
- [ ] Implement bounded read-only exploratory queries behind validation.
- [ ] Add graph projection checkpoints and drift detection.
- [ ] Add full rebuild and rollback procedures.
- [ ] Benchmark traversal depth, fan-out, and path ranking.
- [ ] Test false-edge prevention and candidate-semantic isolation.

### Acceptance

- Graph paths cite relation evidence and canonical sources.
- Missing or stale relations become explicit gaps.
- Projection rebuild reaches parity with expected counts and hashes.
- Semantic similarity alone cannot create a confirmed edge.

## Sprint 11: Context Broker

**Outcome:** assemble minimal, cited, authority-aware context packs for agents.

### Scope

- [ ] Implement context request and context pack contracts.
- [ ] Enforce access and repository scope before retrieval.
- [ ] Combine exact lookup and Qdrant hybrid candidates.
- [ ] Resolve stable entities and reject ambiguity.
- [ ] Traverse allowlisted Neo4j paths.
- [ ] Verify source commit, hash, lifecycle, and projection freshness.
- [ ] Implement explainable path and source ranking.
- [ ] Enforce source, hop, latency, and token budgets.
- [ ] Implement deterministic context pruning and gap reporting.
- [ ] Emit retrieval receipts with candidate and selection counts.
- [ ] Add no-Qdrant, no-Neo4j, stale-projection, and source-only fallbacks.

### Acceptance

- Context packs contain fewer tokens than naive repository search on the eval
  corpus without reducing required-source recall below the agreed gate.
- Every excerpt has a canonical citation.
- Projection outages degrade to source-backed retrieval.
- Ambiguous requests expose alternatives rather than invented certainty.

## Sprint 12: Governance and Resilience

**Outcome:** withstand privileged actions, dependency failures, restarts, and
adversarial inputs safely.

### Scope

- [ ] Implement capability and role policy evaluation.
- [ ] Implement expiring, scoped, and revocable approvals.
- [ ] Add secret manager adapter and credential rotation procedures.
- [ ] Add tenant isolation to PostgreSQL, Qdrant, Neo4j, object storage, logs,
  and context packs.
- [ ] Add leader election or scheduler ownership rules.
- [ ] Add recovery reconciliation for workers, leases, worktrees, and outbox
  projections.
- [ ] Add prompt-injection and tool-abuse test suites.
- [ ] Add chaos tests for PostgreSQL, Qdrant, Neo4j, object storage, and worker
  failure.
- [ ] Add backup and restore drills.
- [ ] Add dependency provenance, image signing, and vulnerability scanning.
- [ ] Produce a formal threat model and incident runbook.

### Acceptance

- A compromised worker cannot expand its authority.
- Cross-tenant test suites pass at every store boundary.
- Restart and partial-outage drills preserve canonical state.
- Privileged actions are traceable to a valid approval.

## Sprint 13: Evaluation Harness

**Outcome:** make routing, retrieval, execution, safety, and cost improvements
measurable and resistant to overfitting.

### Scope

- [ ] Create public synthetic and private holdout evaluation corpora.
- [ ] Separate in-distribution, out-of-distribution, adversarial, and regression
  cases.
- [ ] Evaluate Sprint planning completeness and dependency correctness.
- [ ] Evaluate worker contract adherence and write-scope safety.
- [ ] Measure deterministic lineage precision, recall, and stale detection.
- [ ] Measure Qdrant candidate recall and entity resolution precision.
- [ ] Measure graph path validity and required-source coverage.
- [ ] Measure context token reduction against task success.
- [ ] Measure cancellation, retry, restart, and recovery reliability.
- [ ] Measure model, infrastructure, and operator cost.
- [ ] Add release gates and statistically justified confidence intervals.

### Acceptance

- Every critical capability has a metric, dataset, and failure taxonomy.
- Holdout cases are not available to runtime agents.
- Improvements must beat the current baseline without regressing safety gates.
- Evaluation artifacts are reproducible and versioned.

## Sprint 14: Production Pilot

**Outcome:** complete a real governed software Sprint and establish Forja 1.0
release readiness.

### Scope

- [ ] Select one representative repository and bounded pilot objective.
- [ ] Run discovery, planning, approval, execution, validation, and evidence
  end-to-end.
- [ ] Record human intervention and failure recovery.
- [ ] Measure queue time, runtime, token use, context size, validation quality,
  and operational cost.
- [ ] Run a second clean-clone reproduction.
- [ ] Complete load, soak, cancellation, and restart tests.
- [ ] Establish initial SLOs and alert thresholds.
- [ ] Complete backup, restore, and disaster recovery evidence.
- [ ] Review security findings and dependency provenance.
- [ ] Publish accurate architecture, limitations, and operating instructions.
- [ ] Tag the 1.0 release only after every mandatory gate closes.

### Acceptance

- A real Sprint completes without manual state repair.
- Evidence independently proves the published result.
- A clean environment reproduces the release.
- Documentation distinguishes validated capabilities from future work.

