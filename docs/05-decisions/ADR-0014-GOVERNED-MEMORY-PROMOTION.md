# ADR-0014: Governed Memory Promotion

Status: Accepted for Sprint 07

## Context

Conversation history contains instructions, guesses, transient plans, model
errors, and potentially hostile content. Automatically treating repeated or
semantically similar chat as durable memory would bypass Forja's authority
hierarchy and let prompt injection rewrite future context.

## Decision

1. Raw messages are immutable evidence whose bodies are artifact references.
   Corrections append a new message that explicitly supersedes the prior
   message; they do not mutate history.
2. Working summaries are replaceable, short-lived artifacts and are never an
   authority source by themselves.
3. A memory candidate is an untrusted proposal bound to exact source message
   IDs, artifact hashes, a kind, proposer, expiry, and canonical event.
4. Creating a durable memory record requires a separate authenticated command
   from a `human` principal or a configured policy-owning `system` principal
   with dedicated `memory:promote` permission. Agents and workers cannot hold
   that permission.
5. Promotion records the candidate, promoter, authority class, reason, source
   hashes, expiry, and any superseded memory IDs atomically with an event and
   outbox row.
6. Active memories may be superseded, expired, or tombstoned, never silently
   rewritten. Reads exclude non-active and expired records by default.
7. Embedding and semantic similarity may later discover memory candidates, but
   cannot promote, reactivate, or override a canonical lifecycle decision.
8. Tombstones are canonical and precede object purge or derived-index deletion.

## Consequences

Positive:

- chat cannot become truth through repetition;
- every durable memory has exact provenance and a responsible authority;
- contradictions produce explicit supersession history;
- future semantic retrieval can be rebuilt without losing governance.

Negative:

- useful memories may remain candidates until an authorized promotion occurs;
- promotion and supersession require additional user or policy operations;
- raw message retention and durable memory retention are separate policies.

## Guardrail

Tests must reject self-promotion, agent or worker promotion, missing source
hashes, cross-tenant sources, stale candidate versions, double promotion,
supersession cycles, expired-memory reads, deletion before tombstone, and
re-creation of a tombstoned identity.
