# Isolated Delivery Contract

Status: Contract implemented; Sprint 05 runtime in progress

## Boundary

The delivery boundary converts one approved request into one namespaced Git
reference and one content-addressed receipt. It separates five authorities:

| Authority | Owner |
| --- | --- |
| approve objective, identities, scopes, budgets, and validator IDs | control plane |
| acquire worktree, file, and artifact lease set | delivery service |
| edit pre-created writable roots | bounded worker |
| create commit, validate, and hash evidence | delivery service |
| independently validate a clean checkout | assigned validator |

No model-authored report can grant another authority.

The canonical schemas are:

- [`delivery-request.schema.json`](../../schemas/delivery-request.schema.json);
- [`validation-report.schema.json`](../../schemas/validation-report.schema.json);
- [`evidence-manifest.schema.json`](../../schemas/evidence-manifest.schema.json);
- [`delivery-receipt.schema.json`](../../schemas/delivery-receipt.schema.json).

All four use schema version `1.0`, reject unknown fields, and require semantic
validation in addition to JSON Schema. Publication validation is joint: the
approved request, canonical validation report bytes, evidence manifest bytes,
and receipt are checked as one authority proof rather than independently.

## Request Semantics

The control plane provides an exact base commit and a publication ref beneath
`refs/forja/deliveries/`, plus the nullable expected previous commit for
compare-and-swap publication. It supplies repository and worktree-root paths, but
the delivery service derives the attempt worktree path. Repository-relative
read, write, artifact, and evidence scopes must be canonical non-overlapping
paths without empty, absolute, dot, or parent-traversal components.

Repository and worktree roots must be clean, non-root absolute paths and must
not contain one another. The attempt worktree is derived beneath the approved
worktree root from the delivery identity; callers cannot provide its path.

`author_id` and `validator_id` must differ. Validator IDs address trusted
registry entries; requests never contain an executable or shell string. The
runtime requires full-worktree read scope while the Codex adapter has that
limitation, but it preserves the narrower public contract for adapters that can
prove stronger isolation later.

The supervisor binds each `(delivery_id, attempt_id)` to the SHA-256 digest of
the canonical request before creating or inspecting a worktree. A replay must
present byte-equivalent canonical authority; changing the repository, base
commit, scopes, identities, or any other request field fails closed instead of
reusing the existing attempt path.

## Lease Set

Before creating a worktree, the service atomically acquires:

- one `worktree` lease for the delivery attempt;
- one `file` lease for each normalized write scope and its non-root ancestors;
- one `artifact` lease for each artifact scope and its non-root ancestors.

The lease-set ID identifies the attempt and is deliberately independent from
the worktree resource ID, which identifies the delivery. A retry therefore
uses a fresh attempt lease-set ID while reacquiring the same delivery worktree
fence with a higher token. The sorted set is immutable for the attempt. Partial
acquisition is invalid.
Each protected commit, validation, receipt, and publication operation checks
the exact live owner and fencing token. Expiry or replacement fails closed.
Receipt worktree fences use the delivery ID as their resource ID. File and
artifact fence IDs are canonical repository-relative scopes.

Hierarchical ancestor leasing is intentionally conservative. It prevents
`internal/worker` and `internal/worker/file.go` from being written by different
authors, even though PostgreSQL rows are keyed by exact resource ID. File and
artifact scopes may be siblings beneath a shared ancestor within one atomic
set; scopes that are equal or ancestor/descendant across those kinds remain
invalid.

## Git Identity

The worktree starts detached at one complete 40-character commit. The service
verifies its Git common directory, exact `HEAD`, clean state, index flags, and
physical location before worker launch. The worker does not receive authority
to run publication operations; the supervisor creates the result commit after
scope validation.

