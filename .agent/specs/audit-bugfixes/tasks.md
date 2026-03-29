# Tasks: Audit Bugfixes

**Generated:** 2026-03-29

## Phase 1: Code Fixes

- [x] T001 [P] [FR-1] Verify summary dedup SQL in `internal/maintenance/service.go` — confirm NOT EXISTS check works
- [x] T002 [P] [FR-3] Fix OpenClaw before_tool_call return type in `plugin/openclaw-engram/src/types/openclaw.ts`
- [x] T003 [P] [FR-4] Add content validation to handleStoreMemory in `internal/mcp/tools_memory.go`
- [x] T004 [P] [FR-5] Lower userPrompt threshold from 50 to 10 chars in `internal/worker/sdk/processor.go`
- [x] T005 [FR-6] Add migration for 5 missing concept keywords in `internal/db/gorm/migrations.go`
- [x] T006 Run `go build ./...` to verify

---

## Phase 2: Verification

- [x] T007 [FR-2] Trigger maintenance on deployed server, confirm summaries appear
- [x] T008 [FR-7] Document effectiveness metric limitation for always-inject rules
- [x] T009 [FR-8] Visual verification of dashboard via screenshot

---

## Phase 3: Release

- [x] T010 Create PR, run review, merge
- [x] T011 Tag release
