# Forja

Forja is an open architecture and implementation roadmap for a governed
multi-agent software factory.

It is designed around one principle:

> Agents may propose and execute work, but deterministic contracts decide what
> is authorized, valid, durable, and complete.

## Status

This repository includes the closed **Sprint 06 fail-soft observability plane**
and the in-progress **Sprint 07 artifacts and governed memory candidate** alongside the public
architecture and roadmap. Sprint state is recorded by the mutually exclusive
candidate or receipt in [`docs/evidence`](docs/evidence/); only an authoritative
close receipt closes a Sprint and authorizes its successor. It is not yet a
production-ready multi-agent runtime.

The implemented kernel provides `forjad`, `forja`, canonical contract
validation, a deterministic run state machine, PostgreSQL-backed aggregates and
events, command idempotency, fenced leases, a transactional outbox, projection
replay, repository-scoped authority, semantic schema readiness, backup/restore
tooling, structured redacted logs, graceful shutdown, and reproducible Linux
builds. Its legacy kernel HTTP surface is bearer-authenticated, scope-bound,
and derives audit identity only from server configuration. The official Go MCP
SDK powers an authenticated stdio server with eight
typed, audited tools for Sprint planning, submission, decisions, inspection,
cancellation, and resumption. The Go worker runner now executes Codex CLI in an
independent process group with sanitized environment, sandbox write roots
derived from declared task scopes, bounded runtime and output,
schema-constrained reports, deterministic result classification, and fenced
PostgreSQL attempt recovery. The isolated-delivery library now creates
supervisor-owned commits, performs mechanical and independent clean-checkout
validation, persists content-addressed evidence, and publishes only a
namespaced Git ref through a PostgreSQL-journaled compare-and-swap protocol.
`internal/execution` now composes an approved queued Run, its exact fenced
durable attempt, the real worker supervisor, isolated Git delivery,
independent validation, and receipt-backed publication. Before mutation, an
independent human approves the complete delivery envelope; its immutable event
and SHA-256 bind the base commit, scopes, budgets, identities, validators, and
publication target. A dual scheduler/delivery lease heartbeat cancels work on
lost authority, while persisted evidence and the publication journal support
bounded restart recovery without database editing. Every delivery attempt has
its own immutable human authorization, and completed recovery re-observes the
exact Git ref while retrying idempotent lease release. The publication fence
rejects contradictory Run transitions from journal
preparation until the published delivery closes as `completed`. Its full
approval-to-publication path is exercised against PostgreSQL and real child
processes. A public scheduler/MCP delivery command remains outside Sprint 05.
Sprint 04 is not a production
confidentiality boundary: workers require a dedicated disposable host until
separate-identity containment and credential brokerage close the documented
Sprint 12 gate.

The closed Sprint 06 plane adds W3C-propagated OpenTelemetry traces across
MCP, HTTP, scheduler, worker, validation, delivery, and PostgreSQL boundaries;
closed-label Prometheus metrics; context-derived trace IDs in redacted JSON
logs; and a read-only operational collector for stuck work, leases, outbox,
projection lag, approvals, and crash loops. A pinned local Prometheus, Loki,
Alloy, Tempo, and Grafana profile provides alerts and a runtime dashboard.
Telemetry remains disposable and cannot authorize or alter canonical state.

The Sprint 07 candidate adds content-addressed S3-compatible storage behind an
operator-bound adapter, a PostgreSQL-journaled publication and reconciliation
saga, immutable evidence manifests, conversations and message references,
human- or policy-governed memory promotion, and tombstone-before-purge
retention. Bodies are fully re-read and SHA-256 verified before canonical
activation. A two-plane restore drill recovered a three-object evidence bundle
into a new PostgreSQL database and a separate MinIO data directory, then
revalidated the complete bodies, schema, events, outbox, and command receipts.
Sprint 07 remains non-authoritative until its independent review and closure
receipt are published.

