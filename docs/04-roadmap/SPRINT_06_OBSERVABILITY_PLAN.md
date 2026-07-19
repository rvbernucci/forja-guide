# Sprint 06 Observability Plan

Status: In progress

The observability trust boundary is governed by
[ADR-0012](../05-decisions/ADR-0012-FAIL-SOFT-CONTENT-FREE-OBSERVABILITY.md).

## Outcome

Make one governed task traceable from its external command through scheduling,
worker execution, validation, persistence, and evidence without moving authority
into the telemetry plane or leaking task content.

## Trust Boundary

- PostgreSQL state, receipts, and canonical events remain authoritative.
- Traces, metrics, logs, dashboards, and alerts are derived operational views.
- Export failure must never fail or roll back an otherwise valid state change.
- Metric labels use closed enums; identifiers belong in traces or structured
  metadata, never indexed Prometheus or Loki labels.
- Raw prompts, worker output, credentials, validation bodies, and error strings
  never become trace attributes or metric labels.
- Large logs become content-addressed artifacts; database rows retain only
  bounded metadata and references.

## Delivery Sequence

- [x] Define the stable failure taxonomy and low-cardinality signal contract.
- [x] Add fail-soft OpenTelemetry runtime initialization and W3C propagation.
- [x] Export process and Forja metrics from a loopback Prometheus endpoint.
- [x] Correlate redacted JSON logs with canonical trace and span IDs.
- [x] Instrument HTTP, MCP, scheduler, worker, validation, delivery, and
  PostgreSQL query boundaries.
- [x] Add operational state collectors for stuck runs, expired leases, outbox
  backlog, projection lag, approvals, and worker crash loops.
- [x] Add version-pinned collector, Prometheus, Loki, and Grafana development
  configuration.
- [x] Provision dashboards and alert rules with bounded labels.
- [x] Prove cancellation taxonomy, exporter outage, and secret-fixture behavior.
- [x] Record the observability trust boundary in ADR-0012.
- [x] Rehearse the complete Compose stack on a clean Docker host.
- [ ] Publish reviewed Sprint evidence through the two-phase closure protocol.

## Acceptance

- One synthetic task has a connected MCP-to-publication trace; this proves
  context composition without introducing a public scheduler command.
- Every non-success terminal state maps to one stable failure class and metric.
- Secret fixtures are absent from exported logs and spans.
- Metrics expose no tenant, repository, run, task, attempt, or correlation IDs
  as labels.
- Telemetry exporter failure leaves canonical state transitions unchanged.
- Alert rules distinguish stuck work, lease expiry, backlog, lag, and crash
  loops without high-cardinality series.

## Residual Risk Policy

Sprint 06 does not make dashboards authoritative and does not provide artifact
retention. Durable large-object storage, retention, and deletion remain Sprint
07 responsibilities; Sprint 06 may define and test the reference boundary but
must fail closed rather than persist unbounded log bodies in PostgreSQL.
