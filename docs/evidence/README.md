# Sprint Evidence

Every Sprint closure starts as a complete, non-authoritative candidate:

```text
sprint-XX/
  plan.json
  test-report.json
  validation-report.json
  security-report.json
  rollback-report.json
  metrics-summary.json
  closure-candidate.json
```

The candidate must not authorize another Sprint. It is published to `main` in
its own pull request before the independent review starts. The final
attestation is then submitted in a separate pull request based directly on that
reviewed `main` commit. This two-phase publication preserves the immutable
candidate-to-attestation binding when GitHub squash-merges the final pull
request.

The attestation replaces `closure-candidate.json` with `close-receipt.json` and
may change only the independent review artifact, the master roadmap, and the
detailed roadmap that owns the Sprint. The two closure files are mutually
exclusive. No implementation or mutable evidence changes are permitted in the
attestation commit.

Evidence files use synthetic or sanitized data only. Large logs and runtime
artifacts will be stored outside Git and referenced by stable hashes once the
artifact store is implemented.

`make validate` fails when a Sprint evidence directory is incomplete, has the
wrong Sprint ID, uses an unsupported evidence version, contains both closure
states, or lets a candidate become authoritative or authorize downstream work.
