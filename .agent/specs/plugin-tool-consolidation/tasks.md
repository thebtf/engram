# Tasks: Plugin Tool Consolidation & Redundant Tool Cleanup

**Spec:** .agent/specs/plugin-tool-consolidation/spec.md
**Plan:** .agent/specs/plugin-tool-consolidation/plan.md
**Generated:** 2026-03-28

## Phase 1: Server — Remove Redundant Tools (FR-1)

**Goal:** Remove 7 redundant MCP tools from registration, keep dispatch aliases
**Independent Test:** `tools/list` returns 61 tools; calling removed tool name still works

- [ ] T001 Remove 7 tool definitions from `allTools` slice in `internal/mcp/server.go`
- [ ] T002 Verify dispatch aliases remain in `handleCallTool` switch in `internal/mcp/server.go`
- [ ] T003 Run `go build ./...` and `go test ./internal/mcp/...` to verify no breakage
- [ ] T003a Verify `tools/list` returns exactly 61 tools (was 68) via MCP call or curl

---

**Checkpoint:** Server returns 61 tools. Backward compat confirmed via dispatch aliases.

## Phase 2: OpenClaw Bug Fixes (FR-2)

**Goal:** Fix decisions endpoint and memory_forget default behavior
**Independent Test:** `engram_decisions` hits `/api/decisions/search`; `memory_forget` defaults to suppress

- [ ] T004 [US2] Fix `engram_decisions` to use `client.searchDecisions()` in `plugin/openclaw-engram/src/tools/engram-decisions.ts`
- [ ] T005 [US2] Read suppress handler code in `internal/mcp/tools_memory.go` to verify endpoint path, then add `suppressObservation(id)` method to `plugin/openclaw-engram/src/client.ts`
- [ ] T006 [US2] Change `memory_forget` default from archive to suppress, add `permanent` param in `plugin/openclaw-engram/src/tools/memory-forget.ts`
- [ ] T007 Run `npx tsc --noEmit` in `plugin/openclaw-engram/` to verify compilation
- [ ] T007a Bump openclaw-engram version to 2.0.9 in `plugin/openclaw-engram/package.json`

---

**Checkpoint:** Both bugs fixed. `decisions` uses correct endpoint, `forget` defaults to suppress.

## Phase 3: OpenClaw Tool Expansion (FR-3)

**Goal:** Add 9 new tools to OpenClaw plugin matching primary tool categories
**Independent Test:** OpenClaw exposes 17 tools; each new tool calls correct server endpoint

- [ ] T008 [P] [US2] Add `rateObservation(id, useful)` method to `plugin/openclaw-engram/src/client.ts`
- [ ] T009 [P] [US2] Add `setSessionOutcome(sessionId, outcome, reason)` method to `plugin/openclaw-engram/src/client.ts`
- [ ] T010 [P] [US4] Add `getFileContext(file, project, limit)` method to `plugin/openclaw-engram/src/client.ts`
- [ ] T011 [P] [US2] Add `getTimeline(mode, params)` method to `plugin/openclaw-engram/src/client.ts`
- [ ] T012 [P] [US2] Add `storeCredential(name, value, scope)` and `getCredential(name)` methods to `plugin/openclaw-engram/src/client.ts`
- [ ] T013 [P] [US2] Create `engram_rate` tool in `plugin/openclaw-engram/src/tools/engram-rate.ts`
- [ ] T014 [P] [US2] Create `engram_suppress` tool in `plugin/openclaw-engram/src/tools/engram-suppress.ts`
- [ ] T015 [P] [US3] Create `engram_outcome` tool in `plugin/openclaw-engram/src/tools/engram-outcome.ts`
- [ ] T016 [P] [US4] Create `engram_find_by_file` tool in `plugin/openclaw-engram/src/tools/engram-find-by-file.ts`
- [ ] T017 [P] [US2] Create `engram_timeline` tool in `plugin/openclaw-engram/src/tools/engram-timeline.ts`
- [ ] T018 [P] [US2] Create `engram_changes` and `engram_how_it_works` as search preset wrappers in `plugin/openclaw-engram/src/tools/engram-presets.ts`
- [ ] T019 [P] [US2] Create `engram_vault_store` and `engram_vault_get` in `plugin/openclaw-engram/src/tools/engram-vault.ts`
- [ ] T020 Register all new tools in `plugin/openclaw-engram/src/index.ts`
- [ ] T021 Run `npx tsc --noEmit` in `plugin/openclaw-engram/` to verify compilation
- [ ] T021a Verify all new tool descriptions include WHEN trigger conditions per NFR-3
- [ ] T021b Bump openclaw-engram version to 2.0.10 in `plugin/openclaw-engram/package.json`

