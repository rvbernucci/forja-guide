# VPS Retrieval Runtime Receipt

Status: Sprint 09 pre-closure infrastructure evidence, captured on 2026-07-19.
This receipt is sanitised operational evidence. It is not a private retrieval
evaluation, a deployment approval, or Sprint 09 closure evidence.

## Verified Host Baseline

The governed Hostinger Builder Plane was checked only through short-lived,
role-scoped wrappers. No container inspection, environment dump, database
query, credential read, or secret copy was performed.

| Dependency | Result | Boundary verified |
| --- | --- | --- |
| Qdrant | Running and ready | REST and gRPC are loopback-only; no public port is open |
| Neo4j | Running and ready | HTTP and Bolt are loopback-only; no public port is open |
| Access factory | Working | Separate SSH keys for `mariana-approver`, `mariana-access-admin`, `mariana-infra-admin`, and `mariana-codex` were verified |
| Grants | Closed after verification | No active grant remained after the checks |

The Qdrant status wrapper reported its collection probe as healthy. Neo4j's
operator status also reported a ready local service. Both results were emitted
without secrets or logs.

## Configuration Discovery

The `mariana-codex` execution account was checked for configuration *presence*
only. At the time of this receipt it had no Forja workload unit, no Go binary
on its non-interactive `PATH`, no AWS tooling or configuration files, and no
`AWS_*` or `FORJA_*` environment variable names. Their values were never read.

Therefore the host infrastructure is available, but the governed retrieval
workload is not yet deployed or configured. Running `forja-retrieval preflight`
there would correctly fail closed rather than proving readiness.

## CLI Executor Prepared

The public `forja-retrieval` source and a statically linked `linux/amd64`
binary were staged in the isolated `mariana-codex` workspace from commit
`bebdbf9`. The uploaded binary checksum matched the locally built artifact.
This is an operator CLI, not a long-running service and not a deployed
retrieval workload.

An invocation with no runtime configuration exited non-zero, named the missing
required configuration keys, and wrote no preflight receipt. That proves the
binary starts on the VPS and fails closed before it can contact any dependency.
It does not prove a valid PostgreSQL, Qdrant, S3, or Bedrock connection.

## Wrapper Observation

`mariana-validate-qdrant` returned a local-file-path validation failure when
called through its installed system wrapper. The Qdrant status wrapper was
healthy, so this is an operator-wrapper working-directory defect rather than
evidence of a Qdrant outage. Do not treat this validator as Sprint 09 evidence
until its system-wrapper invocation is repaired and independently rerun.

## Activation Prerequisites

Before preflight or private baseline capture:

1. Decide whether the first governed workload stays an operator CLI or becomes
   a dedicated service. The CLI executor is already staged; a service needs a
   separate systemd deployment contract.
2. Inject the configuration named in
   [the deployment procedure](RETRIEVAL_RUNTIME_DEPLOYMENT.md) through a
   private runtime boundary.
3. Give the workload an AWS role or other standard AWS SDK credential-chain
   source with only the required Bedrock embedding permission.
4. Supply PostgreSQL and, if non-loopback, Qdrant credentials through a secret
   manager. Never copy credentials from Coolify or another workload.
5. For a service deployment, create a reproducible build/runtime contract. For
   the staged CLI, the static binary avoids a Go installation on the VPS.
6. Run the bounded preflight and write its mode-`0600` receipt outside Git.

A successful workload preflight and private four-baseline capture are Sprint 10
activation and quality evidence. They are deliberately not Sprint 09 closure
requirements and cannot be inferred from this infrastructure observation.
