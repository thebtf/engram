# Event Model

engram defines an abstract event model for memory lifecycle operations. Consumer
adapters (Claude Code, Codex, etc.) translate these canonical events to their
native hook/plugin system.

## Canonical Events

### session_start

**Trigger:** Agent session begins.
**Payload:**
```json
{
  "project": "string",
  "session_id": "string",
  "agent_id": "string",
  "cwd": "string",
  "files_being_edited": ["string"]
}
```
**Server endpoint:** `GET/POST /api/context/session-start`
**Claude Code hook:** `plugin/engram/hooks/session-start.js`

> **AMEND 2026-06-11 (W4 triage E1 — doc fix):** Original doc text used `/api/context/inject`. The actual registered route for session_start is `/api/context/session-start` (verified `internal/worker/service.go:1132-1133`). `/api/context/inject` exists as a separate route (service.go:1130-1131) serving the pre-compact priming path, not session_start. The hook (`session-start.js:124`) correctly calls `/api/context/session-start`. Doc corrected to match code.

### session_end

**Trigger:** Agent session ends (stop hook fires).
**Payload:**
```json
{
  "session_id": "string",
  "project": "string",
  "agent_output_text": "string"
}
```
**Server endpoint:** `POST /api/hooks/session-end`
**Claude Code hook:** `plugin/engram/hooks/stop.js`

### memory_write

**Trigger:** Memory stored via MCP tool.
**Payload:** MCP `store_memory` arguments (content, tags, tier, etc.)
**Server endpoint:** MCP stdio `store_memory` tool call
**Claude Code hook:** N/A (MCP tool, not a hook)

### pre_compact

**Trigger:** Context window about to be compacted.
**Payload:**
```json
{
  "project": "string",
  "topic_hint": "string (optional)"
}
```
**Server endpoint:** `GET /api/context/inject` (same as session_start, primes cache)
**Claude Code hook:** `plugin/engram/hooks/pre-compact.js`

### tool_result

**Trigger:** Tool execution complete.
**Payload:**
```json
{
  "tool_name": "string",
  "result_summary": "string"
}
```
**Server endpoint:** N/A (client-side processing only)
**Claude Code hook:** `plugin/engram/hooks/post-tool-use.js`

## Consumer Adapter Matrix

| Event | Claude Code | Codex | OpenClaw | Hermes |
|-------|------------|-------|----------|--------|
| session_start | session-start.js | deferred | deferred | deferred |
| session_end | stop.js | deferred | deferred | deferred |
| pre_compact | pre-compact.js | N/A (no compaction) | deferred | deferred |
| tool_result | post-tool-use.js | deferred | deferred | deferred |
| memory_write | MCP tool | MCP tool | deferred | deferred |

Codex, OpenClaw, and Hermes adapters are deferred until a concrete consumer
exists to test against (PRD §6 Milestone E, finding O3).

## Design Principles

1. **Event model is abstract.** Consumer adapters translate to native format.
2. **Server-side is event-agnostic.** REST/gRPC endpoints accept data; they don't
   know which consumer sent it.
3. **Hooks are client-side.** They run in the consumer's process, not on the server.
4. **MCP tools are the universal adapter.** Any MCP-compatible consumer can use
   engram via the stdio MCP proxy without custom hooks.
