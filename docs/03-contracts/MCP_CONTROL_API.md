# MCP Control API

Status: Implemented in Sprint 03

## Boundary

`forja-mcp` exposes Forja's governed application service through the official
Go Model Context Protocol SDK. MCP is an interaction adapter: tool handlers do
not write SQL, mutate workers, change Git state, or authorize themselves.

Every command follows this path:

```text
MCP input schema
  -> authenticated principal
  -> permission check
  -> canonical control command
  -> idempotent repository transaction
  -> immutable domain events, outbox, and success audit
  -> structured MCP output
```

The authenticated principal carries actor identity, capabilities, tenant ID,
and repository ID. The service rejects the command before persistence unless
that authority exactly matches the repository-bound store.

The stdio adapter assigns a fixed least-privilege capability profile from the
authenticated actor type:

| Actor type | Capabilities |
| --- | --- |
| `agent` (default) | Plan, read, submit, and cancel; cannot decide or resume |
| `worker` | Read only |
| `human` | All control capabilities, including decision and resume authority |
| `system` | All capabilities for a separately authenticated automation boundary |

Choosing `human` or `system` is an explicit trust-boundary configuration, not a
claim inferred from conversational content.

Conversational content is never parsed as command authority. In particular,
text such as `yes`, `approved`, or `go ahead` cannot approve a Sprint. The
approval tool requires the exact pending `decision_id`, its expected version,
a reason, an idempotency key, and a correlation ID.

## Tools

| Tool | Required permission | Canonical effect |
| --- | --- | --- |
| `forja.plan_sprint` | `sprint:plan` | Creates a proposed Sprint and linked draft Run |
| `forja.submit_sprint` | `sprint:submit` | Moves the Sprint and Run to awaiting approval and creates a pending decision |
| `forja.get_sprint` | `control:read` | Reads one Sprint and its pending decision ID |
| `forja.get_run` | `control:read` | Reads one canonical Run |
| `forja.approve_decision` | `decision:decide` | Resolves one pending decision and queues its Run |
| `forja.reject_decision` | `decision:decide` | Rejects one pending decision and begins Run cancellation |
| `forja.cancel_run` | `run:cancel` | Cancels a Run and moves its linked proposed or approved Sprint to `cancelling` |
| `forja.resume_run` | `run:resume` | Requeues a retryable failure or resumes an awaiting decision |

The generated input and output schemas are compatibility-pinned in
[`tools-v1.json`](../../internal/mcpserver/testdata/tools-v1.json). Canonical
Sprint and decision bodies are separately validated by
[`sprint.schema.json`](../../schemas/sprint.schema.json) and
[`decision.schema.json`](../../schemas/decision.schema.json).

## Command Identity

Mutating tools require:

```json
{
  "idempotency_key": "stable-command-key-0001",
  "correlation_id": "corr-stable-command-0001",
  "causation_id": "optional-parent-action"
}
```

Read tools also validate these fields so every accepted MCP action has a stable
caller-supplied audit identity. Reads remain live rather than replaying a stale
snapshot. Repeating a mutating command with the same key and body returns its
stored response. Reusing the key for a different body, actor, or causation
identity fails with `conflict`.

Each original mutation and each successful replay writes one correlated audit
event. Audit payloads mark `replay` explicitly and carry the exact
`command_scope` of the receipt they evidence. The original mutation, its
non-replay success audit, domain events, outbox rows, and replay receipt share
one transaction. A replay audit can prove a retry occurred but cannot replace
the original atomic audit. A failed audit therefore cannot leave an unaudited
state change behind, and legal idempotency-key reuse across aggregate scopes
cannot attach one command's audit to another command.

`expected_version` is mandatory for state-changing tools. Concurrent requests
therefore cannot silently overwrite a newer Sprint, decision, or Run.

## Stdio Authentication

The local stdio transport is enabled by `forja-mcp`. It requires an explicit
principal before starting:

```bash
export FORJA_MCP_ACTOR_ID='codex-co-architect'
export FORJA_MCP_ACTOR_TYPE='agent'
go run ./cmd/forja-mcp
```

