# Sprints 10-14: Forja Alpha Production Plan

Status: Active. Sprint 09 is closed. These Sprints complete Forja Alpha as a
private, evidence-grounded investment-research agent for AMD AI DevMaster
Hackathon Track 2 without weakening the neutral Forja kernel.

The canonical source and storage design is defined in the
[Forja Alpha data architecture](../02-architecture/FORJA_ALPHA_DATA_ARCHITECTURE.md).

## Product Outcome

The release must let a researcher ask a financial question in natural
language, observe a local agent create and execute a bounded plan, inspect every
tool call and source, continue the conversation with governed memory, and
receive a cited research memo. It analyzes reported financial performance,
factor sensitivity, and public institutional disclosures. It does not predict
prices, place trades, or present correlation as causation.

The primary demonstration question is:

> Compare NVIDIA, Microsoft, and Alphabet using point-in-time filings, explain
> changes in operating quality and cash conversion, estimate their historical
> sensitivity to the US 10-year real yield, and show the evidence and limits of
> every conclusion.

## Competition Boundary

- Final submission deadline: 2026-08-06 12:59 America/Sao_Paulo.
- Core language-model and embedding inference runs locally on AMD Radeon and
  ROCm. Remote APIs are not inference fallbacks in the competition profile.
- Public data may be downloaded through governed ingestion jobs. The recorded
  demo executes from hash-pinned local snapshots and survives source outages.
- The product visibly demonstrates local RAG, tool invocation, multi-step
  planning, multi-turn memory, and permission and privacy controls.
- The Radeon Cloud profile uses persistent PVC storage, SSH, reproducible
  setup, and explicit GPU/runtime evidence.
- Source, README, project PDF, three-to-five-minute demo, and submission pull
  request are in English.
- The final AMD pull request title follows
  `Track 2, <Team Name>, Forja Alpha`.

## Cross-Sprint Invariants

- PostgreSQL is canonical. Object storage preserves source bytes and generated
  evidence. Qdrant and Neo4j are rebuildable projections.
- Every source observation records event, publication, availability, and
  ingestion time. Research never reads data unavailable at its `as_of` time.
- Raw source bytes are immutable and content-addressed. Corrections and amended
  filings create new versions; they never rewrite history.
- Numeric facts are selected and calculated by typed tools, not semantic
  retrieval or model arithmetic.
- Qdrant discovers narrative evidence. Neo4j traverses proven relationships.
  Neither establishes fact, identity, permission, or analytical authority.
- Model output cannot approve its own tool access, mutate canonical source
  facts, expand scope, or silently call a remote provider.
- Every claim in a completed memo resolves to source facts, deterministic
  calculations, statistical estimates, or an explicit unsupported gap.
- Public telemetry contains identifiers, states, counts, timings, and hashes,
  but not private prompts, memory bodies, credentials, or licensed data.

## Critical Path

```mermaid
flowchart LR
    S10["Sprint 10\nCanonical data + ROCm"] --> S11["Sprint 11\nTools + evidence fabric"]
    S11 --> S12["Sprint 12\nAgent + product UX"]
    S12 --> S13["Sprint 13\nEvaluation + optimization"]
    S13 --> S14["Sprint 14\nRelease + submission"]
```

No Sprint may claim completion from documentation alone. Its exit requires the
specified runtime evidence and a clean-checkout reproduction.

## Sprint 10: Canonical Financial Data and Local Runtime

**Outcome:** establish a reproducible Radeon/ROCm inference boundary and a
point-in-time financial data foundation for the Magnificent Seven.

### User-Visible Increment

The Alpha interface reports verified local model and embedding health, lists
the available companies and observation windows, and can display source-backed
filing and macro timelines without generating an analytical conclusion.

### Runtime and Deployment

- [ ] Reproduce the current public `main` from the Radeon Cloud persistent-PVC
  template and record the exact source commit.
- [ ] Generate a machine-readable environment receipt covering GPU, ROCm,
  driver, OS, Python, PyTorch, serving runtime, model, precision, and flags.
- [ ] Deploy at least two open-weight instruction-model candidates through a
  local OpenAI-compatible endpoint and select one using a frozen task set.
- [ ] Deploy and benchmark a local embedding model on the same Radeon profile.
- [ ] Implement identity, model, embedding, and GPU health probes; endpoint
  configuration alone must not imply readiness.
- [ ] Prove zero remote core-inference calls with egress observation and an
  explicit disabled-remote-provider test.
