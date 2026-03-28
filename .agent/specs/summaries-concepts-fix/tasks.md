# Tasks: Fix Summaries & Concepts Pipeline

**Generated:** 2026-03-28

## Phase 1: Fix Extraction Prompt (FR-2) — T0

- [x] T001 [FR-2] Add valid concept list to systemPrompt in `internal/worker/sdk/processor.go` (~line 1257)
- [x] T002 [FR-2] Fix example concept from `user-preference` to `workflow` in systemPrompt

---

## Phase 2: Summary Fallback (FR-1) — T1

- [x] T003 [FR-1] Add userPrompt fallback in ProcessSummary in `internal/worker/sdk/processor.go` (~line 555)
- [x] T004 Run `go build ./...` to verify

---

## Phase 3: Concept Backfill Migration (FR-3) — T1

- [x] T005 [FR-3] Add migration in `internal/db/gorm/migrations.go` — keyword-based UPDATE on observations.concepts
- [x] T006 Run `go build ./...` to verify

---

## Phase 4: Release

- [x] T007 Create PR, run review, merge
- [x] T008 Tag release
