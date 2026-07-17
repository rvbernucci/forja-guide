# ADR-0009: Two-Phase Sprint Closure

Status: Accepted

## Context

A Sprint close receipt authorizes downstream work, so it is a trust boundary
rather than ordinary mutable documentation. A review of an uncommitted tree
cannot identify exact bytes, while a candidate and attestation placed in one
squash-merged pull request lose their parent-child topology on `main`.

The repository needs independent review evidence that survives GitHub squash
merges, prevents a reviewed candidate from changing during promotion, and
scales across the planned Sprint roadmap files.

## Decision

Sprint 03 and later use closure protocol v2 in two public phases:

1. A complete `closure-candidate.json` is published to `main` in its own pull
   request. It is explicitly non-authoritative and cannot authorize another
   Sprint.
2. An independent validator reviews that exact public commit.
3. A separate attestation pull request starts from the reviewed `main` commit.
   It replaces the candidate with `close-receipt.json`, adds the hash-pinned
   review artifact, and updates only the master and owning detailed roadmaps.

Protected CI injects its immutable pull-request base SHA or validated `main`
head. Before merge, the repository validator requires that trusted SHA to equal
the reviewed candidate. After publication, it requires the attestation commit
to be an ancestor of the trusted `main` head. It also verifies the fail-closed
candidate, requires the attestation introduction commit to be its direct child,
allowlists the exact promotion paths, and rejects later receipt mutation. The
owning roadmap is derived from the numeric Sprint range.

Sprint 00-02 receipts remain valid under the legacy format. Every closure
candidate uses protocol v2; legacy compatibility applies only to already
closed receipts and cannot create a downgraded candidate.

## Consequences

Positive:

- review provenance is bound to immutable, publicly reachable bytes;
- a single squash-unsafe closure pull request fails protected validation;
- the final attestation cannot hide implementation or evidence changes;
- future Sprints update the correct detailed roadmap.

Negative:

- closing a Sprint requires two pull requests and two protected CI passes;
- full-history checkout and the protected CI base/head SHA are required to
  validate an authoritative protocol-v2 receipt.

## Guardrail

CI must fetch full Git history. Tests must reject unpublished candidates,
candidate or receipt protocol downgrade, ambiguous closure files, unauthorized
next-Sprint fields, non-promotion changes, review artifacts outside the Sprint
evidence directory, and receipts modified after their attestation commit.
