# ADR-0019: Keep AMD Track 2 Core Inference Local

Status: Accepted for Sprints 10-14

## Context

AMD AI DevMaster Hackathon Track 2 requires private local deployment on an AMD
Radeon GPU and ROCm. Core inference cannot depend on remote APIs. The existing
Forja retrieval implementation has a production-oriented Bedrock Titan
embedding adapter, while the competition rewards local inference, measurable
Radeon optimization, and quantization or distillation.

## Decision

1. Add an `amd-track2` deployment profile whose language-model and embedding
   inference both run locally on the designated Radeon GPU through ROCm.
2. Serve the selected open-weight instruction model through the dedicated
   Radeon Cloud vLLM endpoint and consume it through its OpenAI-compatible
   boundary.
3. Select and pin a local embedding model only after Sprint 10 retrieval
   baselines prove its quality and runtime fit.
4. Disable Bedrock and every shared or external model API for core competition
   workflows. Existing adapters remain available for non-competition profiles
   but are not failover paths in the recorded demo or evaluation.
5. Keep PostgreSQL canonical, Qdrant and Neo4j derived, and every model output
   untrusted until deterministic contracts and canonical resolution accept it.
6. Record model revision, precision, quantization or distillation method,
   runtime flags, ROCm environment, GPU identity, latency, throughput, VRAM,
   and task-success evidence without recording private prompt bodies.

## Consequences

- The competition profile can operate without sending repository context,
  conversations, or memory to a remote inference provider.
- Sprint 10 must implement a local embedding adapter and rerun the retrieval
  quality evaluation transferred from Sprint 09.
- Local model failure degrades explicitly; it does not silently call a remote
  provider and invalidate the architecture claim.
- The vLLM endpoint is a replaceable inference boundary, not an authority
  service. Forja remains neutral to the selected open-weight model.
- Model weights and private evaluation corpora remain outside Git and are
  acquired through documented, license-compliant procedures.
