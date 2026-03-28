# Feature: Plugin Tool Consolidation & Redundant Tool Cleanup

**Slug:** plugin-tool-consolidation
**Created:** 2026-03-28
**Status:** Draft
**Author:** AI Agent (reviewed by user)
**Audit:** `.agent/reports/plugin-api-gap-audit-2026-03-28.md`

## Overview

Engram exposes 68 MCP tools but agents use only 2 (store_memory, search). Root cause:
tool overload — 17 search variants, 12 doc tools, 5 vault tools are separate tools instead
of parameters to unified operations. This spec consolidates the tool surface, removes 7
redundant tools, and aligns both plugins (CC and OpenClaw) with the consolidated API.

## Context

### Current State
- **Server:** 68 MCP tools registered, tiered into Core (9), Useful (14), Admin (45)
- **CC Plugin:** All 68 tools available via MCP connection. Hooks cover full lifecycle.
- **OpenClaw Plugin:** 8 tools (12% coverage). Missing: rate, suppress, find_by_file,
  vault, docs, timeline, session outcome. Bug: decisions uses wrong endpoint.
- **Agent adoption:** ~3% tool usage (2 out of 68). Tool discovery is the bottleneck.

### Prior Work
- `mcp-tools-refactoring.md`: Server-side consolidation (FR3-FR5 done: search presets,
  timeline modes, graph_query). Tiering (FR2 done). Type safety (FR1 done).
- `openclaw-engram-plugin/spec.md`: OpenClaw v2 design (research phase, architecture mismatch analysis)
- `closed-loop-learning/spec.md`: Outcome tracking, effectiveness scoring (v2.0.0-v2.0.8)

### What Changed
Gap audit (2026-03-28) revealed: 7 tools are pure duplicates of consolidated tools,
OpenClaw is critically behind, and the real adoption problem is cognitive load, not missing features.

## Functional Requirements

### FR-1: Remove 7 Redundant MCP Tools from Registration

Remove these tools from `tools/list` response. Keep dispatch aliases for backward compatibility.

| Tool | Redundant Because |
|------|------------------|
| `get_context_timeline` | Already consolidated into `timeline(mode="anchor")` |
| `get_timeline_by_query` | Already consolidated into `timeline(mode="query")` |
| `get_recent_context` | Already consolidated into `timeline(mode="recent")` |
| `find_by_file_context` | Near-duplicate of `find_by_file` (same data, different sort) |
| `get_observation_relationships` | Subset of `graph_query(mode="relationships")` |
| `get_graph_neighbors` | Subset of `graph_query(mode="neighbors")` |
| `doc_update` | Alias of `doc_create` (identical handler) |

**Behavior:** Remove from `allTools` slice in `server.go`. Keep `case` entries in
`handleCallTool` dispatch switch so existing clients still work.

### FR-2: Fix OpenClaw Plugin Bugs

**FR-2a:** `engram_decisions` must use `/api/decisions/search` endpoint (dedicated decision
search with type filtering) instead of `searchContext` + client-side filter.

**FR-2b:** `memory_forget` must offer suppress (reversible soft-hide) as default action
instead of archive (permanent removal). Add optional `permanent: true` parameter for archive.

### FR-3: Expand OpenClaw MCP Tools to Match Primary Tools

Add these tools to OpenClaw plugin, matching the 6 PRIMARY categories from the audit:

| Tool | Maps To | Priority |
|------|---------|----------|
| `engram_find_by_file` | `find_by_file` | P1 — critical for pre-edit context |
| `engram_rate` | `rate_memory` | P1 — enables feedback loop |
| `engram_suppress` | `suppress_memory` | P1 — enables curation |
| `engram_outcome` | `set_session_outcome` | P1 — enables closed-loop learning |
| `engram_timeline` | `timeline` | P2 — useful for context |
| `engram_changes` | `search(preset="changes")` | P2 — common query |
| `engram_how_it_works` | `search(preset="how_it_works")` | P2 — common query |
| `engram_vault_store` | `store_credential` | P2 — vault access |
| `engram_vault_get` | `get_credential` | P2 — vault access |

### FR-4: OpenClaw Lifecycle Hook Improvements

