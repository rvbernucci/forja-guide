# Agent Operating Contract

This file applies to every coding agent operating in this repository.

## Mission

Build Forja as a governed, auditable, interoperable multi-agent software
factory. Preserve the distinction between architecture, implemented behavior,
generated evidence, and future plans.

## Required Behavior

- Read `README.md`, `ROADMAP.md`, and the relevant architecture documents before
  changing implementation contracts.
- Treat JSON schemas in `schemas/` as language-neutral public interfaces.
- Add or update an ADR when a decision changes system boundaries, persistence,
  trust, security, or protocol compatibility.
- Keep all examples free of real credentials, private hostnames, customer data,
  personal paths, and production identifiers.
- Use one author per file or artifact during concurrent work.
- Preserve provenance for generated artifacts and never edit generated evidence
  manually.
- Run `make validate` before declaring work complete.

## Forbidden Behavior

- Do not copy private product repositories into this public project.
- Do not commit `.env` files, tokens, passwords, private keys, database dumps,
  raw chats, or production logs.
- Do not present Qdrant similarity as authority.
- Do not create Neo4j edges from embeddings without explicit evidence and a
  promotion rule.
- Do not let an LLM directly mutate production state without authorization,
  validation, and an audit event.
- Do not mark planned capabilities as implemented.

## Authority Hierarchy

```text
runtime evidence and tests
  > source code, schemas, and migrations
  > deterministic indexes and lineage
  > curated graph relations
  > active architecture contracts
  > semantic retrieval
  > chat memory
```

