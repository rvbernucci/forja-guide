# Sprint 03 Closure Evidence Review

## Pass 1: Completion Integrity

- Reviewed basis: merged implementation commit
  `2431b69ac1cc3bc1d6e5d718904542ff49838133` plus the uncommitted closure
  package.
- Finding: 1 P1 evidence-integrity issue.
- Finding detail: `validation-report.json` marked the closure review as passed
  while this artifact still contained a pending placeholder. That made the
  close receipt and Sprint 04 authorization premature.
- Resolution: this artifact now records the independent finding, the validator
  result is `findings_resolved`, and the artifact is hash-pinned.

## Pass 2: Gate Semantics

- Reviewed basis: the corrected uncommitted closure package.
- Finding: 1 P1 evidence-integrity issue.
- Finding detail: Pass 1 described a future no-findings review and PR-head CI as
  internal closure prerequisites while the receipt already declared closure.
  A commit cannot contain proof of its own future CI, making that wording
  circular.
- Resolution: the package now distinguishes completed closure evidence from
  external publication controls. The merged implementation CI, exact-package
  local validation, independent implementation review, independent evidence
  review, security scan, integration suite, and isolated rollback proof are the
  closure basis. Protected PR CI remains a required publication control before
  this proposed receipt can reach `main`; it is not claimed as evidence stored
  inside its own input commit.

## Pass 3: Immutable Review Binding

- Reviewed basis: the corrected but still uncommitted closure package.
- Finding: 1 P1 evidence-integrity issue.
- Finding detail: an uncommitted working tree is mutable, so the prior review
  descriptions could not prove which exact evidence files and receipt they had
  validated.
- Resolution protocol: this package is a non-authoritative closure candidate.
  Sprint 04 remains blocked while the candidate is committed. An isolated
  review must then validate that exact commit SHA. A child attestation commit
  may only bind the immutable review result and promote the candidate receipt;
  it must not alter the reviewed implementation or closure evidence. Protected
  PR CI and post-merge review remain external publication controls.

## Pass 4: Capability Gate Consistency

- Reviewed commit: `5133fc85c1970d16f537116e5d37c77b3c683d77`.
- Finding: 1 P2 roadmap consistency issue.
- Finding detail: the candidate incorrectly changed Gate A from achieved to
  pending even though the authoritative Sprint 02 receipt already proves that
  gate. Only Sprint 03 closure is pending.
- Resolution: Gate A remains achieved. The evidence documentation now defines
  the mutually exclusive candidate and final receipt states enforced by the
  repository validator.

## Pass 5: Evidence Chronology

- Reviewed commit: `1bfcfc8ac5158a5b822a500549a92602a605c6d4`.
- Finding: 1 P2 audit-provenance issue.
- Finding detail: the validation and metrics documents incorporated Pass 4 but
  retained timestamps from before the reviewed commit existed.
- Resolution: original recording timestamps remain immutable historical facts;
  each amended document now carries an explicit `amended_at` timestamp after
  this finding.

## Pass 6: Attestation Integrity

- Reviewed commit: `50f2d97242ba64edfa59cdaff8609865a53618b8`.
- Findings: 2 P1 closure-integrity issues.
- Finding details: protocol v2 could be bypassed by omitting its version, and
  the validator proved only that the cited candidate SHA existed rather than
  proving that the final attestation was a minimal child of that candidate.
- Resolution: Sprint 03 and later receipts must use protocol v2. The validator
  now requires the reviewed candidate to be the attestation's direct parent,
  verifies its fail-closed candidate document, permits only declared promotion
  paths in the child diff, and pins the final receipt to its introduction
  commit. Sprint 04 remains blocked until the corrected candidate passes an
  immutable review.

## Pass 7: Publication Topology and Roadmap Ownership

- Reviewed commit: `6a2ac0d114ba160e812ac7d68f294bba28308835`.
- Findings: 1 P1 publication-integrity issue and 1 P2 roadmap-scaling issue.
- Finding details: a candidate and attestation submitted in one squash-merged
  pull request would lose the direct-parent topology required by protocol v2.
  The attestation path allowlist also fixed every future closure to the Sprint
  00-04 roadmap.
- Resolution: closure now uses two public phases. The fail-closed candidate is
  merged to `main` first, then independently reviewed at that exact public SHA.
  A separate minimal attestation pull request is based directly on the reviewed
  candidate. The validator derives the detailed roadmap from the Sprint ID for
  ranges 00-04, 05-09, and 10-14. Sprint 04 remains blocked throughout the
  candidate phase.

## Pass 8: Published-Base Enforcement and Decision Record

- Reviewed commit: `db45fe497d5390a80202e63929fc8e3cc43fe11f`.
- Findings: 1 P1 publication-integrity issue and 1 P2 governance issue.
- Finding details: the documented two-phase flow did not mechanically require
  the candidate to exist on `origin/main`, so a single pull request could still
  pass before failing after squash. The new trust and compatibility boundary
  also lacked the ADR required by the agent operating contract.
