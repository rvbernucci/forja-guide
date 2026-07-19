# Deterministic Lineage

Status: Implemented by the Sprint 08 candidate; authoritative closure pending.

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
- **Python:** the standard Python AST under an explicit syntax-version
  boundary; no type-checker evidence is claimed yet.
- **Other languages:** file metadata only in Sprint 08. Tree-sitter remains a
  future structural-discovery option.

Adapters emit the same versioned `SymbolCard` and `RelationEvidence` contracts.

## Symbol Cards

Raw code is not the default embedding unit. The indexer creates deterministic
symbol cards:

```yaml
symbol_id: symbol_<sha256-version-identity>
lineage_id: symbol_lineage_<sha256-cross-version-identity>
file_id: file_<sha256-version-identity>
file_lineage_id: file_lineage_<sha256-cross-version-identity>
language: typescript
kind: function
name: calculateBalance
qualified_name: accounting.calculateBalance
signature: "calculateBalance(entries: Entry[]): Balance"
declaration:
  start: {line: 10, column: 1, offset: 180}
  end: {line: 14, column: 2, offset: 420}
exported: true
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
6. emit outbox evidence for future derived-store projections.

Qdrant and Neo4j updates and their projection checkpoints begin in later
Sprints; they are not part of the Sprint 08 publication transaction.

Version IDs bind the exact repository commit, source hash, declaration, and
signature. Separate lineage IDs bind stable file paths and qualified symbol
identity so deltas can distinguish reuse, modification, addition, and deletion
across commits without weakening version identity.

The implemented invalidation planner starts from committed Git changes,
propagates only proven reverse dependency kinds, fails closed around unresolved
relations after structural changes, and permits reuse only when source,
configuration, and adapter-set evidence match exactly. Canonical publication
still rebinds all snapshot-scoped IDs.
