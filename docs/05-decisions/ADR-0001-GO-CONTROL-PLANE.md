# ADR-0001: Go Control Plane

Status: Accepted

## Context

Forja must supervise long-running processes, propagate cancellation, schedule
concurrent work, maintain leases, expose MCP tools, write transactional state,
and export telemetry. The existing product ecosystem may use TypeScript, but
the factory is independent infrastructure.

## Decision

Implement the daemon, scheduler, MCP server, process supervisor, policy engine,
and primary CLI in Go.

Use TypeScript and Python only for adapters where their native ecosystems
provide stronger evidence or tooling, such as the TypeScript Compiler API or
machine-learning experiments.

## Consequences

Positive:

- simple static deployment;
- explicit concurrency and cancellation;
- strong standard library for processes and networking;
- mature PostgreSQL, Prometheus, OpenTelemetry, and MCP ecosystems;
- lower runtime dependency surface.

Negative:

- current TypeScript prototypes require contract parity work;
- language-specific adapters add process boundaries;
- contributors must maintain Go expertise.

## Guardrail

The migration must preserve behavior through language-neutral contracts and
conformance fixtures. Existing prototypes are reference implementations, not
code to translate blindly.

