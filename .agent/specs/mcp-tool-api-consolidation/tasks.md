# Tasks: MCP Tool API Consolidation (61 → 6+1)

**Spec:** .agent/specs/mcp-tool-api-consolidation/spec.md
**Plan:** .agent/specs/mcp-tool-api-consolidation/plan.md
**Generated:** 2026-03-28

## Phase 1: Create Primary Handler Files (FR-1 through FR-6)

**Goal:** 6 new handler files, each routing actions to existing handlers
**Independent Test:** Each handler callable directly with action param, returns same results as old tool

- [ ] T001 [P] [US1] Create `handleRecall` router in `internal/mcp/tools_recall.go` — 13 actions routing to existing search/timeline/pattern handlers
- [ ] T002 [P] [US1] Create `handleStore` router in `internal/mcp/tools_store_consolidated.go` — 4 actions routing to existing store/edit/merge/import handlers
- [ ] T003 [P] [US1] Create `handleFeedback` router in `internal/mcp/tools_feedback.go` — 3 actions routing to existing rate/suppress/outcome handlers
- [ ] T004 [P] [US1] Create `handleVault` router in `internal/mcp/tools_vault_consolidated.go` — 5 actions routing to existing credential handlers
- [ ] T005 [P] [US1] Create `handleDocs` router in `internal/mcp/tools_docs_consolidated.go` — 11 actions routing to existing doc/collection handlers
- [ ] T006 [P] [US1] Create `handleAdmin` router in `internal/mcp/tools_admin.go` — 21 actions routing to existing bulk/tag/graph/maintenance/analytics handlers

---

**Checkpoint:** All 6 primary handlers compile and route correctly. `go build ./...` passes.

## Phase 2: Register Primary Tools + Alias Tier (FR-7, FR-8)

**Goal:** Default tools/list returns 7 tools; cursor=all returns 7 + 61 aliases
**Independent Test:** `tools/list` returns 7; `tools/list?cursor=all` returns 68; all alias calls work

- [ ] T007 [US1] [US3] Replace `allTools` slice in `internal/mcp/server.go` with 7 primary tool definitions (flat schema: action enum + optional params)
- [ ] T008 [US1] Move current 61 tool definitions to `aliasTools` slice in `internal/mcp/server.go` — returned only with `cursor=all`
- [ ] T009 [US2] Update `handleCallTool` dispatch in `internal/mcp/server.go` — primary names call primary handlers; alias names inject action and call primary handlers
- [ ] T010 [US2] Verify all 7 backward compat aliases from PR #112 (get_context_timeline, get_timeline_by_query, get_recent_context, find_by_file_context, get_observation_relationships, get_graph_neighbors, doc_update) still dispatch correctly in `internal/mcp/server.go`
- [ ] T011 Run `go build ./...` to verify compilation in `internal/mcp/`
- [ ] T011a [US3] Measure default tools/list token count (target: <1000 tokens) — count JSON bytes of 7-tool response
- [ ] T011b [US1] Verify aliasTools preserves original schemas exactly (diff old vs new serialization)

---

**Checkpoint:** 7 primary tools registered. 61 aliases in cursor=all. All dispatch paths working. Token count verified.

## Phase 3: Update Tests (US1, US2, US3)

**Goal:** All tests pass with new tool structure
**Independent Test:** `go test ./internal/mcp/ -count=1` passes

- [ ] T012 [US1] Update `TestHandleToolsList` in `internal/mcp/server_test.go` — expect 7 default tools, 68 with cursor=all
- [ ] T013 [US2] Update `TestCallTool_ToolNameRecognition` in `internal/mcp/server_test.go` — test primary + alias names
- [ ] T014 [P] [US2] Add `TestAliasDispatchParity` in `internal/mcp/server_test.go` — verify alias call produces same result as primary+action call for 5 representative tools
- [ ] T015 [US1] Add `TestPrimaryToolActions` in `internal/mcp/server_test.go` — verify each primary tool rejects unknown actions with descriptive error
- [ ] T015a [P] [US1] Spot-check latency: time 10 recall(action="search") calls vs 10 old search() calls, verify <5% overhead in `internal/mcp/server_test.go`
- [ ] T016 Run `go test ./internal/mcp/ -count=1 -timeout 120s` to verify all tests pass

---

**Checkpoint:** All MCP tests pass. Primary tools and aliases verified.

## Phase 4: Version Bump + Release

- [ ] T017 Bump openclaw-engram to 2.1.0 in `plugin/openclaw-engram/package.json`
- [ ] T018 Update `engramInstructions` string in `internal/mcp/server.go` to reference 7 primary tools instead of legacy tool names
- [ ] T019 Create PR, run review, merge
- [ ] T020 Tag v2.1.0, create GitHub release with structured notes
- [ ] T021 Verify `tools/list` returns exactly 7 tools on deployed server
- [ ] T022 Measure context token reduction (target: >80% reduction from ~6100 tokens)

## Dependencies

```
Phase 1 ── T001-T006 all parallel (separate files)
Phase 2 ── T007-T009 sequential (same file) ── T010-T011
Phase 3 ── T012-T015 partially parallel ── T016
Phase 4 ── depends on Phase 3 complete
```

## Execution Strategy

- **MVP scope:** Phase 1-3 (all tools consolidated + tested)
- **Parallel opportunities:** T001-T006 (6 independent files); T014 parallel with T012-T013
- **Commit strategy:** One commit per phase
- **Review gates:** `/code-review lite` after Phase 2, full review before merge
