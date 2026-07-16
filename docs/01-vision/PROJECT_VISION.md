# Project Vision

## Purpose

Forja turns a human objective into governed, reviewable, and resumable software
delivery.

It is not an autonomous coding demo. It is an execution system in which agents
operate under explicit scopes, budgets, contracts, and validation gates.

## User Experience

The primary user works with a permanent **Co-architect** inside a conversational
client. The Co-architect helps clarify intent and proposes a Sprint. Once the
human approves it, Forja coordinates execution without forcing the user to
manually supervise every worker.

The human should be able to:

- discuss a problem naturally;
- inspect the proposed Sprint and its risks;
- approve or reject privileged actions;
- observe progress and blockers;
- inspect evidence rather than trusting summaries;
- pause, resume, cancel, or retry execution;
- understand what changed and why.

## Product Principles

1. **Accuracy before autonomy.** A wrong autonomous action is worse than a
   visible blocker.
2. **Contracts before prompts.** Prompts guide behavior; schemas and policy
   constrain it.
3. **Evidence before completion.** A task is complete only when its acceptance
   evidence exists.
4. **One writer per artifact.** Parallel agents may read widely, but concurrent
   writes require explicit ownership.
5. **Fail closed.** Missing authorization, stale context, invalid output, or
   uncertain identity must stop or safely degrade.
6. **Derived intelligence is replaceable.** Embeddings and graph projections
   can be rebuilt from canonical sources.
7. **Human decisions are durable.** Important approvals and architecture
   decisions become structured artifacts, not lost chat context.
8. **Operational truth is transactional.** Runtime state belongs in a system
   designed for consistency and recovery.
9. **Interoperability over lock-in.** MCP and language-neutral JSON contracts
   preserve client and worker portability.
10. **Measured improvement.** Retrieval, routing, and agent policies require
    evaluation datasets and observable quality budgets.

## Initial Scope

Forja 1.0 targets software engineering workflows:

- repository discovery;
- Sprint planning;
- code and documentation changes;
- isolated worker execution;
- mechanical and independent validation;
- evidence production;
- resumable orchestration.

It does not initially target:

- arbitrary desktop automation;
- unattended production deployment;
- financial or legal authority;
- unrestricted shell or infrastructure access;
- self-modifying security policy.

## Success Definition

Forja 1.0 succeeds when an approved Sprint can move from intent to validated
evidence across a process restart without manual state reconstruction, while
maintaining repository isolation, complete audit events, and deterministic
output contracts.

