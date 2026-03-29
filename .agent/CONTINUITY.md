# Continuity State

**Last Updated:** 2026-03-28 18:00
**Branch:** main
**Server Version:** v2.1.9
**Plugin Versions:** openclaw-engram 2.1.6 (npm)

## Done This Session
- v2.0.8→v2.1.9 (11 releases, 11 PRs: #112-#123)
- MCP tool consolidation: 68→7 primary tools (no legacy aliases in tools/list)
- OpenClaw: 8→17 tools + lifecycle hooks
- CC plugin: 4 user commands, pre-edit guardrails, statusline learning metrics
- Dashboard: concept/type/count fixes, sessions detail view, search misses, graph UX polish, UX tooltips
- Config hot-reload, consistency check endpoint, memory_get import bridge
- Summaries + concepts audit: 3 root causes found and fixed (PR #123)
- TD: all resolved. Inbox: all resolved.
- 4 behavioral rules stored as always_inject

## Current State
All code tasks complete. Embedding API was down (context deadline exceeded) — user reloaded model, now working.

## Known Active Issues
- Embedding API intermittent timeouts (llm.unleashed.nv.md)
- Vector search degrades to FTS→recent when embedding down
- Benchmark script updated but not yet run (parallel=1 default)
