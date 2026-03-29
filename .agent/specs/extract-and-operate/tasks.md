# Tasks: LLM-Driven Memory Extraction

**Generated:** 2026-03-29

## Phase 1: Implementation

- [ ] T001 Add extraction prompt for raw content analysis in `internal/worker/sdk/prompts.go`
- [ ] T002 Add `handleExtractAndOperate` method in `internal/mcp/tools_store_consolidated.go`
- [ ] T003 Add `action="extract"` case to handleStoreConsolidated in `internal/mcp/tools_store_consolidated.go`
- [ ] T004 Add "extract" to store tool action enum in `internal/mcp/server.go` primaryTools()
- [ ] T005 Run `go build ./...` to verify

---

## Phase 2: Release

- [ ] T006 Create PR, run review, merge
- [ ] T007 Tag release