This default co-architect profile deliberately cannot call
`forja.approve_decision`, `forja.reject_decision`, or `forja.resume_run`.
Register a separate, authenticated human or system endpoint when those actions
are required; do not change the co-architect process to self-approve its plan.

The process writes protocol messages only to standard output. Operational
warnings use standard error so they cannot corrupt the MCP stream.

Without `FORJA_DATABASE_URL`, one stdio process uses isolated ephemeral state.
For durable sessions, configure PostgreSQL:

```bash
export FORJA_DATABASE_URL='postgres:///forja?host=/tmp'
export FORJA_DATABASE_AUTO_MIGRATE='true'
go run ./cmd/forja-mcp
```

## Codex Registration

Build a stable local executable:

```bash
go build -trimpath -o "$HOME/.local/bin/forja-mcp" ./cmd/forja-mcp
```

Register the stdio server with Codex:

```bash
codex mcp add forja \
  --env FORJA_MCP_ACTOR_ID=codex-co-architect \
  --env FORJA_MCP_ACTOR_TYPE=agent \
  -- "$HOME/.local/bin/forja-mcp"

codex mcp get forja
```

For a durable local database, add a password-free Unix-socket URL as
`FORJA_DATABASE_URL`. Do not place a password-bearing database URL in shell
history or committed Codex configuration; inject it through an approved local
secret boundary instead.

## Remote HTTP Boundary

Sprint 03 defines but does not deploy the remote Streamable HTTP transport.
`AuthenticatedHTTPBoundary` requires a non-nil bearer verifier and rejects the
request before the MCP handler when authentication is missing or invalid. A
permissive or anonymous remote constructor is intentionally absent.

Before enabling HTTP in a later deployment, Forja must add issuer, audience,
expiry, tenant, repository, revocation, TLS, origin, and rate-limit policies.
The official SDK's Streamable HTTP handler must remain behind this boundary.

## Failure Semantics

- Authorization is checked before command persistence.
- A Run with a pending decision cannot be transitioned by cancellation,
  scheduler, or legacy HTTP commands until that exact decision is resolved.
- A Run linked to a proposed Sprint can only be cancelled or submitted through
  the governed command; generic FSM transitions cannot manufacture approval.
- The generic transition repository rejects `failed_retryable -> queued` and
  `awaiting_decision -> queued`; only the capability-checked resume command can
  perform those pairs.
- An awaiting-decision resume queues a new scheduling cycle. The blocked
  Attempt remains terminal, and execution requires a fresh Attempt plus a new
  immutable delivery authorization for that exact attempt.
- Domain mutations and their events, outbox rows, replay receipts, and success
  audits commit in one PostgreSQL transaction.
- Tool outcomes emit correlated audit events. Cross-scope authorization
  denials are written to the bound repository audit stream without granting
  the denied principal any domain authority.
- An audit persistence failure is returned as an unavailable tool result; a
  retry uses the same command key and cannot duplicate the domain mutation.
- Invalid state transitions and stale versions fail closed.
- PostgreSQL readiness verifies the complete migration ledger and semantic
  schema manifest, including decisions and audit-capable event types.
- Rolling migration 003 back removes `audit` and `decision` events that the
  Sprint 02 event constraint cannot represent. Sprint and Run domain events are
  preserved, derived references are repaired, and the append-only trigger is
  restored before the rollback commits. It first locks governed command and
  cleanup tables to quiesce writers, then refuses to proceed while a
  decision is pending, so no governed aggregate can lose its only resolution
  path. Receipts whose responses depend on removed Sprint 03 state are deleted
  so a later re-upgrade cannot replay a stale decision. Each preserved domain
  event receives its own immutable invalidation marker containing the exact
  removed command scope, command-anchor identity, and domain event ID. The
  marker identity is deterministically recomputable from that evidence.
  Receipt classification consumes exact event IDs rather than reusable actor
  metadata, and also requires the audit's exact `command_scope`.
