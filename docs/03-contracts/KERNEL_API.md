# Kernel HTTP API

Status: Implemented kernel boundary; governed MCP added in Sprint 03

## Boundary

`forjad` exposes a small authenticated HTTP boundary for the kernel. It uses
PostgreSQL when `FORJA_DATABASE_URL` is configured and otherwise starts with
explicit ephemeral in-memory state. Sprint 03 adds the authenticated MCP interaction
boundary described in [MCP Control API](MCP_CONTROL_API.md); direct kernel HTTP
endpoints remain local and do not replace governed MCP authorization.

The `/v1` surface requires one bearer credential supplied only through
`FORJA_HTTP_BEARER_TOKEN`. Liveness, readiness, and version remain public so
orchestrators can operate the process without command authority.
The built-in HTTP server binds only to a numeric loopback IP. The CLI requires
HTTPS before sending the credential to any hostname or non-loopback IP.

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

When a Run is linked to a governed Sprint, this legacy transition endpoint
cannot move a proposed Run toward execution or bypass a pending decision. A
proposed Sprint must use `forja.submit_sprint`; only explicit cancellation is
available before submission. Post-approval worker transitions remain governed
by the canonical FSM.

## Command Identity

The daemon maps the bearer credential to a server-configured principal from
`FORJA_HTTP_ACTOR_TYPE` and the required `FORJA_HTTP_ACTOR_ID`. Callers cannot
select their own actor identity. Reads require `control:read`; create and
generic transition requests require `legacy_run:write`; and every request must
match the daemon's bound tenant and repository.

POST requests accept these headers:

| Header | Purpose |
| --- | --- |
| `Idempotency-Key` | Stable command key; 8-200 characters |
| `Forja-Correlation-ID` | Trace and event correlation |
| `Forja-Causation-ID` | Optional parent command or event |

The daemon generates safe fallback command metadata when command headers are
omitted and returns the effective `Idempotency-Key`. The CLI creates command
identity before transport and accepts `--idempotency-key` so an ambiguous
command can be retried with the same identity. A failed CLI command prints its
effective key.
Both repositories replay the stored response when the same key and request are
reused; reusing a key for a different command fails with `conflict`.
Keys are scoped by tenant, repository, and command scope, so unrelated
aggregates and repositories do not collide. The request fingerprint also binds
the authenticated actor and causation identity, so a key cannot silently replay
a command issued by a different authority. Correlation remains transport
observability and may differ across a legitimate retry.

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

Stable codes are `invalid_argument`, `unauthenticated`, `permission_denied`,
`not_found`, `conflict`, `unavailable`, and `internal`. Internal details are
logged after secret redaction but are not returned to clients.

Missing, malformed, duplicated, or invalid bearer credentials return `401`
with a `WWW-Authenticate` challenge before request parsing. Authenticated
principals without the required capability or repository scope return `403`
before persistence.
