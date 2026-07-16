# Contributing

Thank you for helping build Forja.

## Before Opening a Change

1. Read the relevant architecture document.
2. Open or reference an issue describing the problem and expected evidence.
3. Identify whether the change requires an ADR.
4. Keep implementation, generated evidence, and documentation in separate
   commits when practical.

## Pull Request Requirements

- Explain the user or operator problem.
- Declare the changed trust boundary.
- Include tests or mechanical validation.
- Update affected contracts and documentation.
- State rollback behavior.
- Run `make validate`.

## Commit Style

Use concise imperative subjects:

```text
Add run event schema
Define PostgreSQL outbox contract
Document Qdrant authority boundary
```

## Public Repository Safety

Do not include:

- credentials or secret values;
- private infrastructure addresses;
- raw customer or employee data;
- proprietary source code copied from another repository;
- unredacted agent transcripts;
- large generated indexes or database exports.

Use synthetic examples and stable public identifiers.