**FR-4a:** `session_end` hook must call `/api/sessions/{id}/outcome` with detected outcome
(success/partial/abandoned) based on conversation signals. Reuse CC stop.js signal detection
pattern adapted for OpenClaw's continuous model: success = agent completed task or user
confirmed; partial = mixed signals; abandoned = session timeout with no completion signal.
Exact heuristics defined in plan phase.

**FR-4b:** `session_end` hook must perform utility tracking — detect which injected
observations were referenced ("used") or corrected during the session, and call
`/api/observations/{id}/utility` for each.

**FR-4c:** Register `before_tool_call` hook (exists in OpenClaw SDK but unused by engram
plugin) to inject file-context observations before Write/Edit tools, matching CC PreToolUse
behavior. Call `/api/context/by-file` and inject result as context.

### FR-5: CC Plugin Stop Hook — Use Retrospective API

Replace the manual join logic in `stop.js` (separate calls to
`/api/sessions/{id}/injected-observations` + individual `/api/observations/{id}/utility`)
with the new retrospective API (`/api/sessions/{id}/injections`) that returns enriched
records in a single call.

### FR-6: CC Plugin Statusline — Learning Metrics

Add learning effectiveness indicator to statusline output. Call `/api/learning/effectiveness-distribution`
and show a compact summary (e.g., "eff: 72% high | 15% med | 13% low"). Cache result for
60 seconds client-side — learning metrics change per-session, not per-second.

## Non-Functional Requirements

### NFR-1: Zero Breaking Changes
Existing MCP clients calling removed tool names must continue to work via dispatch aliases.
No tool response format changes. No parameter renames.

### NFR-2: Plugin Non-Blocking (Constitution Principle 3)
All new HTTP calls in hooks must have explicit timeouts (max 5s for data calls, 3s for
fire-and-forget signals). Failures must be swallowed with logging.

### NFR-3: OpenClaw Tool Descriptions Must Include Trigger Conditions
Each new OpenClaw tool description must state WHEN to use it, not just WHAT it does.
Example: "Call this BEFORE modifying any file to check what is known about it."

### NFR-4: Plugin Version Bumps (Constitution Principle 7)
Every plugin file change must bump the respective package version.

## User Stories

### US1: Agent Uses Consolidated Tool Set (P1)
**As an** AI agent using engram, **I want** a small set of well-described tools,
**so that** I can find and use the right tool without scanning 68 options.

**Acceptance Criteria:**
- [ ] Default `tools/list` returns at most 23 tools (current Core + Useful)
- [ ] 7 redundant tools no longer appear in `tools/list`
- [ ] Calling any removed tool name still works via dispatch alias

### US2: OpenClaw Agent Provides Feedback (P1)
**As an** OpenClaw agent, **I want** to rate and suppress observations,
**so that** the memory system learns what is useful.

**Acceptance Criteria:**
- [ ] `engram_rate(id, useful=true)` calls rate_memory endpoint
- [ ] `engram_suppress(id)` calls suppress_memory endpoint
- [ ] `memory_forget(id)` defaults to suppress (not archive)
- [ ] `memory_forget(id, permanent=true)` still archives

### US3: OpenClaw Session Outcome Tracking (P1)
**As an** OpenClaw deployment, **I want** session outcomes recorded automatically,
**so that** closed-loop learning works across both CC and OpenClaw.

**Acceptance Criteria:**
- [ ] `session_end` hook calls `/api/sessions/{id}/outcome`
- [ ] Outcome detection: success (task completed signals), partial (mixed signals), abandoned (no completion)
- [ ] Utility signals sent for injected observations

### US4: OpenClaw Pre-Edit Context (P1)
**As an** OpenClaw agent about to modify a file, **I want** relevant observations injected,
**so that** I don't repeat past mistakes or miss known patterns.

**Acceptance Criteria:**
- [ ] `before_tool_call` hook registered and fires before Write/Edit tools
- [ ] Calls `/api/context/by-file` with the target file path
- [ ] Injects result as context via hook return value

### US5: CC Stop Hook Uses Retrospective API (P2)
**As a** CC session ending, **I want** enriched injection data in one call,
**so that** utility detection is faster and more accurate.

