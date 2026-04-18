# API Contracts

This document covers the MCP tool contracts, known Worker HTTP endpoints, hook interfaces, and the SSE transport protocol.

---

## MCP Tools (nia namespace)

The MCP server exposes 37 tools under the `nia` namespace via the MCP stdio protocol (JSON-RPC 2.0). All tools are registered in `internal/mcp/server.go`.

Tools accept JSON arguments and return JSON results. Error responses follow the MCP spec (code + message).

### Search and Discovery

| Tool | Key Parameters | Description |
|------|---------------|-------------|
| `search` | `query: string`, `project?: string`, `type?: string`, `limit?: int` | Hybrid semantic + FTS search across all memory |
| `timeline` | `project?: string`, `date_start?: int`, `date_end?: int`, `limit?: int` | Browse observations by time range |
| `decisions` | `query: string`, `project?: string` | Find architecture/design decisions (keyword-boosted: "decision chose architecture") |
| `changes` | `query: string`, `project?: string` | Find code modifications (keyword-boosted: "changed modified refactored") |
| `how_it_works` | `query: string`, `project?: string` | System understanding queries (keyword-boosted: "architecture design pattern implements") |
| `find_by_concept` | `concept: string`, `project?: string`, `limit?: int` | Find observations matching a concept tag |
| `find_by_file` | `file: string`, `project?: string` | Find observations related to a specific file path |
| `find_by_type` | `obs_type: string`, `project?: string`, `limit?: int` | Find by type: decision\|bugfix\|feature\|refactor\|discovery\|change |
| `find_similar_observations` | `id: int64`, `limit?: int` | Vector similarity search from a given observation |
| `find_related_observations` | `id: int64`, `relation_type?: string`, `limit?: int` | Graph relation traversal from a given observation |
| `explain_search_ranking` | `query: string`, `project?: string` | Debug output showing ranking scores and fusion details |

### Context Retrieval

| Tool | Key Parameters | Description |
|------|---------------|-------------|
| `get_recent_context` | `project: string`, `limit?: int` | Recent observations for a project |
| `get_context_timeline` | `project: string`, `periods?: int` | Context organized by time periods |
| `get_timeline_by_query` | `query: string`, `project?: string` | Query-filtered chronological timeline |
| `get_patterns` | `project?: string`, `type?: string` | Detected recurring patterns |

### Observation Management

| Tool | Key Parameters | Description |
|------|---------------|-------------|
| `get_observation` | `id: int64` | Retrieve a single observation by ID |
| `edit_observation` | `id: int64`, `title?: string`, `narrative?: string`, `facts?: []string` | Modify observation fields |
| `tag_observation` | `id: int64`, `concepts: []string` | Add concept tags to an observation |
| `get_observations_by_tag` | `concept: string`, `project?: string` | Find observations by concept tag |
| `merge_observations` | `source_id: int64`, `target_id: int64`, `reason?: string` | Merge duplicate observations (source marked superseded) |
| `bulk_delete_observations` | `ids: []int64` | Batch delete observations by ID |
| `bulk_mark_superseded` | `ids: []int64`, `reason?: string` | Mark observations as superseded |
| `bulk_boost_observations` | `ids: []int64`, `boost: float64` | Increase importance scores |
| `export_observations` | `project?: string`, `format?: string`, `limit?: int` | Export observations as JSON |

### Analysis and Quality

| Tool | Key Parameters | Description |
|------|---------------|-------------|
| `get_memory_stats` | `project?: string` | Overall memory statistics (counts, score distributions) |
| `get_observation_quality` | `id: int64` | Quality score breakdown for a single observation |
| `get_temporal_trends` | `project?: string`, `periods?: int` | Trend analysis over time windows |
| `get_data_quality_report` | `project?: string` | Data quality metrics across observations |
| `batch_tag_by_pattern` | `pattern: string`, `concepts: []string`, `project?: string` | Auto-tag observations matching a text pattern |
| `analyze_search_patterns` | — | Search usage analytics (frequent queries, latency stats) |
| `get_observation_relationships` | `id: int64`, `depth?: int` | Full relation graph for an observation |
| `get_observation_scoring_breakdown` | `id: int64` | Detailed scoring formula breakdown |
| `analyze_observation_importance` | `project?: string` | Importance distribution analysis |
| `check_system_health` | — | System health check (DB, vector client, embedding service) |

### Sessions

| Tool | Key Parameters | Description |
|------|---------------|-------------|
| `search_sessions` | `query: string`, `workstation_id?: string`, `project_id?: string`, `limit?: int` | Full-text search across indexed JSONL sessions |
| `list_sessions` | `workstation_id?: string`, `project_id?: string`, `limit?: int` | List sessions with optional filtering |

---

## Worker HTTP API (:37777)

The worker binds on port 37777 (configurable) using the chi router. The following endpoints are confirmed from hook source code and are likely incomplete — additional endpoints exist in `internal/worker/` handlers.

### Confirmed Endpoints (from hook source)

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/context/inject` | Returns observations for session-start context injection. Query params: `project`, `cwd`. Response: `{observations: [...], full_count: int}` |
| `GET` | `/api/sessions` | Find session by Claude session ID. Query param: `claudeSessionId`. Response: `{id: float64, ...}` |
| `POST` | `/sessions/{id}/summarize` | Create session summary. Body: `{lastUserMessage: string, lastAssistantMessage: string}` |
| `GET` | `/health` | Health check (used by `make start-worker` to verify startup) |

### Inferred Endpoints (from hook usage patterns)

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/hooks/user-prompt` | Record user prompt event |
| `POST` | `/api/hooks/post-tool-use` | Record tool invocation event |
| `POST` | `/api/hooks/subagent-stop` | Record subagent completion event |
| `GET` | `/api/status` | Worker status (used by statusline hook for memory count) |

