# Worker Execution Contract

Status: Implemented candidate in Sprint 04

## Boundary

The worker boundary converts one authorized task into one bounded process and
one canonical result. Model output has no scheduler, approval, database, Git
publication, or MCP control authority.

```text
worker-task.json
  -> strict schema and semantic validation
  -> adapter builds argv plus stdin
  -> sanitized environment and private supervisor files
  -> independent process group with runtime budgets
  -> schema-constrained worker report
  -> supervisor-authored worker-result.json
```

The canonical schemas are:

- [`worker-task.schema.json`](../../schemas/worker-task.schema.json);
- [`worker-report.schema.json`](../../schemas/worker-report.schema.json);
- [`worker-result.schema.json`](../../schemas/worker-result.schema.json).

All three currently use schema version `1.0`. Unknown fields and trailing JSON
documents are rejected.

## Task Envelope

A task binds objective, role, repository, worktree, read and write scopes,
evidence location, attempt ordinal, optional model, and budgets. Paths are
absolute at the process boundary; declared scopes are clean repository-relative
paths. Before launch, the supervisor resolves both Git identities and requires
the worktree root to share the declared repository's canonical common Git
directory. The repository, worktree, and evidence root entries themselves must
be real directories rather than symlinks; symlinks in operating-system ancestor
paths remain compatible. An unrelated directory cannot acquire worker sandbox authority.
Sprint 04 accepts only `read_scopes: ["."]`: the current Codex sandbox cannot
enforce narrower confidentiality boundaries. A narrower scope is rejected
rather than represented as security. Write scopes are mechanically enforced.
Each write scope and the evidence root must already exist as a real directory.
The supervisor never creates them, so validation cannot mutate a path through a
symlink or check-to-create race. Non-directories and paths that traverse
symlinks away from their declared repository location are rejected. Sprint 05
creates these directories while holding the worktree's exclusive delivery
lease. Until then, the caller must provide an exclusively owned worktree;
Sprint 04 path validation does not claim to exclude an external concurrent
writer.

`max_retries` counts retries after the first attempt. Therefore an
`attempt_ordinal` greater than `max_retries + 1` is rejected before process
start.

The supervisor enforces:

| Budget | Enforcement |
| --- | --- |
| `wall_clock_ms` | Terminates the process group after total elapsed time |
| `inactivity_ms` | Terminates when neither output stream makes progress |
| `max_output_bytes` | Bounds combined captured stdout and stderr |
| `cancellation_grace_ms` | Bounds `SIGTERM` grace before `SIGKILL` |
| `max_tokens` | Rejects a completed result whose observed usage exceeds the limit |
| `max_commands` | Rejects a completed result whose observed tool calls exceed the limit |

Lifecycle event writes have a 500 ms delivery deadline, further capped by the
remaining wall budget after process start. Output telemetry is
emitted asynchronously so a blocked sink cannot stop cancellation or runtime
timers; delivery failure becomes retryable `telemetry_failure`, not a false
user cancellation.

## Codex Adapter

The initial adapter executes `codex exec` without a shell command string. The
objective is supplied only over stdin. The invocation forces ephemeral mode,
ignores user configuration, sets `approval_policy=never`, uses the
`workspace-write` sandbox, disables sandbox command network access, and asks
Codex CLI to validate its final message against the embedded report schema.
The evidence directory is the primary writable workspace and each declared
write scope is an explicit additional writable root. The model reads the
repository by its absolute path. This blocks an out-of-scope edit-and-restore
sequence that would otherwise disappear from the final diff.

The report and schema files live in a supervisor-private temporary home, not in
the model-writable worktree. A deployment-owned `CODEX_HOME` may provide model
authentication to the Codex process, but an explicit shell environment policy
removes that path and every credential variable from model-launched commands.
Forja, database, Git, SSH-agent, API-token, and arbitrary caller environment
entries are not inherited. Proxy variables are also dropped because proxy URLs
may contain credentials. Environment filtering is not filesystem
confidentiality: Sprint 04 workers require a dedicated disposable host without
unrelated same-user secrets until external isolation and credential brokerage
are implemented.

The supervisor requires a clean worktree, inspects the real post-run Git status,
and rejects omitted or out-of-scope changes. Because Git status omits ignored
files, the supervisor also compares bounded SHA-256 snapshots of ignored files
before and after execution. The snapshot admits at most 2,048 paths, 16 MiB of
path-list output, and 64 MiB of inspected content; a larger baseline is rejected
before launch. Sprint 05 adds worktree creation, delivery leases, validation
pipelines, and controlled publication; Sprint 04
cannot publish a change.

Evidence references use `relative/path#sha256=<64 lowercase hex>` and are
accepted only when the attempt changed that regular file beneath a proper
evidence subdirectory and its content matches the hash. The evidence directory
cannot be the worktree root. Hidden Git index flags such as `assume-unchanged`
and `skip-worktree` are rejected before launch.

## Result Semantics

The supervisor, not the model, authors `worker-result.json`. State coupling is
enforced in the JSON Schema:

| Status | Retryable | Reason and report |
| --- | --- | --- |
| `succeeded` | no | `completed`; completed report required |
| `blocked` | no | `worker_blocked`; blocked report required |
| `failed_retryable` | yes | transient process, timeout, startup, or report failure; no report |
| `failed_terminal` | no | output, process, report, or budget rejection; no report |
| `cancelled` | no | explicit cancellation; no report |

Any attempt without a validated completed or blocked report that changed the
worktree, changed an ignored file, or prevented post-run cleanliness inspection
is reclassified as terminal `worktree_contaminated`. Its report and evidence
references are discarded. A validated blocked report may retain exact,
in-scope changes for human inspection. The caller must quarantine and replace a
contaminated worktree; Sprint 04 never performs destructive cleanup, and Sprint
05 owns automated disposal under an exclusive lease.

Captured stdout and stderr are size-bounded sensitive evidence. Invalid byte
sequences are replaced with U+FFFD, and each SHA-256 digest is computed over
the exact canonical valid-UTF-8 string persisted in the result. Structured
lifecycle events contain stream names and byte counts but never raw output.

## Durable Attempt Lifecycle

PostgreSQL persists `queued -> running -> terminal` under the exact live
scheduler lease and fencing token. Each transition atomically updates the
attempt, appends an immutable event, creates its outbox row, and stores an
idempotency receipt.

After scheduler restart, `ReconcileAbandonedAttempts` marks queued or running
attempts from dead fences as `failed_retryable`. Recovery uses durable lease
identity and aggregate version; it never trusts or signals a historical PID.
The canonical PostgreSQL verifier replays attempt streams and compares them to
the current rows.

Raw stdout and stderr are deliberately excluded from canonical events. The
finish event records only classification, timestamps, exit code, usage,
evidence references, output hashes, and the canonical SHA-256 of the complete
worker report. Completion persistence rejects future finish times and duration
values that do not match the reported interval, then recomputes both output
hashes before committing them. The report hash also participates in the
idempotency request hash, so one completion key cannot replay altered report
content.
