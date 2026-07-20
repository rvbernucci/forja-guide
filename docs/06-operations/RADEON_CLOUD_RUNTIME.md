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

## Local Embedding Benchmark

Use the public embedding input set only as a contract smoke test:

```bash
python3 scripts/benchmark_radeon_embedding.py \
  --input-set internal/alpha/testdata/radeon_embedding_public_v1.json \
  --base-url "$FORJA_ALPHA_EMBEDDING_BASE_URL" \
  --model "$FORJA_ALPHA_EMBEDDING_MODEL" \
  --output /workspace/forja-radeon-embedding-benchmark.json
```

The benchmark rejects non-loopback endpoints and records only input hashes,
embedding hashes, dimensions, vector norms, and latency. It never stores input
text or vectors. A valid report requires every input to produce finite vectors
with consistent dimensions.

## Competition Profile Recovery Check

After destroying and recreating the Radeon instance from the committed source
and persistent PVC snapshots, run the integrated recovery check:

```bash
python3 scripts/verify_competition_profile_recovery.py \
  --runtime-receipt /workspace/forja-radeon-runtime-receipt.json \
  --runtime-readiness /workspace/forja-radeon-runtime-readiness.json \
  --source-restore /workspace/forja-alpha-source-restore-report.json \
  --model-benchmark /workspace/forja-radeon-model-candidate-report.json \
  --embedding-benchmark /workspace/forja-radeon-embedding-benchmark.json \
  --expected-commit "$(git rev-parse HEAD)" \
  --output /workspace/forja-alpha-competition-profile-recovery.json
```

This report is the Sprint 10 bridge between infrastructure and product
readiness. It fails unless the runtime receipt records Radeon/Git evidence,
the readiness report proves loopback-only zero remote core inference, the
source snapshot restore report verifies every required data family, and at
least two local model candidates plus one local embedding endpoint complete
their frozen benchmarks without response-body, input-text, or vector storage.
It is still evidence, not a Sprint closure by itself: raw reports remain
outside Git until reviewed and summarized.

## One-Shot Sprint 10 Evidence Runner

When the Radeon instance is ready and the local model and embedding endpoints
are already serving on loopback, run the whole evidence sequence with one
command:

Use the endpoint preparation checklist in
[`RADEON_ENDPOINT_BOOTSTRAP.md`](RADEON_ENDPOINT_BOOTSTRAP.md) before running
the evidence sequence.

The operator bundle runs a private input preflight before any runtime receipt
or benchmark. If running commands manually, execute the same check first:

```bash
python3 scripts/check_radeon_sprint10_private_inputs.py \
  --snapshot-root /secure/forja \
  --model-candidates /secure/forja/radeon-model-candidates.json \
  --model-base-url "$FORJA_ALPHA_MODEL_BASE_URL" \
  --embedding-base-url "$FORJA_ALPHA_EMBEDDING_BASE_URL" \
  --embedding-model "$FORJA_ALPHA_EMBEDDING_MODEL" \
  --output /workspace/forja-radeon-private-input-preflight.json
```

This report stays private. It proves that the source snapshot files exist, the
two-candidate local model config is filled, and the core inference endpoints
are loopback-only before GPU time is spent on the evidence sequence.

```bash
python3 scripts/run_radeon_sprint10_evidence.py \
  --evidence-dir /workspace/forja-alpha-sprint10-evidence \
  --build-source-manifest \
  --source-manifest /secure/forja/alpha-source-manifest.json \
  --snapshot-root /secure/forja \
  --required-snapshot sec_identity=sec/company_tickers.json \
  --required-snapshot sec_submissions=sec/submissions/CIK0001045810.json \
  --required-snapshot sec_company_facts=sec/companyfacts/CIK0001045810.json \
  --required-snapshot treasury=treasury/real-yield-10y.csv \
  --required-snapshot fred=fred/FEDFUNDS.csv \
  --required-snapshot market=market/NVDA-adjusted.csv \
  --model-candidates /secure/forja/radeon-model-candidates.json \
  --model-base-url "$FORJA_ALPHA_MODEL_BASE_URL" \
  --embedding-base-url "$FORJA_ALPHA_EMBEDDING_BASE_URL" \
  --embedding-model "$FORJA_ALPHA_EMBEDDING_MODEL"
```

