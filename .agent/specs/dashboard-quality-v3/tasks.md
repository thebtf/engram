# Tasks: Dashboard Quality v3

**Generated:** 2026-03-28

## Phase 1: Search Misses Fix (FR-2)

- [x] T001 [FR-2] Fix `fetchSearchMisses` in `ui/src/utils/api.ts` — unwrap `miss_stats` envelope, map `miss_count` → `frequency`

---

## Phase 2: Sessions Backend (FR-3a, FR-3b)

- [x] T002 [FR-3a] Add `min_prompts` param to `ListSDKSessions` in `internal/db/gorm/session_store.go`
- [x] T003 [FR-3b] Add `from`/`to` date params to `ListSDKSessions` in `internal/db/gorm/session_store.go`
- [x] T004 [FR-3a] [FR-3b] Parse `min_prompts`, `from`, `to` in `handleListSessions` in `internal/worker/handlers_sessions.go`
- [x] T005 Run `go build ./...` to verify

---

## Phase 3: Sessions Frontend (FR-3a, FR-3b, FR-3c)

- [x] T006 [FR-3a] [FR-3b] Add `min_prompts`, `from`, `to` to `fetchSDKSessions` in `ui/src/utils/api.ts`
- [x] T007 [FR-3a] [FR-3b] Pass params from `useSessions.ts` to fetch call
- [x] T008 [FR-3c] Add click handler on session row in `ui/src/views/SessionsView.vue`
- [x] T009 [FR-3c] Create `ui/src/views/SessionDetailView.vue` — observations, injections, outcome, summary
- [x] T010 [FR-3c] Add route `/sessions/:id` in `ui/src/router/index.ts`

---

## Phase 4: Pattern Insight Background (FR-1)

- [x] T011 [FR-1] Add LLM client injection to maintenance service in `internal/worker/service.go`
- [x] T012 [FR-1] Add `generatePatternInsights()` task to maintenance cycle in `internal/maintenance/service.go`
- [x] T013 Run `go build ./...` to verify

---

## Phase 5: Release

- [x] T014 Create PR, run review, merge
- [x] T015 Tag release
