
- [x] **[investigate]** ~~Session tracking audit: Active Sessions=0~~ PARTIALLY RESOLVED v2.1.5 — "Sessions Today" replaces misleading in-memory count. Investigation found: counter is working-as-designed (transient, 30min timeout). Remaining: OpenClaw empty sessions (heartbeat filtering) is OpenClaw-side. _2026-03-24_

- [~] **[investigate]** Engram + OpenClaw integration architecture — requires OpenClaw SDK source audit (external repo). Message classification design documented but implementation depends on OpenClaw SDK changes. _2026-03-24_

- [x] **[idea]** ~~UI: memory notes viewer~~ RESOLVED — ObservationsView already has "Memories" toggle with edit/delete. No separate view needed. _2026-03-24_
- [x] **[idea]** ~~Memory: tree structure + Obsidian-style graph~~ IMPLEMENTED v2.1.6 (PR #119) — local graph mode, search, visual styling _2026-03-24_
- [x] **[bug]** ~~Dashboard: Pattern insight background generation~~ IMPLEMENTED v2.1.7 (PR #121) — maintenance Task 18, 5 per cycle. LLM must be available. _2026-03-28_
- [x] **[bug]** ~~Dashboard UX: unclear actions~~ FIXED v2.1.8 (PR #122) — tooltips, cursor-pointer, hover transitions, color-coded actions _2026-03-28_
- [x] **[bug]** ~~Dashboard: Search Misses empty rows~~ FIXED v2.1.7 (PR #121) — envelope unwrap + field mapping _2026-03-28_
- [x] **[bug]** ~~Dashboard: Sessions page useless~~ FIXED v2.1.7 (PR #121) — min_prompts filter, date filters, clickable detail view _2026-03-28_
- [x] **[idea]** ~~Memory: consistency checker~~ IMPLEMENTED v2.1.5 (PR #118) — GET /api/maintenance/consistency _2026-03-24_
- [x] **[idea]** ~~Memory: search indexes~~ RESOLVED — 50+ indexes already exist (FTS tsvector, GIN JSONB, composite covering) _2026-03-24_
- [x] **[idea]** ~~Plugin: memory_get markdown bridge~~ IMPLEMENTED v2.1.5 (PR #118) — store=true flag imports .md into engram _2026-03-24_
- [x] **[investigate]** ~~Audit incomplete specs~~ DONE — 4 specs marked Implemented (plugin-tool-consolidation, mcp-tool-api-consolidation, dashboard-bugfixes-v2, engram-user-commands). Remaining specs have accurate status. _2026-03-28_
- [x] **[debt]** ~~Missing MCP tools: tag_observation~~ Already implemented (server.go line 890). _2026-03-24_ → verified 2026-03-28
- [~] **[bug]** OpenClaw engram v1.4.0 — 90s init delay regression. DEFERRED — external (needs OpenClaw gateway-side profiling, not engram code). _2026-03-25_
- [x] **[debt]** ~~store_memory without always-inject concept~~ Fixed: added always_inject param (PR #98). _2026-03-28_
- [x] **[bug]** ~~CC bug #19225: Stop hooks don't fire~~ MITIGATED — workaround in settings.json, summarization moved to session-start (v2.1.3). Upstream CC issue, not engram. _2026-03-28_
- [x] **[bug]** ~~Dashboard: Concept filter shows "No items to display"~~ FIXED v2.1.1 (PR #114) — JSONB @> server-side filter _2026-03-28_
- [x] **[bug]** ~~Dashboard: "50 obs · 50 prompts" hardcoded~~ FIXED v2.1.1 (PR #114) — real counts from API _2026-03-28_
- [x] **[bug]** ~~Dashboard Summaries empty~~ MITIGATED v2.1.3 (PR #116) — session-start hook now triggers summarization of previous unsummarized session. Root cause: stop hook doesn't fire (CC #19225). _2026-03-28_
- [x] **[idea]** ~~Engram CC plugin user commands~~ IMPLEMENTED (PR #115) — retro, stats, cleanup, export _2026-03-28_

## Investigate Report Findings (2026-03-28)

### P1 — Must Fix
- [x] **[P1]** ~~OpenClaw before_tool_call~~ FIXED v2.2.1 (PR #125) — BeforeToolCallResult added to HookResult _2026-03-29_
- [x] **[P1]** ~~Summaries verification~~ Server-side summarizer deployed, summary generated for session #67376 _2026-03-29_
- [x] **[P1]** ~~Summary dedup~~ VERIFIED — NOT EXISTS check already in Task 19 SQL _2026-03-29_

### P2 — Should Fix
- [x] **[P2]** ~~Store content validation~~ FIXED v2.2.1 — error message clarified _2026-03-29_
- [x] **[P2]** ~~Summary threshold~~ FIXED v2.2.1 — lowered from 50 to 10 chars _2026-03-29_
- [x] **[P2]** ~~Circuit breaker logging~~ FIXED in PR #124 _2026-03-28_
- [ ] **[P2]** Behavioral rules effectiveness metric misleading — measures citation, not compliance. Known limitation, needs design discussion. _F-0-8_ _2026-03-28_
- [x] **[P2]** ~~Concept backfill~~ FIXED v2.2.1 — migration 064 adds 5 missing concepts _2026-03-29_
- [ ] **[P2]** Dashboard not visually verified (Constitution #14). _F-1-13_ _2026-03-28_
