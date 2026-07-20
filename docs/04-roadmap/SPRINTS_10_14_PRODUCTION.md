# Sprints 10-14: AMD Track 2 and Forja 1.0

Status: Active. Sprint 10 is authorized by the authoritative Sprint 09 close
receipt. These Sprints align the Forja 1.0 critical path with the AMD AI
DevMaster Hackathon Track 2, Development and Local Deployment of Private AI
Agents, without weakening the production governance model.

## Competition Boundary

- Final submission deadline: 2026-08-06 12:59 America/Sao_Paulo.
- Core language-model and embedding inference must run locally on an AMD Radeon
  GPU through ROCm. Remote APIs are not core-function fallbacks.
- The competition profile is open source and reproducible. It must demonstrate
  local RAG, tool invocation, multi-step planning, multi-turn memory, and clear
  permission and privacy controls.
- The Radeon Cloud development template uses persistent PVC storage, SSH, and
  continuous Git publication. A dedicated vLLM deployment exposes the selected
  local model through an OpenAI-compatible endpoint.
- All submission material and the final pull request are written in English.
- The final pull request title follows `Track 2, <Team Name>, Forja Radeon`.
- The required demo video is three to five minutes. The project specification
  PDF, complete source and README are mandatory; a presentation or poster is
  also produced even where described as supplementary.

## Sprint 10: Radeon Runtime and Retrieval Evidence

**Outcome:** establish a reproducible AMD Radeon/ROCm runtime, replace remote
core inference in the competition profile, and close the private retrieval
quality debt transferred from Sprint 09.

### Scope

- [ ] Create the competition branch and fork/PR staging layout without placing
  private corpora, credentials, generated vectors, or model weights in Git.
- [ ] Create the Radeon Cloud development template using the recommended base
  image, persistent PVC storage, and SSH access. The public operator procedure
  is defined in `docs/06-operations/RADEON_CLOUD_RUNTIME.md`; the real platform
  template remains to be verified by receipt.
- [ ] Record GPU, ROCm, driver, operating-system, Python, PyTorch, vLLM, and
  model compatibility in a machine-readable environment receipt. The receipt
  schema and collector are implemented; the Radeon instance receipt remains to
  be captured outside Git.
- [ ] Prove persistence and recovery across instance destruction using PVC,
  GitHub, and an independent local backup.
- [ ] Deploy an open-weight instruction model through the dedicated Radeon
  Cloud vLLM API and verify that inference executes on the Radeon GPU.
- [ ] Benchmark at least two model/precision candidates against a frozen public
  task set before selecting the default.
- [ ] Implement a local ROCm embedding provider for the competition profile;
  keep the Bedrock adapter disabled for every core hackathon workflow.
- [ ] Create or import the access-controlled tuning, holdout, OOD, leakage,
  stale, and adversarial retrieval corpus planned in Sprint 09.
- [ ] Execute lexical-only, dense-only, unweighted RRF, and weighted RRF through
  the exact governed runtime and score them offline.
- [ ] Measure Recall@K, Precision@K, MRR, nDCG, entity-resolution accuracy,
  stale rejection, cross-tenant leakage, latency, and projection freshness.
- [ ] Demonstrate that identifier-heavy and conceptual query cohorts select
  appropriate retrieval paths without tuning on holdout labels.
- [ ] Repair and rerun the installed Qdrant system validator so its result is
  independent of the caller's working directory.
- [ ] Publish a sanitized Sprint 10 runtime and evaluation receipt while
  keeping private cases, labels, queries, and vectors outside Git.

### Implementation Progress

- [x] Defined the Radeon Cloud template procedure with persistent PVC, SSH, the
  recommended `GH-proxy-stable` base image, public Git checkout, and no model
  directory until model selection is pinned.
- [x] Added `schemas/radeon-runtime-receipt.schema.json` for sanitized runtime
  evidence.
- [x] Added `scripts/capture_radeon_runtime_receipt.py` to collect bounded GPU,
  ROCm, PyTorch, vLLM, Git, host, and secret-presence evidence without storing
  credential values.
- [ ] Capture the first real receipt from the Radeon Cloud instance and keep
  the raw artifact outside Git.

