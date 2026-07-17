# Runtime and MCP Architecture

Status: Implemented through Sprint 03; worker execution remains proposed

## MCP Boundary

Forja implements its own MCP server while remaining compatible with the
standard Model Context Protocol.

MCP is the interaction boundary, not the internal scheduler or database.

Implemented Sprint 03 tools:

```text
forja.plan_sprint
forja.submit_sprint
forja.get_sprint
forja.get_run
forja.approve_decision
forja.reject_decision
forja.cancel_run
forja.resume_run
```

Every tool:

- validates a versioned input schema;
- authenticates the caller;
- resolves tenant and repository scope;
- checks authorization and policy;
- writes a canonical command or decision;
- returns stable IDs rather than relying on chat text.

The local implementation uses the official Go MCP SDK over stdio. Its generated
schemas are compatibility-pinned, and a deterministic SDK client exercises the
complete proposal, submission, decision, inspection, cancellation, and resume
surface. See the [MCP control API](../03-contracts/MCP_CONTROL_API.md).

Capability separation remains outside conversational control. The default
co-architect `agent` can propose and submit work, but only a separately
authenticated `human` or `system` endpoint can decide or resume it. A single
agent session therefore cannot turn its own plan into authorized execution.
The persistence boundary independently rejects `failed_retryable -> queued`
and `awaiting_decision -> running` through the generic transition API, so a
legacy HTTP caller cannot bypass `run:resume` authorization.

Remote Streamable HTTP is not deployed in Sprint 03. The code defines a
fail-closed bearer authentication middleware and context principal resolver;
later deployment must place the SDK handler behind that boundary. No anonymous
HTTP constructor is provided.

At the daemon HTTP boundary, missing authentication maps to `401`, denied
capabilities map to `403`, malformed commands map to `400`, and conflicts map
to `409`. Its bearer secret is environment-only, maps to a server-configured
principal, and is compared in constant time. Caller-provided actor headers are
ignored. Policy denials are never reported as retryable server failures.

## Run State Machine

```text
draft
  -> awaiting_approval
  -> queued
  -> preparing
  -> running
  -> validating
  -> awaiting_decision
  -> completed

Any active state may transition to:
  -> cancelling -> cancelled
  -> failed_retryable
  -> failed_terminal
```

Transitions use optimistic concurrency and emit immutable events.

## Worker Model

The Go supervisor starts Codex CLI workers as operating system processes.

Each worker receives:

- task ID and attempt ID;
- role contract;
- repository and worktree path;
- read and write scopes;
- context pack reference;
- time, token, and command budgets;
- expected result schema;
- cancellation signal;
- evidence output directory.

Workers do not hold scheduler authority.

## Isolation

The initial isolation hierarchy is:

1. separate Git worktree;
2. dedicated process group;
3. sanitized environment;
4. explicit filesystem scope;
5. command allowlist and timeout;
6. optional container or sandbox adapter for higher-risk tasks.

## Validation

Validation has two layers:

- **mechanical validation:** schemas, tests, formatting, allowed paths, diff
  boundaries, secrets, and generated artifacts;
- **independent validation:** a separate agent or human reviews intent,
  behavior, omissions, and risks.

The author cannot be the only validator of its own work.

## Approvals

Decisions are durable records with:

- requester;
- approver;
- scope;
- action;
- risk class;
- reason;
- issued and expiry time;
- one-time or reusable semantics;
- associated run and evidence;
- revocation state.

Chat text such as "yes" is not sufficient without binding it to the pending
decision ID.

## Recovery

On restart, the daemon:

1. acquires leadership or scheduler leases;
2. finds expired worker and file leases;
3. inspects process and worktree state;
4. reconciles attempts with durable events;
5. retries only idempotent stages;
6. creates explicit recovery evidence.
