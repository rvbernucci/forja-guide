# Radeon Endpoint Bootstrap

Status: Sprint 10 operator checklist. This guide prepares local model and
embedding endpoints for evidence capture; it does not claim that the endpoints
were actually served on Radeon Cloud.

## Target Shape

Forja Alpha expects two loopback-only OpenAI-compatible services on the Radeon
instance:

| Endpoint | Default | Purpose |
| --- | --- | --- |
| Instruction model | `http://127.0.0.1:8000/v1` | planning, synthesis, and local reasoning candidates |
| Embedding model | `http://127.0.0.1:8081/v1` | local vector generation for Qdrant-ready retrieval |

Remote URLs are rejected by the runtime readiness probe. The competition
profile must not use remote APIs as core inference fallbacks.

## Environment

Set these variables in the shell that runs Forja evidence commands:

```bash
export FORJA_ALPHA_MODEL_BASE_URL=http://127.0.0.1:8000/v1
export FORJA_ALPHA_EMBEDDING_BASE_URL=http://127.0.0.1:8081/v1
export FORJA_ALPHA_EMBEDDING_MODEL=<local-embedding-model-id>
export FORJA_ALPHA_ACCELERATOR='AMD Radeon GPU'
export FORJA_ALPHA_SOFTWARE_STACK='ROCm + vLLM'
```

Keep model caches, candidate configuration, benchmark outputs, and raw runtime
receipts outside Git. Prefer persistent PVC paths such as `/secure/forja` and
`/workspace/forja-alpha-sprint10-evidence`.

## Private Candidate File

Create `/secure/forja/radeon-model-candidates.json` with at least two local
instruction candidates:

```json
{
  "schema_version": "1.0",
  "candidate_set_id": "radeon-alpha-v1",
  "candidates": [
    {
      "candidate_id": "candidate-a",
      "base_url": "http://127.0.0.1:8000/v1",
      "model": "<local-model-a>",
      "server": "vllm",
      "quantization": "<precision-or-quantization>",
      "expected_context_tokens": 8192
    },
    {
      "candidate_id": "candidate-b",
      "base_url": "http://127.0.0.1:8001/v1",
      "model": "<local-model-b>",
      "server": "vllm",
      "quantization": "<precision-or-quantization>",
      "expected_context_tokens": 8192
    }
  ]
}
```

The public schema is
[`schemas/radeon-model-candidates.schema.json`](../../schemas/radeon-model-candidates.schema.json).
The file above is private because it may reveal local paths, ports, model
revisions, or operational notes.

## Serving Order

1. Start the instruction candidate services on loopback ports.
2. Start the embedding service on a separate loopback port.
3. Confirm both services expose OpenAI-compatible `/v1/models`.
4. Confirm the embedding service responds through `/v1/embeddings`.
5. Run the readiness verifier before any benchmark.
6. Run the one-shot Sprint 10 evidence runner.

## Minimum Proof Commands

```bash
python3 scripts/verify_radeon_runtime_readiness.py \
  --receipt /workspace/forja-radeon-runtime-receipt.json \
  --model-base-url "$FORJA_ALPHA_MODEL_BASE_URL" \
  --embedding-base-url "$FORJA_ALPHA_EMBEDDING_BASE_URL" \
  --embedding-model "$FORJA_ALPHA_EMBEDDING_MODEL" \
  --require-endpoints \
  --output /workspace/forja-radeon-runtime-readiness.json
```

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

## Closure Rule

The endpoint bootstrap is successful only when the public summary produced from
the private recovery report can be applied by
`scripts/apply_radeon_sprint10_public_summary.py` and then passes
`scripts/verify_sprint10_review_readiness.py`. Until then, Sprint 10 remains a
candidate and Sprint 11 is not authorized.
