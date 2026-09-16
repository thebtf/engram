# Feature Specification: Engram Architectural Recovery

**Feature Branch**: `spec/engram-architectural-recovery`  
**Created**: 2026-08-22  
**Status**: Ready for analysis
**Input**: Governed architectural recovery that makes Engram's persistent-memory loop
complete, observable, default, and safely migratable without redesigning the operator surface.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Keep one project coherent everywhere (Priority: P1)

A coding agent uses the same logical project from a primary checkout, nested directory,
branch, worktree, clone, directory move, or remote rename and receives the same durable
Engram project identity without silently creating a second tenant.

**Why this priority**: Identity is the authorization and migration boundary for every other
recovery outcome.

**Independent Test**: A fixture exercises all supported locations for one anchored project,
then verifies one canonical identity; copied anchors, malformed anchors, ambiguous historical
identifiers, and unanchored writes fail before mutation.

**Acceptance Scenarios**:

1. **Given** a valid repository anchor, **When** supported adapters access the project from a
   primary checkout, worktree, clone, nested directory, branch, moved directory, or renamed
   remote, **Then** each resolves the same canonical project identity.
2. **Given** a copied anchor in a fork, **When** it attempts a project-scoped mutation,
   **Then** the request is held for an explicit identity decision and no data is written.
3. **Given** a missing, malformed, or ambiguous identity, **When** a scoped write is requested,
   **Then** it fails with an actionable onboarding or ambiguity result and creates no fallback
   path-, name-, or hash-derived tenant.

---

### User Story 2 - Retain every accepted session signal (Priority: P1)

An agent's completed, interrupted, replayed, or provider-degraded session produces durable
session evidence with a visible terminal processing state rather than disappearing behind a
hook, timer, or in-memory watermark.

**Why this priority**: A memory product cannot learn from work it cannot durably account for.

**Independent Test**: A controlled adapter fixture sends duplicate, graceful-exit, abrupt-loss,
provider-unavailable, server-restart, and replay events and verifies durable receipt, idempotency,
retry or quarantine, and terminal state for each accepted item.

**Acceptance Scenarios**:

1. **Given** a valid evidence payload, **When** it is acknowledged, **Then** the evidence,
   project, privacy, provenance, fingerprint, and durable processing state are retained.
2. **Given** duplicate delivery, **When** the same payload is replayed, **Then** it returns the
   original logical receipt without duplicate knowledge work.
3. **Given** a provider or worker failure, **When** processing cannot continue, **Then** the
   accepted evidence remains inspectable with a retryable, terminal-failure, or quarantined state.

---

### User Story 3 - Maintain revisable knowledge (Priority: P1)

An agent contributes reusable evidence and Engram reconciles it with existing knowledge before
it affects retrieval, preserving decisions, contradictions, provenance, privacy, and revision
history instead of appending independent notes.

**Why this priority**: Reconciliation is the distinction between persistent storage and a
self-improving knowledge product.

**Independent Test**: A controlled corpus exercises create, strengthen, revise, supersede,
contradiction-pending, reject-noise, reject-policy, and no-op-duplicate decisions with stable
history and one active belief for each resolved proposition and applicability context.

**Acceptance Scenarios**:

1. **Given** new evidence equivalent to active knowledge, **When** it is processed, **Then** the
   recorded decision strengthens or leaves the knowledge unchanged without blind duplication.
2. **Given** conflicting evidence, **When** it cannot be safely resolved, **Then** it remains
   visible as a contradiction rather than becoming active knowledge.
3. **Given** a privacy or policy conflict, **When** activation is forbidden, **Then** the decision
   records why and does not expose the content.

---

### User Story 4 - Receive bounded knowledge for the current task (Priority: P1)

An agent submits an actual task or topic shift and receives a small, relevant, project- and
privacy-scoped knowledge packet with selection rationale; delivery is recorded as exposure.

**Why this priority**: Static recent-memory injection cannot establish relevance, safety, or
learning value for the work actually being performed.

**Independent Test**: A curated corpus verifies relevant, negative, stale, superseded,
cross-project, privacy-restricted, and empty-query behavior with bounded rendering and exactly
one exposure record per delivery.

**Acceptance Scenarios**:

1. **Given** a concrete task, **When** automatic retrieval runs, **Then** it filters scope before
   ranking and returns only a bounded, explainable packet appropriate to that task.
2. **Given** unavailable embedding or reranking capability, **When** retrieval runs, **Then** it
   reports degraded health and retains a useful deterministic fallback without corrupting
   authoritative knowledge.
3. **Given** no relevant result, **When** retrieval runs, **Then** it returns an explicit empty
   result rather than unrelated recent notes.

---