- [ ] Prove source, configuration, and evaluation metadata recovery after
  instance destruction without committing model weights or secrets to Git.
- [ ] Implement a local ROCm embedding provider for the competition profile;
  keep the Bedrock adapter disabled for every core hackathon workflow.

### Implementation Progress

- [x] Defined the Radeon Cloud template procedure with persistent PVC, SSH, the
  recommended `GH-proxy-stable` base image, public Git checkout, and no model
  directory until model selection is pinned.
- [x] Added `schemas/radeon-runtime-receipt.schema.json` for sanitized Radeon
  runtime evidence.
- [x] Added `scripts/capture_radeon_runtime_receipt.py` to collect bounded GPU,
  ROCm, PyTorch, vLLM, Git, host, and secret-presence evidence without storing
  credential values.
- [x] Added `docs/06-operations/RADEON_CLOUD_RUNTIME.md` for Radeon Cloud
  template, first-boot receipt capture, local inference boundary, and no-Git
  artifact rules.
- [x] Added a loopback-only local HTTP embedding provider for Radeon-hosted
  OpenAI-compatible embedding endpoints.
- [x] Added the initial PostgreSQL Alpha financial schema for source systems,
  source objects, issuers, securities, identifiers, filings, XBRL facts,
  metric observations, time series, analysis runs, 13F holdings, research
  sessions, tool invocations, claims, and claim evidence.
- [ ] Capture the first real Radeon Cloud receipt and keep the raw artifact
  outside Git.

### Source Ingestion

- [ ] Implement an SEC identity adapter for CIK, ticker, exchange, and issuer
  aliases, preserving the source snapshot and source limitations.
- [ ] Implement SEC submissions and Company Facts ingestion for 10-K, 10-Q,
  and amendments, with a classified User-Agent and a global request budget
  below the SEC fair-access ceiling.
- [ ] Preserve the complete filing document and structured source payload in
  content-addressed object storage before parsing.
- [ ] Implement Treasury nominal and real yield-curve snapshot ingestion.
- [ ] Implement FRED/ALFRED series and vintage ingestion for the approved macro
  series registry, preserving revision and availability semantics.
- [ ] Implement a provider-neutral adjusted daily market-data adapter with
  license metadata and a hash-pinned CSV import fallback.
- [ ] Import a bounded 13F cohort only after its manager CIK allowlist and
  filing-delay disclosure are reviewed.
- [ ] Add idempotent refresh, conditional retrieval, backoff, rate limiting,
  retry budgets, and source-specific quarantine behavior.

### Canonical PostgreSQL Model

- [x] Add migrations for source systems, ingestion runs, source objects,
  issuers, securities, identifiers, filings, documents, XBRL concepts,
  contexts, facts, metric mappings, time series, observations, institutional
  managers, reports, positions, and data-quality findings.
- [ ] Store accounting values as exact decimals with explicit units, scales,
  currencies, periods, fiscal frames, dimensions, and filing identities.
- [ ] Represent `observed_at`, `period_start`, `period_end`, `filed_at`,
  `available_at`, `ingested_at`, and supersession independently.
- [ ] Quarantine ambiguous dimensions, unsupported units, duplicate contexts,
  impossible periods, and unmapped custom concepts rather than guessing.
- [ ] Bind every parsed row to its immutable source-object hash, parser
  version, ingestion run, and validation result.
- [ ] Add canonical point-in-time views that select only data available at the
  requested research timestamp.

### Quality and Security

- [ ] Build fixtures for amendments, restatements, 53-week fiscal years,
  custom XBRL extensions, split-adjusted prices, missing observations, revised
  macro values, and duplicate 13F positions.
- [ ] Test idempotent replay, partial-download rejection, parser failure,
  source outage, rate-limit response, and object-store recovery.
- [ ] Verify tenant/repository isolation and prevent source credentials from
  entering logs, URLs persisted as evidence, or frontend responses.
- [ ] Publish a sanitized coverage manifest with counts, ranges, hashes,
  freshness, gaps, and licenses, but no restricted data body.

### Sprint 10 Exit Gate

- A clean Radeon instance serves verified local model and embedding endpoints.
- The exact competition profile performs zero remote inference calls.
- Every Magnificent Seven issuer resolves deterministically to its SEC identity.
- At least the latest 10-K and two 10-Q periods per issuer are preserved and
  queryable point-in-time with raw-to-canonical lineage.