Use `--dry-run --output-plan /workspace/forja-alpha-sprint10-plan.json` first
when reviewing an instance setup. The runner executes the smaller audited
scripts in order: optional source-manifest build, runtime receipt, readiness
proof, source restore, local model benchmark, local embedding benchmark, and
competition-profile recovery, then creates the public-safe summary artifact.
It fails fast on the first broken gate and writes sanitized JSON evidence under
the evidence directory. Raw source snapshots, candidate configuration, model
weights, tokens, and private artifacts remain outside Git.

The model candidate file must follow
[`schemas/radeon-model-candidates.schema.json`](../../schemas/radeon-model-candidates.schema.json).
Use
[`internal/alpha/testdata/radeon_model_candidates.example.json`](../../internal/alpha/testdata/radeon_model_candidates.example.json)
as a safe starting point, then replace the example model IDs and ports with the
actual local endpoints running on the Radeon instance. The benchmark will
reject non-loopback URLs even if the JSON shape is otherwise valid.

The generated `radeon-public-summary.json` includes only report hashes,
validity, gate counts, and error classes. It intentionally drops private paths,
logs, prompts, model outputs, vectors, source bodies, and credentials.

Before copying the public summary back to the workstation, diagnose the
private artifact set on the Radeon instance:

```bash
python3 scripts/diagnose_radeon_sprint10_artifacts.py \
  --evidence-dir /workspace/forja-alpha-sprint10-evidence \
  --output /workspace/forja-alpha-sprint10-artifact-diagnosis.json
```

The diagnosis is read-only. It reports the first incomplete artifact and the
next action when a run stops midway. Copy back only the public summary after
the diagnosis reports `stage: ready_to_ingest_public_summary`.

If the public summary passes review, prepare the public Sprint 10 evidence
package without closing the Sprint:

```bash
python3 scripts/ingest_radeon_sprint10_public_summary.py \
  --summary /workspace/forja-alpha-sprint10-evidence/radeon-public-summary.json \
  --output /workspace/forja-alpha-sprint10-public-ingest.json
```

The ingest command copies the public-safe summary into
`docs/evidence/sprint-10/radeon-public-summary.json`, updates metrics,
validation, and the closure candidate to `ready_for_independent_review`, then
runs the readiness verifier. It still keeps the candidate non-authoritative,
leaves `next_sprint_authorized` as `null`, and reports
`next_sprint_authorized: false`. Sprint 11 starts only after a separate
immutable review promotes the candidate to a v2 close receipt.

Before requesting that immutable review, run:

```bash
python3 scripts/verify_sprint10_review_readiness.py \
  --evidence-dir docs/evidence/sprint-10 \
  --output /workspace/forja-alpha-sprint10-review-readiness.json
```

The verifier passes only when the public summary, metrics, validation report,
and closure candidate agree that the real Radeon gates are ready for review
while still refusing to authorize Sprint 11.

To inspect the full gate state and next operator commands without mutating any
evidence, run:

```bash
python3 scripts/report_sprint10_gate_status.py \
  --evidence-dir docs/evidence/sprint-10
```

After an immutable review is recorded under
`docs/evidence/sprint-10/reviews/`, generate the final close receipt with the
fail-closed promoter:

```bash
python3 scripts/promote_sprint10_close_receipt.py \
  --review-artifact docs/evidence/sprint-10/reviews/immutable-candidate-review.md \
  --reviewed-candidate-commit <40-char-candidate-commit> \
  --model <reviewer-id> \
  --dry-run
```

The promoter rejects incomplete real Radeon gates, pre-authorized candidates,
and reviews outside the Sprint evidence folder. The final closure commit must
replace `closure-candidate.json` with `close-receipt.json` and contain only the
promotion artifacts allowed by `scripts/validate_repository.py`.

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