### User Story 5 - Learn from delivery and outcomes honestly (Priority: P2)

An agent's delivered knowledge and session completion produce automatic exposure and outcome
evidence that distinguishes success, partial completion, failure, abandonment, and unknown
without requiring a citation or inventing certainty.

**Why this priority**: Useful learning requires feedback that is automatic enough to cover normal
work and honest enough to preserve uncertainty.

**Independent Test**: Supported-host fixtures cover normal completion, conflicting repeat outcome,
abrupt termination, timeout, explicit feedback, and unavailable providers; each produces one
idempotent outcome disposition or an actionable retained gap.

---

### User Story 6 - Migrate and roll back without losing meaning (Priority: P2)

An operator can inventory fragmented historical identities and knowledge, review a deterministic
merge plan, apply bounded migration, inspect receipts, pause or resume safely, and roll back to
the last compatible read boundary.

**Why this priority**: The recovery touches durable multi-project data and cannot rely on an
unreviewed destructive rewrite.

**Independent Test**: A production-like legacy fixture demonstrates inventory, dry-run, accepted
merge, interrupted backfill, retry, repeat-run idempotency, count/provenance/privacy verification,
compatibility readback, and rollback rehearsal.

---

### User Story 7 - Operate one coherent product path (Priority: P2)

An operator can observe capability health and durable work state while architecture-era flags,
dead routes, duplicate orchestration, obsolete package seams, and stale claims are removed only
after replacement and observation evidence.

**Why this priority**: Flags and parallel paths currently hide incomplete integration and make
runtime status ambiguous.

**Independent Test**: A source, configuration, route, tool, hook, package, and documentation scan
proves every legacy unit has a terminal disposition and that contraction occurs only after the
replacement path and observation gate pass.

---

### User Story 8 - Preserve adjacent product primitives (Priority: P3)

An agent continues using accepted issues, documents, credentials, collections, code intelligence,
Loom, authentication, and state capabilities while the memory core changes, without silently
turning adjacent records into knowledge or redesigning their working surface.

**Why this priority**: Recovery must improve the core without breaking valid adjacent outcomes or
expanding into a separate product program.

**Independent Test**: Compatibility scenarios exercise each retained adjacent capability through
its current supported contract and prove identity handling is consistent where it is project-scoped.

---

### Edge Cases

- A project has no anchor, a malformed anchor, duplicate copied anchor, ambiguous legacy selector,
  intentional directory anchor, nested Git repository, or non-Git directory.
- A valid evidence event is duplicated, too large, redacted, malformed, privacy-restricted,
  interrupted, delayed, replayed, or processed while a worker lease is lost.
- A proposal is duplicate, contradictory, stale, superseded, policy-prohibited, or lacks enough
  evidence to activate.
- Retrieval has no task, empty candidates, unavailable providers, stale candidates, privacy or
  cross-project candidates, or an oversized render packet.
- A migration resumes after interruption, meets a merge conflict, sees a legacy client, detects
  drift, reaches contraction early, or must roll back after visible durable history exists.

## Functional Requirements

- **FR-001**: The product MUST use one canonical project identity for every project-scoped
  operation and must resolve it before data access or mutation.
- **FR-002**: The product MUST distinguish canonical project identity from identifiers, display
  metadata, paths, remotes, branches, and historical selectors.
- **FR-003**: The product MUST define explicit resolution, onboarding, ambiguity, copied-anchor,
  worktree, clone, monorepo, nested-repository, directory-scope, and non-Git behavior.
- **FR-004**: The product MUST prevent request-path creation or mutation from an unresolved,
  ambiguous, path-derived, name-derived, or hash-derived project selector.
- **FR-005**: The product MUST provide a versioned cross-client project descriptor and compatible
  migration window for supported legacy clients.
- **FR-006**: The product MUST inventory every project-bearing record, serialized field, cache
  key, import/export field, job payload, and multiple-project-role relationship before contraction.
- **FR-007**: The product MUST support deterministic project merge planning, conflict classes,
  dry-run receipts, repeat-run idempotency, privacy non-widening, provenance preservation, and
  explicit quarantine for unresolved rows.
- **FR-008**: The product MUST accept supported session or task evidence durably before
  acknowledging it and must persist source, project, principal, privacy, provenance, timestamp,
  and idempotency fingerprint.
- **FR-009**: The product MUST represent asynchronous evidence processing with durable ownership,
  attempts, errors, retry policy, timestamps, and terminal states.
- **FR-010**: The product MUST make pending work discoverable independently of global idleness,
  new-memory counts, process-local watermarks, or provider availability.
- **FR-011**: The product MUST extract bounded knowledge proposals from accepted evidence without
  making the extraction provider a precondition for evidence durability.
