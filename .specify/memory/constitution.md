<!--
Sync Impact Report
- Adopted version change: 3.0.0 -> 4.0.0 (MAJOR relaxation of one governing release gate), effective 2026-09-25. The existing candidate was already 3.0.0; 1.0.0 -> 2.0.0 would downgrade it.
- Prior amendment: 2.0.0 -> 3.0.0 effective 2026-09-15, approved in OMP session 01a08c71-d2b8-7638-a45f-ef4b606fd786; its finding-disposition rule and XI/XIII scope clarification remain in force.
- Operator decision: proceed without Sonar for the v6.49.4 PR #531 OMP plugin hotfix only. Sonar is NOT_PROVEN; prior findings remain unresolved, not accepted or cleared. The exception does not apply to PR #508, another version, or wider scope.
- Unchanged: all core principles and other release, security, review, image, migration, and rollout gates under docs/RELEASE-PROTOCOL.md.
- Revalidated active recovery scope: specs/001-engram-architectural-recovery/spec.md (unchanged outcome); plan.md, tasks.md (AR-7 T085/T086), quickstart.md, IMPLEMENTATION-HANDOFF.md, IMPLEMENTATION-SESSION-PROMPT.txt, checklists/operability.md, and analysis/fr-sc-task-traceability.md (AR-7 exact-head Sonar obligations retained; PR #531 is not AR-7 closure).
- Revalidated other feature scope: specs/010-unified-code-intelligence/spec.md, plan.md, quickstart.md, tasks.md (UCI-1 release gate retained); specs/009-working-agent-memory-r1/spec.md and plan.md (future M6 gate retained); specs/011-operator-code-console/checklists/implementation-readiness.md (non-release Sonar boundary retained). None supplies a PR #531 release task or needs an edit.
- Synchronized consumers: AGENTS.md already names the one-time exception; docs/RELEASE-PROTOCOL.md defines its bounded prerequisites and corrects the incomplete-campaign history. No executable gate changed.
- Revalidation status: scoped constitutional exception recorded; actual release evidence and all remaining prerequisites are still pending.
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
Select that bounded outcome before implementation. Complete its authorized installation and
consumer verification before accumulating the next dependent product frontier. Independent safe
work is permitted; an external release dependency must retain an owner and a resumption action.

### XII. Surface Work Follows Core Truth
This recovery MUST NOT redesign the operator working surface as core recovery work. Existing surfaces MAY be removed or made honestly unavailable when their backing capability is removed. New working-surface design requires a separate accepted Spec Kit feature.

The separately accepted Operator Code Console and basic collection feature MAY proceed after UCI technical acceptance without waiting for Working Agent Memory R1. It MUST preserve UCI authority, evidence, and release boundaries. This exception does not place working-surface implementation in the recovery feature. Book Context is the next separate feature. Working Agent Memory R1 remains retained after Book Context.

### XIII. Evidence Proves Behavior, Not Ceremony
Tests and receipts MUST prove cross-session evidence acquisition, identity convergence,
reconciliation, task-aware retrieval, exposure, outcome recording, migration, rollback, and
installed behavior. Hashes, manifests, route existence, flags, or mocked green surfaces alone do
not establish product acceptance.
Apply these evidence obligations to the accepted slice and affected dependencies, together with
all mandatory regression/security/release checks. They MUST NOT silently add unrelated product
domains to every release. Test doubles prove their stated layer, not installed end-to-end behavior.

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
- Before a release tag or publication, the exact candidate MUST pass a fresh SonarQube analysis
  and its Quality Gate MUST be `OK` under the enforced policy, except for the operator-authorized
  one-time v6.49.4 PR #531 OMP plugin hotfix exception in `docs/RELEASE-PROTOCOL.md`.
  Under that exception Sonar is `NOT_PROVEN`, and previously reported findings remain unresolved;
  the exception MUST NOT be reused for PR #508, another version, or a wider candidate scope.
  Every reported finding MUST otherwise have an evidenced disposition under the release protocol.
  Findings that violate required checks or the accepted safety, integrity, compatibility, or
  user-behavior contract MUST be fixed before release. Nonblocking findings may be deferred only
  by the authorized maintainer; deferral does not bypass a failing Quality Gate outside the
  named exception, required approval, or unresolved mandatory review. Policy thresholds, analysis
  scope, and scanner/security exceptions MUST NOT be changed merely to make a candidate pass.
  All other pre-release and rollout gates in the release protocol remain mandatory for PR #531.
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

**Version**: 4.0.0 (effective 2026-09-25) | **Ratified**: 2026-08-21 | **Last Amended**: 2026-09-25