---

**Checkpoint:** 17 OpenClaw tools registered. All compile. Each calls correct server endpoint.

## Phase 4: OpenClaw Lifecycle Hooks (FR-4)

**Goal:** Add outcome tracking, utility signals, and pre-edit file context injection
**Independent Test:** Session end records outcome; file context injected before Write/Edit

- [ ] T022 [US3] Add outcome detection logic to `session_end` handler in `plugin/openclaw-engram/src/hooks/session-end.ts` (handle gracefully when no DB session ID exists)
- [ ] T023 [US3] Add utility tracking (used/corrected/ignored signals) to `session_end` in `plugin/openclaw-engram/src/hooks/session-end.ts`
- [ ] T024 [US4] Create `before_tool_call` handler in `plugin/openclaw-engram/src/hooks/before-tool-call.ts`
- [ ] T025 [US4] Register `before_tool_call` hook in `plugin/openclaw-engram/src/index.ts`
- [ ] T026 Bump openclaw-engram version to 2.0.11 in `plugin/openclaw-engram/package.json`
- [ ] T027 Run `npx tsc --noEmit` in `plugin/openclaw-engram/` to verify compilation

---

**Checkpoint:** OpenClaw lifecycle matches CC plugin. Outcome recorded, utility tracked, file context injected.

## Phase 5: CC Plugin Improvements (FR-5 + FR-6)

**Goal:** Stop hook uses retrospective API; statusline shows learning effectiveness
**Independent Test:** Stop hook makes fewer HTTP calls; statusline shows `eff: X%`

- [ ] T028 [P] [US5] Replace injected-observations + individual utility calls with `/api/sessions/{id}/injections` in `plugin/engram/hooks/stop.js`
- [ ] T029 [P] [US6] Add learning effectiveness call with 60s cache to `plugin/engram/hooks/statusline.js`
- [ ] T030 Verify stop hook timing improvement via server log comparison
- [ ] T031 Verify statusline shows `eff:` metric or graceful `eff: --` fallback

---

**Checkpoint:** CC plugin uses newer APIs. Statusline shows learning metrics.

## Phase 6: Release

- [ ] T032 Update marketplace metadata in marketplace repo
- [ ] T033 Create git tags per phase (v2.0.9 through v2.0.12) with structured release notes, keeping server + plugin versions aligned per Constitution #15
- [ ] T034 Verify Docker build succeeds for server image

## Dependencies

```
Phase 1 ──────────────────────────────── (independent, can start immediately)
Phase 2 ──┬── T004-T006 parallel ──── T007
Phase 3 ──┼── T008-T019 parallel ──── T020 ── T021
Phase 4 ──┘── depends on Phase 3 (client methods) ── T022-T027
Phase 5 ──────────────────────────────── (independent, can parallel with 2-4)
Phase 6 ──── depends on all phases complete
```

## Execution Strategy

- **MVP scope:** Phase 1 + Phase 2 (server cleanup + bugfixes) — immediate value, minimal risk
- **Parallel opportunities:** Phase 1 || Phase 5 (different repos); T008-T019 within Phase 3 (different files)
- **Commit strategy:** One commit per phase, one PR per phase
- **Review gates:** `/code-review lite` after each phase before PR creation
