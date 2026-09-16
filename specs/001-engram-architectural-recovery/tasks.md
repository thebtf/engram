# Tasks: Engram Architectural Recovery

**Feature**: `001-engram-architectural-recovery`  
**Input**: `spec.md`, `plan.md`, `research.md`, `data-model.md`, `contracts/`, `quickstart.md`,
`evidence/`, and `analysis/traceability-contract.md`  
**Execution boundary**: These tasks are implementation work for a fresh, explicitly authorized
implementation session. AR-0 does not execute them.  
**Organization**: Independently installable release slices AR-1 through AR-7. Test-first tasks are
mandatory for identity, migration, reconciliation, retrieval, and rollback behavior.

## Task Format and Traceability Rules

- Every task cites its primary user story plus FR/SC or an explicit governance obligation.
- `[P]` means the task can run in parallel only after its listed prerequisites are complete and its
  exact file authority does not overlap another active task.
- Tasks that alter a durable schema, resolver, workflow, or deletion ledger require a maker,
  independent checker, migration verifier where relevant, and final release verifier.
- No task authorizes new operator working-surface design.

## Dependencies and Release Order

```text
AR-1 baseline/fences
  -> AR-2 V3 expand
    -> AR-3 convergence/cutover
      -> AR-4 durable evidence/outcome
        -> AR-5 knowledge reconciliation
          -> AR-6 task-aware retrieval/learning
            -> AR-7 contraction/closure
```

- AR-2 cannot begin canonical write cutover until AR-1 inventory and fixture receipt exist.
- AR-3 cannot merge or contract until AR-2 descriptor vectors, additive schema, and dual-compare
  receipts pass.
- AR-3 creates the reusable ProcessingJob schema before T035/T036 identity backfill work; AR-4 adds only SessionEvidence and its evidence-processing linkage to that already durable job substrate.
- AR-4 and AR-5 share the durable job contract but AR-5 cannot activate beliefs before AR-4
  durable evidence semantics are installed.
- AR-6 cannot retire normal static context until AR-5 beliefs/revisions and AR-4 outcomes exist.
- AR-6 T076 static-context cutover is blocked until T075's active scope-matched render-budget policy receipt and both-branch emergency-control test pass.
- AR-7 cannot delete a legacy path before its replacement, compatibility sunset, backup/rollback,
  nonzero observation denominator, and exact source/configuration scan pass.

## Phase 1 — AR-1: Truth, Fences, and Broken-Loop Closure

**Goal**: Make current incompleteness visible, prevent new legacy identity creation, and establish
safe inventories/fixtures without merging projects or deleting capability.

- [ ] T001 [US7] Create redacted current-source inventory command in `internal/recoveryinventory/source_scan.go` — FR-021, SC-014.
- [ ] T002 [P] [US7] Add feature-flag/default/reader inventory producer in `internal/recoveryinventory/flag_scan.go` — FR-021, FR-022.
- [ ] T003 [P] [US7] Add package/route/hook/tool and current-documentation claim scanner in `internal/recoveryinventory/surface_scan.go` — FR-021, FR-022.
- [ ] T004 [P] [US6] Add project-bearing relational/payload/cache/job/import/export-field inventory producer in `internal/recoveryinventory/project_data_scan.go` — FR-006, FR-025.
- [ ] T005 [US7] Add durable metric-denominator model and read-only baseline report in `internal/operability/baseline_metrics.go`, then align existing zero-denominator HTTP metric renderers in `internal/worker/handlers_stats.go` and `internal/worker/handlers_data.go` to explicit `not_computable` semantics — FR-019.
- [ ] T006 [P] [US5] Write failing adapter-contract tests for current automatic outcome callback routes in `internal/worker/outcome_adapter_contract_test.go` — FR-018.
- [ ] T007 [US5] Replace silent missing-outcome callback handling with versioned actionable retirement diagnostics in `internal/worker/handlers_outcome_retirement.go` — FR-018, FR-026.
- [ ] T008 [P] [US1] Add failing selector-only project-creation fence tests in `internal/projectidentity/legacy_fence_test.go` — FR-004.
- [ ] T009 [US1] Enforce no-new legacy-selector project creation and credential-safe remote identity evidence at `internal/db/gorm/project_store.go`, `internal/proxy/identity.go`, and `internal/handlers/engramcore/grpcpool.go` boundaries — FR-004.
- [ ] T010 [US6] Create synthetic fixture export/restore and database ownership/manifest-binding contract in `scripts/recovery/prepare-legacy-fixture.ps1` and recovery helpers — FR-025, SC-013.
- [ ] T011 [US7] Create fixture server health command, exact built-payload provenance, and owned process/run binding contract in `scripts/recovery/start-fixture-server.ps1`, `Makefile`, `cmd/engram-server/main.go`, and `internal/worker/handlers.go` — FR-019, FR-020.
- [ ] T012 [US7] Create named scenario runner and receipt verifier that binds behavior to the live owned fixture process/payload in `scripts/recovery/run-recovery-scenario.ps1` and `scripts/recovery/verify-recovery-receipt.ps1` — FR-028, SC-018.
- [ ] T013 [US7] Add AR-1 installed baseline receipt schema/producer in `internal/recoveryreceipt/ar1_baseline.go` — FR-019, FR-028.
- [ ] T014 [US7] Run independent AR-1 source-inventory and dead-contract checker against `internal/recoveryinventory/` and `internal/worker/` — governance obligation.
- [ ] T015 [US7] Run AR-1 clean-main exact-source build, fixture, installed dogfood, source-commit/payload readback, and observation receipt through `scripts/recovery/run-recovery-scenario.ps1` — SC-018.

