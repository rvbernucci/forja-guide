# ADR-0012: Fail-Soft, Content-Free Observability

Status: Accepted for Sprint 06

## Context

Forja needs connected operational signals across HTTP, MCP, scheduling, worker,
validation, publication, and PostgreSQL boundaries. Telemetry is exported to
systems with different retention and access controls from canonical state.
Allowing telemetry to become authority, carry unbounded content, or block a
valid state transition would weaken the control plane.

## Decision

Forja treats traces, metrics, logs, dashboards, and alerts as derived,
non-authoritative operational views:

1. PostgreSQL events, receipts, and state remain canonical. Telemetry cannot
   approve work, establish completion, or repair canonical state.
2. Process and HTTP boundaries propagate only W3C `traceparent`. Arbitrary
   caller baggage is rejected, and a one-shot worker accepts only one bounded
   `FORJA_TRACEPARENT` value.
3. Trace attributes and metric labels use closed, low-cardinality contracts.
   Raw prompts, outputs, errors, credentials, SQL, arguments, and business
   identifiers are excluded. Correlation and causation use truncated SHA-256
   hashes only in traces.
4. Exporter initialization, export, and shutdown failures are classified and
   surfaced operationally but cannot roll back or reject an otherwise valid
   canonical transition.
5. Structured, redacted JSON logs use `stdout`. Unstructured `stderr` is
   isolated from ingestion and is treated as potentially sensitive operator
   diagnostics. It must be redacted before external collection.
6. Sprint 06 retains only bounded operational metadata. Durable large logs,
   artifact retention, deletion, and content-addressed evidence storage belong
   to Sprint 07. Until then, unbounded bodies fail closed rather than entering
   PostgreSQL or telemetry.

## Consequences

Positive:

- one trace can correlate the governed execution path without exposing task
  content;
- telemetry outages do not become control-plane outages;
- bounded labels prevent cardinality-driven instability;
- operators can rebuild views from canonical state.

Negative:

- traces cannot answer content-level debugging questions;
- hashed identifiers require an authorized local value for correlation;
- separated stderr needs an explicit local handling and deletion policy;
- durable log retention waits for the Sprint 07 artifact boundary.

## Guardrail

Tests must reject arbitrary baggage, invalid or oversized trace parents,
high-cardinality metric labels, raw SQL or arguments, raw secrets in spans, and
unstructured stderr merged into the ingested JSONL stream. Integration tests
must prove exporter failure leaves canonical behavior unchanged and a synthetic
governed execution preserves one connected trace tree.
