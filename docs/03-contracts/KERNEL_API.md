# Kernel HTTP API

Status: Implemented in Sprint 01

## Boundary

`forjad` exposes a small HTTP boundary for the in-memory kernel. It is intended
for local operation and contract testing until the governed MCP surface is
introduced in Sprint 03.

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