**Independent test criterion**: A fresh installed AR-1 reports named nonzero or `not_computable`
denominators, exposes the dead outcome callback contract, prevents selector-only tenant creation,
and emits a redacted baseline/fixture receipt.

## Phase 2 — AR-2: Project Identity V3 Expand

**Goal**: All supported adapters present one V3 anchor descriptor and new scoped workflows resolve a
canonical key while legacy rows/columns remain available.
**Governor correction:** `AR2_GOVERNOR_CORRECTION_2026-08-23_R1` makes T020 the active test-only seam gate and holds T017/T018/T019 until the reviewed RED commit is integrated.

- [ ] T016 [US1] Add failing cross-language V3 anchor/descriptor vectors in `contracts/testdata/project_identity_v3_vectors.json` — FR-005, SC-001.
- [ ] T017 [P] [US1] After T020 freezes RED evidence, implement V3 `.engram-project` parser/validator, tracked repository-root and explicit directory discovery, in-place Engram anchor migration, and the Go descriptor seam in `internal/projectidentity` — FR-001, FR-003.
- [ ] T018 [P] [US1] After T020 freezes RED evidence, implement matching hook-side descriptor module `plugin/engram/hooks/project-identity-v3.js` and adapt `lib.js`/`session-start.js` registration and selector-cache boundaries — FR-005.
- [ ] T019 [P] [US1] After T020 freezes RED evidence, implement matching OpenClaw descriptor module `plugin/openclaw-engram/src/project-identity-v3.ts` and adapt `identity.ts`, `client.ts`, `config.ts`, `index.ts`, and actual hook consumers — FR-005.
- [ ] T020 [US1] Freeze test-only shared-vector RED harnesses for Go `internal/projectidentity` (`ParseAnchorV3`, `DiscoverAnchorV3`, `NormalizeGitRemoteV3`, `BuildDescriptorV3`), hook `project-identity-v3.js`, and OpenClaw `src/project-identity-v3.ts` — FR-005, SC-001.
- [ ] T021 [US1] Add migration `162+` with nullable V3 fields only on `projects` (`project_key`, `anchor_project_id`, `identity_scope`, `identity_status`) plus new `project_identifiers` and `project_merge_audits`; defer every other existing family’s typed key to AR-3 — FR-001, FR-025.
- [ ] T023 [US1] Add typed V3 intents, outcomes, and scope/admin-filter audit boundary in `internal/projectidentity/errors.go` and related value types — FR-003, FR-004.
- [ ] T024 [P] [US1] Adapt gRPC identity resolution through V3 protobuf schema, generated bindings, `Makefile` generation, `internal/grpcserver/server.go`, and `session_start.go` — FR-001, FR-005.
- [ ] T025 [P] [US1] Adapt HTTP context and hook identity intake to the V3 workflow in `internal/worker/handlers_context.go` and route-level refusal tests — FR-001, FR-005.
- [ ] T026 [P] [US1] Adapt MCP/daemon identity boundary in `internal/mcp/`, `internal/handlers/engramcore/{slugcache,module,tools,grpcpool}.go`, and `cmd/engram/wiring.go` — FR-001, FR-005.
- [ ] T027 [US1] Add V3 dual-write/shadow-read comparison receipt in `internal/projectidentity/comparison.go` — FR-005, FR-025.
- [ ] T028 [US1] Add real-fixture behavior tests for worktree/clone/move/remote/nested/non-Git/copied/malformed/ambiguous V3 resolution in `internal/projectidentity/resolver_integration_test.go` — FR-003, SC-001, SC-002.
- [ ] T029 [US1] Add AR-2 compatibility-window and descriptor telemetry receipt in `internal/recoveryreceipt/ar2_identity_expand.go` — FR-005, FR-019.
- [ ] T030 [US1] Run independent AR-2 identity, migration, security, and transport checks plus locally staged dual-representation dogfood using `contracts/project-identity-v3.md` — SC-001, SC-002.