**Acceptance Criteria:**
- [ ] `stop.js` calls `/api/sessions/{id}/injections` instead of separate calls
- [ ] Utility signals derived from enriched response
- [ ] Fewer HTTP calls during stop hook execution

### US6: CC Statusline Shows Effectiveness (P2)
**As a** user monitoring engram health, **I want** to see learning effectiveness at a glance,
**so that** I know the system is improving.

**Acceptance Criteria:**
- [ ] Statusline includes effectiveness distribution summary
- [ ] Format: compact, fits in one statusline segment
- [ ] Graceful fallback if learning endpoint unavailable

## Edge Cases

- OpenClaw `session_end` may fire without a valid DB session ID (session not yet initialized) — must handle gracefully
- `memory_forget` with invalid ID and `permanent=true` — returns "Invalid observation ID" error
- Statusline learning endpoint may return empty data (no sessions with outcomes yet) — show "eff: --"
- Removed tools called via MCP with invalid parameters — same error handling as before
- OpenClaw `before_tool_call` for Write on non-existent file path — skip file context injection

## Out of Scope

- **Tool API consolidation** (merging 17 search tools into single `recall` tool) — separate major refactor
- **Behavioral rules injection** for tool usage patterns — deferred to separate TODO
- **OpenClaw admin tools** (bulk ops, maintenance, patterns, graph) — low agent usage, defer
- **OpenClaw doc tools** (doc_create/read/update/list) — defer until OpenClaw needs document management
- **Dashboard changes** — no UI work in this spec
- **Server-side handler changes** — only MCP tool registration and dispatch, no new endpoints

## Dependencies

- `/api/sessions/{id}/injections` endpoint (v2.0.8) — already deployed
- `/api/learning/effectiveness-distribution` endpoint — already deployed
- `/api/decisions/search` endpoint — already deployed
- OpenClaw plugin SDK hooks API — existing, no changes needed

## Success Criteria

- [ ] `tools/list` returns 61 tools (68 - 7 redundant) with zero client breakage
- [ ] OpenClaw plugin exposes 17 tools (8 existing + 9 new from FR-3)
- [ ] OpenClaw `session_end` records outcomes (verified via `/api/sessions/list`)
- [ ] `engram_decisions` calls correct endpoint (verified via server logs)
- [ ] `memory_forget` defaults to suppress (verified via observation status check)
- [ ] CC stop hook makes fewer HTTP calls (verified via timing logs)
- [ ] CC statusline shows effectiveness metric
- [ ] All existing tests pass: `go test ./...`, `npx tsc --noEmit`

## Clarifications

### Session 2026-03-28

| # | Category | Question | Resolution | Date |
|---|----------|----------|------------|------|
| C1 | Integration | FR-4c: OpenClaw has no before_tool_call hook? | WRONG — `before_tool_call` EXISTS in SDK (types/openclaw.ts:138), engram plugin just never registered it. FR-4c is feasible. | 2026-03-28 |
| C2 | Interaction & UX | FR-4a: What signals for OpenClaw outcome detection? | Reuse CC stop.js pattern adapted for continuous model. Success=task completed/user confirmed, partial=mixed, abandoned=timeout. Detail in plan phase. | 2026-03-28 |
| C3 | Perf/Scale | FR-6: Statusline hammering server with learning endpoint? | 60s client-side cache. Learning metrics change per-session, not per-second. | 2026-03-28 |

## Clarification Summary

| Category | Status |
|----------|--------|
| Functional Scope | Clear |
| User Roles | Clear |
| Domain/Data Model | Clear |
| Data Lifecycle | Clear |
| Interaction & UX Flow | Resolved (C2) |
| Non-Functional: Perf/Scale | Resolved (C3) |
| Non-Functional: Reliability | Clear |
| Non-Functional: Security | Clear |
| Integration | Resolved (C1) |
| Edge Cases | Clear |
| Constraints & Tradeoffs | Clear |
| Terminology | Clear |
| Completion Signals | Clear |
| Miscellaneous | Clear |

**Questions asked/answered:** 3/3
**Spec status:** Ready for planning
**Next:** `/speckit-plan`

## Open Questions

None — all ambiguities resolved via clarification.
