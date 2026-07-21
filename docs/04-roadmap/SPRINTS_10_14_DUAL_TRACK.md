# Sprints 10-14 Dual Track Execution Plan

Status: active execution companion for
[Sprints 10-14](SPRINTS_10_14_PRODUCTION.md). This plan separates work that
requires a live AMD Radeon Cloud notebook from work that can continue on a
local workstation or VPS while Radeon is unavailable.

## Operating Rule

Sprint closure remains sequential. Sprint 10 still closes only after real
Radeon evidence is captured, ingested, and independently reviewed. The
notebook-independent track may prepare code, schemas, tests, fixtures, data
contracts, UI shells, and release materials, but it must not claim Sprint 11
execution authority until Sprint 10 has a protocol v2 `close-receipt.json`.

The separation exists to keep momentum without turning planning artifacts into
fake runtime evidence.

## What Goes On The AMD Radeon Notebook

The Radeon notebook is the competition runtime and benchmark lab. It should
host the local model, local embeddings, Qdrant, Neo4j, PostgreSQL, object
storage snapshots, observability agents, and the Forja Alpha service when we
need to prove local private deployment.

Use persistent PVC for anything that must survive instance destruction:

- model caches, quantized weights, and local serving configs;
- PostgreSQL data directory or export/restore artifacts;
- Qdrant collections and rebuild manifests;
- Neo4j database/projection exports and rebuild manifests;
- `/secure/forja` private source snapshots;
- `/workspace/forja-alpha-sprint10-evidence` private runtime evidence.

Only sanitized summaries, manifests, receipts, and public-safe reports return
to Git.

## What Can Continue Without The Notebook

Notebook-independent work should improve the product and reduce future Radeon
time. It can define schemas, deterministic tools, importers, migration tests,
projection contracts, UI flows, fake/local fixtures, security policies,
documentation, release assets, and dry-run validators.

It cannot prove Radeon local inference, ROCm performance, GPU recovery,
model-load latency, or no-remote-core-inference runtime behavior.

## Track A: AMD Notebook Required

### Sprint A1: Radeon Runtime And Source Restore Proof

Objective: prove the base competition profile can restore code, private
snapshots, local databases, and runtime prerequisites on Radeon.

Checklist:

- [ ] Launch the Radeon Cloud profile with persistent PVC and SSH or web
  terminal access.
- [ ] Clone the exact public branch and run `python3 scripts/validate_repository.py`.
- [ ] Place private snapshots under `/secure/forja` using
  `snapshot-checklist.md`.
- [ ] Generate and verify `/secure/forja/alpha-source-manifest.json`.
- [ ] Capture a real runtime receipt with ROCm, GPU, Python, Git, and command
  availability.
- [ ] Run source restore verification and keep raw receipts outside Git.
- [ ] Export only sanitized `radeon-public-summary.json` when complete.

Exit evidence:

- real Radeon runtime receipt exists privately;
- source snapshot manifest verifies;
- public summary proves source restore and runtime capture without secrets.

### Sprint A2: Local Model Serving On ROCm

Objective: serve at least two local instruction-model candidates through
loopback OpenAI-compatible endpoints.

Checklist:

- [ ] Select two open-weight instruction candidates that fit the available
  Radeon VRAM and competition scenario.
- [ ] Start endpoint A on `127.0.0.1:8000/v1`.
- [ ] Start endpoint B on `127.0.0.1:8001/v1`.
- [ ] Fill `/secure/forja/radeon-model-candidates.json` without placeholders.
- [ ] Run strict private-input preflight before benchmark work.
- [ ] Run the frozen public smoke benchmark and a private tuning benchmark.
- [ ] Select the default local instruction model and record limitations.

Exit evidence:

- two loopback endpoints benchmarked;
- selected model has sanitized latency, throughput, and quality receipts;
- no remote core-inference fallback is required.

### Sprint A3: Local Embedding, Qdrant, And Retrieval Proof

Objective: prove local embeddings and Qdrant retrieval on Radeon.

Checklist:

- [ ] Start a local embedding endpoint on `127.0.0.1:8081/v1`.
- [ ] Benchmark embedding dimensions, latency, finite values, and stability.
- [ ] Start Qdrant on the Radeon profile with persistent storage.
- [ ] Build a small narrative evidence collection from approved filing/method
  chunks.
- [ ] Verify every Qdrant point resolves to PostgreSQL/source-object metadata.
- [ ] Run lexical-only, dense-only, and RRF retrieval smoke tests.

Exit evidence:

- local embedding benchmark passes;
- Qdrant collection is rebuildable from canonical metadata;
- retrieval receipts contain hashes and IDs, not private text bodies.

### Sprint A4: Neo4j Projection And Evidence Path Proof

Objective: prove Neo4j can explain source-to-claim paths without becoming an
authority.

Checklist:

- [ ] Start Neo4j on Radeon or PVC-backed runtime.
- [ ] Project issuer, filing, concept, metric, series, source-object, tool,
  claim, and citation nodes from PostgreSQL-owned IDs.
- [ ] Add only deterministic relationships such as `FILED`, `CONTAINS`,
  `REPORTS`, `DERIVED_FROM`, `USES_SERIES`, and `SUPPORTED_BY`.
- [ ] Verify the graph can be deleted and rebuilt from PostgreSQL plus object
  storage.
- [ ] Run path tests for the primary demo memo.
- [ ] Export sanitized graph projection receipts.

Exit evidence:

- graph projection receipt passes;
- graph path tests resolve to canonical IDs and source hashes;
- Neo4j creates no source facts.

### Sprint A5: Full Local Agent Demo And Recovery

Objective: run the complete Alpha scenario locally on Radeon and prove
destroy/recreate recovery.

Checklist:

