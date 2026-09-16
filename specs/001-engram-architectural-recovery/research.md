# Research: Engram Architectural Recovery

**Feature**: `001-engram-architectural-recovery`  
**Research status**: Source-fresh as of the AR-0 checkout; no live database, deployment, or
runtime mutation was performed.  
**Evidence rule**: Intake files are normative decisions; every current-state statement below is
backed by fresh local source inspection or explicitly marked unavailable under AR-0 scope.

## Freshness and Baseline Drift

- **VERIFIED**: The AR-0 branch begins at local `fb18daab`, one commit ahead of `origin/main`
  `27eb443c`. The one-line SonarQube rule expected by intake as a dirty `AGENTS.md` change is
  already committed locally. Its byte hash matches `HEAD`; AR-0 does not modify or stage it.
  See `.specify/memory/ar-0-preflight.json` and `.specify/memory/ar-0-inherited-AGENTS.diff`.
- **VERIFIED**: `.specify/` was absent before AR-0 and was initialized with Spec Kit 0.16.4 and
  OMP integration. No application, test, migration, deployment, UI, plugin runtime, or global
  configuration path was changed.
- **VERIFIED**: The stated dead automatic outcome endpoints are still absent from server routing:
  `plugin/engram/hooks/session-end.js` calls `/api/sessions/{id}/propagate-outcome`; the OpenClaw
  client calls `/api/sessions/{id}/outcome`; `internal/worker/service.go` registers neither.
- **VERIFIED**: The stated timer-gated transcript-to-candidate path remains present:
  `stop.js` posts session evidence-like transcript data to `/api/hooks/session-end`, the handler
  writes `session_transcripts` only under crystallization gating, and the dream cycle requires
  four-hour idle and memory-count gates before candidate-only extraction.
- **NOT PROVEN IN AR-0**: actual production table contents, live row counts, provider health, and
  installed client population. AR-0 may not query or mutate the live database/deployment. AR-1
  owns a redacted read-only baseline and fixture capture before any migration.

## R-01: Recovery Is a D3 Program, Not a Feature Toggle Sweep

**Decision**: Treat recovery as a D3, multi-consumer, irreversible program delivered by AR-1
through AR-7; preserve a single active Spec Kit triplet as the execution authority.

**Rationale**: Identity convergence, durable evidence, and eventual contraction affect every
transport, durable record, and installed client. The constitution makes their dependency and
migration contracts authoritative.

**Alternatives considered**:

- Enable existing maturity flags: rejected because current flags select competing architectures
  and leave the loop incomplete.
- Rewrite a replacement service beside Engram: rejected by the operator decision to keep a Go
  modular monolith and release small installed slices.
- Preserve every currently implemented subsystem: rejected because live consumers and accepted
  outcome, not code existence, determine preservation.

**Source evidence**: `cmd/engram-server/main.go` starts the one worker service; `go.mod` confirms
one Go module with PostgreSQL/GORM, gRPC, HTTP, and daemon dependencies. Current architecture
spans HTTP, gRPC, MCP proxy, hooks, worker, and database modules.

**Migration/compatibility impact**: Each AR release must be independently installable from clean
`main`; no hidden replacement branch is allowed.

## R-02: Project Identity V3 Becomes the Only Scoped Authority

**Decision**: Introduce an immutable server canonical project key anchored by a tracked V3 root
file. Treat all paths, names, remote values, hashes, legacy selectors, and aliases as evidence or
compatibility identifiers, never as tenant keys.

**Rationale**: Current identity is a compatibility pipeline rather than one durable identity. Go
and JavaScript independently derive path/remote-hash selectors; the server resolves V2 metadata
into a text `projects` row; code-index stores use a separate FK-free slug; hook caches remain
selector-keyed after server canonicalization.

**Alternatives considered**:

- Retain V2 bindings and harden aliases: rejected because selector identity remains mutable and
  data-bearing paths remain fragmented.
- Use normalized remote as canonical: rejected because remote changes, forks, non-Git directories,
  and intentional directory-scoped projects require an anchor independent of remote text.
- Use display name: rejected because names are neither unique nor durable.

**Source evidence**: `internal/proxy/identity.go`, `plugin/engram/hooks/lib.js`,
`internal/grpcserver/server.go`, `internal/db/gorm/project_store.go`,
`internal/handlers/engramcore/slugcache.go`, `plugin/engram/hooks/session-start.js`.

