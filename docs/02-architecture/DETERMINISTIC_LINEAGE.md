# Deterministic Lineage

Status: Proposed

## Decision

Code lineage is extracted deterministically before semantic retrieval is
allowed to influence navigation.

Tags improve filtering, but tags alone do not prove dependency or behavior.

## Extraction Layers

| Layer | Evidence |
| --- | --- |
| Repository | commits, branches, paths, ownership, and file hashes |
| Syntax | files, declarations, symbols, imports, and exports |
| Types | signatures, interfaces, implementations, references, and schemas |
| Data | tables, fields, reads, writes, filters, joins, and aggregations |
| Behavior | tests, routes, commands, traces, and runtime receipts |
| Documentation | applies-to, validates, supersedes, and source-of-truth links |

## Language Adapters

- **TypeScript/JavaScript:** TypeScript Compiler API for module resolution,
  symbols, types, references, and diagnostics.
- **Go:** `go/packages`, `go/types`, and `go/ast`.
- **Python:** Python AST for syntax plus a type-checker adapter when available.
- **Other languages:** Tree-sitter for structural discovery, promoted only when
  stronger language-specific evidence is unavailable.

Adapters emit the same versioned `SymbolCard` and `RelationEvidence` contracts.

## Symbol Cards

Raw code is not the default embedding unit. The indexer creates deterministic
symbol cards:

```yaml
entity_id: symbol:repository:commit:path:qualified-name
language: typescript
kind: function
name: calculateBalance
qualified_name: accounting.calculateBalance
signature: "calculateBalance(entries: Entry[]): Balance"
summary_source: generated
source_path: src/accounting/balance.ts
source_hash: sha256
relations:
  - relation: CALLS
    target_id: symbol:repository:commit:path:sumEntries
    evidence: confirmed_static
tests: []
```

The textual projection of this card may be embedded for discovery. Its
relations come from deterministic evidence, not the embedding.

## Promotion Rules

```text
candidate_semantic
  -> candidate_static
  -> confirmed_static
  -> confirmed_behavioral
  -> runtime_observed
```

Semantic candidates can open an investigation but cannot become authoritative
graph edges without a promotion source.

## Incremental Updates

Each commit produces a change set:

1. identify changed files;
2. invalidate affected symbols;
3. re-run language adapters;
4. compute entity and relation deltas;
5. commit canonical metadata and outbox events;
6. update Qdrant and Neo4j projections;
7. record projection checkpoints.

Stable IDs include repository, commit or version boundary, path, and qualified
symbol identity.