- **FR-012**: The product MUST reconcile each proposal to exactly one persisted terminal decision
  before it affects active knowledge.
- **FR-013**: The product MUST retain active knowledge, revisions, supporting and contradicting
  evidence, provenance, privacy, validity, applicability, and lifecycle history separately.
- **FR-014**: The product MUST treat policy and rule knowledge as a stricter class with explicit
  authority, rather than a second candidate or routing product.
- **FR-015**: The product MUST retrieve automatic context only from an actual task, explicit query,
  or defined topic shift and MUST not use recent memory as its normal relevance substitute.
- **FR-016**: The product MUST apply canonical-project, principal, privacy, status, and validity
  filters before ranking and MUST render an identity- and rationale-bearing bounded packet.
- **FR-017**: The product MUST record one idempotent retrieval exposure for each delivered packet.
- **FR-018**: The product MUST record supported-host session outcome evidence automatically and
  preserve outcome source and certainty across success, partial, failure, abandonment, and unknown.
- **FR-019**: The product MUST expose durable health, backlog, age, terminal-failure, decision,
  revision, retrieval-query, exposure, outcome-coverage, and identity-convergence metrics with
  named denominators.
- **FR-020**: The product MUST represent provider configuration and availability as capability
  health rather than alternative domain workflows; degraded capability MUST be observable.
- **FR-021**: The product MUST classify every existing package, route, hook, tool, schema family,
  flag, projection, and adjacent capability with a terminal or explicitly temporary disposition.
- **FR-022**: The product MUST remove architecture-era feature flags and duplicate orchestration
  only after replacement, compatibility, zero-drift observation, and rollback evidence.
- **FR-023**: The product MUST keep retained projections rebuildable from authoritative records,
  name their consumer and health state, and deny them independent write authority.
- **FR-024**: The product MUST preserve accepted adjacent primitives and their supported contracts
  without automatically treating their records as knowledge.
- **FR-025**: The product MUST provide expand, backfill, compare, cutover, observe, and contract
  migration boundaries with backup/export, receipt, and rollback evidence.
- **FR-026**: The product MUST return actionable retirement behavior for supported temporary
  legacy interfaces and remove them after the declared sunset gate.
- **FR-027**: The recovery MUST defer new operator working-surface design, broad graph products,
  broad temporal interfaces, SaaS expansion, new cognitive families, and unrelated modernization
  to work outside this recovery feature.
- **FR-028**: The recovery MUST deliver independently installable AR-1 through AR-7 slices, each
  with one outcome, test strategy, rollback boundary, installed verification, and observation rule.

## Success Criteria

- **SC-001**: All supported worktrees, clones, branches, nested directories, directory moves, and
  remote renames of an accepted anchored repository resolve one canonical project identity.
- **SC-002**: A copied, malformed, missing, or ambiguous project anchor causes zero project-scoped
  mutations until its prescribed onboarding or review path completes.
- **SC-003**: Every active project-bearing record in the accepted migration fixture has a canonical
  key or a visible quarantine disposition before legacy-identity contraction.
- **SC-004**: Every acknowledged evidence payload in controlled replay, interruption, restart, and
  provider-degradation scenarios reaches `completed`, `terminal_failure`, or `quarantined` with an
  inspectable receipt.
- **SC-005**: The representative installed dogfood window attributes at least 99% of valid accepted
  evidence to a terminal processing state within the configured service objective; every remainder
  is individually attributable.
- **SC-006**: The reconciliation fixture demonstrates every required terminal decision and preserves
  exactly one active belief per resolved proposition and applicability context.
- **SC-007**: Legacy knowledge in the accepted fixture remains retrievable with provenance and
  privacy preserved or is visible in an explicit quarantine ledger.
- **SC-008**: At least 90% of accepted positive queries in the curated corpus return the expected
  relevant belief in the top five; stale or superseded exposure is zero except during explicit
  historical retrieval.
- **SC-009**: Cross-project and privacy-restricted retrieval leakage is zero in the curated corpus.
- **SC-010**: Every automatic rendered packet stays within the declared item and token budget, has
  an explicit empty-result behavior, and generates exactly one exposure record.
- **SC-011**: At least 95% of normally completed supported-host dogfood sessions have automatic
  terminal outcome evidence; every remainder is explicitly unknown, abandoned, or diagnosed.
- **SC-012**: Every retained outcome or confidence change is traceable to stored evidence and can
  be reversed through recorded knowledge revision rather than destructive overwrite.
- **SC-013**: A production-like identity migration fixture proves per-table counts, provenance,
  privacy, conflict disposition, repeat-run idempotency, and rollback rehearsal for every accepted
  merge group.
