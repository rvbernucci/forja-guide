# Security Architecture

Status: MCP, daemon, worker, deterministic-index, and governed-retrieval
controls implemented through Sprint 09; production retrieval activation pending

## Primary Threats

- prompt injection requesting unauthorized tools or scope;
- command injection through model-produced shell;
- cross-tenant data or retrieval leakage;
- stale or poisoned graph and vector projections;
- credential exposure in prompts, logs, artifacts, or Git;
- confused-deputy approvals;
- worker escape from repository scope;
- concurrent write corruption;
- malicious dependencies or generated patches;
- replayed events and duplicate execution.

## Trust Boundaries

```text
human/client
  -> MCP or daemon HTTP authentication
  -> policy and approval engine
  -> scheduler
  -> worker process
  -> repository and tools
  -> validation
  -> artifact publication
```

Every transition reduces or preserves authority. A worker never gains more
authority because an LLM requested it.

## Controls

Implemented through Sprint 03:

- fixed least-privilege stdio profiles: the default `agent` may plan, read,
  submit, and cancel, while decision and resume authority require a separately
  authenticated `human` or `system` principal;
- explicit authenticated identities for MCP stdio sessions;
- fail-closed bearer authentication for the complete daemon `/v1` namespace,
  with server-owned actor identity and constant-time credential comparison;
- capability and exact tenant/repository scope checks before daemon HTTP
  routing, request parsing, or persistence;
- numeric-loopback-only plaintext daemon binding, HTTPS-only hostname and
  remote CLI transport, and redirect rejection so bearer credentials cannot
  cross a transport boundary;
- fail-closed bearer verification boundary for future Streamable HTTP;
- permission checks before canonical command persistence;
- exact tenant and repository authority matching between principal and store;
- stable pending-decision IDs and optimistic versions;
- cancellation guards that cannot strand a pending decision;
- repository-level guards that reserve retry and awaiting-decision resume
  transitions for the capability-checked `ResumeRun` command;
- immutable domain and MCP tool audit events with explicit original-versus-replay
  evidence and exact receipt command scopes;
- rollback invalidations bound to exact command anchors and domain event IDs,
  with receipt recovery consuming event-specific evidence;
- deterministic schema validation and idempotent command replay.

Implemented in Sprint 04:

- strict worker contracts and full-worktree-only read authorization;
- sanitized worker and model-command environments;
- Codex sandbox write roots derived from declared task scopes;
- bounded process groups, timers, output, telemetry, and reaping;
- observed write-scope checks including ignored files and Git index flags;
- hash-verified evidence references and durable output digests.

Implemented through Sprints 08 and 09:

- deterministic source-bound cards with explicit authority, lifecycle, source
  hash, and source-commit provenance;
- independent, fenced Qdrant projector delivery and canonical backlog checks;
- mandatory tenant, repository, lifecycle, authority, and freshness filters
  before dense or sparse ranking;
- distrust of Qdrant payloads followed by fail-closed canonical PostgreSQL
  resolution before any candidate becomes context;
- bounded degraded receipts for unavailable projection freshness or Qdrant rank
  paths, and explicit query failure when embedding generation is unavailable or
  invalid;
- content-free retrieval telemetry and private evaluation boundaries.

Planned controls:

- PostgreSQL row-level security where applicable;
- expiring approvals and grants;
- cgroup, container, or equivalent hostile-descendant containment;
- separate worker identity or container filesystem confidentiality;
- command and network policies;
- secret manager integration with brokered, non-filesystem worker credentials;
- signed or hashed artifacts;
- dependency and secret scanning;
- human approval for privileged operations.

## Retrieval Security

Qdrant queries apply tenant and repository scope before ranking, then require
canonical PostgreSQL resolution. Future Neo4j traversal must preserve the same
rule. Context packs may contain only content the requesting principal is
authorized to read.

Embedding similarity must never bypass access control.

## Model Output Boundary

Model output is untrusted data until:

1. parsed against the expected schema;
2. checked against scope and policy;
3. mechanically validated;
4. independently reviewed when risk requires it;
5. committed through an authorized operation.
