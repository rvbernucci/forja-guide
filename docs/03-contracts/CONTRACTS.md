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
| [`run.schema.json`](../../schemas/run.schema.json) | Canonical run aggregate |
| [`sprint.schema.json`](../../schemas/sprint.schema.json) | Governed Sprint aggregate |
| [`decision.schema.json`](../../schemas/decision.schema.json) | Stable pending and resolved decisions |
| [`run-event.schema.json`](../../schemas/run-event.schema.json) | Immutable state transition and audit event |
| [`artifact.schema.json`](../../schemas/artifact.schema.json) | Durable artifact metadata and provenance |
| [`context-request.schema.json`](../../schemas/context-request.schema.json) | Scoped request for contextual evidence |
| [`context-pack.schema.json`](../../schemas/context-pack.schema.json) | Bounded evidence package returned to an agent |
| [`worker-task.schema.json`](../../schemas/worker-task.schema.json) | Authorized worker objective, scope, and budgets |
| [`worker-report.schema.json`](../../schemas/worker-report.schema.json) | Schema-constrained model completion report |
| [`worker-result.schema.json`](../../schemas/worker-result.schema.json) | Supervisor-authored execution classification and evidence |
| [`delivery-request.schema.json`](../../schemas/delivery-request.schema.json) | Approved Git delivery authority, scopes, identities, and budgets |
| [`validation-report.schema.json`](../../schemas/validation-report.schema.json) | Bounded clean-checkout validation evidence |
| [`evidence-manifest.schema.json`](../../schemas/evidence-manifest.schema.json) | Canonical scoped inventory of immutable delivery evidence |
| [`delivery-receipt.schema.json`](../../schemas/delivery-receipt.schema.json) | Hash-bound publication receipt for a namespaced Git ref |
| [`conversation.schema.json`](../../schemas/conversation.schema.json) | Conversation lifecycle and transcript binding |
| [`message.schema.json`](../../schemas/message.schema.json) | Immutable message parts and source citations |
| [`memory-candidate.schema.json`](../../schemas/memory-candidate.schema.json) | Untrusted proposed learning and resolution lifecycle |
| [`memory-record.schema.json`](../../schemas/memory-record.schema.json) | Explicitly promoted durable memory |
| [`artifact-bundle-manifest.schema.json`](../../schemas/artifact-bundle-manifest.schema.json) | Immutable content-addressed bundle inventory |

The Go kernel embeds these schemas, compiles cross-schema references offline at
startup, and validates run
aggregates at its HTTP and CLI boundaries. The MCP adapter uses typed generated
schemas and compatibility fixtures. See the [kernel API](KERNEL_API.md) and
[MCP control API](MCP_CONTROL_API.md).
The [worker execution contract](WORKER_EXECUTION.md) defines process authority,
budgets, result coupling, and durable recovery.
The [isolated delivery contract](ISOLATED_DELIVERY.md) defines fenced worktree
ownership, independent validation, and controlled Git publication.
The [artifacts and memory contract](ARTIFACTS_AND_MEMORY.md) defines the
PostgreSQL/S3 saga, immutable conversation evidence, and governed promotion.

The Sprint schema encodes the approval coupling directly: only
`awaiting_approval` requires and permits `pending_decision_id`. Proposed,
approved, rejected, and cancelling documents must not carry a pending decision.

## Stable IDs

Public contracts use opaque string IDs with explicit prefixes:

```text
tenant_
repo_
sprint_
task_
run_
attempt_
delivery_
validation_
worker_
artifact_
event_
entity_
decision_
conversation_
message_
part_
citation_
memory_candidate_
memory_
manifest_
```

IDs must not encode credentials, local paths, customer names, or mutable
display labels.

Operational tenant and repository authorities use `tenant_<uuidv4>` and
`repo_<uuidv4>` publicly. Persistence adapters validate those values and map
only their UUID bodies to PostgreSQL `uuid` keys; storage UUIDs are not public
contract identifiers.

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
