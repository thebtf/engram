# Plugin vs API Gap Audit

**Date:** 2026-03-28
**Server version:** v2.0.8
**Plugin versions:** engram hooks (CC), openclaw-engram 2.0.7

## Server Inventory

- **REST API endpoints:** ~130 across 19 functional groups
- **MCP tools:** 68 registered (9 Core, 14 Useful, 45 Admin) + 10 backward-compat aliases
- **Tool tiering:** First `tools/list` returns Core + Useful (23 tools); `cursor="all"` returns all 68

## CC Plugin Coverage (plugin/engram/)

CC Plugin connects via `.mcp.json` to engram MCP server — all 68 tools exposed automatically.
Hooks provide lifecycle automation on top.

### Hook Coverage

| Hook | API Endpoints Used | Status |
|------|-------------------|--------|
| SessionStart | `/api/context/inject`, `/api/sessions`, `/api/sessions/{id}/mark-injected`, `/api/observations/mark-injected` | OK |
| UserPromptSubmit | `/api/context/search`, `/api/sessions/init`, `/sessions/{id}/init`, `/api/sessions/{id}/mark-injected` | OK |
| PreToolUse | `/api/context/by-file` | OK |
| PostToolUse | `/api/sessions/observations` | OK |
| SubagentStop | `/api/sessions/subagent-complete` | OK |
| PreCompact | `/api/backfill/session` | OK |
| Stop | `/api/health`, `/api/sessions`, `/sessions/{id}/summarize`, `/api/sessions/{id}/extract-learnings`, `/api/sessions/index`, `/api/sessions/{id}/injected-observations`, `/api/observations/{id}/utility`, `/api/sessions/{id}/outcome` | OK |
| Statusline | `/api/stats` | OK |

### CC Plugin Gaps

| # | Gap | Severity | Detail |
|---|-----|----------|--------|
| G1 | Stop hook could use retrospective API | LOW | `/api/sessions/{id}/injections` (v2.0.8) returns enriched data; stop.js uses older `/api/sessions/{id}/injected-observations` |
| G2 | Statusline lacks learning metrics | LOW | Shows basic stats only. Could show effectiveness score, active strategy |
| G3 | No "ignored" utility signal | MEDIUM | Stop hook tracks "used" and "corrected" but not explicit "ignored" (injected but never referenced) |

### CC Plugin Verdict: **Well-covered.** Minor improvements possible but no critical gaps.

---

## OpenClaw Plugin Coverage (plugin/openclaw-engram/)

### Tool Mapping

| OpenClaw Tool | Engram MCP Equivalent | Status |
|---------------|----------------------|--------|
| engram_search / memory_search | search | OK |
| engram_remember / memory_store | store_memory (via bulk-import) | OK |
| engram_decisions | decisions | BUG: uses searchContext + filter instead of /api/decisions/search |
| memory_forget | suppress_memory | BUG: uses archive (permanent) instead of suppress (reversible) |
| memory_get | recall_memory | OK |
| memory_migrate | (bulk import) | OK |

### Missing Tools (60 out of 68)

**Core tier missing (HIGH priority):**
- rate_memory — no feedback mechanism
- suppress_memory — memory_forget does archive instead
- find_by_file — critical for pre-edit context
- set_session_outcome — no closed-loop learning

**Useful tier missing (MEDIUM priority):**
- timeline, changes, how_it_works, find_by_concept, find_by_type, find_by_file_context
- find_similar_observations
- get_recent_context
- store_credential, get_credential (vault)
- doc_create, doc_read, doc_update, doc_list (versioned docs)

**Admin tier missing (LOW priority for agents):**
- All bulk operations, curation, patterns, graph, maintenance, analytics tools

### OpenClaw Hook Gaps

| # | Gap | Severity | Detail |
|---|-----|----------|--------|
| H1 | No session outcome recording | HIGH | session_end does backfill only, no outcome call |
| H2 | No utility tracking | HIGH | CC stop.js detects used/corrected/ignored signals, OpenClaw doesn't |
| H3 | No file-context hook | MEDIUM | CC PreToolUse injects file context before Edit/Write, OpenClaw doesn't |
| H4 | No subagent completion signal | LOW | CC has SubagentStop hook, OpenClaw doesn't |

### Bugs Found

| # | Bug | File | Detail |
|---|-----|------|--------|
| B1 | Wrong endpoint for decisions | `src/tools/engram-decisions.ts` | Uses searchContext + client-side type filter instead of `/api/decisions/search` |
| B2 | Archive instead of suppress | `src/tools/memory-forget.ts` | Calls bulk-status(archive) — permanent removal. Should use suppress (reversible) |

### OpenClaw Verdict: **12% coverage (8/68 tools).** Critical gaps in core tools and lifecycle hooks.

---

## Real-World Agent Usage Problem

Despite 68 MCP tools being available, agents in practice predominantly use only:
- `store_memory` — save observations
- `recall_memory` / `search` — retrieve observations

This suggests a **tool discovery and adoption problem**, not a technical gap.

### Root Causes (hypothesis)

1. **Tool overload:** 68 tools overwhelms agent context. Even with tiering, 23 in first page is a lot.
2. **Descriptions not actionable:** Tool descriptions explain WHAT, not WHEN to use them.
3. **No behavioral triggers:** Agents need rules like "before modifying a file, call find_by_file" — the `memory` skill has these but agents often don't load it.
4. **Plugin skill coverage:** The `memory` SKILL.md documents all tools but requires explicit invocation.
5. **Injected behavioral rules help:** `<user-behavior-rules>` with always_inject successfully force specific behaviors.

### Recommended Actions

See section below for prioritized recommendations.

---

## Recommendations

### Priority 1: Fix bugs
- [ ] B1: Fix engram_decisions to use `/api/decisions/search`
- [ ] B2: Fix memory_forget to use suppress instead of archive

### Priority 2: Agent adoption (highest impact)
- [ ] Create always_inject behavioral rules for key tool usage patterns
- [ ] Reduce tool count in first page (tier more aggressively)
- [ ] Improve tool descriptions with trigger conditions

### Priority 3: OpenClaw tool parity
- [ ] Add: rate_memory, suppress_memory, find_by_file, set_session_outcome
- [ ] Add: vault tools (store/get/list/delete credential)
- [ ] Add: timeline, changes, how_it_works

### Priority 4: OpenClaw lifecycle
- [ ] Add session outcome recording to session_end hook
- [ ] Add utility tracking to session_end hook
- [ ] Add file-context injection in before_prompt_build

### Priority 5: CC plugin polish
- [ ] Update stop.js to use retrospective API
- [ ] Add learning metrics to statusline
- [ ] Track "ignored" utility signal