Current planning release: [`v0.1.0`](https://github.com/rvbernucci/forja-guide/releases/tag/v0.1.0).

## Architecture

```mermaid
flowchart LR
    U["Human + Co-architect"] --> M["Forja MCP Server"]
    M --> C["Go Control Plane"]
    C --> P["PostgreSQL + Outbox"]
    C --> W["Codex CLI Worker Pool"]
    W --> G["Isolated Git Worktrees"]
    G --> V["Mechanical + Independent Validation"]
    V --> E["Evidence and Artifacts"]

    C --> B["Context Broker"]
    B --> Q["Qdrant Candidate Discovery"]
    B --> N["Neo4j Path Traversal"]
    B --> S["Canonical Source Resolver"]

    C --> O["Prometheus / Loki / Grafana"]
```

## Data Responsibilities

| System | Responsibility |
| --- | --- |
| PostgreSQL | Transactional truth, runs, approvals, events, leases, memory metadata, and projection state |
| Object storage | Large immutable artifacts, transcripts, patches, reports, and evidence bundles |
| Qdrant | Semantic and lexical candidate discovery |
| Neo4j | Proven relationships, lineage, impact analysis, and bounded graph paths |
| Git | Versioned source code and documentation truth |
| Prometheus, Loki, Grafana | Metrics, logs, traces, and operational visibility |

Qdrant discovers candidates. Neo4j connects entities. Deterministic extractors,
source code, schemas, tests, and runtime receipts establish authority.

## Repository Map

| Path | Purpose |
| --- | --- |
| [`docs/01-vision`](docs/01-vision/) | Product vision, principles, and scope |
| [`docs/02-architecture`](docs/02-architecture/) | System, data, context, runtime, security, and observability architecture |
| [`docs/03-contracts`](docs/03-contracts/) | Contract model and schema guidance |
| [`docs/04-roadmap`](docs/04-roadmap/) | Master plan and Sprint checklists |
| [`docs/05-decisions`](docs/05-decisions/) | Architecture Decision Records |
| [`docs/06-operations`](docs/06-operations/) | Development and operating procedures |
| [`docs/07-evaluations`](docs/07-evaluations/) | Quality, safety, retrieval, and resilience evaluation strategy |
| [`schemas`](schemas/) | Language-neutral JSON Schema contracts |
| [`cmd/forjad`](cmd/forjad/) | Experimental Go daemon |
| [`cmd/forja`](cmd/forja/) | Experimental command-line client |
| [`cmd/forja-mcp`](cmd/forja-mcp/) | Governed MCP stdio control surface |
| [`cmd/forja-worker`](cmd/forja-worker/) | Bounded one-shot Codex worker runner |
| [`internal/execution`](internal/execution/) | Approved Run-to-worker-to-publication orchestration |
| [`internal/delivery`](internal/delivery/) | Isolated worktrees, deterministic commits, validation, evidence, and controlled publication |
| [`internal/observability`](internal/observability/) | Fail-soft traces, bounded metrics, stable failure taxonomy, and operational state collector |
| [`internal/knowledge`](internal/knowledge/) | Governed artifact publication, reconciliation, and retention orchestration |
| [`internal/objectstore`](internal/objectstore/) | Conditional content-addressed S3 publication and full-body verification |
| [`deploy/observability`](deploy/observability/) | Version-pinned local Prometheus, Loki, Alloy, Tempo, and Grafana stack |

See [CHANGELOG.md](CHANGELOG.md) for public release history.

## MCP Quick Start

```bash
go build -trimpath -o "$HOME/.local/bin/forja-mcp" ./cmd/forja-mcp
codex mcp add forja \
  --env FORJA_MCP_ACTOR_ID=codex-co-architect \
  -- "$HOME/.local/bin/forja-mcp"
```

Add `FORJA_DATABASE_URL` through an approved secret boundary for durable state.
Without it, each MCP process uses explicit ephemeral state. See the [MCP
control API](docs/03-contracts/MCP_CONTROL_API.md).

The default `agent` principal may plan, inspect, submit, and cancel work, but it
cannot approve decisions or resume execution. Those capabilities require a
separately authenticated `human` or `system` control boundary; model output
cannot authorize its own execution.

The lower-level `forjad`/`forja` HTTP path is also fail-closed. Set
`FORJA_HTTP_BEARER_TOKEN` and `FORJA_HTTP_ACTOR_ID` in both process
environments; health, readiness, and version are the only anonymous endpoints.
See the [local development guide](docs/06-operations/LOCAL_DEVELOPMENT.md).

## Initial Technology Direction

- **Go** for the daemon, scheduler, MCP server, process supervisor, and control
  plane.
- **PostgreSQL** as the operational system of record.
- **Object storage** for large immutable content.
- **Qdrant** for governed hybrid retrieval.
- **Neo4j** for deterministic and curated graph traversal.
- **Compiler-specific indexers** for code lineage.
- **Prometheus, Loki, Grafana, and OpenTelemetry** for observability.
- **TypeScript or Python adapters** only where their ecosystems provide a
  concrete advantage.

See the [system architecture](docs/02-architecture/SYSTEM_ARCHITECTURE.md) and
[master development plan](docs/04-roadmap/MASTER_DEVELOPMENT_PLAN.md).

## Quality Gate

Run:

```bash
make validate
```

The gate runs Go formatting, module, vet, unit, race, reproducible build, and
process-level smoke checks before validating public files, JSON schemas,
internal Markdown links, private paths, and common credential patterns.

With a disposable PostgreSQL database available, run the durability,
approval-to-publication, rollback compatibility, concurrency, backup/restore,
and process-restart acceptance suite:

```bash
export FORJA_TEST_DATABASE_URL='postgres:///forja_test?host=/tmp'
make test-integration
```

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md), [GOVERNANCE.md](GOVERNANCE.md), and
[SECURITY.md](SECURITY.md) before proposing changes.

## License

Licensed under the [Apache License 2.0](LICENSE).
