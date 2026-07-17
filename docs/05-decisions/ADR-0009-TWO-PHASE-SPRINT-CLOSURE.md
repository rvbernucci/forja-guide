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
allowlists the exact promotion paths, preserves every reviewed candidate field
except the declared attestation fields, and rejects later receipt mutation or a
return to candidate state. The owning roadmap and exact authorized successor
are derived from the numeric Sprint range; Sprint 14 is terminal and authorizes
no successor. An authoritative v2 receipt is invalid without complete Git
history. Preserved candidate values are compared through canonical JSON so
type-changing substitutions cannot exploit host-language equality rules.
Numeric tokens are decoded losslessly for promotion comparison. Authoritative
validation rejects shallow repositories, noncanonical Sprint identifiers, and
any receipt path with more than one introduction across full merge history.

Sprint 00-02 receipts remain valid under the legacy format only when their path
and content hash match the three receipts already published. Every closure
candidate uses protocol v2; legacy compatibility cannot admit a new,
reformatted, mutated, or downgraded receipt.

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
candidate or receipt protocol downgrade, ambiguous closure files, noncanonical
successors, reviewed-content changes, closure reopening, non-promotion changes,
review artifacts outside the Sprint evidence directory, history-free v2
receipts, shallow history, repeated receipt introductions across merged
branches, and receipts modified after their attestation commit.