- Treasury/FRED and approved market observations expose explicit availability
  and revision semantics.
- Destroying the instance loses no committed code, required receipts, or
  persistent data snapshot.

## Sprint 11: Deterministic Finance Tools and Evidence Fabric

**Outcome:** turn canonical data into reproducible financial tools and minimal,
source-grounded context packs.

### User-Visible Increment

The interface can execute one approved deterministic analysis at a time and
show its inputs, formula, output, source citations, freshness, and limitations.

### Metric Normalization

- [ ] Define a versioned canonical metric registry for revenue, operating
  income, net income, operating cash flow, capital expenditure, free cash flow,
  cash, debt, shares, and stock-based compensation.
- [ ] Map standard and issuer-extension XBRL concepts through reviewed rules;
  record mapping confidence and never merge incompatible dimensions.
- [ ] Implement duration/instant selection, quarterly-versus-YTD derivation,
  fiscal-calendar alignment, currency/unit validation, and amendment priority.
- [ ] Produce a reconciliation report from each canonical metric back to the
  filing presentation and raw XBRL facts.

### Deterministic Tool Packs

- [ ] Implement `filings.compare` for period, amendment, and disclosure change.
- [ ] Implement `fundamentals.compute` for growth, margins, cash conversion,
  capital intensity, leverage, and dilution with typed formulas.
- [ ] Implement `factors.estimate` for aligned returns, rolling OLS and Ridge,
  robust errors, diagnostics, and stability checks.
- [ ] Implement `holdings.compare` for manager positions, changes,
  concentration, overlap, and filing-delay disclosure.
- [ ] Require every tool request to declare universe, `as_of`, lookback,
  frequency, missing-data policy, and output budget.
- [ ] Emit an immutable analysis specification and result receipt containing
  code version, input hashes, formula version, diagnostics, and citations.

### Qdrant Narrative Projection

- [ ] Chunk filing sections, notes, accounting policies, risk disclosures, and
  method documentation without embedding numeric tables as authority.
- [ ] Attach issuer, filing, form, section, source hash, filed/available time,
  lifecycle, access scope, concept references, and graph IDs to every point.
- [ ] Generate embeddings locally on Radeon and record model and vector version.
- [ ] Implement exact, dense, sparse, and weighted-hybrid retrieval baselines.
- [ ] Resolve every candidate against current canonical PostgreSQL authority
  before its text enters a context pack.

### Neo4j Evidence Graph

- [ ] Add idempotent nodes for issuer, security, filing, document, section,
  concept, fact, metric, series, observation, manager, holding, analysis, claim,
  and source object.
- [ ] Add only evidence-classified edges such as `FILED`, `CONTAINS`,
  `REPORTS`, `NORMALIZES_TO`, `DERIVED_FROM`, `USES_SERIES`, `HOLDS`, and
  `SUPPORTED_BY`.
- [ ] Bind every edge to canonical IDs, source hashes, projector version, and
  an evidence class; semantic suggestions remain untrusted candidates.
- [ ] Implement independent projector checkpoints, drift detection, full
  rebuild, path allowlists, hop budgets, and stale-projection fallback.

### Context Broker

- [ ] Define typed context requests and packs with user scope, research time,
  source budget, token budget, and required evidence classes.
- [ ] Route exact numeric questions to PostgreSQL/tools and narrative questions
  to Qdrant plus bounded Neo4j paths.
- [ ] Deduplicate overlapping excerpts, preserve counterevidence, and report
  missing or conflicting evidence instead of filling gaps.
- [ ] Emit content-free retrieval receipts for discovery, canonical resolution,
  selection, rejection, freshness, graph traversal, and final context size.

### Sprint 11 Exit Gate

- Every displayed calculation is independently recomputable from cited inputs.
- Every narrative excerpt resolves to a current, permitted source object.
- Qdrant and Neo4j can be destroyed and rebuilt without canonical data loss.
- Point-in-time tests reject look-ahead facts, revised macro values, and late
  filings unavailable at the requested timestamp.
- The primary demo question produces a complete evidence pack before any LLM
  interpretation is enabled.

## Sprint 12: Governed Alpha Agent and Research Workspace

**Outcome:** convert the experience foundation into a complete local research
agent with planning, RAG, tools, memory, permissions, and a fluid web workflow.

### User-Visible Increment

