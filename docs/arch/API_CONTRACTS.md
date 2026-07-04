# API Contracts

MCP tools, HTTP endpoints, gRPC services, and hook interfaces for engram v6.

---

## MCP Tools

Registered in `internal/mcp/server.go` (plus `tools_*.go` for the conditional
families). The `engram` stdio daemon exposes these via JSON-RPC 2.0 over
stdin/stdout.

The advertised tool set is **not a fixed count**: a stable core is always
present, and additional families are advertised only when their store is wired
and their feature flag is on (see "Conditional Tool Families" below). Do not
treat any single total as canonical — inspect `ListTools()` on the running
server for the live set.

### Primary Tools (7 consolidated)

These are the recommended entry points. Each supports an `action` parameter
that routes to the appropriate operation.

| Tool | Actions | Description |
|------|---------|-------------|
| `recall` | search | Search and retrieve memories |
| `store` | create, edit, merge, import | Store, modify, or merge memories |
| `feedback` | rate, suppress, outcome | Rate memories, suppress, record session outcomes |
| `vault` | store, get, list, delete, status | Manage encrypted credentials |
| `docs` | create, read, list, history, comment, collections, documents, get_doc, remove, ingest, search_docs | Versioned documents and collections |
| `admin` | stats, search_analytics, backfill_status | Administrative operations |
| `issues` | create, list, get, update, comment, reopen, close | Cross-project issue tracker |

### Compatibility Tools

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

### Conditional Tool Families

These are advertised only when their backing store is wired AND their gate is
satisfied. Flag-off paths are byte-identical to before the family landed, so a
default deployment sees only the core tools above plus whichever families are
enabled.

| Family | Tools | Advertised when | Since |
|--------|-------|-----------------|-------|
| Memory extras | `find_by_file`, `find_similar_observations`, `get_memory_stats`, `get_memory_brief`, `set_session_outcome`, `import_instincts` | memory/promotion stores wired | v5+ |
| Lifecycle | `lifecycle` | memory + promotion store AND `ENGRAM_LIFECYCLE_ENABLED=true` | v6 |
| Graph | `graph` | graph store AND `ENGRAM_GRAPH_ENABLED=true` | v6 |
| Native state plane | `get_state`, `set_state` | state store wired | v6.30 (CR-006) |
| Principal memory | `query_principal_memory` | principal-memory query service wired | v6.30 (CR-007) |
| Review-loop candidates | `list_candidates`, `get_candidate`, `promote_candidate`, `reject_candidate`, `supersede_candidate` | `ENGRAM_VNEXT_F_ENABLED=true` AND candidate store wired | v6.32 (CR-008) |
| Governance snapshots | `list_snapshots`, `rollback_snapshot`, `pin_snapshot`, `redaction_rules_status` | `ENGRAM_VNEXT_F_ENABLED=true` AND snapshot store wired | v6 (Milestone-F) |
| Rule governance | `rule_governance_health`, `rule_governance_queue`, `rule_governance_snapshots`, `rule_governance_transition`, `rule_governance_pin_snapshot`, `rule_governance_rollback`, `rule_governance_usefulness` | rule-governance read store wired (`usefulness` needs injection telemetry) | v6.29 (RG-3) |
| Bulk ops | `bulk_promote`, `bulk_delete`, `bulk_supersede` | `ENGRAM_VNEXT_F_ENABLED=true` | v6 (Milestone-F) |
| Code intelligence | `codebase_search` | `ENGRAM_CODE_INTEL_ENABLED=true` AND code chunk store wired | v6.13 (CR-006 code-intel) |
| V7 state subsystem | existing `get_state`, `set_state` via v7 `StateWriter` adapter | `ENGRAM_V7_PLUG_ENABLED=true` AND `ENGRAM_V7_S1_STATE=true` AND state store wired | v6.37 (ENG-V7-S1) |
| V7 meta-memory discovery | `know_about` plus session-start `meta_summary` | `ENGRAM_V7_PLUG_ENABLED=true` AND `ENGRAM_V7_S2_METAMEM=true` AND meta-memory index wired | v6.38 (ENG-V7-S2) |

Admin-gated families (candidates, governance, bulk ops) are additionally
enforced at the handler level regardless of advertisement.

#### `know_about` Response Shape

`know_about` is a content-free discovery tool. It accepts a required `topic`, an
optional `project`, and a bounded `limit` up to 25. It returns JSON with this
canonical shape:

```json
{
  "topic": "handoff protocol",
  "project": "engram",
  "count": 0,
  "total_candidates": 0,
  "top_tags": [],
  "date_range": {},
  "memories": []
}
```

Each entry in `memories` may include id, project, title, tags, created/updated
timestamps, score, source, and reason. It must not include memory body text or
content-bearing aliases such as `content`, `body`, `raw_content`, `narrative`,
or `snippet`. A valid topic with no matches returns the empty packet above, not
a tool error.

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
