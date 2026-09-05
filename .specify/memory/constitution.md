<!--
Sync Impact Report
- Version change: 1.0.0 -> 1.1.0
- Modified principles: III. Stable Project Identity Precedes Scoped Access -> III. Stable Typed Context Precedes Scoped Access; VII. Retrieval Is Task-Aware and Bounded expanded for view-pinned code retrieval.
- Added sections: none.
- Removed sections: none.
- Follow-up TODOs: UCI-2 Code UI requires a separate accepted Spec Kit feature under Principle XII; Working Agent Memory R1 must be revalidated against the accepted Space/Source/Checkout/View contract before resumption.
-->

# Engram Constitution

## Core Principles

### I. One Authoritative Product Loop
Engram MUST have one authoritative path from accepted session evidence to reusable knowledge,
task-aware retrieval, exposure, and outcome evidence. Version labels, milestones, and V7
subsystems MUST NOT select competing runtime architectures.

### II. Required Product Behavior Is Default
A capability required for Engram's accepted product outcome MUST be active by construction.
Configuration MAY express deployment variability, external-provider availability, or a
time-bounded emergency stop, but MUST NOT hide incomplete architecture behind a feature flag.

### III. Stable Typed Context Precedes Scoped Access
Every scoped read, write, event, job, cache, and audit record MUST carry the server-resolved
typed context required by its domain. Product knowledge MAY retain a canonical project or Space;
code intelligence MUST use an authorized Source, Checkout, and pinned View, with Space only as
optional grouping. Paths, labels, remotes, branches, hashes, and legacy slugs are evidence,
locators, or typed aliases, never tenant keys or checkout authority. Ambiguity MUST fail closed
before ranking, body access, traversal, or mutation. A legacy project key MUST NOT select a
working tree or View by itself.

### IV. Authority Models Stay Distinct
State, evidence, knowledge, and projections MUST have separate owners and lifecycles. Current
session or project state is not historical knowledge; evidence is not a belief; a graph, summary,
index, cluster, or meta-memory is a rebuildable projection and MUST NOT become a second write
authority.

### V. Durable Work Has Durable State
Every asynchronous product workflow MUST use persisted work records with idempotency keys,
leases or ownership, attempts, errors, timestamps, and terminal states. Timers MAY wake workers;
they MUST NOT determine whether accepted work exists.

### VI. Reconciliation Precedes Accumulation
New evidence MUST be reconciled against compatible existing knowledge before activation. Every
proposal MUST record one terminal decision: create, strengthen, revise, supersede,
contradiction-pending, reject-noise, reject-policy, or no-op-duplicate. Blind append is forbidden
as the default knowledge operation.

### VII. Retrieval Is Task-Aware, View-Pinned, and Bounded
Automatic knowledge retrieval MUST receive an actual task, query, or defined topic shift; filter
by the domain's server-resolved identity, scope, validity, privacy, and principal before ranking;
return a bounded packet; explain selection; and record exposure. Code search, graph, and source
reads MUST authorize Source and Checkout before candidate selection and MUST pin one immutable
View per Source. Search and graph facts returned together MUST come from the same View. Session
start MAY include only current state and universal policy, not a recent-memory dump substituted
for retrieval.

### VIII. Feedback Is Automatic and Epistemically Honest
Engram MUST record retrieval exposure and supported-host completion evidence automatically.
Success, partial, failure, abandonment, and unknown MUST remain distinct, with source and
certainty. Process exit, citation absence, or provider failure MUST NOT manufacture utility or
success claims.

### IX. Removal Requires Evidence; Preservation Requires a Consumer
Every removed unit MUST name its live consumers, data, compatibility impact, export or rollback
boundary, and verification receipt. Every preserved subsystem MUST name an accepted user outcome
and live consumer. Existing code, tests, release recency, or cost alone are not preservation
arguments.

### X. Migrations Conserve Meaning
Identity convergence and schema transitions MUST preserve counts, provenance, revision history,
privacy scope, and auditability. Conflict policy MUST be explicit and deterministic; silent
last-write-wins canonicalization is forbidden. Expand, migrate, verify, observe, and contract are
separate gates.

