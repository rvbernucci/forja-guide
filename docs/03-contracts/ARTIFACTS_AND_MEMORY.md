# Artifacts, Conversations, and Memory Contract

Status: Sprint 07 implemented candidate

## Authority

PostgreSQL owns every identity, lifecycle, reference, promotion, retention
decision, and tombstone. S3-compatible storage owns no business decision and
preserves only immutable bytes addressed by SHA-256.

## Public Contracts

| Contract | Purpose |
| --- | --- |
| `artifact.schema.json` 1.0 | Released core artifact metadata; unchanged by Sprint 07 |
| `conversation.schema.json` 1.0 | Mutable conversation lifecycle and transcript reference |
| `message.schema.json` 1.0 | Immutable ordered message parts and citations |
| `memory-candidate.schema.json` 1.0 | Untrusted proposal and resolution lifecycle |
| `memory-record.schema.json` 1.0 | Promoted memory with explicit authority and supersession |
| `artifact-bundle-manifest.schema.json` 1.0 | Canonically ordered immutable bundle inventory |

Unknown fields are rejected. Public UUID-prefixed IDs map to PostgreSQL UUIDs
only inside the persistence adapter.

## Content Boundary

Messages, memories, manifests, and artifacts contain metadata and exact hashes,
not raw bodies. Every content part references one artifact in the same tenant
and repository. A citation binds both the source artifact ID and the exact
source content hash so later lifecycle changes cannot silently retarget it.

The message `content_hash` is the SHA-256 of the canonical ordered content-part
and citation projection. It proves metadata integrity without making the
message body queryable in PostgreSQL.

## Object Publication

```text
reserve canonical operation
  -> conditionally write content-addressed object
  -> verify provider metadata and complete downloaded bytes
  -> activate artifact and append event/outbox
```

If any step fails, the artifact remains non-active. Retry uses the same
idempotency fingerprint. An existing object is evidence only after exact
verification; ETag equality is insufficient.

Once a content object is tombstoned or purged, publishing the same digest does
not resurrect it under a new artifact ID. Reference creation takes a shared
lock on the exact active artifact while tombstone and purge take conflicting
locks, so a retention decision cannot race a newly committed reference.

The immutable publication intent is stored with the operation journal. A
system reconciler can therefore recover after process loss without borrowing
the original caller identity or reconstructing provenance from incomplete
columns.

## Conversation Rules

- Message sequence numbers are allocated under a conversation row lock.
- Messages, content parts, citations, and closed transcript manifests are
  append-only.
- A correction uses `supersedes_message_id` and preserves the original.
- Closing a conversation requires an immutable transcript bundle manifest.
- That manifest must cite the conversation ID, exact conversation version, and
  SHA-256 inventory of every ordered message ID and content hash. Appending a
  message after manifest creation makes the transcript stale and unclosable.
- Tombstoning hides the conversation from ordinary reads and emits a deletion
  projection event; it does not rewrite message history.

## Memory Rules

- Candidates must cite canonical messages and a proposed content artifact.
- Candidate creation and promotion are separate idempotent commands.
- Only a human or the exact configured policy-system principal with
  `memory:promote` may promote; command metadata alone cannot manufacture that
  authority.
- Promotion may atomically supersede active memories but cannot form cycles.
- Expired, superseded, and tombstoned memories are excluded from normal reads.
- Deletion is tombstone-first and cannot remove still-referenced body objects.
- Active memory reads use one repeatable-read snapshot and one fixed timestamp;
  a result cannot become internally inconsistent while it is assembled.

## Limits

- one message: at most 64 content parts and 128 citations;
- one bundle: at most 4,096 entries and 16 GiB aggregate bytes;
- one bundle: at most 1,024 source references;
- one memory: at most 128 supersession references;
- one object: at most 4 GiB in Sprint 07;
- identifiers, media types, logical paths, and locators are bounded by schema;
- raw content, object keys, credentials, and provider errors are excluded from
  telemetry.

## Failure and Recovery

The operation journal distinguishes retryable provider failure, integrity
failure, interrupted finalization, and canonical conflict. Reconciliation may
verify and finish a previously authorized operation, mark it failed, or queue
an unreferenced object for retention-aware purge. It may never invent an
artifact, message, or memory that lacks canonical authorization.

Operational procedures are defined in
[Artifact Storage Operations](../06-operations/ARTIFACT_STORAGE_OPERATIONS.md).
