# Observability Operations

Status: Sprint 06 implementation

## Authority Boundary

PostgreSQL events, aggregates, receipts, and delivery journals are canonical.
Traces, metrics, logs, alerts, and dashboards are disposable operational views.
An exporter outage must never reject, commit, retry, or roll back a governed
command.

Telemetry never records raw prompts, worker output, SQL, SQL arguments,
credentials, or raw error messages. Prometheus labels use closed enums only.
Run, task, attempt, tenant, repository, worker, and correlation identifiers are
not labels. Trace correlation uses irreversible identifier hashes, while JSON
logs receive canonical trace and span IDs from context rather than caller
fields.

## Runtime Controls

| Variable | Default | Purpose |
| --- | --- | --- |
| `FORJA_OTEL_ENABLED` | `false` | Enables OTLP trace export |
| `FORJA_TRACE_SAMPLE_RATIO` | `0.1` | Samples root traces from `0` to `1` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | SDK default | OTLP endpoint, normally `http://127.0.0.1:4318` |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | SDK default | Use `http/protobuf` with the local stack |

`/metrics` is available only when the telemetry runtime is installed. The
built-in daemon still binds to loopback, so production scraping requires a
deployment-owned authenticated proxy or sidecar boundary.

The stdio MCP process exposes its separate process-local registry on
`127.0.0.1:9464` by default. `FORJA_MCP_METRICS_LISTEN` may select another
numeric loopback address or `off`; remote plaintext binds are rejected.

## Local Stack

Enable Docker Desktop host networking, create an external runtime log
directory, and start the pinned stack on macOS or Windows. Host networking lets
Prometheus scrape the mandatory loopback-only runtime without exposing it to
the LAN:

```bash
mkdir -p /tmp/forja/logs
docker compose -f deploy/observability/compose.yaml up -d
```

On native Linux, use the host-network profile. Prometheus and Grafana bind
their host-network listeners only to loopback, allowing Prometheus to scrape
the mandatory loopback-only daemon without exposing `forjad` to the LAN:

```bash
docker compose -f deploy/observability/compose.linux.yaml up -d
```

Run `forjad` on the host and tee its JSON output to the external directory:

```bash
export FORJA_OTEL_ENABLED=true
export FORJA_TRACE_SAMPLE_RATIO=1
export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
go run ./cmd/forjad --listen 127.0.0.1:8080 \
  > >(tee -a /tmp/forja/logs/forjad.jsonl) 2>&1
```

Local endpoints:

- Grafana: `http://127.0.0.1:3000`
- Prometheus: `http://127.0.0.1:9090`
- Loki: `http://127.0.0.1:3100`
- Tempo: `http://127.0.0.1:3200`
- Alloy: `http://127.0.0.1:12345`

Anonymous Grafana viewer access is intentionally local-development-only. Do
not expose this Compose profile outside loopback.

The clean Linux rehearsal is automated by:

```bash
scripts/observability_stack_smoke.sh
```

It starts the actual daemon and stack, then proves Prometheus scraping, Loki
log ingestion, and Tempo trace search. The script tears down test volumes on
both success and failure.

## Operational Conditions

`forja_operational_condition_items` is derived on scrape with one bounded,
read-only PostgreSQL aggregate query:

- `stuck_runs`: queued or active runs unchanged for 15 minutes without an
  exact live attempt fence; long-running workers with renewed authority are
  excluded;
- `expired_leases`: naturally expired lease rows; explicit releases are
  excluded by their release-time update marker;
- `pending_outbox`, `inflight_outbox`, and `dead_outbox`;
- `projection_lag`: authority-bound outbox rows beyond the oldest checkpoint;
- `pending_approvals`;
- `worker_crash_loops`: runs with at least three retryable attempt failures in
  15 minutes.

Collection has a two-second timeout. Failure removes the condition samples and
sets `forja_operational_collection_success` to zero instead of serving stale
state as if it were current.

## Triage

1. Confirm `forja_operational_collection_success == 1` before trusting derived
   condition panels.
2. Use the failure taxonomy and boundary latency to localize the subsystem.
3. Follow the trace into redacted JSON logs by canonical trace ID.
4. Verify the suspected condition against PostgreSQL canonical state.
5. Use the recovery runbooks; never repair state from Grafana or Prometheus.

Large worker output remains outside PostgreSQL. Sprint 07 owns durable object
storage, retention, and deletion; until then, preserve bounded evidence
references and fail closed rather than storing unbounded bodies.
