# Contract Model

Status: Partially implemented

## Purpose

Forja uses language-neutral contracts between the interaction, control,
execution, intelligence, and evidence planes.

Go structs, TypeScript types, Python models, database migrations, MCP tool
schemas, and tests should be generated from or validated against these
contracts.

## Contract Rules

- Every durable object has a stable ID and schema version.
- Every mutable aggregate has an integer version for optimistic concurrency.
- Every command has an idempotency key.
- Every event records causation and correlation.
- Every artifact records provenance and a content hash.
- Every context pack records selected sources and retrieval evidence.
- Unknown fields are rejected at trust boundaries unless a contract explicitly
  permits extension data.
- Breaking changes require a new schema version and migration plan.

## Initial Schemas

| Schema | Responsibility |
| --- | --- |
| [`run.schema.json`](../../schemas/run.schema.json) | Sprint 01 in-memory run aggregate |
| [`run-event.schema.json`](../../schemas/run-event.schema.json) | Immutable state transition and audit event |
| [`artifact.schema.json`](../../schemas/artifact.schema.json) | Durable artifact metadata and provenance |
| [`context-request.schema.json`](../../schemas/context-request.schema.json) | Scoped request for contextual evidence |
| [`context-pack.schema.json`](../../schemas/context-pack.schema.json) | Bounded evidence package returned to an agent |

The Sprint 01 Go kernel embeds these schemas, compiles them at startup, and
validates run aggregates at its HTTP and CLI boundaries. See the
[kernel API](KERNEL_API.md).

## Stable IDs

Public contracts use opaque string IDs with explicit prefixes:

```text
tenant_
repo_
sprint_
task_
run_
attempt_
worker_
artifact_
event_
entity_
approval_
```

IDs must not encode credentials, local paths, customer names, or mutable
display labels.

## Event Envelope

Events form the durable coordination backbone:

```text
event_id
event_type
schema_version
aggregate_type
aggregate_id
aggregate_version
occurred_at
actor
correlation_id
causation_id
idempotency_key
payload
```

Projection consumers use `event_id` and aggregate version to guarantee
idempotent updates.

## Artifact Boundary

Artifact metadata is relational and queryable. Large bodies live in object
storage.

An artifact is not authoritative merely because it exists. Authority depends
on:

- artifact kind and lifecycle;
- source hierarchy;
- content hash;
- author and validator;
- proof references;
- supersession state;
- repository and commit compatibility.

## Compatibility

Contract tests must verify:

- valid fixtures are accepted by every implementation language;
- invalid fixtures are rejected consistently;
- serialized forms remain stable;
- database constraints match schema requirements;
- MCP tool schemas remain compatible with canonical contracts.