- [ ] Start PostgreSQL, Qdrant, Neo4j, local model, local embedding,
  observability, and Forja Alpha service on the Radeon profile.
- [ ] Execute the primary demo question end to end.
- [ ] Capture local planning, tool calls, retrieval, graph paths, memory,
  citations, and memo output.
- [ ] Capture latency, GPU utilization, p50/p95 response time, and error
  taxonomy.
- [ ] Destroy and recreate the profile, then restore from persistent PVC and
  manifests.
- [ ] Produce sanitized demo/recovery receipts.

Exit evidence:

- full local private agent works on Radeon;
- recovery receipt proves no hidden manual state;
- Sprint 10 can be reviewed for closure if all A1-A5 gates are complete.

## Track B: Notebook Independent

### Sprint B1: Data Contracts And Import Fixtures

Objective: make source data shape, database authority, and restore contracts
fully testable without GPU.

Checklist:

- [x] Freeze source-family contracts for SEC identity, SEC submissions, SEC
  Company Facts, filing documents, Treasury, FRED/ALFRED, market data, and 13F.
- [ ] Add public-safe fixture manifests for each source family.
- [ ] Add importer tests for missing, malformed, stale, duplicated, and
  unsupported source data.
- [ ] Add point-in-time query tests for `available_at <= as_of`.
- [ ] Add source coverage tests for UI/agent audit surfaces.

Current evidence:

```bash
python3 scripts/validate_alpha_source_contracts.py \
  --output /tmp/forja-alpha-source-contract-validation.json
```

The validator cross-checks
`internal/alpha/testdata/alpha_source_family_contracts_public_v1.json` against
the same Sprint 10 required snapshot contract used by the Radeon private-input
preflight and source-manifest builder.

Exit evidence:

- local tests prove canonical PostgreSQL behavior without Qdrant, Neo4j, or
  model inference.

### Sprint B2: Deterministic Tool Receipts

Objective: implement recomputable financial tools and receipt contracts before
model synthesis exists.

Checklist:

- [x] Define tool schemas for filings timeline, fundamentals, factor
  sensitivity, holdings, and evidence-pack assembly.
- [ ] Store tool run metadata, input hashes, output hashes, formula versions,
  estimator versions, diagnostics, and limitations.
- [ ] Add mechanical validators for numeric exactness and citation coverage.
- [ ] Add unsupported-gap receipts when data is missing or not computable.
- [ ] Add local CLI/API smoke tests for each tool.

Current evidence:

```bash
python3 scripts/validate_alpha_tool_contracts.py \
  --output /tmp/forja-alpha-tool-contract-validation.json
```

The validator checks
`internal/alpha/testdata/alpha_tool_contracts_public_v1.json` for the required
tool set, receipt fields, diagnostics, forbidden behaviors, and storage
boundary with `forja.alpha_tool_invocations`.

Exit evidence:

- deterministic tools can produce claim-ready evidence packs without model
  prose.

### Sprint B3: Projection Builders For Qdrant And Neo4j

Objective: build projection pipelines that can run locally against fixtures and
later run unchanged on Radeon.

Checklist:

- [ ] Define Qdrant payload schema for narrative chunks, method docs, evidence
  summaries, and approved memory summaries.
- [ ] Define Neo4j node/edge projection schema from canonical PostgreSQL IDs.
- [ ] Add rebuild commands and drift validators for both projection stores.
- [ ] Add tests proving projections cannot create facts or bypass
  permissions.
- [ ] Add synthetic embedding stubs so projection metadata can be tested
  without GPU.

Exit evidence:

- projection builders are deterministic, rebuildable, and authority-safe.

### Sprint B4: Web Agent Workspace Shell

Objective: create the visible Alpha product experience using mocked/local
fixtures where Radeon inference is not required.

Checklist:

- [ ] Build the research workstation UI with source coverage, model health,
  plan steps, tool traces, retrieval results, graph paths, citations,
  limitations, memory controls, and memo panel.
- [ ] Add backend routes for sessions, plans, tool runs, evidence packs,
  citations, and memory decisions.
- [ ] Add permission prompts and privacy/audit surfaces.
- [ ] Add fixture-driven demo mode for no-GPU development.
- [ ] Add end-to-end tests for the primary scenario skeleton.

Exit evidence:

- a judge can understand the intended product flow before final Radeon
  inference is attached.

### Sprint B5: Evaluation, Release, And Submission Readiness

Objective: prepare evaluation and public release materials before the final
Radeon run.

Checklist:

- [ ] Define quality suites for tools, retrieval, graph paths, memory,
  citations, privacy, and UI workflows.
- [ ] Add release manifest, source audit, clean-checkout reproduction, and
  no-secret checks.
- [ ] Draft README, project PDF, demo script, architecture diagram, and
  optional poster/deck.
- [ ] Add observability dashboards and expected metric names.
- [ ] Add final evidence-to-claim checklist so public claims cannot exceed
  receipts.

Exit evidence:

- release artifacts are ready to be filled with final Radeon numbers once Track
  A evidence exists.

## Coordination Rules

- Track B work can continue while Track A is unavailable.
- Track B outputs are preparatory until Track A proves local runtime behavior.
- Track A receipts are the only authority for Radeon/ROCm claims.
- Sprint 11 implementation may be prepared in Track B, but Sprint 11 closure
  cannot begin until Sprint 10 is promoted to a close receipt.
- Any public claim in README, PDF, video, or PR must point to either a Track A
  receipt or a Track B deterministic test.

## Immediate Next Move While Radeon Is Down

Work on Track B1 and B2 locally: strengthen source contracts, importer tests,
tool schemas, and deterministic receipts. When Radeon returns, resume Track A1
with the generated handoff packet and snapshot checklist.
