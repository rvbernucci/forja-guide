# Runtime and MCP Architecture

Status: Sprint 03 control surface, Sprint 04 bounded worker, Sprint 05 governed
execution pipeline, and Sprint 06 observability foundation authoritatively
closed

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
and `awaiting_decision -> queued` through the generic transition API, so a
legacy HTTP caller cannot bypass `run:resume` authorization.
Resuming an awaiting-decision Run never reopens its terminal blocked Attempt:
it queues the Run so the scheduler must allocate a fresh attempt, fence, and
attempt-scoped delivery authorization.

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

`internal/execution.Pipeline` is the trusted composition boundary. It accepts
only a schema-valid delivery request whose complete JSON envelope is
hash-pinned in an immutable `delivery.authorized` event by an independent
human. That event is accepted only for an approved `queued` Run, its exact
`queued` durable attempt, and a live scheduler fence. The pipeline independently
revalidates the approval before side effects. Authorization streams are keyed
by `(delivery_id, attempt_id)`, so every retry requires a fresh exact approval
without colliding with earlier attempt evidence. It advances the Run through
`preparing`, `running`,
`validating`, and `completed`; starts and finishes the durable attempt; acquires
only request-derived worktree, file, and artifact leases; invokes the real
worker supervisor; and delegates commit, validation, evidence, and publication
to their existing authority boundaries. Any identity, objective, repository,
or fencing mismatch fails before side effects.

While preparation, worker execution, and validation are active, a dual
heartbeat renews both the scheduler fence and the exact immutable delivery
lease set. Renewal failure cancels the supervisor context and becomes a
canonical retryable attempt result. Intentional heartbeat shutdown is
distinguished from renewal failure even if cancellation races an in-flight
repository call. Because lease-set acquisition can itself block, both the
delivery set and scheduler fence are synchronously renewed again after
acquisition and before the first worktree mutation; the periodic heartbeat is
not treated as an initial authority check. When the worker exits, the pipeline
settles the heartbeat before persisting the attempt, so an authority-induced
process cancellation cannot be recorded as an intentional cancellation. A
successful worker result is followed by another synchronous refresh of both
authorities and a fresh heartbeat before commit or validation. Loss of that
heartbeat while the supervisor creates the result commit remains retryable;
ordinary deterministic commit failures remain terminal. `Pipeline.Recover` never assumes a live
process is dead: a replacement scheduler first reconciles the expired fence.
It may then fail an interrupted attempt safely or resume a succeeded attempt
from deterministic Git bytes, exact persisted validation evidence, and the
journaled publication intent. Active delivery authority is renewed first, then
the recovery scheduler fence is synchronously renewed again before the
heartbeat starts and before any Git mutation. Both normal and recovered
publication use a dedicated bounded context detached from caller cancellation.
Recovery accepts
only a replacement fence for the same scheduler resource recorded by the
attempt; repository-wide scheduler authority is insufficient. Durable
non-success attempts are also cleaned and closed after restart rather than
being stranded between attempt completion and Run transition. A succeeded
attempt whose Run was already marked failed after a later-stage error also
retries quarantine and lease release on restart. Completed replay still
traverses the publication service, freshly observes the exact Git ref, and
retries idempotent lease
release. A supervisor error can never be persisted beside a successful attempt;
such contradictions normalize to a canonical retryable result. The live
execution path follows the same ordering: after a terminal Attempt is durable,
the supervisor quarantines its worktree and releases its exact lease set before
publishing `awaiting_decision`, `failed_retryable`, `failed_terminal`, or
`cancelled` as the Run state. A cleanup failure therefore leaves a recoverable
non-terminal Run instead of claiming completion.
Failures before `StartAttempt` leave the Run in `preparing` and its Attempt in
`queued`; only fenced restart reconciliation after scheduler quiescence may
make that work retryable. Once worktree preparation is invoked, even a rejected
prepare is treated as potentially byte-producing. The lease set is released
only after request-bound quarantine succeeds or the pre-worker path is proven
never to have existed.
Quarantine replay is likewise evidence-driven: the manager accepts only a
canonical marker it wrote after the move, bound to the complete delivery
request and observed Git registration state. A destination directory by itself
is never proof of completed cleanup.
When a publication journal already exists, recovery reconstructs commit
identity from its schema-validated receipt rather than writing Git objects
without live delivery authority. Detached lease renewal is explicitly bounded,
and a real renewal failure remains visible even if heartbeat shutdown races it.
Publication preparation fences every incompatible Run transition; once the
journal is `published`, only the matching transition to `completed` is legal.
Pre-`Prepare` recovery recognizes both a wholly absent authority namespace and
a retry whose delivery namespace exists but whose new Attempt binding does not.

## Isolation

The initial isolation hierarchy is:

1. separate Git worktree;
2. dedicated process group;
3. sanitized environment;
4. Codex sandbox roots plus observed write-scope verification and
   full-worktree read scope;
5. command allowlist and timeout;
6. optional container or sandbox adapter for higher-risk tasks.

Process groups provide bounded lifecycle control, not complete containment of
a hostile descendant that creates a new session. Production workers require a
cgroup, container, or equivalent job boundary; narrower read scopes are
rejected until they can be enforced mechanically. Same-user host files are not
made confidential by environment filtering; production also requires a
separate worker identity or container and brokered credentials.

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
