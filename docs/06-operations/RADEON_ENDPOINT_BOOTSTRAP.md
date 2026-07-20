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

Before spending time on the instance, render a public-safe command sheet for
the exact host and port:

```bash
python3 scripts/render_radeon_sprint10_command_sheet.py \
  --host <host> \
  --port <port> \
  --output /tmp/sprint10-radeon-command-sheet.md
```

The command sheet is an operator aid only. It contains no credentials or
private receipts and does not close Sprint 10.

To generate private templates on the Radeon instance:

```bash
python3 scripts/prepare_radeon_sprint10_operator_bundle.py
```

The generator writes a private env template, two-candidate config template, and
ordered evidence script under `/workspace/forja-alpha-sprint10-operator-bundle`.
Fresh templates contain placeholders. Validate the generated shape before
editing with:

```bash
python3 scripts/verify_radeon_operator_bundle.py \
  --bundle-dir /workspace/forja-alpha-sprint10-operator-bundle \
  --allow-placeholders
```

After replacing local model IDs, embedding model ID, and quantization notes,
run the strict bundle pre-flight:

```bash
python3 scripts/verify_radeon_operator_bundle.py \
  --bundle-dir /workspace/forja-alpha-sprint10-operator-bundle
```

Then run the private input pre-flight before spending GPU time on evidence
collection:

```bash
python3 scripts/check_radeon_sprint10_private_inputs.py \
  --snapshot-root /secure/forja \
  --model-candidates /secure/forja/radeon-model-candidates.json \
  --model-base-url "$FORJA_ALPHA_MODEL_BASE_URL" \
  --embedding-base-url "$FORJA_ALPHA_EMBEDDING_BASE_URL" \
  --embedding-model "$FORJA_ALPHA_EMBEDDING_MODEL" \
  --output /workspace/forja-radeon-private-input-preflight.json
```

This check fails if required private snapshots are missing, the local model
candidate file still contains placeholders, or any core inference endpoint is
not loopback-only. It is a private readiness receipt and should stay outside
Git.

Before opening an SSH session from a workstation, classify the endpoint:

```bash
python3 scripts/probe_radeon_ssh.py <host> <port>
```

The probe reports whether an SSH banner is visible, whether the TCP port is
refused, or whether the instance accepted TCP but did not send a banner before
the timeout.

When the instance is still booting, run the workstation preflight before
copying files or starting evidence commands:

```bash
python3 scripts/preflight_radeon_ssh.py <host> <port> \
  --timeout-seconds 180 \
  --interval-seconds 10 \
  --probe-timeout-seconds 8 \
  --wait-output /tmp/forja-radeon-ssh-wait.json \
  --recovery-output /tmp/forja-radeon-ssh-recovery.md \
  --repo-url https://github.com/rvbernucci/forja-guide \
  --branch feat/sprint-10-radeon-runtime-v2 \
  --repo-dir /workspace/forja-guide \
  --output /tmp/forja-radeon-ssh-preflight.json
```

`connected_no_banner` means the TCP proxy accepted the connection but SSH is
not ready yet. Treat `ready` as the only signal that SSH work may proceed. The
wait report also includes `next_action` and `operator_hints`; use those hints
to decide whether to start `sshd` from the Radeon web terminal, recreate the
template with SSH enabled, or replace a stale host/port before copying files.
If the preflight is not ready, it automatically writes
`/tmp/forja-radeon-ssh-recovery.md`; follow that sheet inside the Radeon web
terminal before retrying.
The recovery sheet first runs `scripts/diagnose_radeon_sshd.py` inside the
Radeon web terminal and writes `/workspace/forja-radeon-sshd-diagnosis.json`.
Keep that diagnosis private beside the runtime artifacts; it is a repair aid,
not Sprint closure evidence.

If the web terminal is usable but SSH remains unavailable, render the fallback
bootstrap from the workstation:

```bash
python3 scripts/render_radeon_sprint10_web_terminal_bootstrap.py \
  --repo-url https://github.com/rvbernucci/forja-guide \
  --branch feat/sprint-10-radeon-runtime-v2 \
  --repo-dir /workspace/forja-guide \
  --output /tmp/forja-radeon-web-terminal-bootstrap.sh
```

Then paste or upload that bootstrap into the Radeon web terminal. It prepares
the checkout, validates the repository, generates the private operator bundle,
verifies placeholder-safe shape, and renders the evidence sheet without
starting model servers or collecting evidence.

The fallback evidence sheet can also be rendered directly:

```bash
python3 scripts/render_radeon_sprint10_web_terminal_sheet.py \
  --repo-url https://github.com/rvbernucci/forja-guide \
  --branch feat/sprint-10-radeon-runtime-v2 \
  --repo-dir /workspace/forja-guide \
  --output /tmp/forja-radeon-web-terminal-evidence.md
```

This fallback preserves the same fail-closed evidence order through the Radeon
Jupyter/OpenCode terminal: checkout, bundle generation, strict private
preflights, local endpoint evidence, artifact diagnosis, and export of only
`radeon-public-summary.json`. It does not weaken the Sprint 10 closure rule and
does not authorize Sprint 11.

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
5. Run the private input preflight.
6. Run the readiness verifier before any benchmark.
7. Run the one-shot Sprint 10 evidence runner.

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
the private recovery report can be ingested by
`scripts/ingest_radeon_sprint10_public_summary.py` and then passes
`scripts/verify_sprint10_review_readiness.py`. Until then, Sprint 10 remains a
candidate and Sprint 11 is not authorized.
