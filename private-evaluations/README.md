# Private Evaluation Boundary

This directory is intentionally excluded from public Git. It is the local
staging boundary for retrieval and agent evaluation corpora, captured outcomes,
and reports that cannot be published.

Create split directories only in the controlled environment that owns the
data:

```text
private-evaluations/
  tuning/
    capture-plan.json       # private query templates and four frozen policies
    comparison.json         # captured accepted canonical entity IDs only
    corpus.json             # separately access-controlled labels and safety classes
    comparison-report.json  # offline report; never a runtime input
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

The `capture-plan.json` and `corpus.json` are intentionally separate: the
runtime capture command sees the former, while the offline evaluator sees both
the corpus and the captured comparison. Keep every file mode `0600` or more
restrictive. Capture with `forja-retrieval capture`; score with
`forja-retrieval-eval`. Do not pass labels, expected entity IDs, or safety
classes to the capture command.

The repository validator fails if any file below this directory other than
this README becomes tracked. Access control, encryption, backup, and retention
remain responsibilities of the evaluation-store operator.
