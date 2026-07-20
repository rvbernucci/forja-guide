# ADR-0021: Preserve Point-in-Time Financial Authority

Status: Accepted for Forja Alpha Sprints 10-14

## Context

Forja Alpha combines filings, XBRL, rates, macro series, licensed market data,
and institutional disclosures. Those sources use different identifiers,
publication delays, revisions, amendments, units, periods, and licenses. A
current-value store or vector-only architecture would introduce look-ahead
bias, erase corrections, and let semantic similarity substitute for fact.

## Decision

1. Preserve every acquired source body as an immutable content-addressed object
   before parsing or normalization.
2. Keep canonical identity, lifecycle, exact values, temporal availability,
   permissions, and lineage in PostgreSQL.
3. Record observation, period, filing/publication, availability, ingestion,
   validity, and supersession time independently where applicable.
4. Require every historical research request to declare an `as_of` timestamp
   and exclude data not yet available at that time.
5. Select accounting facts and perform material arithmetic through typed,
   versioned deterministic tools. Language models may plan and explain but do
   not establish source facts or change tool results.
6. Use Qdrant only to discover narrative candidates and Neo4j only to traverse
   evidence-classified relationships. Resolve both projections against
   canonical PostgreSQL authority before use.
7. Treat market-data license, entitlement, retention, and redistribution as
   required source metadata. Do not scrape undocumented endpoints.
8. Preserve amendments and revisions as new versions rather than overwriting
   prior historical knowledge.

## Consequences

- Point-in-time evaluation can detect and reject look-ahead leakage.
- Raw-to-claim lineage remains independently auditable and reproducible.
- Numeric questions avoid lossy embedding lookup and unbounded model arithmetic.
- Derived stores can be destroyed and rebuilt without source-of-truth loss.
- Ingestion and schema work is larger because source clocks, units, dimensions,
  licenses, and correction semantics cannot be collapsed into generic JSON.
- A missing or ambiguous fact becomes a visible gap instead of an inferred
  value.