### Acceptance

- A clean Radeon Cloud instance reproduces the local model endpoint and local
  embedding path from documented commands.
- The core workflow makes zero remote inference calls.
- GPU identity, ROCm execution, latency, throughput, VRAM, and restart evidence
  are recorded without secrets or prompt bodies.
- The four retrieval baselines are measured on tuning and untouched holdout
  partitions, and every stale, unauthorized, and cross-tenant case is rejected.
- Stress-test or instance destruction loses no committed source or required
  evaluation metadata.

## Sprint 11: Graph-Grounded Context Broker

**Outcome:** combine Qdrant discovery, Neo4j proven paths, deterministic lineage,
and canonical resolution into minimal local context packs.

### Scope

- [ ] Define Neo4j node labels, relation types, evidence classes, uniqueness
  constraints, source versions, and relation hashes.
- [ ] Project repository, symbol, type, schema, test, document, artifact,
  Sprint, run, and evidence entities through idempotent outbox deltas.
- [ ] Implement projection checkpoints, drift detection, full rebuild, guarded
  rollback, and false-edge prevention.
- [ ] Implement allowlisted path templates and bounded read-only exploration.
- [ ] Implement context request and context pack contracts with tenant,
  repository, source-commit, lifecycle, and permission checks before retrieval.
- [ ] Combine exact lookup, local-embedding Qdrant candidates, and allowlisted
  Neo4j paths without allowing either derived store to establish authority.
- [ ] Resolve ambiguity through canonical PostgreSQL state and expose bounded
  alternatives instead of invented certainty.
- [ ] Enforce source, hop, latency, and token budgets with deterministic pruning
  and explicit gap reporting.
- [ ] Add source-only, no-Qdrant, no-Neo4j, stale-projection, and local-model
  unavailable fallbacks.
- [ ] Emit content-free retrieval receipts with candidate, resolution,
  selection, rejection, freshness, and token counts.
- [ ] Benchmark graph depth, fan-out, path validity, required-source recall,
  context size, and end-to-end local inference latency on the Radeon runtime.

### Acceptance

- Every context excerpt cites a current canonical source.
- Semantic similarity alone cannot create a confirmed graph edge or authority.
- Context packs reduce tokens versus naive repository search without crossing
  the agreed required-source recall gate.
- Derived-store outages degrade to bounded source-backed behavior.
- The complete retrieval-to-context path uses local Radeon inference.

## Sprint 12: Governed Local Agent Product

**Outcome:** deliver a private software-engineering co-architect that plans,
retrieves, invokes tools, remembers, and executes bounded work locally.

### Scope

- [ ] Define one judge-visible scenario: turn an approved software objective
  into a cited plan, bounded tool calls, validated changes, and evidence.
- [ ] Implement local multi-step planning with explicit budgets, stop
  conditions, retries, and deterministic plan validation.
- [ ] Route model-requested tools through the existing MCP capability,
  identity, scope, approval, lease, and audit boundaries.
- [ ] Integrate the Sprint 11 context broker as the only governed RAG entry
  point for the competition workflow.
- [ ] Integrate local multi-turn conversation and promoted memory with source
  citations, retention, redaction, and deletion behavior.
- [ ] Prevent model output from approving its own privileged operation or
  expanding filesystem, repository, tenant, tool, or credential authority.
- [ ] Run workers under a separate identity or isolation boundary and broker
  model access without exposing host or deployment credentials.
- [ ] Add prompt-injection, indirect-injection, tool-abuse, path-traversal,
  memory-poisoning, and approval-bypass tests.
- [ ] Provide a polished CLI or lightweight local interface that shows the
  plan, citations, tool activity, approvals, progress, final result, and gaps.
- [ ] Add graceful model, tool, retrieval, cancellation, restart, and timeout
  behavior suitable for a live three-to-five-minute demonstration.
- [ ] Record a full local interaction receipt without storing private prompt or
  model-response bodies in public telemetry.

### Acceptance

- The product visibly demonstrates all five Track 2 capability categories:
  RAG, tools, planning, memory, and permission/privacy controls.
- A complete scenario succeeds with core inference on the Radeon GPU and no
  remote core dependency.
