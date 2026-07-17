# ADR-0004: Standard MCP Boundary

Status: Accepted

## Context

Forja needs a conversational control surface for a Co-architect while
preserving portability across Codex and other compatible clients.

## Decision

Build a Forja-owned MCP server using the standard Model Context Protocol and its
official Go SDK.

Forja defines its own tools, resources, schemas, authorization, and internal
behavior. It does not create a proprietary replacement for the wire protocol.

## Consequences

Positive:

- client interoperability;
- established tool and resource semantics;
- less protocol maintenance;
- standard local stdio and remote transport options.

Negative:

- protocol evolution requires compatibility management;
- MCP remains an interaction boundary, so internal scheduling still requires
  dedicated contracts.

## Guardrail

MCP calls create commands and decisions. They do not directly mutate worker,
Git, graph, vector, or deployment state.

## Implementation

Sprint 03 implements the decision with `github.com/modelcontextprotocol/go-sdk`
v1.6.1, a stdio server, eight typed tools, compatibility-pinned schemas,
principal capability and repository-scope checks, durable pending decisions,
and correlated audits. Streamable HTTP remains undeployed behind a required
bearer-verification boundary.
