# Worker Operations

Status: Sprint 04 implementation candidate

## One-Shot Execution

Build the worker runner and inspect its flags:

```bash
go build -trimpath -o ./bin/forja-worker ./cmd/forja-worker
./bin/forja-worker -h
```

Execute one canonical task:

```bash
./bin/forja-worker \
  --task /srv/forja/tasks/task.json \
  --result /srv/forja/results/result.json \
  --codex /usr/local/bin/codex
```

Use `-` for stdin or stdout. The task input is capped at 1 MiB. Result files are
written through a mode-`0600` temporary file, synchronized, and atomically
renamed. Structured lifecycle events go to stderr so they cannot corrupt JSON
on stdout.

Exit codes are `0` for success, `3` for a model-declared blocker, `130` for
cancellation, `1` for execution failure, and `2` for invalid CLI or task input.
A classified result is still written when runtime execution fails; malformed
input cannot produce a result.

## Required Runtime

- Go-built `forja-worker` for the host architecture;
- Codex CLI at the configured executable path;
- an existing repository and isolated worktree;
- an evidence directory lexically and physically inside that worktree;
- model authentication supplied through a deployment-owned `CODEX_HOME`.

Never pass Forja control credentials, database URLs, Git tokens, SSH agent
sockets, or unrelated API keys to the worker process. The supervisor uses an
environment allowlist and sets non-interactive Git safeguards. Codex receives
its authentication home, but commands launched by the model receive only the
explicit safe shell-variable allowlist; proxy variables are not forwarded.
This protects the child-command environment, not every same-user readable file.
Use a dedicated disposable worker host with no unrelated secrets until Sprint
12 provides external identity isolation and credential brokerage.

Declared write scopes and the evidence root must be pre-created real
directories. The Sprint 04 supervisor never materializes them; Sprint 05 owns
their race-free creation under an exclusive worktree lease. The supervisor uses
the evidence directory as Codex's primary writable root and passes only
declared scopes as additional writable roots. Do not run the Sprint 04
supervisor against a worktree with another writer; path validation is not a
substitute for the Sprint 05 exclusive lease. Start only from a
clean worktree; a dirty baseline is rejected, and post-run Git paths must be
reported and remain
inside the task's write or evidence scope. Ignored files are not exempt: a
bounded SHA-256 snapshot detects ignored files created, removed, or modified
during the attempt. Reject worktrees whose ignored baseline exceeds 2,048
paths or 64 MiB rather than weakening this boundary.

Sprint 04 supports only `read_scopes: ["."]`; narrower values fail before
launch. Evidence references identify an attempt-created regular file as
`relative/path#sha256=<digest>`. Worktrees using `assume-unchanged` or
`skip-worktree` index flags are rejected.

Do not register another untrusted adapter unless it enforces equivalent OS
write roots. The generic adapter interface is a trusted integration seam, not
an automatic sandbox.

## Cancellation and Budgets

On cancellation or a runtime violation, the supervisor sends `SIGTERM` to the
launched process group, waits the task's grace budget, sends `SIGKILL`, and
applies a second bounded reap deadline. Cleanup still runs when the direct
child exits after `SIGTERM`, so a same-group descendant that ignores it is
killed. Linux workers also receive a parent-death signal. Tests launch real
children, reject residual group members after normal completion, and prove the
launched group is gone after cancellation. After execution, the supervisor
also revalidates that the evidence path is still a directory resolving to a
proper worktree descendant.

Do not treat a process group as hostile-process containment: a descendant can
create a new session. Production deployment must wrap workers in a cgroup,
container, or equivalent job object that can atomically kill every descendant.

If a result has termination reason `worktree_contaminated`, do not retry or
reuse that worktree. Quarantine it for inspection, reconcile the terminal
attempt, and provision a clean replacement. The Sprint 04 supervisor preserves
the bytes for evidence rather than running destructive Git cleanup; Sprint 05
must automate replacement while holding the exclusive worktree lease.

Set inactivity budgets for the selected model behavior: inference that emits
no streaming output is indistinguishable from a hung process at this boundary.
Keep output limits below the 16 MiB contract maximum. Captured output is
canonicalized to valid UTF-8 and its digest covers that exact persisted string;
treat the content as sensitive evidence.

## Restart Procedure

1. Stop admitting new attempts.
2. Acquire the repository scheduler lease with a new owner identity.
3. Call durable attempt reconciliation with that exact lease proof.
4. Confirm every old `queued` or `running` attempt with a dead fence became
   `failed_retryable` and emitted an outbox-backed recovery event.
5. Resume scheduling only after reconciliation commits.

Do not recover by sending signals to a stored PID. PIDs are intentionally not
part of the durable attempt contract.

## Validation

Run the deterministic worker checks:

```bash
make smoke-worker
go test -race ./internal/worker ./cmd/forja-worker
```

Run durable lifecycle, replay, backup/restore, and restart checks against a
disposable PostgreSQL database:

```bash
export FORJA_TEST_DATABASE_URL='postgres:///forja_test?host=/tmp'
make test-integration
```

The integration suite destroys the `forja` schema. Never target a shared or
production database.

## Rollback

No Sprint 04 database migration is required; the existing attempts table
already contains lifecycle timestamps and versions. To roll back the binary:

1. drain and terminate active worker process groups;
2. reconcile their durable attempts with a live scheduler fence;
3. deploy the previous binary;
4. retain immutable lifecycle events and outbox rows for audit.

Previous binaries ignore the additive worker schemas and lifecycle APIs. They
must not be allowed to create new attempts concurrently with the Sprint 04
supervisor during rollback.