**Clarification resolution**: Clones/worktrees/branches/moves/remotes sharing a valid anchor are
one logical project. A copied anchor in a fork fails closed before mutation until an explicit
same-project or rotate-anchor decision; this is settled in intake `06-PROJECT-IDENTITY-V3.md`, not
an open tenant-boundary question.

**Migration/compatibility impact**: V3 requires an inventory, identifier extraction, merge plan,
nullable typed-key expansion, dual-write/read comparison at application boundaries, restartable
backfill, observation, then contraction. Legacy selector-only clients can resolve only known
identifiers during a bounded window and cannot mint identity.

## R-03: Project-Bearing Data Must Be Enumerated Before Any Merge

**Decision**: Build a machine-readable table/field/payload inventory and merge manifest before
adding authoritative keys or merging any logical projects.

**Rationale**: Fresh source inspection found at least 20 definitely current project-bearing table
families and one additional migration-created `reasoning_traces` family whose runtime liveness is
not provable under AR-0. Serialized snapshot JSON, export/import manifests, hook caches, job
payloads, and multiple-role issue fields extend the boundary beyond ordinary columns.

**Alternatives considered**:

- Merge only the `projects` aliases: rejected because aliases do not rewrite historical rows,
  caches, payloads, imports, exports, or multiple project roles.
- Infer merges from matching names or paths: rejected because it can collapse unrelated projects
  and widen privacy scope.
- Block all later releases until production data is directly inspected: rejected because AR-1 can
  produce a read-only inventory and safe fixture without mutating production.

**Source evidence**: `internal/db/gorm/models.go`, `migrations.go`,
`migration_rule_governance.go`, `migration_temporal_truth.go`,
`internal/governance/export.go`, `state_store.go`, `rule_governance_store.go`,
`dream_cycle.go`, `handlers_backfill.go`, `grpcserver/code_index.go`.

**Migration/compatibility impact**: Unknown rows become quarantine items with evidence and an owner;
they are never assigned by similarity. Merge groups use anchors, explicit approval, corroborated
legacy evidence, and deterministic conflict policy.

## R-04: Accepted Session Evidence Needs a Durable Job State Machine

**Decision**: Replace timer-defined transcript processing with durable `SessionEvidence` and
`ProcessingJob` records. Acknowledgment occurs only after durable intake; providers and timers
control processing availability, not evidence existence.

**Rationale**: Current stop-hook transcript persistence is gated by crystallization, returns 202
before asynchronous persistence, and later dream processing depends on four-hour idleness,
new-memory counts, flags, an in-process watermark, candidate store, and LLM availability.

**Alternatives considered**:

- Reduce the dream-cycle interval: rejected because a shorter timer still makes time and unrelated
  activity determine whether work exists.
- Keep transcript rows as the job queue: rejected because they lack explicit ownership, attempts,
  next-attempt policy, terminal state, and item-level receipt.
- Require LLM availability on callback: rejected because provider degradation must not lose
  accepted evidence.

**Source evidence**: `plugin/engram/hooks/stop.js`, `internal/worker/handlers_hooks.go`,
`internal/db/gorm/transcript_store.go`, `internal/worker/sleep_cycle.go`,
`internal/worker/dream_cycle.go`, `internal/crystallization/candidate_gate.go`.

**Migration/compatibility impact**: AR-4 introduces an additive intake/job boundary and a replay
adapter for legacy transcripts. Existing transcript read paths remain available until processing
receipts and observation prove parity.

## R-05: Outcome Evidence Must Be an Explicit Durable Contract

**Decision**: Replace dead propagation callbacks and opt-in-only outcome writes with supported-host
automatic outcome evidence that records source and certainty; retain explicit feedback as an
additional signal.

**Rationale**: The only verified current outcome writer is the MCP feedback action to
`sdk_sessions`. Claude and OpenClaw automatic outcome URLs have no server route. The backfill
endpoint creates a synthetic SDK session and returns deprecated zero counters; it does not persist
the transcript/intake outcome claimed by callers.

**Alternatives considered**:

- Treat process exit as success: rejected because it fabricates epistemic certainty.
- Keep citation detection as outcome evidence: rejected because citation is optional and sparse.
- Leave automatic callbacks fail-soft: rejected because a missing route becomes invisible loss.

**Source evidence**: `plugin/engram/hooks/session-end.js`,
`plugin/openclaw-engram/src/client.ts`, `plugin/openclaw-engram/src/hooks/session-end.ts`,
`internal/worker/service.go`, `handlers_stats.go`, `handlers_backfill.go`,
`internal/mcp/tools_feedback.go`, `internal/db/gorm/session_store.go`.

