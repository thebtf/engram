# API Contracts

MCP tools, HTTP endpoints, gRPC services, and hook interfaces for engram v6.

---

## MCP Tools (39 total)

Registered in `internal/mcp/server.go`. The `engram` stdio daemon exposes these
via JSON-RPC 2.0 over stdin/stdout.

### Primary Tools (7 consolidated)

These are the recommended entry points. Each supports an `action` parameter
that routes to the appropriate operation.

| Tool | Actions | Description |
|------|---------|-------------|
| `recall` | search, by_file | Search and retrieve memories |
| `store` | create, edit, merge, import | Store, modify, or merge memories |
| `feedback` | rate, suppress, outcome | Rate memories, suppress, record session outcomes |
| `vault` | store, get, list, delete, status | Manage encrypted credentials |
| `docs` | create, read, list, history, comment, collections, documents, get_doc, remove, ingest, search_docs | Versioned documents and collections |
| `admin` | stats, search_analytics, backfill_status | Administrative operations |
| `issues` | create, list, get, update, comment, reopen, close | Cross-project issue tracker |

### Compatibility Tools (31)

Legacy aliases from before tool consolidation. Each maps to a primary tool action.

**Memory:**
`store_memory`, `recall_memory`, `rate_memory`, `suppress_memory`,
`find_by_file`, `find_similar_observations`,
`get_memory_stats`, `set_session_outcome`, `import_instincts`, `backfill_status`

**Sessions:**
`search_sessions`, `list_sessions`

**Credentials:**
`store_credential`, `get_credential`, `list_credentials`, `delete_credential`, `vault_status`

**Documents:**
`list_collections`, `list_documents`, `get_document`, `remove_document`,
`ingest_document`, `search_collection`

**Versioned Documents:**
`doc_create`, `doc_read`, `doc_list`, `doc_history`, `doc_comment`

**Rules:**
`store_rule`, `list_rules` (conditional — only registered when behavioral rules store is initialized)

**System:**
`check_system_health`

---

## HTTP API (:37777)

`engram-server` serves HTTP via chi router on :37777 (cmux multiplexed with gRPC).

### Authentication (v6)

Two-tier token model:
- **Operator token** (`ENGRAM_AUTH_ADMIN_TOKEN`): full admin access
- **Worker keycards** (per-workstation): issued via `/tokens` dashboard page

```
Authorization: Bearer <token>
```

Bypass: `ENGRAM_AUTH_SKIP_LOCAL=true` skips auth for RFC 1918 addresses.

### Core Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/context/inject` | Context injection for session-start. Params: `project`, `cwd`. Returns memories as JSON. |
| `GET` | `/api/version` | Server version string. |
| `GET` | `/api/logs` | Recent log lines from in-memory ring buffer. |
| `GET` | `/api/health` | Health check. |
| `GET` | `/api/memories` | List/search memories. |
| `POST` | `/api/memories` | Create memory. |
| `PATCH` | `/api/memories/:id` | Update memory. |
| `DELETE` | `/api/memories/:id` | Delete memory. |
| `GET` | `/api/issues` | List issues. |
| `POST` | `/api/issues` | Create issue. |
| `PATCH` | `/api/issues/:id` | Update issue (status, labels, etc.). |
| `GET` | `/api/tokens` | List API tokens. |
| `POST` | `/api/tokens` | Create worker keycard. |
| `DELETE` | `/api/tokens/:id` | Revoke token. |

### Hook Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/hooks/session-start` | Session initialization + context injection. |
| `POST` | `/api/hooks/user-prompt` | Record user prompt text. |
| `POST` | `/api/hooks/post-tool-use` | Record tool invocation. |
| `POST` | `/api/hooks/pre-tool-use` | Pre-tool context enrichment. |
| `POST` | `/api/hooks/pre-compact` | Pre-compaction state capture. |
| `POST` | `/api/hooks/stop` | Session end summary. |
| `POST` | `/api/hooks/session-end` | Final session recording. |
| `POST` | `/api/hooks/subagent-stop` | Subagent completion event. |
| `GET` | `/api/status` | Statusline data (memory count). |

### Dashboard

Vue.js dashboard served at `/` (embedded from `ui/dist/` at build time).
Real-time updates via SSE event bus at `/api/events`.

---

## gRPC Services (:37777)

Multiplexed on the same port via cmux. The `engram` daemon connects via gRPC.

Services defined in `internal/grpcserver/`:
- Memory operations (store, recall, search)
- Session management
- Context injection
- Health checks

Proto definitions in `proto/` directory.

---

## Hook Interfaces

### Input Format

All hooks receive JSON on stdin from Claude Code:

```json
{
  "hook_type": "session-start",
  "session_id": "uuid",
  "project": "project-slug",
  "cwd": "/path/to/project",
  "transcript_path": "/path/to/transcript.jsonl"
}
```

### Output Format

Hooks write to stdout for Claude Code to consume:

- `session-start.js`: `<engram-context>...</engram-context>` block
- `statusline.js`: statusline text
- Other hooks: empty (fire-and-forget)

### Configuration

Hook registration in `plugin/engram/hooks/hooks.json`. Each hook specifies:
- `type`: PreToolUse, PostToolUse, Stop, etc.
- `command`: path to JS file
- Matching rules (tool names, event types)

---

## Response Format

All HTTP API responses are JSON. Serialization via `github.com/goccy/go-json`.

Error responses:
```json
{
  "error": "human-readable message"
}
```

No internal error details are exposed in HTTP responses (logged server-side only).
