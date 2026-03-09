# Continuity State

**Last Updated:** 2026-03-09
**Session:** Full backfill + agent adoption improvements

## Done
- Self-learning plan: all 3 phases complete (Phase 4 deferred to v1.1)
- RAG improvements plan: ALL 3 PHASES COMPLETE
- FalkorDB optional graph backend: ALL 6 PHASES COMPLETE
- Embedding platform split: Windows build fix COMPLETE
- Deployment cleanup: ALL 4 PHASES COMPLETE
- **FalkorDB int64 panic fix** (commit `39cead0`)
- **MCP panic recovery** (commit `cf20eb7`)
- **Plugin marketplace restructured**: lightweight `thebtf/engram-marketplace` repo
- **MCP instructions** (`8e28f2a`)
- **Auto-sync workflow** (`3ab5321`)
- **Plugin version bump** (`f083efb`): 0.5.0 → 0.5.1
- **Plugin install fix** (`395e698`): restructured marketplace to prevent recursive install
- **Session backfill Phase 1 core** (`861a807`→`e17a60b`):
  - Refactored PoC → 5 production packages: sanitize, chunk, extract, metrics, backfill.go
  - Added `POST /api/backfill` + `GET /api/backfill/status` server endpoints
  - Created `cmd/engram-cli/` with `backfill` subcommand
  - Added `models.SourceBackfill` source type
  - FR4: Semantic dedup (cosine > 0.92) in server endpoint (`247791f`)
  - FR5: Temporal decay for backfill source in scoring (`d516072`)
  - FR6: Progress tracking with --resume and --state-file (`e17a60b`)
  - MCP tool: `backfill_status` via callback injection (`3585e43`)
- **Server-side LLM extraction** (`493fa1f`):
  - Added `POST /api/backfill/session` — server parses raw JSONL + extracts via LLM
  - CLI v0.2.0 — thin client, no local LLM dependency
  - Added `ProcessSession` method to `backfill.Runner`
- **Agent adoption improvements** (`f914a27`):
  - Added MANDATORY rules at top of MCP instructions (`internal/mcp/server.go`)
  - Added reminder at end of `<relevant-memory>` block (`plugin/engram/hooks/user-prompt.js`)
  - Goal: agents proactively call `find_by_file`, `decisions`, `search` before working

## Now
Full backfill running: `engram-cli backfill --concurrency 3 --resume` processing ~4301 sessions.
Background task `b8tnhyi98`. At ~163/4301 (~3.8%) as of last check.
Server restarted mid-run (Watchtower pulled new image from `f914a27` push) — ~12 connection errors around sessions 45-71, self-recovered.

## Verified Complete (this session audit)
- Collection MCP Tools plan (`vast-wishing-taco.md`): ALL 5 phases done
- RAG Improvements plan: ALL 3 phases done

## Next
- Monitor backfill completion (~4301 sessions total)
- Phase 3: Quality gate — verify search precision hasn't degraded >5%
- Phase 4: Quality report for user decision
- Test agent adoption: verify new agents call engram tools proactively

## Open Questions
- None

## Known Pre-existing Test Failures (Windows)
- `TestSafeResolvePath` — Windows path separator mismatch
- `TestConfigSuite/TestLoad_TableDriven` — env var isolation issue
- `TestKillProcessOnPort_NoProcess` — `lsof` not available on Windows
- `go-tree-sitter` — CGO build constraints exclude Windows

## Key Files
- Backfill packages: `internal/backfill/` (sanitize, chunk, extract, metrics, backfill.go)
- Backfill CLI: `cmd/engram-cli/main.go`
- Backfill server: `internal/worker/handlers_backfill.go`
- Backfill spec: `.agent/specs/session-backfill.md`
- Plugin source of truth: `plugin/` (hooks, skills, commands)
- MCP instructions: `internal/mcp/server.go` (line 295, `engramInstructions` const)
- FalkorDB client: `internal/graph/falkordb/client.go`
- MCP streamable handler: `internal/mcp/streamable.go`

## Plan Documents
- Global Roadmap: `.agent/plans/global-roadmap.md`
- Collection MCP Tools: `~/.claude/plans/vast-wishing-taco.md`
- RAG Improvements: `.agent/plans/rag-improvements.md`
