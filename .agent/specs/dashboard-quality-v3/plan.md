# Implementation Plan: Dashboard Quality v3

**Spec:** .agent/specs/dashboard-quality-v3/spec.md
**Created:** 2026-03-28

## Phases

### Phase 1: Search Misses Fix (FR-2) — frontend only

**File:** `ui/src/utils/api.ts`
- Fix `fetchSearchMisses`: unwrap `miss_stats` from envelope, map `miss_count` → `frequency`
- 5 lines of code

### Phase 2: Sessions Backend (FR-3a, FR-3b) — Go

**File:** `internal/db/gorm/session_store.go`
- Add `min_prompts` and `from`/`to` params to `ListSDKSessions`

**File:** `internal/worker/handlers_sessions.go`
- Parse `min_prompts`, `from`, `to` query params in `handleListSessions`

### Phase 3: Sessions Frontend (FR-3a, FR-3b, FR-3c) — Vue

**File:** `ui/src/composables/useSessions.ts`
- Pass `min_prompts=1`, `from`, `to` to `fetchSDKSessions`

**File:** `ui/src/utils/api.ts`
- Add `min_prompts`, `from`, `to` params to `fetchSDKSessions`

**File:** `ui/src/views/SessionsView.vue`
- Add click handler on session row → navigate to detail
- Create inline detail panel or new route `/sessions/:id`

**File:** `ui/src/views/SessionDetailView.vue` (NEW)
- Show: metadata (outcome, duration, prompt count), observations list, injections, summary

### Phase 4: Pattern Insight Background (FR-1) — Go

**File:** `internal/maintenance/service.go`
- Add Task 18: `generatePatternInsights()` — query generic patterns, call LLM, persist
- Need: inject LLM client into maintenance service (or add a callback)

**File:** `internal/worker/service.go`
- Pass LLM client to maintenance service constructor

## Files Summary

| Phase | Backend | Frontend |
|-------|---------|----------|
| 1 | — | api.ts (5 lines) |
| 2 | session_store.go, handlers_sessions.go | — |
| 3 | — | useSessions.ts, api.ts, SessionsView.vue, SessionDetailView.vue (NEW) |
| 4 | maintenance/service.go, worker/service.go | — |
