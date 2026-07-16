# Security Architecture

Status: Proposed

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
  -> MCP authentication
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

- authenticated MCP sessions;
- role and capability policies;
- tenant and repository scope on every canonical record;
- PostgreSQL row-level security where applicable;
- expiring approvals and grants;
- isolated worktrees and process groups;
- command and network policies;
- secret manager integration;
- signed or hashed artifacts;
- immutable audit events;
- deterministic schema validation;
- dependency and secret scanning;
- projection provenance and freshness checks;
- human approval for privileged operations.

## Retrieval Security

Qdrant and Neo4j queries must apply tenant and repository scope before ranking
or traversal. Context packs may contain only content the requesting principal is
authorized to read.

Embedding similarity must never bypass access control.

## Model Output Boundary

Model output is untrusted data until:

1. parsed against the expected schema;
2. checked against scope and policy;
3. mechanically validated;
4. independently reviewed when risk requires it;
5. committed through an authorized operation.

