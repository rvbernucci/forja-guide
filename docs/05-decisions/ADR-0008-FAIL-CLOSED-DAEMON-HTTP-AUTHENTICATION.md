# ADR-0008: Fail-Closed Daemon HTTP Authentication

Status: Accepted

## Context

The governed MCP surface authenticates principals and separates capabilities,
but the older kernel `/v1` endpoints originally accepted commands without a
credential. They also accepted actor headers and generated a
`system`/`anonymous` identity when those headers were absent. A network caller
could therefore create or transition Runs while assigning its own audit
identity.

The low-level HTTP surface remains useful for kernel testing and administration,
but local deployment is not an authorization boundary. It must preserve the
same fail-closed identity, capability, and repository-scope invariants as the
governed control plane.

## Decision

Every `/v1` request requires exactly one bearer credential. The secret:

- is supplied only through `FORJA_HTTP_BEARER_TOKEN`;
- must contain 32-4096 bytes without whitespace;
- is compared in constant time; and
- is never accepted through a flag, JSON file, URL, or actor header.

Because the daemon currently serves plaintext HTTP, it may bind only to a
numeric loopback IP. The CLI likewise sends a bearer over `http://` only to a
numeric loopback IP and requires `https://` for hostnames and non-loopback IPs.
It refuses redirects rather than forwarding the reusable credential to a new
authority or through a protocol downgrade.

The daemon maps the credential to a principal configured with
`FORJA_HTTP_ACTOR_TYPE` and `FORJA_HTTP_ACTOR_ID`. It authenticates first, then
checks capability and bound tenant/repository scope before parsing or
persisting a request. Reads require `control:read`; generic writes require the
dedicated `legacy_run:write` capability. Caller-supplied actor headers have no
authority and are ignored.

`/healthz`, `/readyz`, and `/version` remain public operational endpoints.
Missing or invalid credentials return `401`; insufficient capability or scope
returns `403`.

## Consequences

Positive:

- anonymous callers cannot mutate or inspect Run state;
- audit identity cannot be spoofed through HTTP headers;
- authentication and authorization failures occur before persistence;
- the CLI and daemon share one explicit secret boundary.

Negative:

- local daemon startup now requires a stable actor ID and bearer secret;
- a static bearer maps to one principal, so deployments needing multiple
  identities must replace the authenticator rather than share the secret.

## Guardrail

Tests must prove rejection of missing, malformed, duplicated, and invalid
credentials; denial for missing capability and wrong scope; absence of
persistence on denial; authenticated read/write behavior; and immunity to actor
header spoofing. Authentication must cover the complete `/v1` namespace before
method/path routing. Process smoke tests must use only environment-delivered
credentials.