The attempt path is derived as
`<worktree-root>/<delivery-id>/<attempt-id>`. A repeated prepare may reuse that
path only when repository identity, detached `HEAD`, base commit, cleanliness,
ignored-file absence, and index flags all still match. A retry uses a new
attempt ID and therefore a new path at the original base commit. An attempt
identity with an existing quarantine destination is retired and cannot be
prepared again. A rooted, atomic per-attempt lifecycle lock serializes prepare,
inspect, and quarantine across manager instances; an abandoned lock fails
closed for later lease-aware reconciliation. Repository Git reads have a
two-second deadline and mutations a 30-second deadline. Interrupted mutations
preserve any reachable bytes under quarantine, write an explicit
`reconciliation-required` marker, and return failure because filesystem
position cannot prove Git administrative registration. The identity remains
non-reusable. Hooks are disabled, and effective local or worktree-scoped
clean, smudge, or process filters are rejected before checkout because they
could execute host commands outside the worker sandbox.

Write and artifact directories are created through a rooted filesystem handle
while the lease set is live. Their logical and resolved positions must match;
scope or namespace symlinks fail before worker launch. Dirty, clean-retired, or
unverifiable paths can be moved to a non-reusable quarantine namespace without
deleting bytes; registered worktrees move through Git so their administrative
metadata remains inspectable only when their Git common directory matches the
request's authorized repository. Otherwise, rooted quarantine preserves bytes
without mutating external Git metadata. Quarantine also verifies the immutable
request digest before touching the attempt path. An interrupted Git move never
reports successful quarantine until its administrative metadata is reconciled.
Physical deletion after worker
exposure remains pending a joint live-lease and process-quiescence proof. Only a fresh checkout
whose preparation failed before exposure may be removed immediately.

Canonical delivery identity contains:

- exact base and result commits;
- result tree object ID;
- byte-sorted changed paths;
- SHA-256 of `git diff --binary --full-index <base> <result>`;
- validation and evidence references with SHA-256 digests.

The service publishes with compare-and-swap ref semantics. A missing target is
created only if it is still missing; an existing target advances only from the
approved previous object ID.

## Validation

Built-in checks always run before configured validators:

1. exact changed paths and declared scopes;
2. symlink, ignored-file, and hidden-index safety;
3. secret-pattern scanning;
4. JSON and registered schema validity;
5. generated-file policy.

Their stable receipt IDs are `scope-boundary`, `filesystem-safety`,
`secret-scan`, `schema-validation`, and `generated-file-policy`. All five must
be present as passing `built_in` checks; another check kind cannot impersonate
them.

Configured format and test validators are trusted argv arrays stored in the
runtime registry. They have wall-clock and output budgets and receive a
sanitized environment. Reports contain hashes and bounded details, not raw
unbounded output.

Independent validation checks out the result commit into a new clean worktree,
recomputes the patch identity, and reruns the required registry entries under a
validator identity different from the author. A passing author-side check
cannot substitute for this lane.

## Failure and Retry

- A clean transient failure may retry from the original base in a new
  worktree and with a new attempt ID.
- Scope disagreement, stale fences, self-validation, or publication ref
  conflicts are terminal for the attempt.
- A dirty failed or unverifiable worktree is quarantined and never reset or
  reused.
- Clean worktrees may be removed only after their receipt or failure record is
  durable.
- A receipt replay must be byte-equivalent; the same delivery ID cannot publish
  different content.

## Receipt Authority

A delivery receipt exists only for a published result. It does not mean the
default branch merged the commit. Authority is limited to the namespaced ref,
the recorded lease fences, and the exact hashes in the receipt. Branch merge
and release policy remain separate governed decisions.

The receipt is authoritative only when its request and passing independent
report agree on delivery, commits, patch, identities, and publication ref; its
content digests match the canonical report and manifest bytes; every changed
path is within the approved write scopes; every evidence reference is within
the evidence scope; and its sorted lease fences exactly equal the hierarchical
lease set derived from the approved request.
The canonical evidence manifest inventories byte-sorted paths, hashes, sizes,
and media types. Every entry must remain inside the approved evidence scope,
the manifest cannot include itself, and it must include the exact canonical
validation report referenced by the receipt.
Every mechanical validator ID approved by the request must also appear as a
passing check in the independent report. The receipt's previous publication
commit must exactly match the request, including `null` when creating a ref.
