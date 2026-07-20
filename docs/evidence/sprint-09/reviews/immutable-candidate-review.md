# Sprint 09 immutable closure audit

PR #58 corrected the sole promotion blocker. No remaining promotion-blocking finding was identified.

## Immutable scope

| Role | Commit | Tree |
|---|---|---|
| Subject | `ddaba97dc51a9d70e1dce0a82093fab6d5317040` | `417eb1e3cfef4a7360b8df1aa5730acc95c4108b` |
| Implementation basis | `a9a971cff16ee50ab4fafca3a475f117a47f438d` | `1f85fb5f8bd87ed65b1c07dfe8358067a9556d20` |
| Failed candidate | `c4ae1aab5c43d54e2f25232ccf19d83d9e0874a0` | `b0a251c86cd3c45d64a9c9869d0cda08f75b7bae` |

Verified direct ancestry:

```text
a9a971c implementation basis
  -> c4ae1aa failed evidence candidate (#57)
    -> ddaba97 corrected evidence candidate (#58)
```

Basis-to-subject changes are limited to the eight Sprint 09 evidence files. PR #58 itself changes only `ci-receipt.md`, `rollback-report.json`, `test-report.json`, and `validation-report.json`; it changes no implementation.

## Findings

- PR #58 accurately separates the evidence:

  - PostgreSQL integration proves cross-`Store` advisory-lock serialization and the bounded mutation context.
  - The independently executed live-Qdrant gate proves collection creation, query paths, atomic alias replacement, readback, guarded rollback, deletion, and cleanup through the guard interface.
  - All four corrected artifacts explicitly state that no composed PostgreSQL-plus-live-Qdrant integration test is claimed.

- The implementation basis contains the relevant TOCTOU controls: cutover and rollback require a non-nil distributed guard; PostgreSQL holds a tenant/repository/alias transaction-scoped advisory lock under a 30-second context; observation, expected-target comparison, atomic Qdrant delete/create, and post-mutation readback occur while guarded. Stale expectations, missing guards, ambiguous alias observations, and failed guard acquisition fail closed.

- Retrieval remains fail closed: mandatory pre-ranking scope/lifecycle filters, exact generation binding, projector-backlog checks before Qdrant access, canonical PostgreSQL re-resolution, zero accepted context on stale/unknown projection state or resolver failure, and tombstone-before-derived-delete behavior are present.

- Protocol v2 is correctly pre-attestation: `closure-candidate.json` is version `2.0`, `candidate`, non-authoritative, records independent validation as false, and sets `next_sprint_authorized` to `null`. No `close-receipt.json` or Sprint 10 evidence directory exists. Sprint 10-14 checkboxes remain open and the roadmaps explicitly keep Sprint 10 unauthorized.

- Exact committed CI provenance is [run 29726147010](https://github.com/rvbernucci/forja-guide/actions/runs/29726147010), [job 88299583914](https://github.com/rvbernucci/forja-guide/actions/runs/29726147010/job/88299583914), recorded as `success`, 8m44s, on basis `a9a971c...`. The basis workflow defines PostgreSQL durability and live-Qdrant lifecycle as separate steps. Live re-fetch was unavailable because this audit environment had no GitHub network or browser access; the immutable receipt and local workflow were verified.

## Mechanical verification

Passed:

- Clean detached worktree equals the subject tree.
- Full Git history present; commit/tree/parent topology verified.
- `git fsck --full --no-dangling`.
- `git diff --check`.
- All Sprint 09 JSON parsed successfully.
- Protected-main repository validation: 83 Markdown files and 34 schemas.
- `gofmt` inspection and `go mod verify`.
- Evidence basis uniformity and fail-closed closure fields.
- No tracked private evaluation material beyond `private-evaluations/README.md`.

The recorded metrics independently reproduce from the basis: 463 tracked non-evidence files, 120 Sprint-changed paths, 197 Go files, 82 Go test files, 575 Go test/fuzz entry points, 78 retrieval entries, 55 Python tests, 34 schemas, nine migrations, four baseline policies, and zero private quality evaluations.

The full `make validate` and fresh Go test execution could not run in this read-only sandbox: `npm ci` was denied permission to create `node_modules`, and Go was denied a temporary build directory. No repository file changed.

## SHA256 bindings

| Artifact | SHA256 |
|---|---|
| `closure-candidate.json` | `b374eedf1d1771df5512efeae734e326ae92140a242bdcdddc168a7c1423cc6a` |
| `metrics-summary.json` | `87bcebb15447f56e64ad32401a4c4d419ba33bc673533e88b5a925c009feeff7` |
| `plan.json` | `7c0f0ab9f8e4a7f1b70ed1e311c0b3fc1c46ff8e945acda505eec50f12213298` |
| `rollback-report.json` | `4738cd88016756aa604923cca76baa1e6cd3682b546115ab8ce72bddaf7a1d45` |
| `security-report.json` | `ce2d519cf82afc91095d3a8ef90111e69398cca0c120e1a706df9cd0522add19` |
| `test-report.json` | `a3a47d3a985c7ff4c9e84979ffb0ef27ec10e4bbf87e2caa49312f444732e05f` |
| `validation-report.json` | `c8910bb228802086120505d77347898d7f08b05587f9fa359d2539fb5a85916e` |
| `ci-receipt.md` | `8b217a64300f57df178072dd85c993384885d3606b80dcf9698a019603500c32` |

## Blocking findings

None.

## Deferred, non-blocking risks

- Qdrant aliases are endpoint-global while the PostgreSQL lock namespace is tenant/repository/alias scoped. Shared endpoints therefore require globally unique aliases or endpoint isolation.
- Direct administrator/out-of-band Qdrant alias mutation bypasses the governed lock and remains unsupported.
- Private holdout/OOD/adversarial quality evaluation, workload-role Bedrock proof, Radeon-local inference, provider activation, and deployment-host replay remain Sprint 10 gates.
- The installed Qdrant validator's caller-working-directory dependency remains deferred.
- No canonical exact-lookup availability fallback is implemented.
- Qdrant/Neo4j administrators remain trusted infrastructure roles.
- No separate scanner-backed reachable-vulnerability count is claimed.
- Operational derived-store rebuild execution still requires its own immutable receipt.

Provenance: single-agent, read-only review of the exact detached subject; no Codex Security workflow, scan artifact, external mutation, or file edit was performed.

VERDICT: PASS
