# Implementation Plan: Plugin Tool Consolidation & Redundant Tool Cleanup

**Spec:** `.agent/specs/plugin-tool-consolidation/spec.md`
**Created:** 2026-03-28
**Status:** Draft

## Tech Stack

No new dependencies. All work in existing codebases:

| Component | Stack | Notes |
|-----------|-------|-------|
| Server MCP tools | Go | `internal/mcp/server.go` — tool registration |
| OpenClaw plugin | TypeScript | `plugin/openclaw-engram/src/` — tools, hooks, client |
| CC plugin hooks | JavaScript | `plugin/engram/hooks/` — stop.js, statusline.js |

## Architecture

No architectural changes. All modifications are:
- **Server:** Remove entries from `allTools` slice, keep dispatch aliases
- **OpenClaw:** Add client methods → add tool files → register hooks
- **CC:** Modify existing hooks to use newer API endpoints

## File Plan

### Phase 1: Server — FR-1 (Remove 7 Redundant Tools)

**Files modified:** 1

| File | Change |
|------|--------|
| `internal/mcp/server.go` | Remove 7 tool definitions from `allTools` slice. Keep `case` entries in `handleCallTool` switch for backward compat. |

**Tools removed from registration:**
- `get_context_timeline` (line ~654)
- `get_timeline_by_query` (line ~673)
- `get_recent_context` (line ~636)
- `find_by_file_context` (line ~602)
- `get_observation_relationships` (line ~1011)
- `get_graph_neighbors` (line ~1024)
- `doc_update` (line ~1434)

**Verification:**
- `go build ./...` — compiles
- `go test ./internal/mcp/...` — existing tests pass
- Manually verify: `tools/list` returns 61 tools (was 68)
- Manually verify: calling removed tool name via dispatch still works

**PR:** `chore: remove 7 redundant MCP tool registrations`

---

### Phase 2: OpenClaw — FR-2 (Bug Fixes)

**Files modified:** 3

| File | Change |
|------|--------|
| `plugin/openclaw-engram/src/tools/engram-decisions.ts` | Switch from `client.searchContext()` to `client.searchDecisions()`. Remove client-side type filter. |
| `plugin/openclaw-engram/src/tools/memory-forget.ts` | Change default action from archive to suppress. Add `permanent` parameter. Archive only when `permanent=true`. |
| `plugin/openclaw-engram/src/client.ts` | Add `suppressObservation(id)` method calling `POST /api/observations/{id}/feedback` with action "suppress" (verify endpoint). |

**Verification:**
- `npx tsc --noEmit` — compiles
- Test: `engram_decisions("Redis")` → server logs show `/api/decisions/search` hit
- Test: `memory_forget(123)` → observation status = "suppressed" (not deleted)
- Test: `memory_forget(123, permanent=true)` → observation archived

**PR:** `fix: openclaw decisions endpoint + memory_forget suppress default`

---

### Phase 3: OpenClaw — FR-3 (Expand Tools)

**Files created:** 5 new tool files
**Files modified:** 2

| File | Change |
|------|--------|
| `src/client.ts` | Add methods: `rateObservation(id, useful)`, `suppressObservation(id)`, `setSessionOutcome(sessionId, outcome, reason)`, `getFileContext(file, project)`, `getTimeline(mode, params)` |
| `src/index.ts` | Register 9 new tools |
| `src/tools/engram-rate.ts` | **NEW** — rate_memory: `POST /api/observations/{id}/feedback` |
| `src/tools/engram-suppress.ts` | **NEW** — suppress_memory: `POST /api/observations/{id}/feedback` (suppress action) |
| `src/tools/engram-outcome.ts` | **NEW** — set_session_outcome: `POST /api/sessions/{id}/outcome` |
| `src/tools/engram-find-by-file.ts` | **NEW** — find_by_file: `GET /api/context/by-file?path=X` |
| `src/tools/engram-timeline.ts` | **NEW** — timeline: `POST /api/context/search` with timeline params |

**P2 tools** (changes, how_it_works, vault_store, vault_get) implemented as thin wrappers:
- `engram_changes` → `client.searchContext({ preset: "changes" })`
- `engram_how_it_works` → `client.searchContext({ preset: "how_it_works" })`
- `engram_vault_store` → `client.storeCredential(name, value, scope)`
- `engram_vault_get` → `client.getCredential(name)`

These can be added to existing tool files or as individual files — decision at implementation time.

**Tool descriptions MUST include trigger conditions (NFR-3):**
```
engram_find_by_file: "Call BEFORE modifying any file. Returns what engram knows about it."
engram_rate: "Call after using a recalled memory. Rate whether it was helpful."
engram_outcome: "Call at session end. Record whether the session goal was achieved."
```

**Verification:**
- `npx tsc --noEmit` — compiles
- Test each new tool with a real engram server call
- Verify tool count: 17 OpenClaw tools total (8 existing + 9 new)

**PR:** `feat: expand openclaw-engram tools (rate, suppress, outcome, file, timeline, vault)`

---

### Phase 4: OpenClaw — FR-4 (Lifecycle Hooks)

**Files created:** 1
**Files modified:** 2

| File | Change |
|------|--------|
| `src/hooks/before-tool-call.ts` | **NEW** — Detect Write/Edit tool names in event, call `client.getFileContext(path)`, inject as context |
| `src/hooks/session-end.ts` | Add outcome detection + `/api/sessions/{id}/outcome` call. Add utility tracking: get injected observations, detect used/corrected/ignored signals, call `/api/observations/{id}/utility`. |
| `src/index.ts` | Register `before_tool_call` handler |

