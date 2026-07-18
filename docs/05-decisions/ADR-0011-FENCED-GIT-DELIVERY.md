# ADR-0011: Fenced and Reproducible Git Delivery

Status: Accepted for Sprint 05

## Context

Sprint 04 can execute a bounded worker and prove what it changed, but the caller
must already own an isolated worktree. The worker cannot safely create its own
workspace, decide that its output is valid, or publish a Git ref. Exact leases
alone also do not prevent two declared directory scopes from overlapping.

Sprint 05 needs a delivery boundary that preserves useful agent autonomy while
making workspace ownership, validation, evidence, and publication mechanical
and replayable.

## Decision

Forja will deliver changes through a supervisor-owned pipeline:

1. Acquire one atomic PostgreSQL lease set containing the worktree lease plus
   normalized hierarchical file and artifact leases. Keys are sorted before
   locking; any conflict aborts the complete acquisition.
2. Derive a new worktree beneath an operator-owned root and check out one exact
   40-character commit. The worker receives only pre-created writable roots.
3. Let the bounded worker produce uncommitted changes and a constrained report.
   The worker has no Git commit, ref-update, lease, validator, or publication
   authority.
4. Have the delivery service inspect scope, create the commit, and compute
   canonical changed-path, tree, patch, and evidence hashes.
5. Run bounded mechanical checks, then assign an independent validator whose
   identity differs from the author. Independent checks run against a clean
   checkout of the result commit.
6. Publish only a namespaced delivery ref with Git compare-and-swap semantics.
   Default and protected branches remain outside this Sprint's authority.
7. Persist a content-addressed receipt and release only the exact fenced lease
   set. Dirty or unverifiable failures are quarantined rather than reset.

Canonical patch identity is the SHA-256 of Git's binary, full-index diff from
the exact base commit to the result commit. Changed paths are normalized,
deduplicated, byte-sorted repository-relative paths. Validator definitions are
trusted registry entries addressed by stable IDs; requests cannot inject shell
commands.

Repository and worktree roots are canonical, non-root, absolute, and disjoint.
The attempt ID identifies the immutable lease set, while the delivery ID
identifies its worktree lease. A clean retry uses a new attempt ID and advances
the delivery fence. File and artifact lease IDs are canonical
repository-relative scopes. These identities are revalidated from the receipt
rather than trusted as opaque strings.

Every untrusted worker adapter must declare versioned isolation metadata that
selects a trusted supervisor-side policy. The policy, not the adapter,
independently proves that the canonical invocation enforces the operating-system
writable roots derived by the delivery service. Adapters without a matching
policy fail registration.

Delivery Git reads and mutations have separate internal deadlines, disable
repository hooks, and reject effective local or worktree-scoped clean, smudge,
and process filters before checkout. An atomic filesystem lifecycle lock
serializes prepare, inspect, and quarantine for one attempt across manager
instances. Interrupted mutations are reconciled into preserved quarantine
bytes or a non-reusable tombstone, receive a `reconciliation-required` marker,
and return failure rather than treating filesystem position as proof of Git
administrative state. Attempt
paths are derived from delivery and attempt identities beneath a rooted
operator directory. Each attempt also stores a supervisor-owned digest of its
canonical request, preventing an existing path from being replayed with altered
authority.
An attempt with an existing quarantine destination is permanently retired from
preparation; every retry must use a new attempt identity.
Logical and resolved namespace and writable-scope positions must match, so a
symlink cannot redirect checkout preparation. Contaminated, clean-retired, or
unverifiable bytes move to a non-reusable quarantine namespace. Physical
deletion after worker exposure requires a joint live-lease and process-
quiescence proof; until that proof is implemented, the runtime preserves the
bytes rather than trusting a check-then-delete sequence.
Quarantine verifies the same immutable request digest and invokes Git move only
when the attempt's common directory matches the authorized repository; foreign
or unverifiable Git metadata is never mutated.

Result commits are built with a temporary index, deterministic supervisor
identity, deterministic parent-relative timestamp, and a fixed delivery
message. This does not stage the author checkout or move its detached `HEAD`.
Only approved write scopes enter that tree. A separate full snapshot covers
write and artifact scopes; both snapshots are repeated, so out-of-authority or
concurrently changing code and artifact bytes fail before validation.
Mechanical and independent validation use separate fresh worktrees. Trusted
validators are direct argv invocations whose resolved executable content,
environment, timeout, and output budget are bound into their command digest;
process groups are terminated on cancellation, timeout, or overflow. The
independent lane must reproduce every required check after mechanical
preflight. Both lanes' bounded outputs and reports are atomically persisted in
a content-addressed manifest beneath a disjoint operator evidence root.

## Consequences

Positive:

- workers can produce real changes without obtaining publication authority;
- overlapping authors and stale owners fail before publication;
- every accepted patch can be reconstructed and revalidated from a clean
  clone;
- retries cannot inherit a dirty or adversarial workspace;
- Git refs provide an atomic, inspectable publication boundary.

Negative:

- hierarchical leases may conservatively serialize non-overlapping paths with
  a shared ancestor;
- clean-checkout validation costs additional I/O and execution time;
- delivery requires complete local Git objects and a durable PostgreSQL lease
  service;
- the default branch still requires an external governed merge process.

## Guardrail

Tests must reject partial lease sets, ancestor/descendant write conflicts,
stale fencing tokens, arbitrary worktree paths, symbolic-link escapes,
self-validation, mutable validator commands, non-reproducible hashes,
publication compare-and-swap failures, worktree reuse after contamination, and
receipt replay with different content.