- Resolution: protocol-v2 validation now requires the candidate commit to be
  reachable from `origin/main`, with a repository-level Git test covering the
  unpublished and published states. ADR-0009 records the two-phase trust model,
  compatibility rule, exact promotion surface, and full-history CI guardrail.

## Pass 9: Trusted Base and Main-Tip Stability

- Reviewed commit: `9ea0f8fd0fb3dc3304b52adc352ea5c80fbbce4e`.
- Findings: 2 P1 publication-integrity issues.
- Finding details: the `origin/main` tracking ref was mutable inside the
  checkout, and an ancestry-only check still accepted an attestation after
  unrelated work advanced `main`, producing an invalid squash parent.
- Resolution: protected CI now injects the immutable pull-request base SHA or
  validated `main` head. A pre-merge attestation requires exact candidate/base
  equality; a published attestation must be an ancestor of the trusted main
  head. Tests cover an unpublished candidate, a valid candidate base, a stale
  advanced base, and a published attestation.

## Pass 10: Candidate Protocol Downgrade

- Reviewed pull request: `https://github.com/rvbernucci/forja-guide/pull/13` at
  `21a4f49e83d5367ce3580c0d84a6752bd67b9df8`.
- Finding: 1 P1 protocol-integrity issue from CodeRabbit.
- Finding detail: candidate validation required protocol v2 only for Sprint 03
  and later, allowing a newly introduced candidate under a legacy Sprint ID to
  omit the protocol version.
- Resolution: every closure candidate now requires protocol v2. Legacy
  compatibility remains restricted to the closed receipts for Sprints 00-02,
  and a regression test proves that it cannot downgrade a candidate.

## Pass 11: Legacy Receipt Promotion Downgrade

- Reviewed commit: `3ebfb86d0df3de9ce7ce818b377bc6b22206373e`.
- Finding: 1 P1 closure-integrity issue from an isolated Codex CLI review.
- Finding detail: a protocol-v2 candidate using a legacy Sprint ID could still
  be replaced by a new receipt without a protocol version, bypassing immutable
  review and authorizing arbitrary downstream work.
- Resolution: legacy receipt compatibility is now content-addressed. Only the
  exact three historical receipts at their canonical paths are accepted; every
  new or changed receipt must use protocol v2. A regression test exercises the
  previously accepted downgrade.

## Pass 12: Promotion Integrity and Irreversibility

- Reviewed public candidate:
  `c7216ba518f850526c77caf28365220412b948c5`.
- Findings: 3 P1 closure-integrity issues and 1 P2 fail-closed issue from an
  isolated Codex CLI review.
- Finding details: attestation could rewrite reviewed candidate fields,
  authorize an arbitrary successor, later replace a receipt with a candidate,
  or self-assert a v2 receipt when Git history was unavailable.
- Resolution: the validator now derives the exact successor from the canonical
  Sprint sequence, treats Sprint 14 as terminal, compares the full promoted
  receipt against the reviewed candidate with an explicit attestation-field
  allowlist, rejects any candidate after receipt introduction, and requires Git
  history for authoritative v2 validation. Reproduction tests cover all four
  exploits.

## Pass 13: Type-Strict Candidate Preservation

- Reviewed commit: `cd75df3f8864ad6df32c36188fcb4b21541e0a76`.
- Finding: 1 P1 closure-integrity issue from an isolated Codex CLI review.
- Finding detail: Python dictionary equality considers JSON `true` equal to
  `1`, and likewise conflates some integer and floating-point values, allowing
  preserved receipt fields to change type during promotion.
- Resolution: candidate and receipt projections are now compared as canonical,
  finite JSON encodings. A regression assertion proves that `true` cannot be
  promoted as `1`.

## Pass 14: Complete-History and Lossless Promotion Review

- Reviewed public candidate:
  `361a28c65f9c1f33d84b4f133f5ab2d4a496b3bb`.
- Findings: 2 P1 closure-integrity issues and 2 P2 fail-closed issues from an
  isolated Codex CLI review.
- Finding details: a receipt could be deleted, replaced by a candidate, and
  reintroduced without final-tip detection; noncanonical numeric or Unicode
  Sprint IDs could derive a successor; shallow clones could claim complete
  history; and floating-point decoding could collapse distinct reviewed JSON
  numbers before comparison.
- Resolution: protocol-v2 receipts now require exactly one historical
  introduction and a non-shallow repository. Sprint IDs must use the canonical
  ASCII `00` through `14` form. Promotion validation decodes every JSON number
  as a lossless kind-and-lexeme value before recursively type-strict comparison.
  Regression tests reproduce each exploit and prove it fails closed.