**Outcome detection heuristics (FR-4a, from clarification C2):**
- Parse last N messages for completion signals (task done, confirmed by user)
- success = explicit completion signal found
- partial = some work done, no clear completion
- abandoned = session timeout / disconnect with no completion
- Fire-and-forget: 3s timeout, failures swallowed (Constitution #3)

**Utility tracking (FR-4b):**
- Get list of injected observation IDs from session start context
- Scan conversation for references to observation content (title/keyword match)
- "used" = observation content referenced in agent response
- "corrected" = agent contradicted or modified observation content
- "ignored" = injected but never referenced (implicit)

**File context injection (FR-4c):**
- Register `api.on('before_tool_call', handler)`
- Check if tool name matches Write/Edit patterns
- Extract file path from tool input
- Call `GET /api/context/by-file?path={file}&project={project}&limit=5`
- Return context as `appendSystemContext` (verify OpenClaw hook return type)
- 200ms timeout (matches CC PreToolUse), graceful failure

**Verification:**
- `npx tsc --noEmit` — compiles
- Test: end a session → check server for outcome record
- Test: Edit a known file → check that file context was injected
- Test: timeout scenario → no error propagated

**PR:** `feat: openclaw lifecycle hooks (outcome, utility, file context)`

---

### Phase 5: CC Plugin — FR-5 + FR-6

**Files modified:** 2

| File | Change |
|------|--------|
| `plugin/engram/hooks/stop.js` | Replace `GET /api/sessions/{id}/injected-observations` + individual utility calls with single `GET /api/sessions/{id}/injections`. Parse enriched response for utility detection. |
| `plugin/engram/hooks/statusline.js` | Add `GET /api/learning/effectiveness-distribution` call with 60s cache. Show compact summary: `eff: 72% high | 15% med | 13% low`. Graceful fallback: `eff: --` if unavailable or no data. |

**Stop hook changes (FR-5):**
```javascript
// Before: 2 calls + N utility calls
const injected = await lib.requestGet(`/api/sessions/${dbId}/injected-observations`);
for (const obs of injected) {
  await lib.requestPost(`/api/observations/${obs.id}/utility`, { signal });
}

// After: 1 call, utility signals derived from enriched data
const injections = await lib.requestGet(`/api/sessions/${dbId}/injections`);
// injections[] has: observation_id, title, effectiveness_score, utility_score
// Use title for keyword matching against conversation
```

**Statusline changes (FR-6):**
```javascript
let cachedEffectiveness = null;
let cacheTime = 0;

// In render function:
if (Date.now() - cacheTime > 60000) {
  try {
    cachedEffectiveness = await lib.requestGet('/api/learning/effectiveness-distribution', 3000);
    cacheTime = Date.now();
  } catch { /* swallow */ }
}
// Format: "eff: 72% high | 15% med" or "eff: --"
```

**Verification:**
- Test stop hook: end a session → verify fewer HTTP calls in server logs
- Test statusline: check that effectiveness appears after 1+ session with outcome
- Test graceful fallback: disconnect server → statusline shows `eff: --`

**PR:** `feat: cc stop hook retrospective API + statusline learning metrics`

---

## Phases Summary

| Phase | FR | Scope | Files | PR |
|-------|-----|-------|-------|----|
| 1 | FR-1 | Server: remove 7 tools | 1 Go | `chore: remove 7 redundant MCP tool registrations` |
| 2 | FR-2 | OpenClaw: fix 2 bugs | 3 TS | `fix: openclaw decisions endpoint + forget suppress` |
| 3 | FR-3 | OpenClaw: add 9 tools | 7 TS | `feat: expand openclaw-engram tools` |
| 4 | FR-4 | OpenClaw: lifecycle hooks | 3 TS | `feat: openclaw lifecycle hooks` |
| 5 | FR-5+6 | CC: stop + statusline | 2 JS | `feat: cc stop retrospective + statusline learning` |

**Dependencies:** Phase 3 builds on Phase 2 (client.ts methods). Phase 4 builds on Phase 3.
Phases 1 and 5 are independent — can run in parallel with anything.

**Version bumps (Constitution #7, #15 — unified versioning):**
- Phase 1: server only → server tag v2.0.9
- Phase 2: openclaw-engram 2.0.9 (bugfix), server tag v2.0.9
- Phase 3: openclaw-engram 2.0.10 (new tools), server tag v2.0.10
- Phase 4: openclaw-engram 2.0.11 (hooks), server tag v2.0.11
- Phase 5: CC plugin hooks changed, server tag v2.0.12
- All versions stay in 2.0.x patch range, aligned per Constitution #15

## Unknowns and Risks

| Unknown | Impact | Resolution |
|---------|--------|------------|
| `before_tool_call` hook return type in OpenClaw | HIGH | Verified: exists in SDK types. Check if `appendSystemContext` works as return. Test in Phase 4. |
| Suppress endpoint path | MED | Need to verify: is it `POST /api/observations/{id}/feedback` with `{action:"suppress"}` or a separate endpoint? Read handler code. |
| OpenClaw tool name conventions | LOW | Existing tools use `engram_*` and `memory_*` prefixes. New tools follow `engram_*` pattern. |

## Constitution Compliance

| Principle | Compliance |
|-----------|-----------|
| #1 Server-Only | OK — plugins are stateless HTTP consumers |
| #3 Non-Blocking Hooks | OK — all new hooks: 200ms-5s timeouts, fire-and-forget |
| #7 Bump Plugin Version | OK — version bumps planned per phase |
| #8 Complete Implementations | OK — no stubs, every tool fully implemented |
| #10 Immutable Data | OK — no mutations in hook logic |
| #12 Tool Count Budget | OK — net reduction: -7 server tools, OpenClaw tools are separate namespace |
| #15 Version Tracking | OK — openclaw-engram 2.0.9→2.0.11 across phases, unified with server |