### Authentication

When `ENGRAM_API_TOKEN` is set, all requests to the worker require:
```
Authorization: Bearer <token>
```

Requests without the token return HTTP 401.

### Response Format

All API responses are JSON. The worker uses `github.com/goccy/go-json` for faster serialization.

### Dashboard

The worker serves a Vue.js dashboard at `/` (embedded from `ui/dist/` at build time). The dashboard provides a real-time view of observations and search via SSE events.

---

## MCP SSE Transport (integrated into worker :37777)

The worker exposes the MCP protocol over HTTP Server-Sent Events for remote workstations.

### Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/sse` | SSE stream — client subscribes to receive MCP server messages |
| `POST` | `/message` | Client sends MCP requests (JSON-RPC) |

### Authentication

Bearer token required when `ENGRAM_API_TOKEN` is set:
```
Authorization: Bearer <token>
```

### Protocol Flow

```
Remote workstation                    Central SSE Server (:37778)
      |                                        |
      | GET /sse  (SSE subscribe)              |
      |--------------------------------------->|
      |                          SSE stream    |
      |<---------------------------------------|
      |                                        |
      | POST /message (MCP JSON-RPC request)   |
      |--------------------------------------->|
      |               Process request via DB   |
      |                  SSE event (response)  |
      |<---------------------------------------|
```

The `bin/mcp-stdio-proxy` bridges this to the MCP stdio protocol expected by Claude Code:
- Reads MCP JSON-RPC from stdin
- POSTs to `/message` on the remote SSE server
- Subscribes to the SSE stream for the response
- Writes the response to stdout

---

## Hook Interfaces

All hooks are JavaScript files in `plugin/engram/hooks/`, executed via `node` by the Claude Code plugin system.
Each hook reads JSON from stdin, processes it, and writes a JSON response object to stdout.
Hooks communicate with the remote worker via HTTP using the shared `lib.js` module.

### Hook Output Contract (stdout)

All hooks (except the statusline hook) must write a JSON object to stdout via `lib.js`'s `writeResponse()`:

```json
{
    "continue": true,
    "hookSpecificOutput": {
        "hookEventName": "string",
        "additionalContext": "string"
    }
}
```

The `hookSpecificOutput` field is only included when the hook returns a non-empty string (e.g., `session-start` returns an XML context block). For fire-and-forget hooks, only `{ "continue": true }` is written.

The **statusline hook** is the exception: it writes plain text to stdout (not JSON).

### Hook Input (all hooks receive via stdin)

```json
{
    "session_id": "string",
    "cwd": "string"
}
```

The `lib.js` module derives:
- Worker URL from `ENGRAM_URL` environment variable (origin only; path stripped)
- Project identifier using `ProjectIDWithName(cwd)`:
  1. **Primary:** git remote origin URL + relative repo path → SHA-256 hash prefix (cross-workstation stable)
  2. **Fallback:** CWD path hash (for directories without a git remote)

### session-start Hook

```json
// Input (stdin JSON)
{
    "session_id": "string",
    "cwd": "string",
    "source": "startup|resume|clear|compact"
}
// Return value (stdout JSON): { "continue": true, "hookSpecificOutput": { "hookEventName": "session-start", "additionalContext": "<engram-context>...</engram-context>" } }
// On error or empty context: { "continue": true }
```

**Behavior:**
1. GET `/api/context/inject?project=X&cwd=Y`
2. Returns context as XML block injected into Claude Code session
3. First `full_count` (default 25) observations: full detail (title + narrative + facts)
4. Remaining observations: condensed (title + subtitle only)
5. Silent failure on error (returns empty string — Claude Code continues normally)

### user-prompt Hook

**Input:** BaseInput + prompt text fields
**Return value (stdout JSON):** `{ "continue": true }` (fire-and-forget, no additionalContext)
**Effect:** Records user prompt in `user_prompts` table via worker POST

### post-tool-use Hook

**Input:** BaseInput + tool name + tool input/output fields
**Return value (stdout JSON):** `{ "continue": true }`
**Effect:** Records tool invocation event via worker POST

### subagent-stop Hook

**Input:** BaseInput + subagent fields
**Return value (stdout JSON):** `{ "continue": true }`
**Effect:** Records subagent completion event via worker POST

### stop Hook

```go
// Input
type Input struct {
    hooks.BaseInput
    TranscriptPath string `json:"transcript_path"` // path to session JSONL transcript
    StopHookActive bool   `json:"stop_hook_active"`
}
// Return value (stdout JSON): { "continue": true }
```

**Behavior:**
1. GET `/api/sessions?claudeSessionId=X` to find session record
2. Read `transcript_path` JSONL file directly from disk
3. Extract last user message and last assistant message
4. POST `/sessions/{id}/summarize` with extracted messages
5. Worker generates session summary in `session_summaries` table

**Note:** Transcript file must be accessible from the hook process filesystem.

### statusline Hook

**Input:** BaseInput
**Return value:** Short status string for Claude Code statusline
**Effect:** GET `/api/status` — returns memory count, e.g. `"🧠 42 memories"`

---

## MCP stdio Protocol (JSON-RPC 2.0)

The MCP server (`bin/mcp-server`) communicates via newline-delimited JSON on stdin/stdout.

**Request format:**
```json
{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","arguments":{"query":"authentication","project":"myapp"}}}
```

**Response format:**
```json
{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"..."}]}}
```

**Initialization:**
```json
// Client sends:
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"claude-code","version":"..."}}}

// Server responds with capabilities including tools list
```

All logs go to **stderr** — stdout is reserved for the MCP protocol.