- **SC-014**: A source/configuration/package/route/tool/hook scan finds zero unclassified
  project-bearing path, architecture-era flag, deleted capability, or legacy identity algorithm.
- **SC-015**: The final recovery release has zero active architecture-era flags, duplicate
  injection branches, dead hook/route contracts, or maturity-flag consumer in supported current
  artifacts.
- **SC-016**: Every preserved projection has a named consumer, authoritative source, rebuild
  procedure, and health state; no projection is required to accept evidence or resolve identity.
- **SC-017**: Accepted adjacent issue, document, credential, collection, code-intelligence, Loom,
  authentication, and state workflows pass their versioned compatibility checks after each relevant
  release slice.
- **SC-018**: AR-1 through AR-7 each build from clean current `main`, produce an installed release
  receipt, and do not depend on unobserved behavior from the immediately preceding slice.

## Operational Acceptance Declarations

### Evidence Processing Service Objective

Before AR-4 dogfood can claim SC-005, the active release configuration receipt MUST declare one
finite positive `evidence_processing_terminal_objective` duration, its measurement start event,
eligible evidence definition, owner (`AR-4 session-evidence owner`), effective release, and
rollback/revision reference. The value is selected from AR-1 baseline evidence and approved in the
AR-4 release configuration; it is not inferred from an empty metric. Without an active declaration,
SC-005 is `not_computable` and AR-4 cannot claim its terminal-attribution exit criterion.

### Retrieval Render Budget

Before AR-6 corpus/cutover can claim SC-010, the active retrieval policy receipt MUST declare a
stable `render_budget_id`, finite positive `max_items`, finite positive `max_tokens`, owner
(`AR-6 retrieval owner`), effective release, fallback behavior, and rollback/revision reference.
The budget is selected from the AR-6 measured corpus and is tested at its active value. Without an
active declaration, SC-010 is `not_computable` and the static-context cutover is blocked.

## Key Entities

- **Canonical Project**: An immutable logical-project identity with mutable display metadata and
  historical identifiers.
- **Project Identifier and Merge Record**: Historical identity evidence and auditable convergence
  decisions distinct from the canonical project.
- **Session Evidence**: Immutable accepted session or task signal with privacy, provenance, and
  fingerprint.
- **Processing Job**: Durable work ownership and terminal-state record for accepted evidence or
  migration activity.
- **Knowledge Proposal**: Bounded claim offered by evidence for reconciliation.
- **Knowledge Belief and Revision**: Active reusable proposition and append-only history of changes
  and supporting or contradicting evidence.
- **Retrieval Exposure**: Append-only record that a belief was selected, rendered, and delivered
  for a concrete task or query.
- **Session Outcome Evidence**: Automatic or explicit completion evidence with source and certainty.
- **Capability Health**: Observable configured, available, degraded, paused, or failed status for
  external capabilities and operational controls.
- **Migration Receipt and Quarantine Item**: Auditable progress, conflict, unresolved-data, and
  rollback-boundary record.

## Scope Boundaries

### In Scope

- Canonical project identity, durable session evidence, proposal reconciliation, task-aware
  retrieval, automatic exposure and outcome evidence, safe migration and contraction, honest
  operability, projection disposition, adjacent-capability continuity, and AR-1 through AR-7
  recovery sequencing.

### Out of Scope

- New operator working-surface design in this recovery feature. The separately accepted Operator Code Console exception in Constitution Principle XII remains outside this feature's work, evidence, and release authority.
- New graph products or graph-first retrieval.
- Broad temporal-truth user interfaces.
- SaaS or multi-tenant expansion.
- New cognitive feature families.
- Cosmetic or unrelated modernization.

### Constitution 2.0 revalidation

Constitution 2.0.0 permits the separately accepted Operator Code Console and basic collection feature after UCI technical acceptance. It does not change this recovery's outcomes, AR-1 through AR-7 sequencing, or no-surface implementation boundary. Feature 011 owns the exception; this recovery feature does not.

## Assumptions

- The recovery remains one modular product with authoritative durable storage and no new network
  service.
- Implementation begins only after explicit operator authorization in a fresh implementation
  session.
- Existing historical plans, release reports, flags, comments, and tests are evidence to reconcile,
  not active authority unless adopted by the recovery artifacts.
- Production access, deployed configuration, live data mutation, and global tooling changes are
  outside AR-0.
- Acceptance thresholds that require baseline measurement are established before the release slice
  that claims them; no current implementation is presumed to satisfy them.

## Dependencies

- The ratified recovery constitution.
- Fresh source, schema, client, and installed-version inventories.
- A production-like legacy fixture, backup/export capability, and release-by-release installed
  dogfood evidence for implementation acceptance.
