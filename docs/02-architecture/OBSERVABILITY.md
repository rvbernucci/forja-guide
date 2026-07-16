# Observability Architecture

Status: Proposed

## Stack

- OpenTelemetry for context propagation and telemetry generation.
- Prometheus for metrics.
- Loki for structured logs.
- Grafana for dashboards and alert exploration.
- Object storage for long-term evidence bundles.

## Correlation IDs

Every signal carries, when applicable:

```text
tenant_id
repository_id
sprint_id
run_id
task_id
attempt_id
worker_id
trace_id
model_provider
model_name
tool_name
```

Prometheus labels must remain low-cardinality. High-cardinality identifiers
belong in logs and traces.

## Metrics

Core metrics:

- run duration and queue delay;
- state transition counts;
- worker starts, exits, timeouts, and cancellations;
- retry and recovery counts;
- lease contention and expiration;
- validation pass rate;
- approval latency;
- context retrieval latency and candidate counts;
- graph path success and gap rate;
- Qdrant and Neo4j projection lag;
- token and model cost;
- evidence completeness;
- operator intervention rate.

## Logs

Logs are structured JSON and never contain:

- secrets;
- raw environment variables;
- full private prompts by default;
- customer content without explicit classification;
- unredacted command output.

Large worker logs are written to object storage and referenced from canonical
artifact metadata.

## Dashboards

Initial dashboards:

1. Factory overview.
2. Active runs and queue.
3. Worker health and saturation.
4. Validation and failure taxonomy.
5. Retrieval and graph quality.
6. Projection freshness.
7. Cost and token budgets.
8. Approvals and security events.

## SLO Candidates

- control API availability;
- run event durability;
- scheduler recovery time;
- projection lag;
- context pack latency;
- cancellation propagation time;
- evidence completeness.

SLOs become binding only after baseline measurement.