**Independent test criterion**: All supported adapters resolve the same key for one anchored
fixture, unsafe anchors mutate neither representation, and installed dogfood proves V3/legacy
comparison without legacy contraction.

### AR-2 Delta Dependencies

- T016 freezes the versioned descriptor/vector contract before T020 captures test-only RED against the named production module seams.
- T017, T018, and T019 begin in parallel only after the T020 RED commit is integrated and reviewed.
- T021 and T023 may run in parallel after Wave A; T022 begins only after both expose the additive storage and typed-outcome seams.
- T024, T025, and T026 are parallel adapter translations only after T022; they may not define resolver policy or mutate canonical bindings.
- T027 precedes T028/T029 comparison evidence; T030 and T088–T091 accept only the exact integrated candidate.


## Phase 3 — AR-3: Project Identity Convergence and Cutover

**Goal**: Historical project data converges through approved, restartable, meaning-preserving merge
operations and typed keys become authoritative before contraction.

- [ ] T031 [US6] Add failing project-bearing inventory completeness tests in `internal/projectidentity/inventory_test.go` — FR-006, SC-003.
- [ ] T032 [US6] Implement identity identifier extraction and relational/payload/cache/job/import/export-field inventory in `internal/projectidentity/inventory.go` — FR-006.
- [ ] T033 [US6] Implement dry-run merge manifest generation against `contracts/project-merge-manifest.schema.json` in `internal/projectidentity/merge_manifest.go` — FR-007.
- [ ] T034 [US6] Add merge-manifest schema/vector tests in `internal/projectidentity/merge_manifest_test.go` — FR-007.
- [ ] T035 [US6] Add reusable ProcessingJob schema and typed-key backfill job kind in `internal/db/gorm/migration_project_identity_v3.go` and `internal/projectidentity/backfill_job.go` — FR-007, FR-025.
- [ ] T036 [US6] Add interrupted/resumed/idempotent backfill fixture tests in `internal/projectidentity/backfill_job_integration_test.go` — FR-007, SC-013.
- [ ] T037 [US6] Implement deterministic conflict/quarantine ledger handling in `internal/projectidentity/merge_conflicts.go` — FR-007.
- [ ] T038 [US6] Implement bounded merge apply and audit receipt in `internal/projectidentity/merge_apply.go` — FR-007, FR-025.
- [ ] T039 [P] [US8] Cut over adjacent issues/documents/credentials/state identity boundaries and migrate continuity-slot state into State Plane with an AR-3 receipt in `internal/db/gorm/` and `internal/worker/` — FR-013, FR-024, SC-017.
- [ ] T040 [P] [US8] Cut over code-index identity boundary and cache identifiers in `internal/grpcserver/code_index.go` and `internal/handlers/codeintel/` — FR-006, FR-024.
- [ ] T041 [US6] Add merge receipt, per-family semantic payload-preservation, and rollback rehearsal in `internal/projectidentity/merge_receipt.go` — FR-025, SC-013.
- [ ] T042 [US6] Add legacy redirect/read compatibility and sunset counters in `internal/projectidentity/compatibility.go` — FR-005, FR-026.
- [ ] T043 [US6] Run AR-3 independent migration verifier, fixture apply/replay/rollback, and installed observation through `scripts/recovery/run-recovery-scenario.ps1` — FR-007, SC-003, SC-013.

**Independent test criterion**: Every accepted fixture row has a typed key or quarantine item;
merge receipts prove per-family semantic preservation, privacy non-widening, idempotency, and
rollback without legacy-column contraction.

## Phase 4 — AR-4: Durable Session Evidence and Outcome

**Goal**: Completed/interrupted work is durably accounted for independent of hooks, providers,
timers, and process restarts.

