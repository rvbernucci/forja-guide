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

## Runtime Boundary

- Core language-model inference for the competition profile runs locally on AMD
  Radeon GPU through ROCm.
- Core embeddings for the competition profile run locally on AMD Radeon GPU.
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

## No-Git Boundary

Do not commit:

- private prompts, labels, retrieval cases, vectors, or model outputs;
- model weights, GGUF files, safetensors, or generated vector stores;
- platform tokens, SSH private keys, API keys, database URLs, or object-storage
  credentials;
- raw command logs that include absolute private paths or environment dumps.
