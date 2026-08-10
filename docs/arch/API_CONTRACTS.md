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

### Primary Tools (9)

These are the stable entry points. Action enums list only routes that have live handlers.

| Tool | Actions | Description |
|------|---------|-------------|
| `recall` | search | Search and retrieve memories |
| `store` | create, edit, import | Create, edit, or import memories |
| `feedback` | suppress, outcome | Suppress memories or record session outcomes |
| `vault` | store, get, list, delete, status | Manage encrypted credentials |
| `settings` | set, get, list, delete | Manage recall model settings |
| `docs` | create, read, list, history, comment, collections, documents, get_doc, remove, ingest | Manage versioned documents and collections |
| `admin` | stats; purge_project when enabled | Read administrative telemetry or perform gated project purge |
| `issues` | create, list, get, update, comment, reopen, close | Cross-project issue tracker |
| `check_system_health` | none | Report subsystem health |

### Expanded Tools

The expanded surface contains only provider-backed tools whose handler is currently usable. Retired compatibility names are neither advertised nor dispatched. Clients should inspect `tools/list` with `include_all: true` rather than assuming a historical alias exists.

### Conditional Tool Families

| Family | Tools | Advertised when | Since |
|--------|-------|-----------------|-------|
| Behavioral rules | `store_rule`, `list_rules` | behavioral-rules store wired | v5 |
| Expanded memory retrieval | `recall_memory` | memory store wired | v5 |
| Adaptive memory brief | `get_memory_brief` | memory store wired AND `ENGRAM_ADAPTIVE_ENABLED=true` | v6 |
| S6 significance | `rate_memory_significance` | S6 outcome flags enabled AND significance updater wired | v6.41 |
| Ambient hints | `get_ambient_hints` | ambient-hints flag enabled AND hint queue wired | v6.42 |
| Ingest | `ingest` | memory store wired | v6 |
| Lifecycle | `lifecycle` | memory + promotion store AND `ENGRAM_LIFECYCLE_ENABLED=true` | v6 |
| Graph | `graph` | graph store AND `ENGRAM_GRAPH_ENABLED=true` | v6 |
| Native state plane | `get_state`, `set_state` | state store wired | v6.30 (CR-006) |
| Principal memory | `query_principal_memory` | principal-memory query service wired | v6.30 (CR-007) |
| Experience history | `experience_history.read`, `experience_history.detail` | experience provider wired | v6.35 (CR-009) |
| Temporal truth | `temporal_truth`, `temporal_truth_refresh` | temporal provider wired AND temporal-truth flag enabled | v6.36 (CR-011) |
| Directive capture | `remember_directive` | directive service wired AND directive-capture flag enabled | v6.39 (ENG-V7-S4A) |
| Review-loop candidates | `list_candidates`, `get_candidate`, `promote_candidate`, `reject_candidate`, `supersede_candidate` | `ENGRAM_VNEXT_F_ENABLED=true` AND candidate store wired | v6.32 (CR-008) |
| Governance snapshots | `list_snapshots`, `rollback_snapshot`, `pin_snapshot`, `redaction_rules_status` | `ENGRAM_VNEXT_F_ENABLED=true` AND snapshot store wired | v6 (Milestone-F) |
| Rule governance | `rule_governance_health`, `rule_governance_queue`, `rule_governance_snapshots`, `rule_governance_transition`, `rule_governance_pin_snapshot`, `rule_governance_rollback`, `rule_governance_usefulness` | rule-governance read store wired (`usefulness` also needs injection telemetry) | v6.29 (RG-3) |
| Bulk ops | `bulk_promote`, `bulk_delete`, `bulk_supersede` | `ENGRAM_VNEXT_F_ENABLED=true` | v6 (Milestone-F) |
| Code intelligence | `codebase_search` | `ENGRAM_CODE_INTEL_ENABLED=true` AND code chunk store wired | v6.13 (CR-006 code-intel) |
| V7 meta-memory discovery | `know_about` plus session-start `meta_summary` | `ENGRAM_V7_PLUG_ENABLED=true` AND `ENGRAM_V7_S2_METAMEM=true` AND meta-memory index wired | v6.38 (ENG-V7-S2) |

Admin-gated families (candidates, governance, bulk ops) are additionally enforced at the handler level regardless of advertisement.

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

### Hook and Observation Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/hooks/session-start` | Session initialization + context injection. |
| `POST` | `/api/hooks/user-prompt` | Record user prompt text. |
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

Hook registration in `plugin/engram/hooks/hooks.json` specifies each Claude Code event,
its command, and any matching rules. The current set does not capture tool results
automatically; use MCP `store` and `recall` for deliberate memory operations.

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
