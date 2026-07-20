# ADR-0017: Retrieve Memory Through Authorized Derived Bodies

Status: Accepted for Sprint 09 implementation

## Context

An active memory record carries durable lifecycle metadata and binds one
content-addressed artifact. Its semantic body is not stored in PostgreSQL.
Embedding only the memory ID, kind, and promotion metadata would be safe but
would not make the memory useful for conceptual retrieval. Reading arbitrary
artifact bodies inside a projector would be useful but would bypass artifact
authority, retention, size, media-type, and redaction controls.

## Decision

1. A memory retrieval card is built from a **derived retrieval body**, never
   directly from an arbitrary artifact object.
2. The body builder first resolves the active `memory_records` row, its exact
   artifact ID/content hash, source candidate, authority class, lifecycle, and
   expiry through PostgreSQL.
3. A dedicated, read-only artifact gateway must verify the canonical artifact
   row and exact object version before reading. It accepts a retrieval-specific
   policy, not an agent-provided object key.
4. The gateway permits only explicitly approved text media types, a bounded
   byte limit, and a deterministic redaction/normalization profile. Binary,
   oversized, retained-but-restricted, missing, or hash-mismatched bodies
   produce no card and no context.
5. The derived body, its redaction-profile version, original content hash, and
   retrieval-card hash are recorded in canonical projection provenance. Qdrant
   stores only the resulting bounded card and vectors.
6. Memory lifecycle changes tombstone the corresponding canonical projection
   receipt before any derived vector deletion. A superseded, expired, or
   tombstoned memory can never resolve as context.
7. Query and observability outputs expose only canonical IDs, bounded reason
   codes, and hashes. They never expose raw memory bodies, object keys,
   redaction output, or provider requests.

## Consequences

Positive:

- active memories can provide semantically meaningful retrieval without making
  object storage or Qdrant authoritative;
- a new redaction profile or embedding model can rebuild from canonical state;
- restricted artifacts fail closed instead of silently becoming model context.

Negative:

- Sprint 09 needs a small explicit artifact-read capability and private test
  corpus before memory projection can be enabled;
- metadata-only memory cards are intentionally rejected as an insufficient
  substitute for semantic retrieval.

## Guardrail

Tests must prove denial for cross-tenant records, inactive/expired memory,
artifact hash/version mismatch, unapproved media type, body-size overflow,
redaction failure, stale derived provenance, and any attempt to log or return
the body. A valid body must be reproducible from a pinned policy/version and
must resolve only while every canonical dependency remains active.
