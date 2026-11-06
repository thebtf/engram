<!--
Sync Impact Report
- Current amendment: 7.0.0 -> 7.1.0 (MINOR recovery-scope amendment). Authority: native main USER directives on 2026-11-04 at 17:35:04Z (message prefix b75affa3) and 17:36:05Z (80adf21f) require an urgent release to restore a broken service before subsequent remarks; the Engram Part 3 directive also requires accepted work to converge into current main. Align accepted-behavior finding classification with the bounded outcome in XI and scoped evidence in XIII. Unchanged defects in untouched, unrelated behavior retain an owner without automatically expanding an installed-service recovery; normal releases retain supported-path regression obligations. For a qualifying availability emergency, the release protocol defines the cut-specific restoration and safety blockers, deferrals and applicable mandatory conditions. Synchronize AGENTS.md and docs/RELEASE-PROTOCOL.md, including reviewed-PR integration ownership and safe primary-checkout convergence; review affected references for an unqualified all-defects release veto. No supported-platform, authorization, privacy, production data-preservation, review, scan, migration, rollback or rollout guarantee is removed. This documentation correction claims no runtime or installed acceptance.
- Historical amendment: 6.0.0 -> 7.0.0 (MAJOR change to governing delivery ordering), effective 2026-11-02 by explicit product direction. Ordinary task-context usefulness and generic external clients' own linked knowledge follow their real provider dependencies; full Feature 011 and Book Context are not blanket predecessors. Clarify canonical external-client context and authored knowledge relations without a hosted assistant, new IAM, second content authority, or restored graph editor. Revalidate Feature 009 spec/plan/acceptance, Feature 010 ordering, architecture rationale, Decisions 18–22, and the affected current task/checklist pointers. Preserve all current data/authorization/release boundaries, including the 6.0.0 post-release Sonar timing policy. Documentation adoption does not claim any runtime, migration, private isolation, or installed acceptance.
- Historical amendment: 5.0.0 -> 6.0.0, documentation alignment required by the operator's 2026-10-09 Sonar-post-release decision and 2026-10-10 AGENTS correction mandate. Deliver the working release after applicable functional/security/integrity proof; analyze the exact released candidate afterward without a Sonar merge/tag/publication veto. Confirmed substantial defects route to a corrective patch. AGENTS.md and docs/RELEASE-PROTOCOL.md are aligned; older Sonar timing predicates in recovery specs/tasks/checklists are superseded, not proof of a current campaign or waiver of their other obligations. No implementation, runtime, release, CI or scanner effects are part of this amendment.
- Historical amendment: 4.0.0 -> 5.0.0 (MAJOR release-gate availability relaxation), effective 2026-09-26 by operator directive. It allowed unavailable Sonar to be recorded as `SKIPPED_UNAVAILABLE / NOT_PROVEN` without blocking other delivery; actual results and remaining obligations stayed intact. Its availability-only timing rule is superseded by the post-release amendment above.
- Adopted version change: 3.0.0 -> 4.0.0 (MAJOR relaxation of one governing release gate), effective 2026-09-25. The existing candidate was already 3.0.0; 1.0.0 -> 2.0.0 would downgrade it.
- Prior amendment: 2.0.0 -> 3.0.0 effective 2026-09-15, approved in OMP session 01a08c71-d2b8-7638-a45f-ef4b606fd786; its finding-disposition rule and XI/XIII scope clarification remain in force.
- Historical 2026-09-25 operator decision: proceed without Sonar for the v6.49.4 PR #531 OMP plugin hotfix only. Sonar was NOT_PROVEN; prior findings remained unresolved, not accepted or cleared. That candidate-only decision and the 2026-09-26 availability rule do not govern the post-release amendment above.
- Unchanged: all core principles and other release, security, review, image, migration, and rollout gates under docs/RELEASE-PROTOCOL.md.
- Historical 2026-09-26 recovery revalidation snapshot: specs/001-engram-architectural-recovery/spec.md; plan.md, tasks.md (AR-7 T085/T086), quickstart.md, IMPLEMENTATION-HANDOFF.md, IMPLEMENTATION-SESSION-PROMPT.txt, checklists/operability.md, and analysis/fr-sc-task-traceability.md. The then-recorded exact-head Sonar obligations are superseded only in timing/veto semantics; PR #531 was not AR-7 closure.
- Historical feature revalidation snapshot: specs/010-unified-code-intelligence/, specs/009-working-agent-memory-r1/ and specs/011-operator-code-console/. Their accepted product, integrity and evidence requirements remain; old release-blocking Sonar predicates read under the current post-release policy. This correction does not claim new feature acceptance or an executed revalidation campaign.
- Historical consumer synchronization: AGENTS.md and docs/RELEASE-PROTOCOL.md retained the v6.49.4 exception and incomplete-campaign diagnostics. Current post-release synchronization is recorded in the amendment above; no executable gate changed.
- Historical 2026-09-25 revalidation status: scoped constitutional exception recorded; actual release evidence and all remaining prerequisites were pending.
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
typed context required by its domain. Product knowledge MAY use an authenticated external-client
self context, a canonical project or Space, or explicit global context under its accepted owner.
Self denotes a durable client principal, not a hosted assistant runtime. Explicit invalid context
MUST NOT silently fall back. Affiliation MUST NOT imply visibility or a new grant;
code intelligence MUST use an authorized Source, Checkout, and pinned View, with Space only as
optional grouping. Paths, labels, remotes, branches, hashes, and legacy slugs are evidence,
locators, or typed aliases, never tenant keys or checkout authority. Ambiguity MUST fail closed
before ranking, body access, traversal, or mutation. A legacy project key MUST NOT select a
working tree or View by itself.

