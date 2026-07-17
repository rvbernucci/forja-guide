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

The candidate must not authorize another Sprint. After an independent review is
bound to the exact candidate commit, the final attestation replaces
`closure-candidate.json` with `close-receipt.json`. The two files are mutually
exclusive.

Evidence files use synthetic or sanitized data only. Large logs and runtime
artifacts will be stored outside Git and referenced by stable hashes once the
artifact store is implemented.

`make validate` fails when a Sprint evidence directory is incomplete, has the
wrong Sprint ID, uses an unsupported evidence version, contains both closure
states, or lets a candidate become authoritative or authorize downstream work.
