# Sprints 10-14 Data Execution Matrix

Status: Active planning companion for
[Sprints 10-14](SPRINTS_10_14_PRODUCTION.md).

This matrix turns the Alpha data architecture into executable work. It is the
bridge between source extraction, durable storage, projection stores,
deterministic tools, local Radeon inference, and Sprint closure evidence.

## Operating Principle

Forja Alpha must never ask the model to be the database. The local model plans,
selects tools, and writes bounded explanations. PostgreSQL, object storage,
typed Go tools, Qdrant, and Neo4j each carry a separate responsibility:

| Responsibility | Owner |
| --- | --- |
| Preserve source bytes | Content-addressed object storage on persistent PVC |
| Decide identity, time, facts, permissions, and lineage | PostgreSQL |
| Discover relevant narrative context | Qdrant local embeddings |
| Explain evidence paths and entity relationships | Neo4j projection |
| Compute accounting, factor, and holdings results | Typed deterministic tools |
| Plan, ask follow-ups, and synthesize cited memos | Local Radeon model |
| Prove runtime, quality, and recovery | Sanitized receipts and tests |

## Sprint Data Matrix

| Sprint | Data to extract or produce | Canonical storage | Projection or runtime | Closure evidence |
| --- | --- | --- | --- | --- |
| 10 | Magnificent Seven identity, SEC submissions, Company Facts, filings, Treasury, FRED/ALFRED, adjusted market snapshots, model and embedding benchmarks | `alpha_source_object`, `alpha_ingestion_run`, issuer/security/identifier, filing, XBRL, metric, series tables | Loopback model endpoint, loopback embedding endpoint, point-in-time views | Runtime receipt, readiness report, source-restore report, model benchmark, embedding benchmark |
| 11 | Filing sections, risk factors, accounting policies, footnotes, method docs, tool outputs, evidence packs | PostgreSQL metadata plus object artifacts for chunks, packs, calculations, and receipts | Qdrant narrative corpus, Neo4j evidence graph, deterministic tool registry | Projection receipts, retrieval evals, graph path tests, tool receipts |
| 12 | Research sessions, plans, messages, promoted memories, permission decisions, memo drafts, citations | Research session, message, memory, tool invocation, claim, and claim-evidence tables | Web UI, local model planner, memory retrieval, context broker | End-to-end demo trace, memory audit, permission audit, cited memo artifact |
| 13 | Evaluation sets, latency runs, GPU metrics, answer-quality labels, retrieval labels, tool accuracy checks | Evaluation specs, run receipts, result hashes, telemetry snapshots | Prometheus/Loki/Grafana evidence, local model benchmark variants | Accuracy, latency, stability, privacy, and recovery reports |
| 14 | Submission PDF, demo video evidence, final README facts, release manifest, AMD PR materials | Release artifacts, immutable manifest, final evidence summaries | Public repository and Radeon demo profile | Clean checkout reproduction, public-source audit, submission checklist |

## Source Extraction Contracts

Each source adapter must produce three things before the agent can use the
data: preserved bytes, canonical rows, and a receipt.

| Source family | Preserved object | Canonical rows | Required receipt fields |
| --- | --- | --- | --- |
| SEC identity | `company_tickers.json` snapshot | issuers, securities, identifiers | source hash, source time, issuer coverage, missing covered tickers |
| SEC submissions | `CIK*.json` snapshots | filings, forms, periods, filing clocks | CIK match, accepted forms, skipped forms, availability clock |
| SEC Company Facts | `CIK*.json` snapshots | taxonomies, concepts, contexts, raw facts, metric observations | fact counts, units, currencies, quarantines, mapping version |
| SEC documents | XHTML/XML/inline XBRL objects | filing documents, sections, source anchors | primary document hash, parser version, section coverage |
| Treasury | curve CSV/XML snapshots | series and observations | vintage policy, skipped rows, 10-year real-yield coverage |
| FRED/ALFRED | vintage CSV snapshots | macro series and observations | realtime clocks, revision policy, allowed series |
| Market data | licensed adjusted-price CSV snapshots | price and return series | provider, license boundary, adjustment policy, missing rows |
| 13F | SEC quarterly ZIP and filing artifacts | managers, reports, positions, resolutions | filing delay, CUSIP resolution status, unresolved holdings |

## Database Build Order

The Alpha database should be rebuilt in this order so failures are local and
easy to diagnose:

1. Create tenant, repository, source-system, and object-storage scopes.
2. Seed issuer identity and reviewed security identifiers.
3. Preserve SEC submissions and filing identities.
4. Preserve Company Facts and raw XBRL-like rows.
5. Seed reviewed metric definitions and issuer-scoped mappings.
6. Promote first-pass metric observations with explicit limitations.
7. Preserve Treasury, FRED, and market snapshots.
8. Publish point-in-time views and source coverage views.
9. Project approved narrative chunks into Qdrant from PostgreSQL-owned source
   metadata.
10. Project PostgreSQL canonical IDs into Neo4j evidence paths.
11. Register deterministic tools and bind their outputs to source hashes.
12. Create research sessions, claims, citations, and memo artifacts.

## Projection Rules

Qdrant and Neo4j are not authorities. They are rebuildable projections:

| Projection | Allowed inputs | Forbidden behavior |
| --- | --- | --- |
| Qdrant | Filing sections, notes, policies, risk disclosures, method docs, evidence summaries, governed memory | Serving exact accounting values, selecting canonical facts, bypassing PostgreSQL permissions |
| Neo4j | Canonical IDs, source hashes, filing paths, fact paths, metric paths, analysis paths, claim evidence | Creating facts that do not exist in PostgreSQL, turning semantic candidates into verified evidence |

Every projection point or edge must contain enough metadata to resolve back to
PostgreSQL and object storage before it can enter a context pack.

## Tool And Claim Evidence

Every user-facing claim must land in one of these classes:

| Claim class | Required support |
| --- | --- |
| Reported fact | Canonical metric observation plus filing/source-object citation |
| Derived metric | Formula version plus all input observations |
| Factor estimate | Analysis spec, input window, estimator version, diagnostics, and result hash |
| Holdings observation | 13F report, filing delay, position, and security-resolution state |
| Narrative statement | Cited section chunk resolved through PostgreSQL and source object |
| Limitation or gap | Unsupported-data receipt or quality finding |

The model may phrase the claim, but it cannot promote unsupported text into
verified evidence.

## Alpha Readiness Checklist

- [ ] A clean Radeon instance can rebuild the database from committed source
  plus private hash-pinned snapshots.
- [ ] Local model and embedding endpoints are loopback-only and benchmarked.
- [ ] PostgreSQL can answer issuer, filing, metric, series, and source-coverage
  queries without Qdrant or Neo4j.
- [ ] Qdrant can retrieve narrative evidence and every point resolves back to
  a permitted PostgreSQL/source-object record.
- [ ] Neo4j can explain source-to-claim paths for the primary demo memo.
- [ ] Deterministic tools produce receipts with input hashes and formula or
  estimator versions.
- [ ] The web UI exposes model health, source coverage, tool traces, citations,
  and limitations.
- [ ] The final demo runs from local Radeon inference and does not require
  remote core-model APIs.
