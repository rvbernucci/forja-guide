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
