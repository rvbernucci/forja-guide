# Local Observability Stack

This directory provisions a development-only telemetry stack for Forja. All
published ports bind to loopback, all images use exact release tags, and all
state stays in named Docker volumes or the external `FORJA_LOG_DIR`.

Pinned components:

- OpenTelemetry Collector Contrib `0.153.0`;
- Prometheus `3.12.0`;
- Loki `3.7.2`;
- Alloy `1.16.1`;
- Tempo `2.10.5`;
- Grafana `13.1.0`.

Enable Docker Desktop host networking, then start and validate on macOS or
Windows. Host networking is required so Prometheus can reach Forja's mandatory
loopback-only listeners without exposing them on a LAN interface:

```bash
mkdir -p /tmp/forja/logs
docker compose -f deploy/observability/compose.yaml config --quiet
docker compose -f deploy/observability/compose.yaml up -d
docker compose -f deploy/observability/compose.yaml ps
```

On native Linux, use the host-network profile so Prometheus can reach the
mandatory loopback-only daemon without exposing it on a LAN interface:

```bash
docker compose -f deploy/observability/compose.linux.yaml config --quiet
docker compose -f deploy/observability/compose.linux.yaml up -d
docker compose -f deploy/observability/compose.linux.yaml ps
```

Both profiles bind the Prometheus and Grafana web servers explicitly to
`127.0.0.1`; only those two services use host networking. Alloy tails
`*.jsonl` from `FORJA_LOG_DIR`. The Go OTLP
exporter sends traces to the Collector on `127.0.0.1:4318`, which forwards them
to Tempo. Grafana provisions all three data sources and the runtime dashboard.

The clean-host CI rehearsal runs `scripts/observability_stack_smoke.sh`. It
starts the Linux profile and a real `forjad`, then proves Prometheus scraping,
Loki log ingestion, and Tempo trace search before removing every test volume.

This profile is not a production security boundary. Production deployment
requires authentication, TLS, retention policy, resource sizing, image digest
pinning, backup policy, and a deployment-owned metrics proxy.
