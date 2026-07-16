# ADR-0003: Derived Intelligence Stores

Status: Accepted

## Context

Qdrant and Neo4j provide specialized retrieval and traversal capabilities, but
dual writes and competing authority would make failure recovery unsafe.

## Decision

Treat Qdrant and Neo4j as independently rebuildable projections produced from
PostgreSQL outbox events and canonical source artifacts.

Qdrant discovers candidates. Neo4j serves proven and curated relations.
Canonical source, deterministic extractors, schemas, tests, and runtime evidence
establish authority.

## Consequences

Positive:

- projection failures do not corrupt canonical state;
- model or index migrations can use blue-green rebuilds;
- drift and projection lag are measurable;
- each store can scale independently.

Negative:

- eventual consistency must be visible;
- projection workers and checkpoints add operational complexity;
- context serving needs safe degraded modes.

## Guardrail

Direct application writes to both PostgreSQL and a derived store in the same
logical command are forbidden.

