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
The delivery ID identifies the worktree lease; file and artifact lease IDs are
canonical repository-relative scopes. These identities are revalidated from
the receipt rather than trusted as opaque strings.

Every untrusted worker adapter must declare a versioned isolation capability
and prove that its effective operating-system writable roots equal the roots
derived by the delivery service. Adapters without equivalent enforcement fail
registration.

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
