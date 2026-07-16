# ADR-0005: Deterministic Code Lineage

Status: Accepted

## Context

Semantic embeddings can find conceptually related code, but they cannot prove
imports, calls, type compatibility, data access, tests, or runtime behavior.
Embedding raw files also creates noisy and unstable retrieval units.

## Decision

Run compiler-specific deterministic indexers before semantic projection.

Generate stable symbol and behavior cards from source evidence. Embed their
textual projections for discovery while keeping relationships tied to compiler,
schema, test, runtime, or curated evidence.

## Consequences

Positive:

- explainable graph edges;
- better retrieval units than arbitrary file chunks;
- deterministic incremental invalidation;
- explicit gaps when evidence is unavailable.

Negative:

- each language needs a maintained adapter;
- compiler versions can change extraction behavior;
- dynamic languages have weaker static evidence.

## Guardrail

`candidate_semantic` relationships may guide investigation but cannot enter
authoritative paths until promoted by a declared evidence source.