**Migration/compatibility impact**: AR-1 exposes dead callback state and establishes baseline
metrics; AR-4 supplies versioned durable outcome intake with unknown/abandoned timeout semantics.

## R-06: Knowledge Reconciliation Replaces Candidate-Only Parallel Truth

**Decision**: Model evidence, proposals, active beliefs, revisions, contradictions, and exception
review as distinct authoritative records. A proposal must receive one recorded decision before it
changes active knowledge.

**Rationale**: Current dream extraction routes decisions to a gated candidate store; promotion,
decay, bulk operations, lifecycle, rule governance, and memory writes remain separate authorities.

**Alternatives considered**:

- Promote every candidate to a memory: rejected because it bypasses contradiction, provenance,
  privacy, and revision semantics.
- Extend the current candidate status machine: rejected because it conflates proposals, review,
  active knowledge, and historical revisions.
- Reintroduce demolished scoring code: rejected because recovery must create the required outcomes
  through new contracts rather than copy obsolete implementations.

**Source evidence**: `internal/crystallization/candidate_gate.go`,
`internal/db/gorm/candidate_store.go`, `internal/bulkops/facade.go`,
`internal/lifecycle/sleep.go`, `internal/mcp/tools_candidates.go`,
`internal/mcp/rule_governance_intent.go`.

**Migration/compatibility impact**: AR-5 maps legacy memory and candidate data through a migration
adapter, maintains one authoritative new-write path, and quarantines unresolved semantics.

## R-07: One Task-Aware Retrieval Workflow Replaces Static Context Assembly

**Decision**: Build one query-driven retrieval and rendering workflow over active knowledge,
recording exposure for each delivery. Session start is limited to state and universal policy.

**Rationale**: Current session start uses SQL-backed issues, recent memories, VNext scoring,
continuity slot, and rule-router variants before a substantive task exists. Worker context inject
and MCP recall follow separate paths with different flag, privacy, lifecycle, and fallback
semantics.

**Alternatives considered**:

- Tune static recent-memory scoring: rejected because no task query exists to establish relevance.
- Retain independent worker and MCP ranking authorities: rejected because selection rationale and
  exposure cannot be consistent across competing product paths.
- Require embedding/reranking providers: rejected because lexical fallback must remain usable and
  capability loss must be observable.

**Source evidence**: `internal/grpcserver/session_start.go`,
`internal/worker/handlers_context.go`, `internal/worker/retrieval.go`,
`internal/mcp/tools_recall.go`, `internal/mcp/tools_memory.go`,
`plugin/engram/hooks/session-start.js`.

**Migration/compatibility impact**: AR-6 runs shadow comparisons over a curated corpus, replaces
normal static injection only after bounded-packet/exposure/outcome gates, and leaves old client
names as thin aliases only where a consumer remains.

## R-08: Architecture-Era Flags Contract into Capability Health

**Decision**: Delete VNext, V7, milestone, candidate, lifecycle, legacy injection, and
architecture-selection flags after replacement evidence. Retain only typed operational capability,
privacy, provider, budget, and emergency-mode configuration with owner and expiry requirements.

**Rationale**: Current flags have direct environment readers, config-backed readers, startup
snapshots, per-request reads, and inconsistent boolean parsing. The runtime flag endpoint does not
always report effective state, and documentation omits some exposed switches.

**Alternatives considered**:

- Centralize existing flags without deleting them: rejected because the product would retain
  competing runtime architectures.
- Turn all controls on immediately: rejected because this hides migration and compatibility risk.
- Leave flags as permanent operator choices: rejected because maturity is not valid deployment
  variability.

**Source evidence**: `internal/config/config.go`, `internal/worker/handlers_system.go`,
`internal/worker/service.go`, `internal/cognitive/core/flag_config.go`,
`internal/grpcserver/session_start.go`, `internal/worker/sleep_cycle.go`,
`internal/crystallization/candidate_gate.go`, `.env.example`,
`docs/arch/CONFIGURATION.md`.

**Migration/compatibility impact**: AR-1 inventories all readers/defaults/consumers; AR-2 through
AR-6 replace their semantics; AR-7 removes each flag together with code, docs, tests, mocks,
manifests, routes, tools, and UI claims.

## R-09: Projections and Adjacent Primitives Remain Separate

**Decision**: Preserve issues, documents, vault, collections, Loom, code intelligence,
authentication, and State Plane as adjacent capabilities. Classify graph, summaries,
meta-memory, clusters, indexes, and effectiveness views as rebuildable projections or delete them
after a terminal decision.