### XI. Releases Improve the Installed System
Recovery MUST ship as small installable releases from clean current `main`, each with one bounded
outcome, migration/rollback boundary, behavioral evidence, installed verification, and an
observation rule. A hidden long-lived replacement branch is forbidden.

### XII. Surface Work Follows Core Truth
This recovery MUST NOT redesign the operator working surface. Existing surfaces MAY be removed or
made honestly unavailable when their backing capability is removed. New working-surface design
requires a separate accepted Spec Kit feature after recovery closure.

### XIII. Evidence Proves Behavior, Not Ceremony
Tests and receipts MUST prove cross-session evidence acquisition, identity convergence,
reconciliation, task-aware retrieval, exposure, outcome recording, migration, rollback, and
installed behavior. Hashes, manifests, route existence, flags, or mocked green surfaces alone do
not establish product acceptance.

### XIV. Recover Outcomes Without Resurrecting Demolished Implementations
The historical demolition establishes evidence about obsolete implementations, not a permanent
product constraint. Engram MUST NOT copy demolished v5 implementations, but MUST recover required
product outcomes through the architecture specified by the active constitution and feature
contracts.

## Architecture and Data Constraints

- Engram remains a Go modular monolith with PostgreSQL as authoritative storage; this recovery
  MUST NOT introduce a new network service.
- Application workflows own transport-independent use cases; adapters validate input and call one
  workflow; repositories receive resolved typed identity, not raw project strings.
- Code-derived indexes and graphs MUST use server-resolved Source, Checkout, and View identity;
  product domains MAY continue through versioned canonical-project or Space compatibility until
  their own migration is accepted. Compatibility aliases MUST NOT select code authority directly.
- Providers, embeddings, reranking, graphs, summaries, and caches are capability or projection
  concerns. Their absence MUST be visible as health state and MUST NOT block durable evidence
  acceptance or select a second domain workflow.
- Any temporary rollout control MUST name its owner, protected risk, safe default, metrics,
  removal release or date, both-branch test, and tracked deletion task. It MUST NOT survive two
  completed recovery releases without an explicit constitutional amendment.
- Secrets, credentials, and private content MUST NOT enter analysis artifacts, logs, exports,
  merge fingerprints, proposals, embeddings, or equality comparisons without their governing
  privacy policy and explicit authorization.

## Delivery and Evidence Gates

- Constitution conflicts block planning and implementation.
- Any new project identifier scheme requires a versioned contract, cross-language vectors, a
  migration path, compatibility window, and sunset gate.
- Any new durable asynchronous path requires a persisted state machine, idempotency, retry,
  terminal failure, and degraded-provider semantics.
- Any new projection requires a named authority, consumer, rebuild procedure, health contract,
  and no write authority.
- Before a release tag or publication, the exact candidate MUST pass a fresh SonarQube analysis;
  every reported finding MUST be fixed; the exact head MUST be rescanned; and the Quality Gate
  MUST be `OK`.
- A destructive contraction occurs only after backup or export, migration receipts, zero-drift
  observation, compatibility sunset, and rollback boundary evidence exist.

## Governance

This constitution is the governing contract for Engram Architectural Recovery. It explicitly
supersedes `.agent/specs/constitution.md`; that file remains immutable historical evidence and has
no authority over this recovery. The active constitution, accepted feature specification, recorded
clarifications, contracts, plan, checklists, and tasks take precedence in that order.

Amendments require an explicit operator decision, a version bump, a Sync Impact Report, and
revalidation of every affected specification, checklist, plan, and task. A MAJOR version changes
or removes a governing principle; a MINOR version adds or materially expands one; a PATCH version
clarifies without changing meaning. Every release and implementation review MUST check conformance
to this constitution, preserve evidence of any exception, and reject scope expansion by workaround.

**Version**: 1.1.0 | **Ratified**: 2026-08-21 | **Last Amended**: 2026-09-05
