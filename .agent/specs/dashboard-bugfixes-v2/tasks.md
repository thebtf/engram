# Tasks: Dashboard Bugfixes v2

**Spec:** .agent/specs/dashboard-bugfixes-v2/spec.md
**Plan:** .agent/specs/dashboard-bugfixes-v2/plan.md
**Generated:** 2026-03-28

## Phase 1: Server-Side Filters (FR-1 + FR-2)

**Goal:** Concept and type filters work via server-side queries
**Independent Test:** Select concept/type in UI → correct filtered results with pagination

- [x] T001 [US1] [US2] Read `handleGetObservations` in `internal/worker/handlers_data.go` — understand current pagination query
- [x] T002 [US2] Add `type` query param to `handleGetObservations` in `internal/worker/handlers_data.go`
- [x] T003 [US1] Add `concept` query param to `handleGetObservations` in `internal/worker/handlers_data.go`
- [x] T004 [US1] [US2] Add `type` and `concept` params to `fetchObservationsPaginated` in `ui/src/utils/api.ts`
- [x] T005 [US1] Wire concept dropdown to re-fetch with server param in `ui/src/views/ObservationsView.vue`
- [x] T006 [US2] Wire type buttons to re-fetch with server param in `ui/src/views/ObservationsView.vue`
- [x] T007 [US2] Remove client-side `filteredObservations` in `ui/src/views/ObservationsView.vue`
- [x] T008 Run `go build ./...` to verify backend compilation

---

**Checkpoint:** Concept and type filters fetch server-side. Pagination counts correct.

## Phase 2: Real Counts (FR-3)

**Goal:** Display actual observation and prompt counts
**Independent Test:** Page shows real numbers matching API stats

- [x] T009 [US3] Find hardcoded "50 obs · 50 prompts" in `ui/src/views/` or `ui/src/components/`
- [x] T010 [US3] Replace with real counts from API response in relevant Vue component
- [x] T011 [US3] Verify counts update when filters change

---

**Checkpoint:** Real counts displayed.

## Phase 3: Summaries Investigation (FR-4)

**Goal:** Summaries tab shows data
**Independent Test:** At least 1 summary visible after session with summarization

- [x] T012 [US4] Test `/api/summaries` endpoint via curl — check response format
- [x] T013 [US4] If empty: check `stop.js` summarize call and server handler for errors
- [x] T014 [US4] Fix root cause of missing summaries in server or hook code
- [x] T015 [US4] Verify Summaries tab displays data

---

**Checkpoint:** Summaries visible in dashboard.

## Phase 4: Visual Verification + Release

- [x] T016 Screenshot all 4 fixes (concept filter, type filter, counts, summaries)
- [x] T017 Create PR, run review, merge
- [x] T018 Bump version, tag release

## Dependencies

```
Phase 1: T001 → T002-T003 parallel → T004 → T005-T007 parallel → T008
Phase 2: independent of Phase 1
Phase 3: independent (investigation)
Phase 4: depends on all phases
```

## Execution Strategy

- **MVP:** Phase 1 + 2 (filters + counts)
- **Parallel:** T002||T003, T005||T006||T007, Phase 2 || Phase 3
- **Commit:** One per phase
