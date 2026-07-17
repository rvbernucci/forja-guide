# ADR-0006: PostgreSQL Credential Boundary

Status: Accepted

## Context

PostgreSQL recovery commands were previously given a connection URI as a
positional argument. When that URI contained a password, operating-system
process inspection could expose the credential through the child command line.
Forja must support password-bearing and multi-host libpq URIs without printing,
persisting, or forwarding their credentials in process arguments.

Process environments are less broadly visible than command lines on supported
systems, but they are not a secret vault. The same operating-system user,
privileged processes, crash tooling, or an unsafe child environment may still
observe them.

## Decision

Operators provide recovery credentials through `FORJA_DATABASE_URL` or an
inherited libpq credential such as `PGPASSWORD`. Recovery scripts accept no
database URI argument.

Before starting a PostgreSQL client, the connection helper:

- validates a `postgres` or `postgresql` URI without reducing multi-host
  semantics;
- removes an embedded password from URI user information;
- exports that password only as `PGPASSWORD`;
- preserves an inherited `PGPASSWORD` when the URI contains no password;
- unsets the original credential-bearing `FORJA_DATABASE_URL`; and
- passes only a sanitized, password-free URI to `psql`, `pg_dump`, and
  `pg_restore`.

The daemon continues to receive its database URL through the environment and
opens it in-process through pgx. It may not log, relay, or place that value in a
child process argument. Deployment systems should inject credentials from a
secret manager into a dedicated operating-system identity and minimize the
environment inherited by unrelated children.

## Consequences

Positive:

- routine process listings no longer expose PostgreSQL passwords;
- recovery scripts have one explicit credential boundary;
- password-free, password-bearing, Unix-socket, and multi-host URIs retain
  their libpq behavior;
- command history need not contain a positional credential.

Negative:

- credentials remain observable to sufficiently privileged processes;
- the helper requires Python 3 before a PostgreSQL client starts;
- operators must inject the environment safely and avoid diagnostic dumps of
  process environments.

## Guardrail

No Forja script may place a credential-bearing database URI or password in a
PostgreSQL client argument, log line, evidence artifact, or committed file.
