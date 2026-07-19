# ADR-0015: Governed Hybrid Retrieval

Status: Accepted for Sprint 09

## Context

Dense embeddings improve conceptual discovery while lexical retrieval remains
stronger for identifiers, paths, error codes, and exact names. Qdrant supports
both representations, payload filtering, named vectors, and atomic collection
aliases. It is nevertheless a derived, eventually consistent store whose
payloads and scores cannot establish Forja authority.

The existing outbox is one competing dispatcher group. Reusing its global
`published` state for independent Qdrant and future Neo4j consumers would let
one projector consume an event on behalf of another and create false
checkpoints.

## Decision

1. Use the official, version-pinned Qdrant Go client behind a narrow adapter.
2. Store dense and sparse named vectors with hard-filter payload indexes in a
   versioned physical collection addressed through a stable alias.
3. Build deterministic retrieval cards from canonical entities instead of
   embedding arbitrary raw files by default.
4. Apply tenant, repository, lifecycle, authority, staleness, language, and
   kind filters to every dense and sparse query before ranking. Couple commit
   filtering to artifact family: `symbol` and `test` cards must match the
   requested commit, while `decision`, `memory`, and `incident` cards must
   have no commit and may be queried only through an unrestricted whole-
   repository scope.
5. Fuse the two bounded rank lists in Forja with weighted reciprocal rank
   fusion. The policy and every component rank remain visible in the receipt.
6. Resolve every candidate against current PostgreSQL state. Missing, stale,
   unauthorized, or hash-mismatched candidates are rejected. An ambiguity is
   rejected as context but may return bounded, scope-authorized canonical
   entity-ID alternatives without cards, hashes, paths, or vector payloads.
7. Add independent per-projector delivery state, leases, retries, dead letters,
   and contiguous checkpoints derived from canonical outbox history.
8. Re-embedding uses blue-green collection generations and an atomic alias
   swap after mechanical completeness and source-hash verification.
9. Qdrant snapshots may accelerate recovery but never replace replay from
   PostgreSQL and canonical artifacts.

## Consequences

Positive:

- exact and semantic retrieval complement each other without hiding ranking;
- tenant and repository scope are enforced before approximate search;
- projection loss or corruption is recoverable without canonical data loss;
- future Neo4j projection receives an independent delivery cursor;
- embedding upgrades can roll forward or back without query downtime.

Negative:

- every candidate requires canonical resolution before use;
- per-projector delivery state and contiguous checkpoints add PostgreSQL work;
- two rank queries and explicit fusion cost more than one approximate query;
- model and collection generations require operational lifecycle management.

## Guardrails

- Similarity score, Qdrant payload, or vector presence never establishes
  authority, freshness, identity, or access.
- No query may omit tenant and repository filters.
- A source-bound card may never match another commit. A repository-global card
  may never enter a path-restricted or partially denied scope.
- No projector may use the global outbox `published` state as its independent
  cursor.
- An alias may switch only to a verified complete generation.
- Retrieval failure may reduce results but must never broaden scope.
