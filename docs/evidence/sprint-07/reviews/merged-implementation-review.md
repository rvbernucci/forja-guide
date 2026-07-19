# Sprint 07 Merged Implementation Review

## Scope

- Reviewed range base: `115b6c117c5ffcc42b7a86f786aa89fb41aac554`
- Final public implementation: `1844cf2f046bb244372336b7bc7ecf0073a19194`
- Final reviewed PR head: `0eed709c12b4955550f7db935219ba608c392208`
- Shared tree: `a1b0781ce9eb2b2f645feededddf7746ce052303`
- Public review: [pull request 43](https://github.com/rvbernucci/forja-guide/pull/43)

The squash-merged implementation and the final reviewed PR head have the exact
same Git tree. The review therefore covers the bytes published by the public
implementation commit, not merely a similar patch.

## Review Chain

The first independent whole-range review identified eleven actionable issues:

1. artifact tombstoning could race a new canonical reference;
2. generated timestamps destabilized replay hashes across clock changes;
3. policy memory promotion did not require its configured authenticated
   principal and dedicated permission;
4. physical purge omitted the provider version identifier;
5. tombstoned or purged content could enter an unrecoverable republication
   saga;
6. receipt verification accepted orphan knowledge events;
7. active-memory reads could cross lifecycle or expiry boundaries;
8. conversation closure did not bind exact final transcript bytes;
9. Go results could violate required-array and bounded-source schema rules;
10. concurrent publication was not proven through the complete service path;
11. the restore claim lacked a reproducible two-plane runner.

Those findings were resolved by the lifecycle, authority, replay, retention,
transcript, schema, concurrency, and restore hardening committed through
`7fecda366c55bf9446566bbf016dcc4d6ad98401`.

A focused correctness review then found that invalid reconciliation descriptors
were counted as terminal without persisting terminal canonical state. Commit
`bd1b3b565ff2a755a9ffcb0fe665dda1569000a1` made the conflict durable and added
unit and PostgreSQL integration coverage.

The next provenance review found that transcript source references were checked
as a superset rather than an exact set. Commit
`10fe77d0a3a93a644e696917dbae206b18bb83b1` enforced exact cardinality and set
equality and added an oversized-source regression test.

The public CodeRabbit review of the complete 63-file range then found two final
issues: a bare `..` logical path bypassed the traversal guard, and two
conversation commands mapped a missing aggregate to an internal database
fault. Commit `0eed709c12b4955550f7db935219ba608c392208` fixed both findings with
contract and PostgreSQL regression tests. CodeRabbit automatically marked both
threads resolved and returned a passing final status.

In total, fifteen distinct actionable findings were resolved before candidate
publication.

## Security Review

Independent security review found no critical or high-severity vulnerability.
Three reported observations were triaged as non-actionable under the declared
threat model:

- `memory:promote` is intentionally omitted from broad permission sets so it
  remains a separately granted privilege;
- application receipts are not designed to resist a trusted PostgreSQL DBA;
- fixed credentials exist only inside ephemeral loopback MinIO rehearsals.

The final public review additionally verified the changed authorization,
storage, retention, transcript, and tenant-isolation paths. Reachability
scanning reported no known vulnerability in the final dependency graph.

## Mechanical Corroboration

- Final implementation PR workflow:
  <https://github.com/rvbernucci/forja-guide/actions/runs/29682094002>
- Local `make validate`: passed after the final review fixes.
- Local PostgreSQL integration and rollback suite: passed after the final
  review fixes.
- Real PostgreSQL plus versioned-MinIO two-plane restore: passed with three
  restored artifacts and identical source/restored physical inventories.
- `govulncheck@v1.6.0`: no reachable vulnerabilities found.

## Residual Risks

- Active-memory listing is bounded to 500 records but currently performs
  per-record loading. Batching is a measured performance optimization, not a
  correctness or authority blocker.
- Proposed-memory operational counts do not yet have a dedicated partial
  index. Existing query timeouts preserve fail-soft behavior; index tuning
  belongs to measured performance work.
- PostgreSQL DBA, host root, CI administrator, and configured object-store
  administrator remain trusted deployment roles.

## Conclusion

No actionable implementation finding remains. Sprint 07 is eligible to publish
a fail-closed evidence candidate, but only a separate immutable review of that
published candidate may authorize Sprint 08.
