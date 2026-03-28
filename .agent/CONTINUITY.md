# Continuity State

**Last Updated:** 2026-03-28 15:00
**Branch:** main
**Server Version:** v2.1.7
**Plugin Versions:** openclaw-engram 2.1.6 (npm)

## Done This Session
- v2.0.8→v2.1.7 (10 releases, 10 PRs: #112-#121)
- MCP tool consolidation: 68→7 primary (recall/store/feedback/vault/docs/admin/health)
- Legacy aliases removed from tools/list (dispatch-only)
- Plugin expansion: OpenClaw 8→17 tools + lifecycle hooks (before_tool_call, session outcome)
- CC plugin: 4 user commands (retro/stats/cleanup/export), pre-edit guardrails, statusline learning metrics
- Dashboard fixes: concept filter (JSONB @>), type filter (server-side), real counts, session-start summarizer
- Dashboard quality v3: search misses fix, sessions page redesign (filter/detail view), pattern insight background
- Config hot-reload (no os.Exit)
- Consistency check endpoint (GET /api/maintenance/consistency)
- memory_get store flag (import .md to engram)
- Graph UX: local mode, node search, visual styling
- Summaries: ProcessSummary builds from observations when no transcript
- 3 behavioral rules (always_inject: find_by_file, decisions, rate)
- TD: 21/21 resolved. Inbox: all resolved except 1 external (OpenClaw architecture)
- Benchmark script updated (max_tokens:4096, parallel=1)

## PRs: #112-#121 (10 PRs merged)
## Releases: v2.0.8, v2.0.9, v2.1.0-v2.1.7

## Current State
All code tasks complete. Dashboard deployed and running.

## Known Issues
- Summaries: depend on LLM availability (circuit breaker may block)
- Pattern insights: background gen depends on LLM (same circuit breaker)
- 704 observations without vectors, 3341 stale relations (from consistency check)

## Next
- Visual verification of dashboard (Constitution #14)
- Run maintenance to trigger pattern insight generation
- Monitor summaries after next session cycle
