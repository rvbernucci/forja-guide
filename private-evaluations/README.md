# Private Evaluation Boundary

This directory is intentionally excluded from public Git. It is the local
staging boundary for retrieval and agent evaluation corpora, captured outcomes,
and reports that cannot be published.

Create split directories only in the controlled environment that owns the
data:

```text
private-evaluations/
  tuning/
  holdout/
  ood/
  adversarial/
  regression/
```

Each run must retain the corpus ID and SHA-256, code commit, embedding
descriptor, sparse encoder version, policy hash, command, captured outcomes,
and generated report. Every captured outcome includes only accepted canonical
entity IDs, bounded per-case latency, and projection-lag count. Do not retain
query text, cards, vectors, payloads, provider responses, or labels in a
runtime-visible location. Holdout, OOD, and adversarial results must never
select the policy they evaluate, and no private answer may be made available to
the Forja runtime.

The repository validator fails if any file below this directory other than
this README becomes tracked. Access control, encryption, backup, and retention
remain responsibilities of the evaluation-store operator.