### IV. Authority Models Stay Distinct
State, evidence, knowledge, and projections MUST have separate owners and lifecycles. Current
session or project state is not historical knowledge; evidence is not a belief; a graph, summary,
index, cluster, or meta-memory is a rebuildable projection and MUST NOT become a second write
authority. Explicit user-authored links between knowledge records are authoritative assertions
owned by that knowledge use case; their graph indexes and views remain projections. Such links
MUST NOT be confused with automatically derived code relations or grant access to an endpoint.

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

The separately accepted Operator Code Console and basic collection feature MAY proceed after its UCI dependencies without waiting for Working Agent Memory R1. Ordinary task-context delivery and generic external clients' own linked knowledge MUST follow their actual accepted provider dependencies; they MUST NOT wait for full Feature 011, Book Context, all Assistant work, or unrelated task-field features as blanket prerequisites. Feature 010 retains UCI authority and same-View correctness; Feature 011 retains operator-surface ownership. An available deterministic code path MAY serve a named-symbol task without claiming semantic acceptance. This amendment does not move a UI redesign into core recovery, restore retired graph products, waive current release/data/access boundaries, or equate design/source readiness with installed useful behavior.

### XIII. Evidence Proves Behavior, Not Ceremony
Tests and receipts MUST prove cross-session evidence acquisition, identity convergence,
reconciliation, task-aware retrieval, exposure, outcome recording, migration, rollback, and
installed behavior. Hashes, manifests, route existence, flags, or mocked green surfaces alone do
not establish product acceptance.
Apply these evidence obligations to the accepted slice and affected dependencies, together with
mandatory regression/security/release checks, subject to the qualifying availability-emergency
exception in the release protocol. Platform-enforced requirements, explicit unsafe-effect
approvals, and security, data and required-restoration-path blockers remain binding. Deferred
review and tests remain pending, not passed. Normal release gates remain unchanged. These
obligations MUST NOT silently add unrelated product domains to every release. Test doubles prove
their stated layer, not installed end-to-end behavior.

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
- After working release delivery, run exact-released-candidate SonarQube analysis when the
  service, tool and runtime are available. Retain actual results and evidenced finding
  dispositions; unavailable or incomplete analysis is `NOT_PROVEN`, never PASS or zero findings.
  Availability, numeric Quality Gate and issue count MUST NOT veto merge/tag/publication
  or create a release Ask. Confirmed substantial defects MUST route to a corrective patch.
  Concrete known security and data-integrity defects retain their independent safety boundary.
  Classify accepted-behavior findings under the release protocol's bounded recovery rule and XIII:
  for normal releases, a defect in a required path or a new/worsened supported-path regression
  blocks. For a qualifying availability emergency, the protocol defines the cut-specific
  restoration and safety blockers, permitted deferrals and applicable mandatory conditions.
  An unchanged defect in untouched, unrelated behavior may remain owned follow-up only when
  deferral does not worsen release safety or violate a mandatory condition. This policy does not
  authorize shipping a known unsafe change.
  Do not alter thresholds, scope or scanner exceptions, suppress findings or relabel historical
  failures to manufacture green. All other functional, Docker/image, supported-platform,
  migration, recovery and rollout obligations in the release protocol remain in force, subject
  to its qualifying availability-emergency exception.
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

**Version**: 7.1.0 (2026-11-04; recovery-scope amendment) | **Ratified**: 2026-08-21 | **Last Amended**: 2026-11-04
