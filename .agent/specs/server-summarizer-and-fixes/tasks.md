# Tasks: Server-Side Summarizer + Investigate Fixes

**Generated:** 2026-03-29

## Phase 1: Server-Side Summarizer (FR-1)

- [x] T001 [FR-1] Add `summarizeUnsummarizedSessions()` method to `internal/maintenance/service.go`
- [x] T002 [FR-1] Add Task 19 call in `runMaintenance` in `internal/maintenance/service.go`
- [x] T003 [FR-1] Run `go build ./...` to verify

---

## Phase 2: P1 Fixes (FR-2, FR-3, FR-4)

- [x] T004 [P] [FR-2] Remove `guidance` from warningTypes in `plugin/engram/hooks/pre-tool-use.js`
- [x] T005 [P] [FR-3] Remove client-side summarizer from `plugin/engram/hooks/session-start.js` (replaced by server-side)
- [x] T006 [P] [FR-4] Add recovery logging to CircuitBreaker in `internal/worker/sdk/processor.go`

---

## Phase 3: Release

- [x] T007 Create PR, run review, merge
- [x] T008 Tag release

## Dependencies

T001-T002 sequential (same file). T004-T006 parallel (different files). Phase 2 independent of Phase 1.
