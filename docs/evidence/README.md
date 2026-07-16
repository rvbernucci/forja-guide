# Sprint Evidence

Every closed Sprint publishes a complete evidence set:

```text
sprint-XX/
  plan.json
  test-report.json
  validation-report.json
  security-report.json
  rollback-report.json
  metrics-summary.json
  close-receipt.json
```

Evidence files use synthetic or sanitized data only. Large logs and runtime
artifacts will be stored outside Git and referenced by stable hashes once the
artifact store is implemented.

`make validate` fails when a Sprint evidence directory is incomplete, has the
wrong Sprint ID, uses an unsupported evidence version, or lacks a closed
receipt.

