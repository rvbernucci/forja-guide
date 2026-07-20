# ADR-0020: Bound Forja Alpha as a Local-First Vertical

Status: Accepted for Sprints 10-14

## Context

Forja Alpha introduces financial research behavior into a domain-neutral agent
kernel. If financial contracts enter the kernel prematurely, later verticals
inherit accidental coupling. The AMD Track 2 profile also requires private
local inference, while the Alpha experience handles prompts and research state
that must not silently cross a remote model boundary.

## Decision

1. Keep financial contracts, planning, tools, and presentation behavior under
   `internal/alpha` until a second vertical proves a reusable kernel contract.
2. Reuse the governed Forja execution, evidence, retrieval, memory,
   observability, and authorization boundaries without making the kernel aware
   of companies, filings, factors, holdings, or investment-research outputs.
3. Require model and embedding endpoints configured for the competition
   profile to use `localhost` or an explicit loopback IP. Reject remote core
   inference during configuration rather than silently degrading to it.
4. Keep the Alpha HTTP listener on loopback by default. Non-loopback deployment
   is unsupported until an authenticated, authorized, encrypted ingress owns
   that exposure.
5. Treat endpoint configuration as intent, not health evidence. Readiness must
   distinguish configured-but-unprobed endpoints from verified local runtime.
6. Never manufacture a financial answer when data, retrieval, analytical, or
   inference adapters are unavailable. Return an explicit capability state and
   bounded evidence plan instead.

## Consequences

- The public kernel remains reusable for non-financial projects.
- Alpha can evolve quickly without weakening canonical Forja authority.
- Private prompts do not leave the host through a core model or embedding
  fallback.
- Container and remote-host deployments need a governed ingress before they
  may expose the Alpha HTTP interface beyond loopback.
- Sprint 10 must add endpoint identity and health probes before the interface
  can claim core inference is ready.