The researcher asks the primary question, watches the plan and local tool calls
execute, inspects evidence, receives a cited memo, asks a follow-up, and can
delete the conversation and promoted memory.

### Planning and Orchestration

- [ ] Replace the static preview plan with a typed local-model planner whose
  output is schema-validated before execution.
- [ ] Restrict plans to allowlisted tools, bounded companies, approved data
  windows, budgets, dependencies, stop conditions, and retry policies.
- [ ] Route every tool request through existing MCP capability, identity,
  scope, approval, lease, and audit boundaries.
- [ ] Execute independent read-only steps concurrently while preserving a
  deterministic dependency graph and cancellation semantics.
- [ ] Add verify, repair, and fail-closed paths for malformed plans, missing
  evidence, stale data, tool errors, timeout, and local-model failure.
- [ ] Prevent the planner or writer from approving privileged operations or
  changing canonical data and analysis results.

### Memo Composition and Verification

- [ ] Define a research-memo contract separating reported facts, calculated
  metrics, statistical estimates, interpretation, counterarguments, and gaps.
- [ ] Pass only the bounded evidence pack to the local writer model.
- [ ] Verify citations, numbers, units, periods, company identities, and claim
  support mechanically before releasing a memo.
- [ ] Reject unsupported causal language, forecasts presented as facts, stale
  evidence, and outputs that omit material statistical limitations.
- [ ] Preserve the complete private memo as an access-controlled artifact and
  expose only content-free operational telemetry.

### Memory and Privacy

- [ ] Persist conversations, messages, citations, working summaries, memory
  candidates, and approved memory through existing governed stores.
- [ ] Promote durable memory only through an explicit policy or user action;
  raw chat is never automatically treated as truth.
- [ ] Scope retrieval by user, workspace, research session, issuer, and source
  lifecycle before embedding or graph traversal.
- [ ] Implement redaction, retention, export, deletion, tombstone, and derived
  Qdrant/Neo4j cleanup behavior.
- [ ] Add prompt-injection, indirect-injection, cross-session leakage,
  memory-poisoning, tool-abuse, and approval-bypass tests.

### Product Experience

- [ ] Stream plan, step, tool, evidence, verification, and completion events to
  the existing three-pane web interface.
- [ ] Add source cards, citation inspection, formula drill-down, data freshness,
  runtime/GPU status, cancellation, retry, and degraded-state UI.
- [ ] Add conversation history and follow-up flow without exposing hidden
  chain-of-thought; show concise reasons, evidence, and decisions instead.
- [ ] Meet keyboard, IME, responsive, screen-reader, focus, contrast, and error
  recovery requirements on desktop and mobile.
- [ ] Make the primary workflow understandable without CLI access or manual
  database inspection.

### Sprint 12 Exit Gate

- The product visibly demonstrates all five Track 2 capabilities: RAG, tools,
  planning, memory, and permission/privacy controls.
- The primary scenario and at least four adversarial variants complete with
  local Radeon inference and no remote core dependency.
- Every released material claim has a valid citation or deterministic result.
- Cancellation, restart, source outage, tool failure, and local-model failure
  produce clear bounded states without fabricated completion.
- A judge can operate the full scenario from the web interface alone.

## Sprint 13: Evaluation, ROCm Optimization, and Safety Closure

**Outcome:** improve local performance and task success using reproducible
evidence rather than demo-specific tuning.

### Evaluation Corpus

- [ ] Freeze public, tuning, untouched holdout, OOD, stale, adversarial, and
  end-to-end suites across filings, fundamentals, factors, holdings, retrieval,
  planning, tools, memory, permissions, and memo verification.
- [ ] Generate cases from source templates while preventing prompt, answer,
  source-period, and issuer leakage between partitions.
- [ ] Add mechanically graded numeric and contract cases plus citation and
  evidence-grounding review rubrics.
- [ ] Record dataset version, generation lineage, split manifest, hashes,
  licenses, and private/public boundary.

### Model and Runtime Optimization

- [ ] Establish FP16/BF16 baselines for instruction and embedding models.
- [ ] Evaluate Radeon-compatible quantization or distillation and record exact
  model revisions, artifacts, hashes, conversion tools, and runtime flags.
- [ ] Tune context length, prefill, batching, concurrency, prefix caching,
  decoding, memory utilization, and tool parallelism against held-out tasks.
- [ ] Measure startup, model load, TTFT, inter-token latency, tokens/second,
  GPU utilization, peak VRAM, p50/p95 response time, and end-to-end duration.
