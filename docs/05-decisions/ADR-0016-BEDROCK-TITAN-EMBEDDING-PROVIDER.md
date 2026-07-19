# ADR-0016: Governed Bedrock Titan Embedding Provider

Status: Accepted for Sprint 09 implementation; production activation remains
blocked on IAM deployment evidence and private evaluation results.

## Context

Forja needs a production embedding provider for governed hybrid retrieval. The
existing MarIAna estate already uses Amazon Bedrock and a Titan v2, 1024-dimension
Qdrant contract. OpenAI subscription credentials and application bearer keys are
not an acceptable identity boundary for the Forja control plane.

## Decision

Use Amazon Bedrock Runtime through the AWS SDK for Go v2. The first provider
candidate is Titan Text Embeddings v2, configured as:

```text
model: amazon.titan-embed-g1-text-02
version: g1-text-v2-1024
dimensions: 1024
normalization: true
region: explicit runtime configuration
```

The Go adapter uses the standard AWS SDK credential chain only. In production,
the preferred identity is an AWS workload role with short-lived credentials.
The adapter never reads `CHAVE_API_AWS_BEDROCK`, `AWS_BEARER_TOKEN_BEDROCK`, or
an application-specific secret. It makes no network call while being constructed.

Before activation, an operator must prove that the exact model ID has access in
the selected region. The historical MarIAna identifier
`amazon.titan-embed-text-v2:0` is not silently substituted: its compatibility
must be measured and any change is a new collection generation.

## Execution Boundary

The VPS/Coolify wrapper boundary governs an approved re-embedding job, not AWS
credentials. A future wrapper may accept only an operation ID, registered
generation, expected snapshot, bounded batch limit, and explicit dry-run flag.
It must not accept model IDs, prompt text, arbitrary paths, shell fragments,
raw vectors, or secrets. The job runs under a service identity that has only
`bedrock:InvokeModel` permission for the selected model and the minimal Qdrant
write path needed by the projector.

## Consequences

- A Bedrock failure is bounded and causes no point publication or checkpoint
  advancement.
- Provider output is untrusted: the adapter checks response content type, size,
  JSON shape, dimensions, and finite vector values before returning it.
- Model, version, dimensions, and sparse encoder version remain part of the
  collection generation hash; re-embedding uses a green collection and guarded
  alias cutover.
- The existing Coolify bearer key may remain temporarily for the legacy
  TypeScript runtime, but it is not the Forja production identity.

## Non-goals

- This ADR does not authorize a Bedrock call, create an IAM role, change
  Coolify secrets, add sudoers, or deploy a worker.
- It does not select an AWS region, create a corpus, or validate retrieval
  quality. Those require separate operator evidence.

## References

- [Amazon Bedrock API keys](https://docs.aws.amazon.com/bedrock/latest/userguide/api-keys.html)
- [AWS SDK for Go v2: Bedrock Runtime](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/go_bedrock-runtime_code_examples.html)
- [Titan Text Embeddings v2 model card](https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-amazon-titan-text-embeddings-v2-2.html)
