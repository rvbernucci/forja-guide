# Kernel HTTP API

Status: Implemented through Sprint 02

## Boundary

`forjad` exposes a small HTTP boundary for the kernel. It uses PostgreSQL when
`FORJA_DATABASE_URL` is configured and otherwise starts with explicit
ephemeral in-memory state. It remains a local control boundary until the
governed MCP surface is introduced in Sprint 03.

All request bodies:

- must be JSON when present;
- are limited to 1 MiB;
- reject unknown fields;
- reject trailing JSON documents;
- fail with a stable machine-readable error code.

Every run returned by the daemon is validated against
[`run.schema.json`](../../schemas/run.schema.json) before it crosses the
boundary.

## Endpoints

| Method | Path | Result |
| --- | --- | --- |
| `GET` | `/healthz` | Process liveness |
| `GET` | `/readyz` | Readiness for commands |
| `GET` | `/version` | Build metadata |
| `POST` | `/v1/runs` | Create a draft run |
| `GET` | `/v1/runs/{run_id}` | Inspect a run |
| `POST` | `/v1/runs/{run_id}/transitions` | Apply an optimistic FSM transition |

Create request:

```json
{
  "objective": "Build a validated synthetic run"
}
```

Transition request:

```json
{
  "expected_version": 1,
  "target_state": "awaiting_approval"
}
```

The expected version prevents concurrent writers from silently overwriting a
newer aggregate state.

## Command Identity

POST requests accept these headers:

| Header | Purpose |
| --- | --- |
| `Idempotency-Key` | Stable command key; 8-200 characters |
| `Forja-Correlation-ID` | Trace and event correlation |
| `Forja-Causation-ID` | Optional parent command or event |
| `Forja-Actor-Type` | `human`, `agent`, `worker`, or `system` |
| `Forja-Actor-ID` | Stable actor identity |

The daemon generates safe fallback command metadata when headers are omitted
and returns the effective `Idempotency-Key`. The CLI always sends fresh command
identity. In PostgreSQL mode, replaying the same key and request returns the
stored response; reusing a key for a different command fails with `conflict`.
Keys are scoped by tenant, repository, and command scope, so unrelated
aggregates and repositories do not collide. The request fingerprint also binds
the actor and causation identity, so a key cannot silently replay a command
issued by a different authority. Correlation remains transport observability
and may differ across a legitimate retry.

Each accepted durable command commits the aggregate state, immutable event,
transactional outbox row, and idempotency receipt in one transaction.

In PostgreSQL mode, `/readyz` verifies exact parity with the embedded migration
ledger and a versioned semantic schema manifest. The manifest pins every
canonical table's ordered columns, data types, nullability, defaults and
identity generation; the complete constraint and index sets; and the
complete trigger set, enabled states, function attributes, and function bodies.
Readiness also verifies the bound tenant/repository authority. Connectivity
alone is not considered command readiness.

## Error Contract

Errors use:

```json
{
  "error": {
    "code": "invalid_argument",
    "message": "..."
  }
}
```

Stable codes are `invalid_argument`, `not_found`, `conflict`, `unavailable`,
and `internal`. Internal details are logged after secret redaction but are not
returned to clients.
