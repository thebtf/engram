# Implementation Plan: Dashboard Bugfixes v2

**Spec:** .agent/specs/dashboard-bugfixes-v2/spec.md
**Created:** 2026-03-28
**Status:** Draft

## Tech Stack

| Component | Stack | Files |
|-----------|-------|-------|
| Backend | Go | `internal/worker/handlers_data.go` |
| Frontend | Vue 3 | `ui/src/views/ObservationsView.vue`, `ui/src/utils/api.ts` |
| Stop hook | JS | `plugin/engram/hooks/stop.js` |

## Phases

### Phase 1: FR-1 + FR-2 — Concept and Type Filters (Server-Side)

**Backend:**
1. Read `handleGetObservations` in `handlers_data.go` — find where pagination query is built
2. Add `type` and `concept` query params to the handler
3. Pass to `GetAllRecentObservationsPaginated` (or equivalent) as WHERE clauses

**Frontend:**
4. Read `fetchObservationsPaginated` in `api.ts` — add `type` and `concept` params
5. Read `ObservationsView.vue` — find where concept dropdown triggers fetch
6. Wire concept/type selection to re-fetch with server params instead of client filter
7. Remove client-side `filteredObservations` computed property

### Phase 2: FR-3 — Real Counts

1. Find where "50 obs · 50 prompts" is displayed (likely `ObservationsView.vue` or parent)
2. Replace hardcoded values with API response totals
3. If API doesn't return totals — add to response

### Phase 3: FR-4 — Summaries Investigation

1. Check `/api/summaries` endpoint — does it return data?
2. If empty: check if `stop.js` calls `/sessions/{id}/summarize`
3. If stop hook doesn't fire (CC bug #19225): check if periodic recorder generates summaries
4. If summaries table empty: check server-side summarize handler for errors

### Phase 4: Visual Verification + Release

- Screenshot each fix (Constitution #14)
- PR + review + merge
- Bump version

## Constitution Compliance

| Principle | Status |
|-----------|--------|
| #14 Visual Verification | Mandatory — screenshots required |
| #12 Tool Count Budget | N/A — no MCP tool changes |
| #15 Version Tracking | Bump after merge |
