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

## Data-To-Database Contract

The Alpha must be understandable from its data stores. Each extracted record is
assigned to exactly one authority first, then projected only when projection
adds value.

| Data family | Extracted data | Authority | Projection | Product use |
| --- | --- | --- | --- | --- |
| Issuer identity | CIK, ticker, aliases, security IDs, exchange labels | PostgreSQL | Neo4j issuer/security nodes | deterministic company resolution |
| SEC filing timeline | accessions, forms, report periods, accepted/filed/available clocks, primary docs | PostgreSQL + object storage | Neo4j filing/document paths | point-in-time filing planner and source cards |
| SEC reported facts | taxonomy, concepts, contexts, raw facts, units, decimals, fiscal frames | PostgreSQL + object storage | Neo4j concept/fact/metric paths | fundamentals and cited accounting claims |
| Filing narrative | sections, notes, policies, risks, MD&A, citations | Object storage + PostgreSQL metadata | Qdrant chunks + Neo4j section paths | RAG, evidence drawer, memo support |
| Macro and rates | Treasury yields, real yields, FRED/ALFRED vintages | PostgreSQL + object storage | Neo4j series paths | factor tools and availability-aware analysis |
| Market data | adjusted prices, returns, benchmarks, corporate-action policy | PostgreSQL + object storage | Neo4j series/security paths | factor sensitivity and return alignment |
| 13F holdings | managers, reports, holdings, changes, filing delays, unresolved securities | PostgreSQL + object storage | Neo4j manager/holding paths | institutional disclosure analysis |
| Research sessions | prompts, plans, messages, tool calls, decisions, citations, claims, memos | PostgreSQL + object storage | Qdrant approved memory, Neo4j claim paths | multi-turn workspace and replay |
| Runtime evidence | GPU profile, model receipts, benchmark receipts, recovery reports | PostgreSQL metadata + object storage | Prometheus/Loki/Grafana/Tempo summaries | demo proof, optimization, release claims |

Rules:

- PostgreSQL owns identity, time, permission, numeric selection, and claim
  state.
- Object storage owns immutable bytes and large artifacts.
- Qdrant owns only semantic discovery over approved text or memory summaries.
- Neo4j owns explainable paths between canonical IDs, never source truth.
- Observability owns content-free runtime facts, not prompt or source content.

## Sprint 10-14 Database Build Plan

This is the practical build order for turning the architecture into an Alpha
product. Each Sprint may add code, schemas, and fixtures, but the stores keep
their authority boundaries.

### Sprint 10: Canonical Data Spine

PostgreSQL tables and views to rely on:

- issuer, security, identifier, source system, source object, and ingestion run
  records;
- SEC filing identity, filing document metadata, XBRL taxonomy, concept,
  context, raw fact, reviewed mapping, and metric-observation records;
- Treasury, FRED/ALFRED, market price, return, and approved series records;
- point-in-time views that require `available_at <= as_of`;
- source coverage views that show row counts, object hashes, ingestion state,
  and quality state.

Object storage artifacts:

- SEC identity, submissions, Company Facts, filing documents, Treasury,
  FRED/ALFRED, market-data, and manifest snapshots;
- Radeon runtime, local model, local embedding, benchmark, readiness, and
  recovery receipts.

Acceptance:

- canonical queries work without Qdrant or Neo4j;
- source bytes can be restored from manifests;
- local model and embedding endpoints are proven on Radeon/ROCm;
- no Sprint 11 work starts from a candidate-only Sprint 10 closure.

### Sprint 11: Tools, Qdrant, And Neo4j Evidence

PostgreSQL records to add:

- deterministic tool registry, tool input contracts, tool invocation receipts,
  formula versions, estimator versions, diagnostics, and evidence-pack
  metadata;
- chunk metadata, projection versions, projection checkpoints, retrieval
  receipts, graph projection receipts, claim candidates, and unsupported-gap
  receipts.

Object storage artifacts:

- filing section extractions, method docs, evidence packs, calculation outputs,
  retrieval test corpora, graph export manifests, and projection drift reports.

Qdrant points:

- narrative filing sections, notes, accounting policies, risk disclosures,
  method docs, evidence summaries, and approved memory summaries only;
- each point carries issuer, filing, source hash, section, available time,
  graph IDs, access scope, chunking version, embedding model, and projection
  version.

Neo4j nodes and edges:

- issuer, security, filing, document, section, concept, raw fact, metric,
  observation, series, manager, holding, analysis, claim, citation, and source
  object;
- relationships such as `FILED`, `CONTAINS`, `REPORTS`, `NORMALIZES_TO`,
  `DERIVED_FROM`, `USES_SERIES`, `HOLDS`, and `SUPPORTED_BY`, each bound to
  canonical IDs and source hashes.

Acceptance:

- a complete evidence pack for the primary demo question exists before memo
  synthesis;
- Qdrant and Neo4j can be deleted and rebuilt from PostgreSQL and object
  storage;
- semantic retrieval never promotes a fact without PostgreSQL/tool validation.

### Sprint 12: Agent Workspace, Memory, And Permissions

PostgreSQL records to add:

- research session, message, plan, plan step, permission decision, tool lease,
  local model invocation, context pack, citation, claim, memo, memory
  candidate, approved memory, deletion request, tombstone, and replay receipt.

Object storage artifacts:

- memo bodies, exported sessions, replay traces, UI evidence, content-redacted
  event streams, and private conversation artifacts.

Projection rules:

- Qdrant receives only approved memory summaries and permitted narrative
  evidence;
- Neo4j receives claim-to-citation and memo-to-evidence paths;
- all projections must be cleaned or tombstoned after memory deletion.

Acceptance:

- the judge can operate the primary scenario from the web UI;
- planning, RAG, tools, memory, and privacy controls are all visible;
- every released memo claim resolves to deterministic evidence or an explicit
  unsupported gap.

### Sprint 13: Evaluation And ROCm Optimization

PostgreSQL records to add:

- evaluation suite, split manifest, benchmark run, quality label, mechanical
  grader result, human-review rubric, ROCm profile, performance baseline,
  safety finding, regression decision, and release-quality gate.

Object storage artifacts:

- sanitized benchmark logs, charts, GPU metric snapshots, failure examples,
  replay packages, attack cases, and before/after profile comparisons.

Observability artifacts:

- Prometheus metrics, Loki log labels, trace IDs, GPU utilization, model-load
  timings, TTFT, tokens/second, p50/p95 response time, retrieval latency, graph
  latency, tool latency, and verification latency.

Acceptance:

- optimization improves a declared metric without breaking accuracy,
  citations, numeric exactness, privacy, or no-remote-core-inference gates;
- holdout data is never available to runtime agents or tuning prompts;
- every public performance claim has a receipt.

### Sprint 14: Release Freeze And Submission

PostgreSQL records to freeze:

- release manifest, final scenario receipts, final benchmark summaries,
  documentation facts, demo run metadata, public artifact links, and submission
  checklist.

Object storage artifacts:

- final PDF, demo video, poster or slide deck, release screenshots, evidence
  summary, source manifests, projection manifests, and independent backup
  receipt.

Acceptance:

- a fresh Radeon Cloud environment reproduces the documented workflow;
- source, README, project document, demo video, and optional deck are public
  and consistent with evidence;
- no secrets, private evaluation bodies, prohibited data, or remote core-model
  dependency are present in the release.

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
