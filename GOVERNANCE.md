# Governance

## Roles

| Role | Responsibility |
| --- | --- |
| Maintainer | Accepts roadmap, architecture, security, and release decisions |
| Author | Produces an assigned code, document, schema, or test artifact |
| Validator | Reviews behavior independently from the author |
| Operator | Runs approved deployments and records operational evidence |
| Contributor | Proposes changes through issues and pull requests |

One person or agent may hold multiple roles across the project, but the same
change should not rely on the author as its only validator.

## Decision Process

- Reversible implementation decisions may be accepted through pull requests.
- Changes to protocol, persistence, security, authority, or trust boundaries
  require an ADR.
- Security decisions may block a release regardless of roadmap priority.
- Generated evidence cannot change policy.

## Architecture Decision Records

ADRs live in `docs/05-decisions/`.

Accepted ADRs are immutable. A new ADR may supersede an earlier decision while
preserving its historical context.

## Release Readiness

A release requires:

- all mandatory tests passing;
- schemas and migrations validated;
- no unresolved critical security findings;
- rollback instructions;
- image and dependency provenance;
- evidence for restart, timeout, cancellation, and partial failure behavior;
- updated public documentation.

