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

For hybrid `recall_memory` (`ENGRAM_VNEXT_ENABLED=true`), the advertised
`tier_filter` selects retrieval layers: `tier0_exact`, `tier1_fts`,
`tier1_vector`, or `tier2_graph`. Omitted or empty means all retrieval layers.
Validate this enum independently of `ENGRAM_LIFECYCLE_ENABLED`; unknown values
and lifecycle memory tiers return `invalid_tier_filter:` rather than silently
selecting no retrieval layer. Privacy, ranking, limits and graph opt-in remain
unchanged. When `ENGRAM_VNEXT_ENABLED=false`, preserve the existing unadvertised
lifecycle-only filter under `ENGRAM_LIFECYCLE_ENABLED=true`; it is not an alias
for the public hybrid enum.

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

### Knowledge readers

`GET /api/projects` returns `{items:[{id,display_name}]}` from live canonical
`projects` rows, not session labels or legacy selectors. The authoritative
`display_name` falls back to the same stored ID; duplicate labels remain distinct
IDs. Existing ordinary authenticated readers retain their server-wide project
scope. HAP project/legacy credentials remain forbidden by the existing route
policy. The catalog response is `private, no-store`, never publicly cached.

`GET /api/memories` and `GET /api/documents` require one exact existing live
canonical `project` ID and return `{items,total,offset,limit,has_more}`. No legacy
array/`documents`/page-length `count` shape remains. Defaults: `offset=0`,
`limit=50`; bounds: `offset>=0`, `1<=limit<=500`. `items` is always an array.
`total` counts the visible query set before pagination; hidden rows affect neither
count nor page. `has_more` compares the returned page with that visible total.

`query` is a trimmed, case-insensitive literal substring (valid UTF-8, maximum
1024 bytes, no control characters). SQL bind parameters and LIKE escaping keep
`%`, `_`, `\\` and SQL-looking text literal. Memory queries search content, after
the existing active/nondeleted/current-validity, workstation, principal, domain
and flag-controlled legacy privacy filters. Ordering remains
`created_at DESC, id DESC`; status and provenance fields keep their meaning.

Memory accepts repeated exact `filter` values: `all`, `active`, `flagged`,
`stale`, `low`, `chain`. For example, `filter=low&filter=chain` intersects the
selected predicates with **AND before count/page**, preserving the independently
toggleable Memory Lab chips. Omitted filters mean `all`; explicit `all` is neutral
with other selections. Valid duplicates are deduplicated. At most six incoming
values are accepted before deduplication; blank, unknown, differently cased or
CSV values are 400, including an invalid later value. No scalar-first fallback,
JSON/CSV alternative or page-local filtering exists. The existing active-only
eligible corpus is unchanged. Zero-valued `omitempty` confidence/citation metrics
remain unknown, not new ratings. `chain` matches exact tags or a supersession link.

Memory `epistemic_type` repeats exact existing values `fact`, `experience`,
`opinion`, `observation`; document `doc_type` repeats literal type values.
Selected types use **OR within the type set**, AND with every other query,
filter, status and canonical/privacy predicate before count/page. Omission is
unrestricted; one value is the scalar subset. Each parameter accepts at most
six raw values before deduplication and validates every value, including later
ones. Blank/control/malformed UTF-8 values are 400. Document values retain the
128-byte raw UTF-8 bound and Unicode whitespace trimming; comma-containing
types are literal values, not CSV. Types apply after latest-version-per-path
selection, so matching only an older version never admits the document.

`GET /api/memories/:id?project=<exact live canonical ID>` requires the same
fresh project authorization and uniquely owned alias set before loading the ID.
It returns the existing memory DTO with `project` set to that canonical ID.
Wrong project, hidden or unknown ID: nondisclosing 404; invalid ID/missing project:
400; noncanonical/removed project: 403. Detail retains the existing nondeleted-row
semantics; it does not broaden page eligibility or rewrite stored provenance.

`GET /api/memories/:id/audit?project=<exact live canonical ID>` applies that same
project/alias and workstation/principal/domain read policy before fetching audit
rows, and retains the existing manage permission. Missing project is 400;
nonlive/noncanonical project is 403; hidden, cross-project or unknown memory is
nondisclosing 404. No context-free legacy fallback exists. `limit` retains its
default 50 and range 1..200. The unchanged safe DTO includes action, actor,
session reference, reason, timestamp and state-presence flags, never raw states.
Audit validation errors (invalid ID, malformed query, missing/invalid project or
limit) use plain-text 400 bodies; project authorization uses a JSON error object
with status 403, matching the existing OpenAPI response schemas.

Documents select the latest version of each path **before** filtering. Search is
limited to public list metadata (`path`, `doc_type`, `author`), not content,
arbitrary JSON metadata or secrets. `doc_type` (128 bytes) and literal
`path_prefix` (4096 bytes) remain supported; ordering is `created_at DESC, id DESC`.
Document content/version/history shapes and comments are unchanged. Content,
version and history reads use the same exact live canonical project and lawful
stored alias set as list/count. The current document schema has no independent
principal/privacy ACL: its reader boundary is the existing authenticated
project-reader scope.

Missing/blank project or invalid parameters: 400. Noncanonical, missing, removed
project or forbidden credential: nondisclosing 403. Authentication remains 401;
unavailable store: 503; unexpected query failure: 500 without SQL diagnostics.
Offsets are stable only while the dataset is unchanged, not snapshot/cursor
tokens. An explicit UI “All projects” context uses descriptor IDs; empty project
never widens server reads and does not trigger an automatic content scan.

Stored legacy TEXT projects are readable only when the live canonical row's
existing `legacy_ids` maps each selector uniquely back to that same ID. Aliases
claimed by another, removed or merged project are excluded. Canonical descriptors
come from `projects`, including memory-only/document-only registered projects,
not from sessions or label grouping. Unregistered orphan TEXT rows have no lawful
catalog ID and remain untouched; this reader cannot register or infer one.
No orphan endpoint, migration, raw-selector fallback or automatic registration
is introduced. Existing authorized data diagnostics are separate from this API.

For real-API preview, compose this backend with the UI envelope consumers and an
existing authorized database. No migration, provider or identity onboarding is
introduced. The focused SQLite fixture executes production handlers/stores and
SQL count/window/pagination, with an explicit test-only lowering of PostgreSQL
array/JSON containment syntax over real fixture rows. It cannot certify those
PostgreSQL operators, collation, session membership or migration/locking behavior.
The same cases retain a conditional isolated `DATABASE_DSN` integration test;
production data is not a fixture.


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