- [ ] T044 [US2] Add failing durable evidence acknowledgment/replay tests in `internal/sessionintake/intake_integration_test.go` — FR-008, SC-004.
- [ ] T045 [US2] Add SessionEvidence schema and evidence-processing linkage to the reusable ProcessingJob substrate in `internal/db/gorm/migration_session_evidence.go` — FR-008, FR-009.
- [ ] T046 [US2] Implement `AcceptSessionEvidence` atomic receipt workflow in `internal/sessionintake/accept.go` — FR-008.
- [ ] T047 [US2] Implement persisted lease/fencing/retry/terminal job repository in `internal/sessionintake/job_store.go` — FR-009, FR-010.
- [ ] T048 [US2] Add job crash/restart/stale-owner/duplicate/provider-failure tests in `internal/sessionintake/job_store_integration_test.go` — FR-009, FR-010, SC-004.
- [ ] T049 [US2] Adapt Claude Stop and supported hook intake to the versioned durable receipt in `plugin/engram/hooks/stop.js` and `internal/worker/handlers_hooks.go` — FR-008.
- [ ] T050 [P] [US5] Implement versioned automatic outcome adapter contract in `internal/learning/outcome_intake.go` — FR-018.
- [ ] T051 [P] [US5] Adapt Claude and OpenClaw terminal-event clients in `plugin/engram/hooks/session-end.js` and `plugin/openclaw-engram/src/hooks/session-end.ts` — FR-018, FR-026.
- [ ] T052 [P] [US5] Implement outcome certainty/timeout/conflict retention in `internal/learning/outcome_store.go` — FR-018.
- [ ] T053 [US5] Add automatic outcome duplicate/timeout/conflict/adapter-gap tests in `internal/learning/outcome_store_integration_test.go` — FR-018, SC-011.
- [ ] T054 [US2] Replace sleep-cycle demand gating with durable eligible-job polling in `internal/sessionintake/worker.go` — FR-010.
- [ ] T055 [US2] Add transcript replay adapter and migration receipt in `internal/sessionintake/legacy_transcript_adapter.go` — FR-008, FR-025.
- [ ] T056 [US5] Implement and test the versioned RecoveryAcceptancePolicy, evidence-processing service objective, backlog/age/failure/outcome metrics, and capability-health receipt in `internal/operability/acceptance_policy.go` and `internal/operability/evidence_metrics.go` — FR-019, FR-020, SC-005.
- [ ] T057 [US2] Run AR-4 fault-injection, restart/replay, provider-degraded, acceptance-policy, and installed dogfood evidence receipt — SC-004, SC-005, SC-011.

**Independent test criterion**: Every fixture event receives a durable receipt and reaches a
visible terminal state; provider loss cannot erase evidence; automatic outcomes preserve unknown
and abandonment rather than inventing success.

## Phase 5 — AR-5: Knowledge Core and Reconciliation

**Goal**: New evidence produces revisable knowledge through one reconciliation authority rather
than candidate-only append/promotion paths.

- [ ] T058 [US3] Add failing reconciliation decision-matrix fixtures in `internal/knowledge/reconciliation_integration_test.go` — FR-012, SC-006.
- [ ] T059 [US3] Add KnowledgeProposal, KnowledgeBelief, KnowledgeRevision, and evidence-link migration in `internal/db/gorm/migration_knowledge_core.go` — FR-012, FR-013.
- [ ] T060 [US3] Implement proposal persistence from completed evidence jobs in `internal/knowledge/proposal.go` — FR-011, FR-012.
- [ ] T061 [US3] Implement one reconciliation workflow and append-only revision writer in `internal/knowledge/reconcile.go` — FR-012, FR-013.
- [ ] T062 [P] [US3] Implement strict policy/rule authority and exception review integration in `internal/knowledge/policy.go` and `internal/governance/knowledge_exception.go` — FR-014.
- [ ] T063 [P] [US3] Implement legacy memory/candidate semantic mapping and quarantine in `internal/knowledge/legacy_adapter.go` — FR-013, SC-007.
- [ ] T064 [US3] Add contradiction, privacy, revision, supersession, and no-op idempotency tests in `internal/knowledge/reconcile_test.go` — FR-012 through FR-014.
- [ ] T065 [US3] Add projection-event contract without projection write authority in `internal/knowledge/projection_events.go` — FR-023.
- [ ] T066 [US3] Run AR-5 independent knowledge checker, legacy fixture mapping, and installed revision/supersession dogfood receipt — SC-006, SC-007, SC-012.

