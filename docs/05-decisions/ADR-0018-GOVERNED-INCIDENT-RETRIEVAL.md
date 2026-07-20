# ADR-0018: Derive Retrieval Incidents From Immutable Attempt Failures

Status: Accepted for Sprint 09 implementation

## Context

Failed worker attempts contain useful operational learning: a repeated process
failure, a rejected budget, or a lost scheduler fence can affect future task
planning. Raw worker stdout, stderr, prompts, and full reports are not safe
retrieval material. They may contain secrets, customer content, implementation
details outside the request scope, or adversarial text.

Creating a second mutable incident table would also duplicate authority that is
already present in the fenced attempt lifecycle and append-only event stream.

## Decision

1. An incident is a derived, immutable view of exactly one current terminal
   `failed_retryable` or `failed_terminal` attempt.
2. Its authority is the matching immutable `attempt.finished` or
   `attempt.reconciled` event at the attempt's current aggregate version. A
   queued, running, succeeded, blocked, cancelled, stale, or mismatched attempt
   produces no incident card.
3. The public incident contract stores only stable identifiers, terminal
   classification, retryability, derived severity, event identity, timestamp,
   output/report hashes, and existing evidence references. It never stores
   stdout, stderr, prompt text, worker report body, object keys, or event body.
4. `incident_id` is deterministic from `attempt_id`; `source_hash` is a stable
   hash of the safe incident view. Projection and resolution independently
   rebuild the view and require both hashes to match before accepting context.
5. Severity is deterministic: retryable failures are `warning`; terminal
   failures are `critical`. No model, heuristic, or Qdrant payload may change
   that classification.
6. Qdrant is discovery only. The PostgreSQL resolver re-reads the exact current
   attempt and immutable event before returning an incident candidate.

## Consequences

Positive:

- failure patterns become discoverable without exposing execution bodies;
- no new mutable incident authority or migration is required;
- replay and recovery preserve the same canonical source;
- Qdrant compromise cannot manufacture an operational incident.

Negative:

- incident cards are intentionally sparse and cannot explain arbitrary logs;
- resolution records and human remediation remain a later operational workflow;
- only failures represented by the governed attempt lifecycle can be retrieved.

## Guardrail

Tests must reject event/attempt version mismatches, success or cancellation
states, altered source hashes, unknown classifications, unsorted or duplicate
evidence references, and any attempt to include raw worker output in a card or
retrieval response.
