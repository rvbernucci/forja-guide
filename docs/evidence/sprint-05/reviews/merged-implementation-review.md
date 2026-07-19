# Sprint 05 Merged Implementation Review

## Reviewed Basis

- Sprint 04 authoritative close commit:
  `d6eda8dc12a5ecf5a6e2783f7302a6d38a9b9ed4`
- Initial merged Sprint 05 implementation:
  `30aecd97135d5e4014991f61e2bfa955cf4b9b13`
- Final public implementation after recovery hardening:
  `c2549e4e3c416410fe39996f43d6293ecb131d60`
- Implementation pull requests:
  [#32](https://github.com/rvbernucci/forja-guide/pull/32) and
  [#33](https://github.com/rvbernucci/forja-guide/pull/33)

## Review Chain

The implementation underwent 27 isolated Codex CLI review passes while the
end-to-end delivery branch was still mutable. The final pre-merge pass reported
no actionable correctness issue and ran the complete validation and PostgreSQL
integration suites.

An isolated review of the first public squash commit then found three issues:

1. Projection replay rejected valid Sprint 04
   `awaiting_decision -> running` history after the runtime resume path changed
   to `queued`.
2. Pipeline recovery loaded external validation files before reconciling an
   already prepared or published journal, which could strand a publication
   after its Git CAS.
3. Cleanup could report `ErrWorktreeNotFound` without first honoring a durable
   `reconciliation-required` marker from an interrupted Git worktree move.

PR #33 resolved all three findings. Replay now accepts the retired transition
only for immutable historical events while runtime commands remain closed.
Journal-only recovery reconstructs and verifies the request, Git result, lease
fences, canonical receipt, and stored authority, receipt, and intent hashes; it
can only observe/finalize or observe/abandon and cannot issue a new CAS.
Worktree cleanup checks the supervisor-owned reconciliation marker before any
missing-path inference.

The hardening patch passed a focused isolated review with no actionable issue.
After squash merge, a final isolated review of exact public commit
`c2549e4e3c416410fe39996f43d6293ecb131d60` confirmed that legacy projection
replay, journal-only publication recovery, and reconciliation-marker handling
remain consistent with their surrounding contracts. Relevant package tests
passed during that review.

## Mechanical Corroboration

- PR #32 quality workflow:
  <https://github.com/rvbernucci/forja-guide/actions/runs/29664347291>
- Initial implementation `main` workflow:
  <https://github.com/rvbernucci/forja-guide/actions/runs/29664694892>
- PR #33 hardening workflow:
  <https://github.com/rvbernucci/forja-guide/actions/runs/29665599912>
- Final implementation `main` workflow:
  <https://github.com/rvbernucci/forja-guide/actions/runs/29665750739>

Every workflow passed repository validation and the PostgreSQL 18 durability
suite. Local acceptance additionally passed `make validate`, the complete
PostgreSQL integration target, real Git publication and crash recovery,
rollback against the Sprint 04 binary, and durable process restart.

CodeRabbit completed PR #33's status context but explicitly reported that no
review ran because its usage limit had been reached. It is not counted as
independent validation.

## Result

All identified findings were resolved. No actionable correctness issue remains
in the final public implementation review. Sprint closure still requires a
separate review of the immutable, fail-closed evidence candidate.