**Rationale**: Current HTTP/gRPC/MCP/daemon surfaces expose these capabilities with different
consumers and gates. Issues and vault have direct UI consumers; many other valid server/tool
surfaces have no direct UI consumer, which is not evidence of absence or deletion authority.

**Alternatives considered**:

- Convert every project-scoped adjacent record into a belief: rejected because it erases native
  semantics and violates authority separation.
- Delete all non-core routes: rejected because accepted adjacent contracts and daemon consumers
  would break.
- Preserve graphs as a second memory write authority: rejected by the constitution.

**Source evidence**: `internal/worker/service.go`, `internal/mcp/server.go`,
`proto/engram/v1/engram.proto`, `cmd/engram/wiring.go`,
`internal/handlers/{engramcore,loom,codeintel}`, `ui/src/router/index.ts`,
`ui/src/composables/useIssues.ts`, `ui/src/composables/useVault.ts`.

**Migration/compatibility impact**: AR-3 applies V3 identity to adjacent data; AR-7 performs
terminal keep/delete/projection decisions and removes stale endpoints such as the retired feedback
import response with versioned retirement behavior where consumers exist.

## R-10: Security, Privacy, and Operability Are Release Gates

**Decision**: Treat identity, privacy scope, credential metadata, transcript redaction, export,
audit, job lease loss, provider degradation, backup/export, and rollback as first-class
acceptance contracts. No secret content enters specifications, inventories, receipts, or
fingerprints.

**Rationale**: Recovery alters durable project scope, retained agent outputs, credentials, and
migration paths. The effective risk class is S4 for future data migration and production rollout;
AR-0 only documents the controls.

**Alternatives considered**:

- Rely on auth-disabled dogfood evidence: rejected because it cannot establish production
  authorization behavior.
- Snapshot raw transcript or credential values for analysis: rejected because it leaks sensitive
  data and is unnecessary for structural inventory.
- Defer rollback design until migration implementation: rejected because contraction becomes
  irreversible without a rehearsed boundary.

**Source evidence**: `internal/worker/handlers_hooks.go` redacts transcript text before storage;
`internal/worker/service.go` route readiness/auth grouping; `internal/worker/handlers_vault.go`;
`internal/grpcserver/server.go` identity error boundary; `internal/governance/export.go`.

**Migration/compatibility impact**: Every migration slice requires fixture-level backup/restore,
redacted inventory, audit receipts, and a defined last-compatible release boundary.

## R-11: Package, Route, Hook, and Tool Disposition Is a Program Ledger

**Decision**: Use one source disposition inventory with `target-core`, `move-and-reshape`,
`projection`, `adjacent`, `compatibility-temporary`, `quarantine-pending-proof`, `delete`, or
`historical-immutable` for every recovery-relevant unit.

**Rationale**: Current HTTP routes, gRPC tools, MCP advertisement, daemon modules, hooks, and UI
consumers are not equivalent; a route returning 410, a hidden tool, an absent UI consumer, and a
live daemon module each need different treatment.

**Alternatives considered**:

- Infer deletion from missing UI: rejected because daemon/MCP consumers can be live without a UI.
- Preserve 410 routes as current products: rejected because they are explicit retirement seams.
- Delete only source packages: rejected because flags, docs, tests, mocks, navigation, packaging,
  and consumer adapters would preserve false architecture.

**Source evidence**: `internal/worker/service.go`, `handlers_backfill.go`, `internal/mcp/server.go`,
`internal/mcp/server_test.go`, `cmd/engram/wiring.go`, hook scripts, and UI route/composable files.

**Migration/compatibility impact**: Each removal has source references, inbound/outbound consumers,
replacement or reason, affected data, compatibility and rollback, first-unused release,
contraction release, verifier, and receipt.

## Research Debts and Boundaries

- **AR-1 research debt**: Read-only production table/row counts, live selector fragmentation,
  client-version census, feature-value census, and a redacted legacy fixture must be acquired under
  the implementation session's explicit operational authorization.
- **AR-2 research debt**: Cross-language V3 descriptor vectors and every client integration require
  exact client inventory after the AR-1 baseline.
- **AR-3 research debt**: Merge groups, destructive conflict dispositions, and production backup
  evidence require operator-approved manifests; AR-0 does not invent them.
- **AR-4 to AR-6 research debt**: Provider reliability, task corpus thresholds, dogfood traffic,
  latency budgets, and outcome coverage require measured installed baselines.
- **AR-7 research debt**: Projection consumer/rebuild decisions must be finalized only after the
  preceding installed observation receipts prove their replacement or independent contract.