- A compromised or injected worker cannot broaden its authority or read
  unrelated credentials and files.
- Multi-turn interaction remains smooth, interruptible, restart-safe, and
  understandable to a judge unfamiliar with the codebase.

## Sprint 13: ROCm Optimization and Evaluation

**Outcome:** improve local inference performance without sacrificing agent task
success, safety, or reproducibility.

### Scope

- [ ] Freeze public, private holdout, OOD, adversarial, and regression suites
  for planning, RAG, tools, memory, permissions, and end-to-end task success.
- [ ] Establish FP16 or BF16 baseline latency, throughput, VRAM, startup time,
  context limits, tool-call validity, and task success.
- [ ] Evaluate a Radeon-compatible quantized or distilled configuration and
  record its exact model, revision, runtime flags, and artifact hashes.
- [ ] Tune bounded batching, concurrency, prefix caching, context length,
  prefill, decoding, and memory utilization using evidence rather than defaults.
- [ ] Measure time to first token, inter-token latency, tokens per second, GPU
  utilization, peak VRAM, p50/p95 latency, and end-to-end task duration.
- [ ] Compare optimized and baseline profiles with confidence intervals and
  explicit quality and safety non-regression gates.
- [ ] Measure context token reduction against required-source recall and final
  task success.
- [ ] Run load, soak, cancellation, restart, malformed-output, and dependency
  failure tests on the exact Radeon deployment.
- [ ] Produce sanitized benchmark tables and charts from machine-readable raw
  receipts.
- [ ] Pin dependencies, model revisions, images, prompts, datasets, and runtime
  flags needed for independent reproduction.

### Acceptance

- The selected profile materially improves at least one GPU performance metric
  without crossing the agreed task-success or safety regression gate.
- Results distinguish model latency from retrieval, orchestration, and tool
  latency.
- Holdout cases remain unavailable to runtime agents and tuning prompts.
- Every published benchmark is reproducible from versioned configuration and
  sanitized evidence.

## Sprint 14: Pilot, Release, and AMD Submission

**Outcome:** complete a real governed local-agent pilot and submit a polished,
reproducible Track 2 entry before the official deadline.

### Scope

- [ ] Run one representative software Sprint from objective through planning,
  approval, local retrieval, local inference, tool execution, validation, and
  evidence publication.
- [ ] Record human intervention, degraded paths, retries, recovery, queue time,
  runtime, context size, token use, GPU metrics, task quality, and cost.
- [ ] Reproduce the release from a clean Radeon Cloud template and a clean
  source checkout without undocumented files or manual state repair.
- [ ] Establish demonstration SLOs and verify startup, interaction, timeout,
  cancellation, restart, PVC recovery, and no-remote-core behavior.
- [ ] Publish an English project specification PDF with scenario, architecture,
  capabilities, model, local deployment, privacy, and ROCm optimization.
- [ ] Publish an English README with environment, dependencies, setup, model
  acquisition, startup, usage, evaluation, limitations, and exact reproduction.
- [ ] Record a three-to-five-minute English demo showing the real Radeon GPU,
  complete workflow, local inference metrics, result, and recovery behavior.
- [ ] Publish an English presentation or poster focused on practical value,
  architecture, governance, local privacy, benchmarks, and open-source impact.
- [ ] Audit repository history and submission artifacts for credentials,
  private corpora, personal paths, copyrighted data, and unsupported claims.
- [ ] Fork the official AMD repository and open the final pull request titled
  `Track 2, <Team Name>, Forja Radeon` with every required link accessible.
- [ ] Tag the public release only after the exact submission commit passes all
  mandatory quality, security, reproducibility, and documentation gates.
- [ ] Preserve source and evidence in Git/PVC/local backup, then destroy idle
  Radeon instances to stop credit consumption.

### Acceptance

- A clean Radeon environment reproduces the submitted workflow.
- The video proves core inference and optimization on AMD Radeon/ROCm.
- Source, README, PDF, video, and presentation links are public and written in
  English.
- The final submission contains no secret, private evaluation body, or remote
  core inference dependency.
- A real bounded Sprint completes without manual canonical-state repair, and
  its evidence independently supports every published claim.