**Independent test criterion**: All required decisions are demonstrated, one-active-belief holds,
and each legacy memory/candidate is mapped or quarantined with provenance/privacy retained.

## Phase 6 — AR-6: Task-Aware Retrieval and Learning Cutover

**Goal**: Automatic context is a bounded packet for the actual task and delivery/outcomes feed one
honest learning loop.

- [ ] T067 [US4] Add failing task-trigger/filter/budget/exposure corpus tests in `internal/retrieval/task_retrieval_integration_test.go` — FR-015 through FR-017, SC-008 through SC-010.
- [ ] T068 [US4] Implement task/query/topic-shift capture boundary in `internal/retrieval/request.go` — FR-015.
- [ ] T069 [US4] Implement canonical-project/privacy/status/validity/applicability filter pipeline in `internal/retrieval/filter.go` — FR-016, SC-009.
- [ ] T070 [US4] Implement one candidate/rank/rerank/fallback/budget/render workflow in `internal/retrieval/service.go` — FR-016, SC-008, SC-010.
- [ ] T071 [US4] Implement idempotent RetrievalExposure persistence and delivery receipt in `internal/learning/exposure.go` — FR-017.
- [ ] T072 [US5] Implement evidence-based utility/review proposal flow in `internal/learning/utility.go` — FR-018, FR-019, SC-012.
- [ ] T073 [US4] Adapt gRPC, HTTP context, MCP recall, and prompt hooks to one retrieval workflow in `internal/grpcserver/session_start.go`, `internal/worker/handlers_context.go`, `internal/mcp/tools_recall.go`, and `plugin/engram/hooks/user-prompt.js` — FR-015 through FR-017.
- [ ] T074 [P] [US4] Add lexical/degraded-provider, explicit-empty-result, and `ENGRAM_INJECT_UNIFIED` true/false emergency-branch corpus cases with identical canonical-project/privacy validation in `internal/retrieval/task_retrieval_test.go` — FR-016, SC-008 through SC-010.
- [ ] T075 [P] [US4] Implement and test the versioned retrieval render-budget declaration plus AR-6 quality/latency/exposure/outcome metrics receipt in `internal/operability/acceptance_policy.go` and `internal/operability/retrieval_metrics.go` — FR-019, SC-008 through SC-011.
- [ ] T076 [US4] After T075's active scope-matched render-budget receipt passes, run shadow comparison and cut over normal static recent-memory injection, then record AR-6 continuity-slot replacement observation in `internal/worker/handlers_context.go` — FR-013, FR-015, FR-022.
- [ ] T077 [US4] Run AR-6 curated-corpus, degraded-provider, privacy-leakage, installed dogfood, and rollback-boundary proof — SC-008 through SC-012.

**Independent test criterion**: A concrete task produces one bounded, explainable, privacy-safe
packet and one exposure record; unavailable providers yield visible degraded/empty behavior rather
than static unrelated context.

## Phase 7 — AR-7: Architectural Contraction and Recovery Closure

**Goal**: The installed codebase exposes one recovered architecture with no maturity-era flag,
legacy identity writer, duplicate product path, dead contract, or unclassified unit.

