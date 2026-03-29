# Tasks: Reasoning Traces (System 2 Memory)

**Generated:** 2026-03-29

## Phase 1: Data Model + Migration

- [x] T001 Add `reasoning_traces` table migration in `internal/db/gorm/migrations.go`
- [x] T002 Add GORM model `ReasoningTrace` in `internal/db/gorm/models.go`
- [x] T003 Add `ReasoningTraceStore` with Create/Search/GetBySession in `internal/db/gorm/reasoning_store.go` — include vector embedding on store for semantic search
- [x] T004 Run `go build ./...` to verify

---

## Phase 2: Reasoning Detection + Extraction

- [x] T005 Add reasoning pattern detector in `internal/worker/sdk/reasoning_detector.go`
- [x] T006 Add reasoning extraction LLM prompt in `internal/worker/sdk/prompts.go`
- [x] T007 Add quality evaluation prompt in `internal/worker/sdk/prompts.go`
- [x] T008 Integrate detection + extraction into ProcessObservation in `internal/worker/sdk/processor.go` — include quality threshold check (≥0.5 to store)
- [x] T009 Run `go build ./...` to verify

---

## Phase 3: MCP Tool Integration

- [x] T010 Add `action="reasoning"` to handleRecall in `internal/mcp/tools_recall.go`
- [x] T011 Add handleReasoningSearch method in `internal/mcp/tools_recall.go`
- [x] T012 Run `go build ./...` to verify

---

## Phase 4: Context Injection

- [x] T013 Add reasoning trace injection to context inject handler in `internal/worker/handlers_context.go`
- [x] T014 Run `go build ./...` to verify

---

## Phase 5: Release

- [x] T015 Create PR, run review, merge
- [x] T016 Tag release

## Dependencies

Phase 1 → Phase 2 (needs model) → Phase 3 (needs store) → Phase 4 (needs retrieval)