- [ ] Separate ingestion, retrieval, graph, tool, model, verification, and UI
  latency in every benchmark.

### Quality and Safety Gates

- [ ] Measure task completion, citation precision/recall, numeric exactness,
  required-source recall, plan validity, tool-call validity, unsupported-claim
  rate, stale rejection, and cross-scope leakage.
- [ ] Compare baseline and optimized profiles with confidence intervals and
  declared non-inferiority margins.
- [ ] Run load, soak, cancellation, restart, malformed output, dependency
  outage, disk pressure, and model-crash tests on the exact Radeon profile.
- [ ] Test prompt injection in filings, retrieved text, memory, and tool output.
- [ ] Publish sanitized raw receipts, benchmark tables, charts, and known
  limitations without exposing private cases or prompt bodies.

### Sprint 13 Exit Gate

- The selected profile improves a declared GPU or end-to-end metric without
  crossing task-success, citation, numeric, or safety regression gates.
- Holdout data remains inaccessible to runtime agents and tuning prompts.
- The full primary scenario meets a frozen interaction SLO on Radeon Cloud.
- Every public performance and quality claim is reproducible from versioned
  configuration and sanitized evidence.

## Sprint 14: Pilot, Release, and AMD Submission

**Outcome:** ship a polished, reproducible Forja Alpha release and a complete
Track 2 submission.

### Production Rehearsal

- [ ] Run the primary scenario and a second holdings-focused scenario from a
  clean Radeon Cloud template and clean source checkout.
- [ ] Rehearse first startup, warm startup, cancellation, restart, PVC recovery,
  data restore, projection rebuild, local-model outage, and source outage.
- [ ] Verify that no undocumented file, local cache, private credential, or
  manual database repair is required.
- [ ] Audit egress and prove zero remote core inference in the exact recorded
  release profile.
- [ ] Record intervention, degraded paths, retries, queue time, context size,
  GPU metrics, task quality, and final evidence hashes.

### Packaging and Documentation

- [ ] Publish one-command setup and startup paths plus a pinned Radeon Cloud
  template, dependency lockfiles, model-acquisition guide, and sample data path.
- [ ] Publish an English README covering architecture, requirements, local
  privacy, sources, licenses, setup, use, evaluation, limitations, and recovery.
- [ ] Publish the English project-specification PDF with application scenario,
  architecture diagram, capabilities, models, deployment, data lineage,
  privacy, and ROCm optimization evidence.
- [ ] Record a three-to-five-minute English demo showing actual Radeon GPU
  execution, question, plan, tools, evidence, memo, follow-up, privacy control,
  and measured performance.
- [ ] Publish an English presentation or poster focused on practical value,
  local privacy, evidence grounding, deterministic finance, and GPU results.
- [ ] Audit repository history and artifacts for secrets, private corpora,
  licensed payloads, personal paths, unsupported claims, and stale screenshots.

### Submission and Closure

- [ ] Fork the official AMD repository and open the final pull request titled
  `Track 2, <Team Name>, Forja Alpha` with every required link public.
- [ ] Recheck the official rules and submission guide on the submission day;
  record any material rule drift before changing the release.
- [ ] Tag the release only after the exact commit passes quality, security,
  clean deployment, data recovery, benchmark, documentation, and demo gates.
- [ ] Preserve source and receipts in Git, PVC, and independent backup, then
  destroy idle Radeon instances to stop credit consumption.

### Sprint 14 Exit Gate

- A new Radeon environment reproduces the submitted workflow from documented
  commands and approved data snapshots.
- The video proves local core inference and measured optimization on AMD
  Radeon/ROCm through a fluid judge-visible product.
- Source, README, PDF, video, and presentation links are public and in English.
- The release contains no secret, private evaluation body, prohibited data, or
  remote core-inference dependency.
- Independent evidence supports every published capability, benchmark, and
  data-quality claim.

## Deferred Beyond the Submission

- Real-time trading feeds, order execution, portfolio recommendations, price
  prediction, options analytics, and autonomous financial decisions.
- Universal issuer coverage before the Magnificent Seven data contracts and
  quality gates are stable.
- News and social-media ingestion without a reviewed license and provenance
  policy.
- Treating 13F filings as current positions or complete portfolio exposure.
- Promoting Alpha-specific finance contracts into the neutral Forja kernel
  without evidence from another vertical.
