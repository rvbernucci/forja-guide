# Sprint 05 Isolated Delivery Plan

Status: In progress

## Outcome

Convert one approved worker task into a bounded Git commit, reproducible
validation evidence, and an atomically published delivery reference without
granting the worker lease, commit, validation, or publication authority.

## Trust Boundary

- The scheduler supplies an approved delivery request; model output cannot
  widen paths, validators, budgets, identities, or the target reference.
- PostgreSQL lease sets are the authority for worktree, file, and artifact
  ownership. Every protected transition carries the exact live fencing token.
- The delivery service derives the worktree path beneath an operator-owned
  root and checks out an exact commit, never an untrusted branch name.
- The worker may edit only pre-created writable roots. It cannot commit, update
  refs, decide validation, or publish its own evidence.
- Mechanical validation runs before independent validation. Independent
  validation uses a clean checkout and an identity different from the author.
- Publication is a compare-and-swap update of a delivery ref. The default
  branch is never updated by this Sprint.

## Delivery State Machine

```text
authorized
  -> leased
  -> worktree_prepared
  -> worker_completed
  -> commit_created
  -> mechanically_validated
  -> independently_validated
  -> published

Any pre-publication failure -> failed or retryable
Dirty or unverifiable failure -> quarantined
Stale fence or changed target ref -> conflict
```

Only the delivery service can advance this state machine. A failed clean
attempt may retry from the same immutable base in a new worktree. A
contaminated worktree is quarantined and never reused.

## Implementation Slices

### 1. Contracts and policy

- [x] Define request, validation-report, and receipt schemas.
- [x] Record the delivery trust boundary in ADR-0011.
- [x] Require distinct author and independent-validator identities.
- [x] Define canonical patch, changed-path, tree, and evidence hashes.
- [x] Add strict Go contract mappings and valid/invalid fixtures.

### 2. Atomic lease authority

- [x] Add `artifact` as a canonical lease resource type.
- [x] Acquire the worktree plus hierarchical file and artifact lease set in one
  transaction and deterministic order.
- [x] Renew and release only the exact owner and fencing-token set.
- [x] Reject overlapping writers, stale fences, partial grants, and lease
  expansion after work starts.

### 3. Worktree lifecycle

- [x] Resolve and verify the repository root and exact base commit.
- [x] Derive an attempt path beneath a non-symlink operator root.
- [x] Create a detached isolated worktree and pre-create writable directories.
- [x] Verify Git common-directory identity, clean state, and exact `HEAD`.
- [ ] Remove clean worktrees and quarantine dirty or unverifiable worktrees.
  Quarantine is implemented; post-worker deletion awaits live-lease and
  process-quiescence proof.
- [x] Retry only from a newly created worktree at the original base commit.

### 4. Validation and evidence

- [x] Capture the base commit, result commit, tree hash, canonical binary patch
  hash, and sorted changed paths.
- [x] Run trusted argv-only validators with bounded time and output.
- [x] Always enforce path scope, secret scanning, JSON schema validity, and
  generated-file policy before configured format and test validators.
- [x] Reproduce the result commit in a clean checkout for independent
  validation.
- [x] Hash every validation result and produce a canonical evidence manifest.
- [x] Reject self-validation, missing checks, output overflow, and changed
  evidence.

### 5. Controlled publication and recovery

- [x] Create the commit with a supervisor-owned deterministic identity.
- [x] Publish only to `refs/forja/deliveries/<delivery-id>` using compare and
  swap against the expected old object ID.
- [x] Persist the delivery receipt before releasing the lease set.
- [x] Make receipt creation and publication replay-safe.
- [x] Reconcile exact prepared attempts after lease expiry without deleting
  quarantined evidence or inferring publication from timing.

### 6. Acceptance and closure

- [x] Complete one synthetic approved task through validated publication.
- [x] Prove concurrent overlapping authors cannot both acquire authority.
- [x] Prove stale fencing tokens cannot commit or publish.
- [x] Prove out-of-scope, ignored, symlink, and hidden-index mutations fail.
- [x] Reproduce validation from a clean clone using only receipt references.
- [x] Run race, integration, rollback, and independent security reviews.
- [ ] Publish a fail-closed Sprint 05 evidence candidate and close it through
  the two-phase protocol.

## Acceptance Evidence

- Contract fixtures and cross-field semantic tests.
- PostgreSQL concurrency tests for atomic hierarchical lease sets.
- Real Git worktree tests for create, retry, conflict, quarantine, and cleanup.
- Validator fault injection for timeout, output bounds, secret findings, and
  clean-clone disagreement.
- An end-to-end synthetic repository whose receipt reconstructs the exact patch
  and validator results.
- An isolated rollback rehearsal against the Sprint 04 close commit.

## Rollback

Stop new delivery intake, let live leases expire or release their exact fence,
retain quarantined worktrees and receipts, remove only verified-clean temporary
worktrees, and inspect the publication journal. When that journal is empty,
reverse Sprint 05 migrations under the existing migration barrier and deploy
the authoritative Sprint 04 commit. After any publication history exists,
schema downgrade is deliberately refused: preserve the journal, keep delivery
intake disabled, and use forward repair. Never delete receipts, reset an
operator branch, or delete unverified work to force a rollback.
