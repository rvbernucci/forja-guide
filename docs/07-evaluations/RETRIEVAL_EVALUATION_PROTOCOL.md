# Governed Retrieval Evaluation Protocol

Status: Sprint 09 implementation protocol. It evaluates retrieval outside the
runtime authority path; expected entities and private labels never enter a
retrieval request, Qdrant payload, trace, metric, or context pack.

## Corpus Splits

| Split | Storage | Permitted use |
| --- | --- | --- |
| `public` | Versioned repository fixture | Contract and regression smoke tests |
| `tuning` | Access-controlled evaluation store | Select weights, limits, or model versions |
| `holdout` | Access-controlled evaluation store | Final quality claims only |
| `ood` | Access-controlled evaluation store | Unseen repositories, languages, and vocabulary |
| `adversarial` | Access-controlled evaluation store | Leakage, stale, malformed, and scope-bypass resistance |
| `regression` | Access-controlled evaluation store | Reproduce a fixed prior failure |

The public synthetic corpus is
[`internal/retrieval/testdata/retrieval_evaluation_public_v1.json`](../../internal/retrieval/testdata/retrieval_evaluation_public_v1.json).
Its matching, deliberately perfect smoke-test outcomes are
[`internal/retrieval/testdata/retrieval_evaluation_public_outcomes_v1.json`](../../internal/retrieval/testdata/retrieval_evaluation_public_outcomes_v1.json).
It uses symbolic identities only and is not representative of production
quality. Private corpus locations, query text, cards, expected answers, and
source identifiers must not be committed to this public repository.

For local controlled runs, use the ignored
[`private-evaluations`](../../private-evaluations/README.md) boundary. The
repository validator permits only its explanatory README to be tracked; every
corpus, captured outcome, and report below it remains outside public Git.
Production evaluation storage must add its own access control, encryption,
backup, and retention policy.

## Contract

Each corpus uses
[`schemas/retrieval-evaluation-corpus.schema.json`](../../schemas/retrieval-evaluation-corpus.schema.json).
A positive case has one or more `required_entity_ids`; a safety case has only
`expected_no_accepted: true`. Case IDs must be unique inside a corpus.

Capture each evaluated run separately as `EvaluationOutcome` values: only the
ordered, canonically accepted entity IDs are scored. Rejected Qdrant payloads
are never credited as retrieved context.

`forja-retrieval-eval` is a bounded offline CLI for that scoring step. It has
no network, database, environment-secret, or model-provider configuration.
It validates the corpus, outcome capture, and generated report against the
versioned schemas before writing the report atomically. For a public smoke run:

```bash
go run ./cmd/forja-retrieval-eval \
  --corpus internal/retrieval/testdata/retrieval_evaluation_public_v1.json \
  --outcomes internal/retrieval/testdata/retrieval_evaluation_public_outcomes_v1.json \
  --output /tmp/forja-retrieval-public-report.json \
  --k 10 \
  --commit "$(git rev-parse HEAD)" \
  --embedding-model fixture --embedding-version v1 \
  --embedding-dimensions 3 \
  --sparse-encoder-version sparse-fixture-v1 \
  --policy-hash sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
```

The fixture is only a contract smoke test. Real evaluation must supply the
actual, immutable policy hash and embedding descriptor from the run receipt.

## Metrics

`ScoreRankings` computes deterministic macro averages at an explicit bounded
K:

- Recall@K measures required entities recovered.
- Precision@K measures relevance of the accepted positions actually returned,
  up to K; an intentionally short governed result is not treated as unsafe.
- MRR measures the rank of the first required entity.
- nDCG@K measures the ordering of all required entities.
- `expected_no_accepted_pass / expected_no_accepted_cases` measures the safety
  subset for stale, cross-tenant, and other mandatory-rejection cases.

Every report must record corpus ID, corpus SHA-256, split, code commit,
embedding descriptor, sparse encoder version, policy hash, K, sample count,
metric values, wall-clock timings, and any degraded outcomes. Never select
weights, models, or policies using holdout/OOD/adversarial results.

## Required Comparisons

For the same frozen corpus and canonical resolver, compare lexical-only,
dense-only, unweighted RRF, and the configured weighted RRF. Reports must keep
all failed/degraded runs and demonstrate zero accepted results for every stale
and unauthorized safety case. Quality claims require a separate holdout run;
the public fixture only verifies evaluator behavior.

`CompareRequiredRankings` enforces that exact four-variant set and emits its
metric records in a stable baseline order. Each variant carries the SHA-256 of
its policy. It is intentionally a reporter, not an optimizer: only a controlled
`tuning` split may inform a proposed policy change, while holdout, OOD, and
adversarial comparisons remain non-selection evidence.
