# Artifact Storage Operations

Status: Sprint 07 operational baseline

PostgreSQL is the canonical authority for object identity, lifecycle,
references, retention, events, outbox delivery, and replay receipts. The
S3-compatible provider preserves immutable bytes only. A provider object must
never be treated as a canonical artifact without the matching active database
operation and artifact record.

## Configuration Boundary

The object-store adapter accepts only operator configuration:

- bucket;
- region;
- optional S3-compatible base endpoint;
- path-style addressing mode.

Credentials come from the AWS SDK credential chain or workload identity. Do
not place access keys in Forja commands, artifact metadata, object keys,
events, logs, traces, or repository files. Tenant and repository prefixes and
content-addressed keys are derived internally and cannot be selected by an
agent or end user.

## Publication Recovery

`knowledge.Reconciler` processes bounded stale operation batches. For each
operation it:

1. loads the immutable publication intent from PostgreSQL;
2. derives the expected object key from authority and SHA-256;
3. downloads and re-hashes the complete body;
4. verifies size, media type, metadata, checksum, and ETag continuity;
5. atomically activates the object, operation, artifact, event, outbox row,
   and reconciliation receipt; or
6. records `retryable_provider`, `interrupted`, or terminal `integrity`
   failure without inventing success.

Only a `system` principal may execute reconciliation. Operators should alert
on `artifact_reconciliation` and `artifact_integrity_failures`, inspect the
provider independently, and retry only recoverable classes. Never repair an
integrity failure by changing the expected hash or editing canonical rows.

## Retention and Purge

Purge is deliberately two-step:

1. A human or system command archives and tombstones the artifact in
   PostgreSQL and emits its event/outbox record.
2. The retention worker selects only tombstoned objects with no live artifact
   alias or canonical reference, deletes by exact derived key and ETag, and
   marks the object `purged` in a second transaction.

Live conversations, message parts, citations, proposed candidates, active or
superseded memories, transcript ownership, and non-transcript bundle
manifests block tombstoning. A provider `not found` result can complete purge
only after PostgreSQL has already selected that exact tombstoned candidate.
Derived-store deletion consumers must process the tombstone outbox event
before removing Qdrant or Neo4j projections.

## Backup

Create a consistent recovery point as an operational procedure:

1. Stop artifact publication, memory promotion, retention, reconciliation,
   and projection intake.
2. Allow in-flight operations to commit or become
   `reconciliation_required`.
3. Run `scripts/postgres_backup.sh` and record its archive hash.
4. Capture a versioned or immutable provider snapshot/inventory for the
   operator-bound prefix.
5. Record database archive identity, provider snapshot identity, authority,
   start/end time, and operator in an immutable evidence bundle.
6. Resume intake only after both sides are durably retained.

A database archive without its matching object snapshot is not a complete
Forja backup.

## Restore Drill

Restore only into isolated authority:

1. Restore provider bodies under their original content-addressed keys without
   enabling application traffic.
2. Restore PostgreSQL with `scripts/postgres_restore.sh` into an empty staging
   database.
3. Run `scripts/postgres_verify.sh`.
4. Run bounded artifact reconciliation until no recoverable stale operation
   remains.
5. Verify every active object referenced by a representative bundle and
   compare complete SHA-256, size, media type, and ETag evidence.
6. Rebuild derived stores from canonical outbox history.
7. Enable reads first, then controlled writes, and retain the prior recovery
   point until verification is signed off.

Missing or mismatched bodies fail the restore. Do not downgrade an active
artifact, rewrite a hash, or delete a reference merely to make verification
green.

## Credential Rotation

1. Provision a new least-privilege credential or workload identity with
   access only to the configured bucket/prefix and required conditional
   object operations.
2. Validate read, conditional create, checksum-enabled download, and
   ETag-guarded delete in staging.
3. Roll processes so the AWS SDK resolves the new identity.
4. Run a publication and verification canary with non-sensitive bytes.
5. Revoke the old identity and confirm no reconciliation or retention error
   increase.

Rotation never changes object keys, hashes, canonical rows, or manifests.

## Telemetry Safety

Only bounded aggregate counts and stable failure classes are exported. Raw
content, object keys, hashes, ETags, credentials, provider responses, and raw
errors are forbidden in metric labels and trace attributes.
