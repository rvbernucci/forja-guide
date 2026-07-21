# Evaluation Strategy

Status: Proposed

## Purpose

Forja must prove that additional agents and intelligence layers improve
delivery rather than only increasing activity, tokens, and complexity.

## Evaluation Axes

| Axis | Primary measures |
| --- | --- |
| Correctness | Acceptance criteria satisfied, tests passed, validator findings |
| Safety | Scope violations, authorization bypasses, secret exposure, tenant leakage |
| Reliability | Completion, recovery, retry, timeout, and cancellation success |
| Context | Required-source recall, citation validity, stale rate, token count |
| Lineage | Relation precision, coverage, evidence strength, false-edge rate |
| Efficiency | Runtime, model tokens, compute, storage, and operator interventions |
| Governance | Approval correctness, evidence completeness, audit continuity |

## Dataset Structure

Maintain:

- public synthetic fixtures;
- internal development cases;
- private holdout cases;
- out-of-distribution cases;
- adversarial prompt-injection and tool-abuse cases;
- historical regression cases.

The runtime agents must not access private holdout answers.

## Baselines

Compare:

1. human-guided single agent without retrieval;
2. single agent with naive repository search;
3. Forja exact and deterministic context only;
4. Forja exact plus Qdrant;
5. Forja exact, Qdrant, and Neo4j;
6. complete governed multi-agent execution.

This isolates the real value of each layer.

## Retrieval Evaluation

The implemented retrieval corpus boundary and deterministic ranking metrics are
defined in the [governed retrieval evaluation protocol](RETRIEVAL_EVALUATION_PROTOCOL.md).
The broader strategy remains proposed until its private corpus and release
gates are independently operated.

Measure:

- recall of required files, symbols, tests, and documents;
- precision of selected context;
- entity resolution accuracy;
- stale, cross-tenant, and unauthorized rejection separately;
- p95 retrieval latency and projection freshness;
- graph path validity;
- citation and source-hash correctness;
- stale projection detection;
- context token reduction;
- downstream task success.

A smaller context pack is not better if it omits necessary evidence.

## Routing and Agent Evaluation

Measure:

- correct specialist selection;
- unnecessary agent calls;
- author-validator independence;
- planner dependency completeness;
- failed task classification;
- retry usefulness;
- escalation precision;
- model and infrastructure cost.

## Radeon Local Model Candidate Evaluation

Sprint 10 uses a separate local-model candidate benchmark before selecting the
instruction model for the AMD demo profile. The public smoke task set is
[`internal/alpha/testdata/radeon_model_selection_public_v1.json`](../../internal/alpha/testdata/radeon_model_selection_public_v1.json).
It checks benchmark plumbing only; it is not a quality claim.

Run the benchmark against loopback OpenAI-compatible endpoints on Radeon:

```bash
python3 scripts/benchmark_radeon_model_candidates.py \
  --task-set internal/alpha/testdata/radeon_model_selection_public_v1.json \
  --candidates /secure/forja/radeon-model-candidates.json \
  --output /workspace/forja-radeon-model-candidate-report.json
```

Private tuning and holdout task sets stay outside Git. Reports store response
hashes, latency, completion length, finish reason, and provider token usage
when available, but never response bodies. Model selection must consider task
completion, safety behavior, local latency, model load/startup cost, GPU memory,
and failure modes; a faster model is not acceptable if it loses evidence
discipline or financial-safety boundaries.

## Statistical Discipline

- report sample size;
- separate tuning and holdout sets;
- include uncertainty intervals;
- avoid selecting policies on the same cases used for final claims;
- preserve failed and inconclusive runs;
- record model, prompt, code, schema, and dataset versions.

## Release Gate

No release may improve cost while regressing correctness or safety below its
declared gate.
