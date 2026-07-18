# Sprint 05 Isolated Delivery Plan

Status: In progress

Schema `1.1` and migrations 005-006 form one unreleased Sprint 05 delivery.
After release, their contract bytes and migration checksums are immutable; any
later change requires a new contract version or forward migration.

## Outcome

Convert one approved worker task into a bounded Git commit, reproducible
validation evidence, and an atomically published delivery reference without
granting the worker lease, commit, validation, or publication authority.

## Trust Boundary

- The scheduler supplies an approved delivery request; model output cannot
  widen paths, validators, budgets, identities, or the target reference.
- An independent human hash-pins the complete request in an immutable approval
  event after the Sprint decision and exact queued attempt exist.
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
- [x] Bind request, report, manifest, receipt, journal, and leases to one
  tenant/repository identity and operator-authorized canonical Git root.
- [x] Require immutable human approval of every request field before runtime
  or filesystem side effects.
- [x] Publish the repository-scoped delivery artifacts as schema version `1.1`;
  reject the unclosed `1.0` draft rather than changing it silently.

### 2. Atomic lease authority

- [x] Add `artifact` as a canonical lease resource type.
- [x] Acquire the worktree plus hierarchical file and artifact lease set in one
  transaction and deterministic order.
- [x] Renew and release only the exact owner and fencing-token set.
- [x] Renew scheduler and delivery authority continuously and cancel execution
  if either heartbeat loses its fence.
- [x] Synchronously refresh both authorities after potentially blocking scope
  acquisition and before the first worktree mutation.
- [x] Reject overlapping writers, stale fences, partial grants, and lease
  expansion after work starts.
- [x] Bind the request and publication intent's minimum 60-second lease TTL to
  the exact persisted lease-set and member duration.
- [x] Persist the lease set's immutable authorized TTL and reject replay or
  renewal that attempts to change it.
- [x] Require legacy lease sets to drain before migration 006 rather than
  inferring their original TTL from independently sampled timestamps.

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
- [x] Reopen and hash the persisted evidence inventory before journal mutation
  and again inside the fenced Git callback, with enumeration and reads pinned to
  one opened directory identity.
- [x] Revalidate the exact lease set and minimum authority horizon after the
  in-fence evidence read and immediately before Git mutation.
- [x] Reobserve the exact publication ref after concurrent completion or
  recovery transition before releasing any lease.
- [x] Pin one stable publication-operation timestamp across receipt, journal,
  concurrent replay, and crash recovery.
- [x] Reconcile exact prepared attempts after lease expiry without deleting
  quarantined evidence or inferring publication from timing.
- [x] Reobserve the approved pre-CAS ref under the publication lock, retire the
  exact intent as `abandoned`, and release its lease so a clean retry can proceed.
- [x] Reject cross-repository path redirection before journal or Git mutation.
- [x] Detect replacement of the operator-authorized physical repository path
  before durable publication.
- [x] Serialize first publication preparation and cancellation through the
  same locked Run; reject cancellation after `prepared` or `published`.
- [x] Recover through fresh PostgreSQL Store and publication-service instances
  after an injected crash between real Git CAS and SQL publication commit.
- [x] Expose pipeline recovery that reconciles expired attempts, reloads exact
  persisted validation evidence, and invokes journal recovery without manual
  database edits.

### 6. Acceptance and closure

- [x] Complete one synthetic approved task through validated publication.
- [x] Reject schema-invalid, post-approval-mutated, and stale-fence requests
  before durable or filesystem mutation.
- [x] Persist a canonical retryable result when the worker fails before it can
  return a valid result contract.
- [x] Normalize a schema-valid successful worker result accompanied by a
  supervisor error into a retryable durable attempt and Run outcome.
- [x] Settle heartbeat failure before persisting a worker result, normalize
  authority-induced cancellation as retryable, then refresh and restart both
  authorities before post-worker Git mutation.
- [x] Finish already-cancelling Runs after process quiescence, accept a missing
  worktree only for a pre-worker `preparing` recovery, and retain retryability
  when lease loss interrupts result-commit creation.
- [x] Reconstruct journaled commit identity from receipt bytes without live Git
  mutation, bound detached recovery renewal, and preserve real renewal errors
  that race intentional heartbeat shutdown.
- [x] Recognize never-created retry bindings and fence every Run transition
  incompatible with a prepared or published delivery journal.
- [x] Resume blocked work through a new queued scheduling cycle, reject
  context-only cancellation, and require durable governed cancellation state.
- [x] Bind loaded report and manifest identities to the exact approved request
  and serialize rollback compatibility checks with command writers.
- [x] Keep caller-interrupted result commits retryable and reject archive-only
  null encodings that canonical runtime request bytes cannot produce.
- [x] Refresh the recovery scheduler synchronously after active delivery-lease
  renewal and require a durable request-bound marker for quarantine replay.
- [x] Prove same-delivery retries receive independent attempt-scoped human
  authorizations and remain reconstructible by archive verification.
- [x] Revalidate the exact Git ref and retry lease release when replaying a
  completed publication.
- [x] Release authority, quarantine, and only then close a failed validation;
  cleanup failure leaves the Run recoverable.
- [x] Bind recovery to the attempt's recorded scheduler resource and close
  interrupted terminal non-success attempts without rerunning the worker.
- [x] Retry cleanup for succeeded attempts whose Run already reached a failed
  state after a later-stage failure.
- [x] Settle a durable publication conflict before rebuilding a commit from a
  worktree that conflict cleanup may already have quarantined.
- [x] Hold and revalidate the original scheduler lease row through immutable
  delivery-authorization commit.
- [x] Emit audited `replay=true` success evidence for idempotent delivery
  authorization retries.
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
reverse migrations 006, 005, and 004 in that order under the existing migration
barrier, then deploy the authoritative Sprint 04 commit. The automated
`scripts/rehearse_sprint05_rollback.sh` drill starts that exact binary against
the downgraded migration-003 schema before reapplying the current schema. After
any publication history exists, schema downgrade is deliberately refused:
preserve the journal, keep delivery intake disabled, and use forward repair.
Downgrade is also refused before the first migration is reversed when any
`delivery.authorized` event or `authorize_delivery:*` receipt remains, because
the Sprint 04 verifier cannot reconstruct that Sprint 05 authority stream.
Never delete receipts, reset an operator branch, or delete unverified work to
force a rollback.
