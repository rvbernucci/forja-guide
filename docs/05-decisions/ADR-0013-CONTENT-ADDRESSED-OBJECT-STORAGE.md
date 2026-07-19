# ADR-0013: Content-Addressed Object Storage Saga

Status: Accepted for Sprint 07

## Context

Forja must preserve evidence bundles, transcripts, logs, media, and datasets
that are too large or sensitive to place in PostgreSQL. PostgreSQL and an
S3-compatible service cannot participate in one atomic transaction. Treating a
successful object write as canonical would also let an external serving layer
invent authority or retain an object after its database reference disappeared.

## Decision

1. PostgreSQL is the authority for artifact identity, lifecycle, provenance,
   authorization, retention, and references. S3-compatible storage preserves
   bytes only.
2. The application records a bounded operation journal before any object
   mutation. The saga transitions through reserved, uploading, verified, and
   active states; interruption remains recoverable and never implies success.
3. Object keys are derived by trusted code from an operator-bound namespace and
   `sha256/<first-two-hex>/<remaining-hex>`. Callers cannot provide endpoint,
   bucket, key, credentials, encryption key, or authority prefix.
4. Publication requires an expected SHA-256, size, and media type. The adapter
   uses a conditional create, sends SHA-256 checksum metadata where supported,
   and performs a bounded full-body read after a first write. An existing key
   is accepted only after exact metadata and body verification.
5. The final PostgreSQL transition binds the verified object key, version or
   ETag evidence, hash, size, and operation identity in one event/outbox
   transaction. Failure before that transition leaves no active artifact.
6. Canonical deletion first records a tombstone and outbox event. Physical
   purge occurs only after retention expires and PostgreSQL proves no live
   artifact, citation, message, manifest, or memory reference remains.
7. Object-store credentials come only from the process environment or a future
   credential broker. They are never accepted through an API, stored in
   PostgreSQL, logged, traced, or exposed to workers.
8. Provider-specific ETags are transport evidence, not content hashes. SHA-256
   remains the canonical byte identity.

Amazon S3 documents conditional `If-None-Match: *` writes and full-object
SHA-256 checksums for `PutObject`. The implementation uses those controls when
the configured S3-compatible provider supports them and still performs its own
full-body verification before canonical activation.

## Consequences

Positive:

- duplicate bytes converge on one immutable object without duplicate
  authority;
- crashes and partial uploads are visible and recoverable;
- object-store compromise cannot independently promote memory or artifacts;
- PostgreSQL tombstones can drive future Qdrant and Neo4j deletion safely.

Negative:

- publication has more round trips than a blind object upload;
- an interrupted saga may leave an unreferenced object that reconciliation must
  inventory and eventually purge;
- full-body verification costs bandwidth but is required for the first
  publication of canonical evidence.

## Guardrail

Tests must cover conditional-write races, existing-object verification, short
and oversized bodies, hash mismatch, provider checksum mismatch, missing
objects, interrupted finalization, cross-tenant key attempts, tombstone order,
retention, live-reference protection, restore, and credential redaction.

Primary protocol references:

- <https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes.html>
- <https://docs.aws.amazon.com/AmazonS3/latest/userguide/checking-object-integrity-upload.html>
