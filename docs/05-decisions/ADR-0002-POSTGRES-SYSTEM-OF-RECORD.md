# ADR-0002: PostgreSQL System of Record

Status: Accepted

## Context

Forja needs consistent state for runs, events, approvals, leases, artifacts,
chats, memory, and projection checkpoints. Splitting canonical state across
document, graph, and vector databases would create recovery and authority
ambiguity.

## Decision

Use PostgreSQL as the operational system of record.

Use relational columns for identity, lifecycle, authorization, and common query
fields. Use `JSONB` for versioned provider-specific or evidence payloads.

Use object storage for large immutable bodies.

## Consequences

Positive:

- transactional state and event commits;
- optimistic concurrency and durable idempotency;
- one backup and recovery authority;
- strong tenant isolation options;
- flexible metadata without a separate document database.

Negative:

- high-churn message and event tables require partitioning and retention;
- large bodies must be managed through object references;
- schema discipline remains necessary despite JSONB flexibility.

## Guardrail

Neo4j, Qdrant, and caches may not become implicit canonical stores.

