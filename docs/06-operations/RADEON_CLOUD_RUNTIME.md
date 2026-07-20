# Radeon Cloud Runtime

Status: Sprint 10 operator procedure. This document prepares the AMD AI
DevMaster Track 2 profile; it does not close Sprint 10 by itself.

## Template

Create the Radeon Cloud template with these public settings:

| Field | Value |
| --- | --- |
| Title | `Forja Radeon Alpha` |
| Description | `Governed private AI agent runtime with local ROCm inference, RAG, tools, memory, and permission controls.` |
| Category | `Agentic AI` |
| Tags | `forja`, `rocm`, `radeon`, `local-agent`, `rag`, `mcp`, `go` |
| Container Image | `GH-proxy-stable (amd-oneclick-base:git-proxy-test-20260528-1125)` |
| Deploy Type | `Notebook (Jupyter / OpenCode)` |
| GitHub Repo URL | `https://github.com/rvbernucci/forja-guide` |
| Branch | `main` |
| Notebook Path | leave empty unless a dedicated demo notebook is committed |
| SSH Access | enabled with the registered public key |
| Storage | `Persistent (PVC)` |
| Model Directory | `None` until the selected model is pinned |

Use persistent PVC for development and benchmarks. Local SSD can be used only
for disposable experiments whose outputs have already been pushed to Git or
backed up outside the instance.

## First Boot

Run these commands from the repository checkout on the Radeon instance:

```bash
python3 scripts/capture_radeon_runtime_receipt.py \
  --output /workspace/forja-radeon-runtime-receipt.json
python3 scripts/validate_repository.py
```

The receipt is mode `0600`, redacted, and machine-readable. It records command
availability, hashes, bounded excerpts, Git identity, ROCm probes, PyTorch ROCm
probes, vLLM availability, and sensitive environment variable presence without
recording secret values.

Keep the raw receipt outside Git. Publish only a sanitized summary under
`docs/evidence/sprint-10/` when Sprint 10 is ready for closure.

After the local instruction model and embedding servers are running, verify the
competition boundary with the readiness probe:

```bash
python3 scripts/verify_radeon_runtime_readiness.py \
  --receipt /workspace/forja-radeon-runtime-receipt.json \
  --model-base-url "$FORJA_ALPHA_MODEL_BASE_URL" \
  --embedding-base-url "$FORJA_ALPHA_EMBEDDING_BASE_URL" \
  --embedding-model "$FORJA_ALPHA_EMBEDDING_MODEL" \
  --require-endpoints \
  --output /workspace/forja-radeon-runtime-readiness.json
```

The verifier accepts only loopback OpenAI-compatible endpoints, probes
`/v1/models` and `/v1/embeddings`, validates that the receipt declares remote
core inference disabled, and writes a mode `0600` readiness report. Keep the raw
report outside Git beside the runtime receipt. A sanitized summary can be
published only after model names, private paths, and operational details are
reviewed.

## Local Model Candidate Benchmark

Use the public task set only as a contract smoke test:

```bash
python3 scripts/benchmark_radeon_model_candidates.py \
  --task-set internal/alpha/testdata/radeon_model_selection_public_v1.json \
  --candidates /secure/forja/radeon-model-candidates.json \
  --output /workspace/forja-radeon-model-candidate-report.json
```

The candidate config is private and contains loopback OpenAI-compatible model
endpoints:

```json
{
  "schema_version": "1.0",
  "candidates": [
    {
      "candidate_id": "qwen-or-gemma-local",
      "base_url": "http://127.0.0.1:8000",
      "model": "local-model-id"
    }
  ]
}
```

The benchmark rejects non-loopback endpoints and records only response hashes,
latency, finish reason, response length, and token usage when the local server
returns it. It does not store response bodies. Real model selection must run on
a private tuning task set outside Git and then be confirmed on an untouched
holdout before Sprint 10 can select an instruction model for the demo profile.

## Runtime Boundary

- Core language-model inference for the competition profile runs locally on AMD
  Radeon GPU through ROCm.
- Core embeddings for the competition profile run locally on AMD Radeon GPU.
- The competition embedding provider uses a loopback-only OpenAI-compatible
  `/v1/embeddings` endpoint, typically served by vLLM or SGLang on the same
  Radeon instance. Non-loopback embedding endpoints fail during configuration.
- Remote APIs may be used for development research, but they are not accepted
  as core-function fallbacks in the Track 2 submission profile.
- The Bedrock retrieval adapter remains available for non-competition
  deployments, but it is disabled for every core Sprint 10 Radeon workflow.
- Secrets must arrive through the platform or deployment secret boundary, never
  command-line flags, committed files, prompts, receipts, or screenshots.

## Evidence To Capture

Sprint 10 must preserve sanitized evidence for:

- GPU model, driver, ROCm, operating system, Python, PyTorch, vLLM, and selected
  model revision.
- vLLM endpoint startup, readiness, local GPU execution, latency, throughput,
  VRAM, and restart behavior.
- PVC persistence by destroying and recreating an instance, then reusing the
  Git checkout and required metadata without manual repair.
- Local embedding provider output dimensions, model revision, latency, and
  retrieval compatibility.
- Lexical-only, dense-only, unweighted RRF, and weighted RRF retrieval baseline
  results on tuning and untouched holdout partitions.

## Local Embedding Provider

Use the local provider with an endpoint base URL such as
`http://127.0.0.1:8000`. The provider appends `/v1/embeddings`, sends one raw
card or query at a time, requires JSON float embeddings, validates dimensions,
rejects non-finite values, and returns only sanitized errors.

The provider is intentionally loopback-only. This keeps Sprint 10 aligned with
the Track 2 rule that core inference must run locally on Radeon/ROCm.

## No-Git Boundary

Do not commit:

- private prompts, labels, retrieval cases, vectors, or model outputs;
- model weights, GGUF files, safetensors, or generated vector stores;
- platform tokens, SSH private keys, API keys, database URLs, or object-storage
  credentials;
- raw command logs that include absolute private paths or environment dumps.
