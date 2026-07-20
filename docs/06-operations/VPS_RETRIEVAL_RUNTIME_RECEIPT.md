# VPS Retrieval Runtime Receipt

Status: Historical sanitized operator note from 2026-07-19, excluded from the
Sprint 09 closure evidence set because no immutable underlying command receipt
is committed. It is not a private retrieval evaluation or deployment approval.

## Reported Host Baseline

The original operator note reported checks through short-lived, role-scoped
wrappers and reported that no container inspection, environment dump, database
query, credential read, or secret copy was performed. These observations are
not independently reviewable from this repository.

| Dependency | Operator-reported result | Reported boundary |
| --- | --- | --- |
| Qdrant | Historically reported running and ready | REST and gRPC were reported as loopback-only; no public port was reported open |
| Neo4j | Historically reported running and ready | HTTP and Bolt were reported as loopback-only; no public port was reported open |
| Access factory | Historically reported working | Separate SSH keys for `mariana-approver`, `mariana-access-admin`, `mariana-infra-admin`, and `mariana-codex` were reported as verified |
| Grants | Historically reported closed after verification | The note reported that no active grant remained after the checks |

The note recorded healthy Qdrant and Neo4j wrapper summaries without retaining
their command output. They are historical context only.

## Configuration Discovery

The note reports that the `mariana-codex` execution account was checked for
configuration *presence* only. At that time it had no Forja workload unit, no Go binary
on its non-interactive `PATH`, no AWS tooling or configuration files, and no
`AWS_*` or `FORJA_*` environment variable names. Their values were never read.

The only safe present-tense conclusion is that no deployment readiness is
proven. Running `forja-retrieval preflight` without its required configuration
must fail closed rather than proving readiness.

## CLI Executor Prepared

The note reports that the public `forja-retrieval` source and a statically
linked `linux/amd64` binary were staged in an isolated workspace from commit
`bebdbf9`. The uploaded binary checksum matched the locally built artifact.
This is an operator CLI, not a long-running service and not a deployed
retrieval workload.

The reported invocation with no runtime configuration exited non-zero, but no
immutable command receipt is committed. Independently reviewable source tests
cover that fail-closed behavior; this note does not prove a valid PostgreSQL,
Qdrant, S3, or Bedrock connection.

## Wrapper Observation

The note reports that `mariana-validate-qdrant` returned a local-file-path
validation failure through its installed system wrapper. Treat the suspected
working-directory defect as unresolved, not as evidence of either Qdrant health
or outage, until independently rerun with an immutable receipt.

## Activation Prerequisites

Before preflight or private baseline capture:

1. Decide whether the first governed workload is an operator CLI or a dedicated
   service. Do not assume the historically reported staged binary still exists;
   a service needs a separate systemd deployment contract.
2. Inject the configuration named in
   [the deployment procedure](RETRIEVAL_RUNTIME_DEPLOYMENT.md) through a
   private runtime boundary.
3. Give the workload an AWS role or other standard AWS SDK credential-chain
   source with only the required Bedrock embedding permission.
4. Supply PostgreSQL and, if non-loopback, Qdrant credentials through a secret
   manager. Never copy credentials from Coolify or another workload.
5. For a service deployment, create a reproducible build/runtime contract. A
   newly verified static CLI binary can avoid a Go installation on the VPS.
6. Run the bounded preflight and write its mode-`0600` receipt outside Git.

A successful workload preflight and private four-baseline capture are Sprint 10
activation and quality evidence. They are deliberately not Sprint 09 closure
requirements and cannot be inferred from this infrastructure observation.