- [ ] T078 [US7] Add failing exact source/configuration/package/route/tool/hook/UI/documentation scan covering both `ui/` and `apps/operator-console/` in `scripts/recovery/scan-contraction.ps1` — FR-021, FR-022, SC-014, SC-015.
- [ ] T079 [US7] Remove every architecture-era flag's reader/default/configuration/manifests/cache-or-payload key/tests/mocks/current docs/routes/tools/`ui/` and `apps/operator-console/` claims/package seams/release assertions in `internal/`, `plugin/`, `docs/`, `ui/`, and `apps/operator-console/` — FR-022, SC-015.
- [ ] T080 [US7] Retire legacy selector creation, V2 derivation, compatibility bridge, and text-key columns after sunset in `internal/projectidentity/` and `internal/db/gorm/` — FR-004, FR-025, FR-026.
- [ ] T081 [US7] Remove static normal injection, independent candidate orchestration, timer watermark gates, dead callbacks, and reserved continuity slot only after the AR-3 migration receipt and AR-6 replacement observation in `internal/`, `plugin/`, and `internal/db/gorm/` — FR-010, FR-013, FR-015, FR-022.
- [ ] T082 [US7] Apply final graph/meta-memory/cluster/summary/vector keep-as-projection or delete decisions and reconcile both `ui/` and `apps/operator-console/` claims without new surface design in `internal/graph/`, `internal/cognitive/`, `internal/retrieval/`, `internal/mcp/`, `docs/`, `ui/`, and `apps/operator-console/` — FR-023, FR-027.
- [ ] T083 [US8] Verify adjacent issues/documents/vault/credentials/collections/code-intelligence/Loom/state/authentication compatibility and detach remaining cognitive-era gating in `internal/`, `cmd/engram/`, `plugin/`, and auth integration contracts — FR-024, SC-017.
- [ ] T084 [US7] Remove or archive stale current claims with status in `README*.md`, `docs/`, `plugin/engram/skills/`, `docs/swagger.*`, `ui/`, `apps/operator-console/`, mocks, and release manifests — FR-021, FR-022.
- [ ] T085 [US7] Run exact-head SonarQube analysis, fix every finding, rerun exact head, and require `OK` before final backup/restore/release evidence in `sonar-project.properties` and release receipt evidence — constitution delivery gate.
- [ ] T086 [US6] After T085 exact-head SonarQube `OK`, execute final backup/restore, supported-upgrade, contraction, and clean-install verification in `scripts/recovery/run-recovery-scenario.ps1` — FR-025, SC-013, SC-018.
- [ ] T087 [US7] Run independent AR-7 source/deletion/security/release verifier and publish final installed receipt in `internal/recoveryreceipt/ar7_closure.go` — SC-014 through SC-018.

**Independent test criterion**: Exact source/configuration scans, clean install, supported upgrade,
restore rehearsal, and installed dogfood prove all Gates A-I; every inventory entry has a terminal
disposition and no new operator-surface implementation work exists.

## Cross-Cutting Release Discipline

- [ ] T088 [P] Maintain exact AR-2 FR/SC-to-task traceability, ownership paths, dependency edges, and acceptance evidence in `analysis/fr-sc-task-traceability.md`, `analysis/ar2-current-source-reconciliation.md`, and `analysis/ar2-d2-decomposition.md` — governance traceability obligation.
- [ ] T089 Maintain AR-2 independent checker, migration verifier, reviewer, and judge receipt references in `analysis/ar2-pipeline-receipt.json` while preserving the historical AR-0 `analysis/pipeline-receipt.json` — governance obligation.
- [ ] T090 Run the exact integrated AR-2 candidate build, applicable tests, migration/vector/payload validation, locally staged dogfood, rollback rehearsal, and worktree housekeeping before any later tag authority — FR-028, SC-018.
- [ ] T091 Enforce no new operator working-surface design through release-plan review that emits a read-only verdict; persist that output after analysis in `specs/001-engram-architectural-recovery/analysis/spec-kit-analysis-rN.md` — FR-027.

## Parallel Opportunities

- AR-1 inventory producers T002–T004 can run in parallel after T001 establishes common output
  shape; T006 and T008 can run independently.
- AR-2 hook/client descriptor work T018–T019 can run in parallel after T017; transport adapters
  T024–T026 can run in parallel after T022–T023.
- AR-3 adjacent/code-index cutovers T039–T040 can run in parallel after typed-key boundary T035.
- AR-4 outcome adapter/client work T050–T053 can run in parallel with durable intake T046–T049
  after the shared ProcessingJob schema exists.
- AR-5 policy/exception and legacy adapter work T062–T063 can run in parallel after T061.
- AR-6 request/filter/service/exposure work must serialize around the one Retrieval authority;
  corpus emergency-branch T074 and policy/metric T075 may run in parallel after T070–T071, while
  cutover T076 is serial after T075's policy receipt and both-branch evidence.
- AR-7 adjacent compatibility T083 may run after its AR-3 identity boundary; projection decision
  T082 is serial before stale-claim cleanup T084 because both own docs/, `ui/`, and
  `apps/operator-console/` disposition surfaces.

## Implementation Strategy

1. Ship AR-1 as the safe walking skeleton for honest observability and legacy fencing.
2. Expand, observe, and converge identity before migrating core workflow state.
3. Make evidence durable before reconciling knowledge; reconcile knowledge before replacing
   retrieval; record exposure/outcomes before using learning signals.
4. Contract only after backup, compatibility, observation, and rollback gates. Do not re-open
   scope into the operator working surface.
