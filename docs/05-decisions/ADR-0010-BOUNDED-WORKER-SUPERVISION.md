# ADR-0010: Bounded Worker Supervision

Status: Accepted for Sprint 04

## Context

Forja must execute coding agents without granting them scheduler, approval, or
database authority. A model may hang, fork children, emit unbounded output,
reuse host configuration, or terminate between an operating-system side effect
and a durable state update. Treating `exec.Command` as the execution contract
would make those failure modes implicit and adapter-specific.

## Decision

The control plane owns a generic Go supervisor. Adapters produce immutable
process invocations; they do not start processes or mutate run state.

Every invocation is governed by a versioned worker task contract and has:

- a dedicated process group;
- a sanitized, allowlisted environment;
- an isolated temporary home and explicit worktree;
- wall-clock, inactivity, output, cancellation, and retry budgets;
- structured lifecycle events that never include raw stdout or stderr;
- bounded stdout and stderr capture with content hashes;
- a schema-constrained final worker report;
- deterministic result classification;
- durable attempt transitions and restart reconciliation.

The initial Codex adapter uses `codex exec` in ephemeral mode, ignores user
configuration, forces `approval_policy=never`, selects the `workspace-write`
sandbox, disables sandbox command network access, supplies the objective over
stdin, and constrains the final response with the canonical worker-report
schema. Authentication may be read by the Codex process from a deployment-owned
`CODEX_HOME`. The evidence directory is the primary writable root and each
declared write-scope directory is exposed separately with `--add-dir`; the
repository remains readable by absolute path. Final Git and ignored-file
inspection is a second scope check rather than the only write boundary.

An explicit child-command environment allowlist prevents tools from inheriting
the authentication location, credential variables, or proxy settings. Forja
control credentials, database URLs, Git credentials, and caller-provided
environment entries are never forwarded. This environment rule does not make
unrelated same-user host files confidential because `workspace-write` permits
broad reads. Production therefore requires credentials unavailable to the
worker OS identity, delivered through a broker or equivalent external
containment. Until then, workers run only on a dedicated disposable host with
no unrelated readable secrets.
The schema and report targets live in a supervisor-private temporary directory,
outside the model-writable worktree, preventing report-path symlink replacement.

Cancellation sends `SIGTERM` to the launched process group, waits for a bounded
grace period, then sends `SIGKILL` and applies a second bounded reap deadline.
Every post-start termination path inspects and cleans residual members even if
the direct child exited cooperatively; normal completion rejects any residual
group as a process failure, and post-`SIGKILL` cleanup must prove the group is
gone within a second bounded deadline. The evidence directory is physically
revalidated after execution before a report can succeed. On Linux the
direct child additionally uses a parent-death signal. A process group cannot
contain a hostile descendant that deliberately creates a new session, so a
cgroup, container, or equivalent job boundary is mandatory before production
execution of untrusted adapters. A restarted supervisor does not trust an old PID: active
attempts without a current fenced owner are reconciled to an explicit
retryable failure before new work is scheduled.

## Consequences

- Worker adapters remain replaceable and mechanically testable.
- Model output cannot approve work or directly update scheduler state.
- A process can edit only the provided worktree sandbox; actual Git status and
  a bounded before/after content snapshot of ignored files are checked against
  declared write scopes before success. Codex write roots enforce these scopes
  during execution. A future untrusted adapter must provide an equivalent OS
  boundary before registration. Sprint 05 adds worktree creation, validation
  pipelines, and delivery leases.
- Network and filesystem isolation remain process-sandbox guarantees, not
  prompt instructions.
- Sprint 04 accepts only full-worktree read scope. Narrower read isolation,
  hostile-descendant containment, same-user host confidentiality, and
  credential brokerage remain fail-closed production gates.
- Raw process output is bounded, canonicalized to valid UTF-8, and hashed from
  that exact persisted representation. It remains sensitive evidence and must
  not be copied into logs.
- An attempt without a validated completed or blocked report that leaves
  observed or unverifiable worktree changes becomes terminal
  `worktree_contaminated`. Sprint 04 preserves the worktree for quarantine and
  forbids retry; Sprint 05 owns leased disposal and replacement. A validated
  blocked report may preserve exact, in-scope changes for human inspection.
- Higher-risk tasks may later replace the process launcher with a container or
  stronger sandbox without changing public task/result contracts.

## Rejected Alternatives

- Let each adapter manage `exec.Cmd`: cancellation and budgets would diverge.
- Pass the parent environment through unchanged: workers would inherit control
  credentials and unrelated secrets.
- Use a shell command string: quoting and injection would become part of the
  trust boundary.
- Recover workers by PID alone: PID reuse makes that evidence unsafe.
