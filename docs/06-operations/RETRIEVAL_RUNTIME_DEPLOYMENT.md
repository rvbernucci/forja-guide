# Governed Retrieval Runtime Deployment

Status: Sprint 10 activation operator procedure built on the Sprint 09
foundation. This document prepares a
workload for the bounded retrieval preflight and private baseline capture; it
does not activate a collection, choose a policy, or authorize a re-embedding.

An unverified historical operator note is retained in the
[VPS retrieval runtime receipt](VPS_RETRIEVAL_RUNTIME_RECEIPT.md). It reports
that Qdrant and Neo4j were available behind local-only boundaries on
2026-07-19, but it does not prove current availability and is not Sprint 09
closure evidence. A successful workload preflight is required before use.

## Environment Boundary

Start from
[`deploy/retrieval/forja-retrieval.env.example`](../../deploy/retrieval/forja-retrieval.env.example)
inside the deployment platform's private configuration boundary. Do not commit
the resulting file or copy another application's full environment into the
Forja process.

Required non-secret configuration:

| Variable | Purpose |
| --- | --- |
| `FORJA_TENANT_ID` / `FORJA_REPOSITORY_ID` | Single canonical authority scope |
| `FORJA_RETRIEVAL_COLLECTION` | Stable Qdrant alias or physical collection |
| `FORJA_QDRANT_HOST` / `FORJA_QDRANT_GRPC_PORT` / `FORJA_QDRANT_TLS` | Verified Qdrant transport boundary |
| `AWS_REGION` | Explicit Bedrock region |
| `FORJA_S3_BUCKET` / `FORJA_S3_REGION` | Governed memory-body capability |
| `FORJA_S3_ENDPOINT` / `FORJA_S3_PATH_STYLE` | Optional compatible S3 endpoint selection |

`FORJA_DATABASE_URL` and a remote `FORJA_QDRANT_API_KEY` are secrets supplied
only through the deployment secret manager. They are never command arguments,
receipts, cards, prompts, or committed files.

The Go AWS SDK resolves credentials directly through its standard chain. A
workload role with short-lived credentials is the production target. An
operator must not use SSH/container inspection to copy an application Bedrock
key. `CHAVE_API_AWS_BEDROCK` and `AWS_BEARER_TOKEN_BEDROCK` are explicitly
rejected by the retrieval runtime.

## Readiness Sequence

1. Configure the isolated retrieval workload without legacy Bedrock variables.
2. Confirm the configured Qdrant endpoint has TLS and an API key when it is
   not loopback.
3. Confirm PostgreSQL migrations and the registered retrieval generation are
   current.
4. Run the private preflight, retaining its mode-`0600` receipt outside Git:

```bash
go run ./cmd/forja-retrieval preflight \
  --timeout 20s \
  --output /secure/forja/retrieval-preflight.json
```

5. Only after a successful preflight, run the label-free four-baseline capture
   described in the [retrieval evaluation protocol](../07-evaluations/RETRIEVAL_EVALUATION_PROTOCOL.md).
6. Score the comparison using the separately access-controlled corpus. Keep
   holdout, OOD, and adversarial outputs out of policy selection.

The preflight receipt proves dependency readiness only. It does not prove
retrieval quality or a workload role's least-privilege IAM policy. Execution of
this procedure is a Sprint 10 activation gate, not a Sprint 09 closure gate.
