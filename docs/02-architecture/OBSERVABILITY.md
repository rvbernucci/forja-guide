# Observability Architecture

Status: Sprint 06 foundation implemented; closure pending

The trust, propagation, persistence, and failure behavior of this subsystem is
recorded in
[ADR-0012](../05-decisions/ADR-0012-FAIL-SOFT-CONTENT-FREE-OBSERVABILITY.md).

## Stack

- OpenTelemetry for W3C context propagation and content-free traces.
- Prometheus for metrics.
- Loki for structured logs.
- Grafana for dashboards and alert exploration.
- Tempo for local trace exploration.
- Object storage for long-term evidence bundles beginning in Sprint 07.

## Correlation IDs

Structured logs may carry the canonical trace and span IDs derived from their
Go context. High-cardinality business identifiers are not metric labels.
Traces may carry irreversible hashes for correlation and causation, but never
their raw values. The current stable span attributes are:

```text
forja.boundary
forja.operation
forja.outcome
forja.failure_class
forja.correlation_hash
forja.causation_hash
```

A synthetic governed-delivery test proves one connected trace tree from an MCP
parent context through scheduler, worker dispatch, independent validation, and
publication. This verifies context composition without claiming a public MCP
scheduler command; that product surface remains outside Sprint 06.

Prometheus labels are closed enums. Tenant, repository, Sprint, Run, task,
attempt, worker, correlation, and causation identifiers are forbidden labels.

## Metrics

Implemented foundation metrics:

- boundary operation count, duration, outcome, and stable failure class;
- in-flight operations;
- telemetry-plane failures;
- stuck active runs and expired leases;
- pending, in-flight, and dead outbox rows;
- canonical outbox-to-projection checkpoint lag;
- pending approval count;
- worker crash-loop count;
- process and Go runtime metrics.

Retrieval quality, graph quality, model cost, token budgets, and evidence
completeness are added only with the subsystems that own those measurements.

## Logs

Logs are structured JSON and never contain:

- secrets;
- raw environment variables;
- full private prompts by default;
- customer content without explicit classification;
- unredacted command output.

Worker output is currently bounded before persistence. PostgreSQL attempt
events retain only hashes, termination metadata, usage, and bounded evidence
references, not stdout or stderr bodies. Durable large-log object storage and
retention are Sprint 07 work.

## Dashboards

The provisioned Sprint 06 dashboard covers:

1. Operational state collection health.
2. Active anomaly counts and projection freshness.
3. Operation throughput and P95 boundary latency.
4. Stable failure taxonomy.
5. Redacted JSON logs.

Retrieval, graph, and model-cost dashboards remain intentionally absent until
their canonical producers exist.

## SLO Candidates

- control API availability;
- run event durability;
- scheduler recovery time;
- projection lag;
- context pack latency;
- cancellation propagation time;
- evidence completeness.

SLOs become binding only after baseline measurement.
