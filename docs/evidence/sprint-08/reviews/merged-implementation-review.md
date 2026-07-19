# Sprint 08 Merged Implementation Review

## Scope

- Reviewed range base: `e763f85bef2de14d92d924300839d88b00ff00d0`
- Final public implementation: `0cbce2a7256df476a94b96624834a9abd32c0bf8`
- Final reviewed PR head: `88fe7c502e65062ebe8a12f781710882a4433724`
- Shared tree: `d9c24401cf5d35022dc9bd4159c3b4d78ca2784e`
- Public review: [pull request 46](https://github.com/rvbernucci/forja-guide/pull/46)

The squash-merged implementation and the final reviewed PR head have the exact
same Git tree. The review therefore covers the bytes published by the public
implementation commit, not merely a similar patch.

## Review Chain

The first independent whole-range audit identified eight actionable authority,
replay, and lineage issues:

1. retry evidence changed when the clock changed;
2. artifact identity could be reserved before repository authority preflight;
3. initial and incremental semantic deltas could misrepresent canonical state;
4. overloaded declarations could collapse into one lineage record;
5. relation source ownership could disagree with its file authority;
6. supersession lacked an explicit durable event;
7. CLI callers could select canonical authority through an actor flag;
8. caller array order could change canonical artifact bytes.

Iterative focused audits then identified seven additional semantic-delta issues:

1. adapter reuse ignored configuration-file changes;
2. PostgreSQL trusted caller-supplied semantic delta labels;
3. callers could label arbitrary changes as renames;
4. empty and whitespace previous IDs crossed semantic states inconsistently;
5. copied and modified files could produce false rename inference;
6. ambiguous equal-content rename candidates were not conservative enough;
7. previous-entity IDs were not constrained to the entity kind they reference.

All fifteen findings were resolved with regression coverage. A final isolated
audit reported no remaining blocking finding.

The public CodeRabbit review then found one additional TypeScript Compiler API
issue: export modifiers on `const`, `let`, and `var` belong to the enclosing
`VariableStatement`, so exported variables were being fingerprinted as private.
Commit `88fe7c502e65062ebe8a12f781710882a4433724` corrected the AST ownership rule
and added regressions for all three declaration forms, multiple declarations,
private variables, and direct function exports. CodeRabbit marked the thread
resolved and returned a passing final status.

In total, sixteen distinct actionable implementation findings were resolved
before candidate publication.

## Public CI Findings

The first public workflow exposed that the TypeScript adapter required Node 24
while the default runner supplied Node 22. The workflow now installs exact Node
24.18.0 with `actions/setup-node@v6.5.0` pinned by commit SHA and verifies the
major version before deterministic dependency installation.

- Failed environment-mismatch run:
  <https://github.com/rvbernucci/forja-guide/actions/runs/29688307203>
- Corrected final PR run:
  <https://github.com/rvbernucci/forja-guide/actions/runs/29689138201>
- Exact merged-main run:
  <https://github.com/rvbernucci/forja-guide/actions/runs/29689354760>

An additional independent workflow review found no blocking issue in the
runtime pin, action pin, npm cache boundary, or deterministic install sequence.

## Security Review

No critical or high-severity vulnerability was found. The final implementation
keeps tenant and repository authority in PostgreSQL, reads committed Git objects
instead of mutable worktree bytes, treats adapter output as untrusted, rejects
unsupported repository object types and ambiguous paths, and excludes source
content and high-cardinality identities from telemetry.

The bundled adapters are trusted release components with bounded process
lifecycle and filtered environments; this is not claimed to be a hostile-code
sandbox. Third-party adapter execution remains prohibited until the later
isolation Sprint.

## Mechanical Corroboration

- Final implementation PR workflow: passed on PostgreSQL 18.3.
- Exact public `main` workflow: passed on PostgreSQL 18.3.
- Local `make validate`: passed after the final review fix.
- Fresh-database PostgreSQL integration and rollback suite: passed.
- Real Git indexing drill: four snapshots, two repository authorities, four
  canonical receipts, selective adapter reuse, and configuration invalidation.
- `govulncheck@v1.6.0`: no reachable vulnerabilities found.

## Residual Risks

- Bundled adapters are trusted application components, not hostile repository
  plugins. Production third-party adapter isolation remains future work.
- Python dynamic dispatch and other unresolved relations remain explicit gaps;
  the indexer does not invent semantic certainty.
- Git, PostgreSQL, object-store, CI, and host administrators remain trusted
  deployment roles.
- Qdrant and Neo4j projections remain absent and cannot yet serve semantic or
  graph retrieval.

## Conclusion

No actionable implementation finding remains. Sprint 08 is eligible to publish
a fail-closed evidence candidate, but only a separate immutable review of that
published candidate may authorize Sprint 09.
