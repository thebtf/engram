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

For ordinary hybrid `recall_memory`, the advertised `tier_filter` selects retrieval
layers: `tier0_exact`, `tier1_fts`, `tier1_vector`, or `tier2_graph`. Omitted or
empty means all retrieval layers. Unknown values and lifecycle memory tiers return
`invalid_tier_filter:`. Privacy, ranking, limits and graph opt-in remain unchanged.
There is no historical startup-flag alternative to this public hybrid enum.

### Conditional Tool Families

| Family | Tools | Advertised when | Since |
|--------|-------|-----------------|-------|
| Behavioral rules | `store_rule`, `list_rules` | behavioral-rules store wired | v5 |
| Expanded memory retrieval | `recall_memory` | memory store wired | v5 |
| Adaptive memory brief | `get_memory_brief` | memory store wired | v6 |
| S6 significance | `rate_memory_significance` | deferred; startup activation retired | v6.41 |
| Ambient hints | `get_ambient_hints` | deferred; startup activation retired | v6.42 |
| Ingest | `ingest` | memory store wired | v6 |
| Lifecycle | `lifecycle` | memory + promotion store wired | v6 |
| Graph | `graph` | retained graph store wired | v6 |
| Native state plane | `get_state`, `set_state` | state store wired | v6.30 (CR-006) |
| Principal memory | `query_principal_memory` | principal-memory query service wired | v6.30 (CR-007) |
| Experience history | `experience_history.read`, `experience_history.detail` | experience provider wired | v6.35 (CR-009) |
| Temporal truth | `temporal_truth`, `temporal_truth_refresh` | temporal provider wired AND temporal-truth flag enabled | v6.36 (CR-011) |
| Directive capture | `remember_directive` | deferred; startup activation retired | v6.39 (ENG-V7-S4A) |
| Review-loop candidates | `list_candidates`, `get_candidate`, `promote_candidate`, `reject_candidate`, `supersede_candidate` | candidate store wired | v6.32 (CR-008) |
| Governance snapshots | `list_snapshots`, `rollback_snapshot`, `pin_snapshot`, `redaction_rules_status` | snapshot store wired | v6 (Milestone-F) |
| Rule governance | `rule_governance_health`, `rule_governance_queue`, `rule_governance_snapshots`, `rule_governance_transition`, `rule_governance_pin_snapshot`, `rule_governance_rollback`, `rule_governance_usefulness` | rule-governance read store wired (`usefulness` also needs injection telemetry) | v6.29 (RG-3) |
| Bulk ops | `bulk_promote`, `bulk_delete`, `bulk_supersede` | ordinary v7 runtime; admin authorization still required | v6 (Milestone-F) |
| Code intelligence | `codebase_search` | `ENGRAM_CODE_INTEL_ENABLED=true` AND code chunk store wired | v6.13 (CR-006 code-intel) |
| V7 meta-memory discovery | `know_about` plus session-start `meta_summary` | deferred; startup activation retired, historical data retained | v6.38 (ENG-V7-S2) |

Admin-gated families (candidates, governance, bulk ops) are additionally enforced at the handler level regardless of advertisement.

`lifecycle` exact-ID reads (`info`, `decay_preview`) use ordinary reader
caller/project, principal/domain and workstation privacy authority before loading
metadata. Like recall without an explicit `session_id` argument, these reads do
not narrow visibility with transport session metadata; lifecycle has no such
argument. Mutations retain guarded by-ID manage authority, including the context
session restriction when present. Hidden and absent IDs return the same tool
error without lifecycle metadata or an existence signal. Read-only callers may
read authorized metadata but cannot mutate. Tier validation, changes and
promotion history run in one authorized transaction; a history-write failure
rolls back the tier change. `sleep_status` remains content-free. Lifecycle is not
forwarded by `--session-memory`.

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

`make worker` regenerates `docs/docs.go`, `docs/swagger.json`, and
`docs/swagger.yaml` with Swag 1.16.6 before building the server. The producer
derives local Go package directories from the server dependency graph, puts
`cmd/engram-server` first, and parses `main.go` with internal-package parsing
enabled and dependency parsing disabled. Package discovery and generation
errors stop the build; generated files must not be patched by hand.

`GET /api/stats` returns uptime, session and queue activity, retrieval statistics,
readiness, and runtime memory statistics. `database` is present when the store is
initialized; `project` and `projectObservations` are present only when a nonempty
project is supplied and its count lookup succeeds; `rateLimiter` is present when
configured. A nonempty `project` scopes retrieval and the conditional project
fields only; other statistics remain global. With no project, retrieval counters
are summed across tracked projects. This response does not contain vector-cache
or graph fields. A successful response is JSON; invalid project validation returns
plain-text 400, and the readiness gate returns plain-text 503 when not ready.
Swagger 2 lists response media types for the whole operation, not per status.

Context GET and POST operations have separate annotations on real routed handlers;
the GET entry points delegate to the same implementation without changing request
semantics. GET requires `project` in the query string, and context search also
requires a nonempty `query`. GET ignores JSON bodies. POST `/api/context/inject`
requires JSON with a canonical `project` UUID; query `project`, `agent_id`,
`session_id`, `cwd`, and `files_being_edited` are not fallbacks. Its `format`
selector remains query-only. The request schema uses the same DTO as the decoder;
the `project` requirement describes the existing canonical-UUID validation.

POST context search and session-start preserve their query/JSON alternatives:
query `project` and JSON `project` are individually optional, but a canonical
project UUID must be supplied through one of them. Search also requires a nonempty
`query` through either location. Nonempty body fields override query values;
search `session_id` overrides even when empty, while session-start trims project
and session ID and only overrides with nonempty values. Search tolerates an
invalid/absent body and retains query values; session-start requires a JSON body
and rejects invalid or empty JSON (use `{}` to retain query values). Search
`limit` remains query-only (default 50, maximum 200), `obs_type` body-only;
session-start limits remain body-only, with zero
delegating to the existing server defaults. Source session IDs remain attribution,
not authentication or a new visibility boundary; client `cwd` is ignored.

Swagger 2 cannot encode the cross-location project/query alternatives for those
two POST operations. Their descriptions and existing HTTP fixtures remain the
contract for that condition; generic search/session-start body and response
schemas are not complete conditional schemas. Separate inject GET/POST schemas
remove the false required query field, not every existing schema limitation.

Context search shares memory retrieval with MCP: vectors plus FTS and literal
content/tag matches, with configured cross-encoder reranking and no query expansion.
Unavailable embeddings degrade to text retrieval; memory-store lookup failures
return HTTP 500, not an empty successful search. REST retains its observation-shaped
response and audit-only source-session attribution without MCP memory mutations.

### Typed memory cards

In authenticated mode an enabled admin with a persisted `engram_auth` browser
session can enroll an existing canonical principal/kind pair using
`POST /api/auth/principals`. Bearer and `X-Auth-Token` credentials take precedence
over cookies; omit them to use the browser session. HMAC-only sessions do not
qualify. Auth-disabled middleware supplies a synthetic identity, not an authenticated
keycard or persisted browser session. A validated keycard for the enrolled pair can
omit both `project` and `context` on `POST /api/memories` to create a self-owned
card. `X-Engram-Identity` selects a typed context; it does not authenticate.
`source_agent` is provenance,
not owner authority. The optional context object is `{"kind":"self"}`,
`{"kind":"project"}`, or `{"kind":"global"}`. Project context requires an
existing canonical `project`; self and global require the project to be absent.
Invalid, empty, repeated or conflicting selectors do not fall back to self.
Self ownership is independent of the visibility tier; global defaults to
principal-private. Explicit workstation-private scope remains bound to the
writing keycard even when another credential has the same principal.

REST create and revision apply builtin and configured operator redaction before
storing content. If redaction removes the full body, the request is refused;
a refused revision does not advance the version or create history. Nonempty
whitespace-only input remains accepted when no rule removes it.

An optional nonempty `request_key` (at most 1024 UTF-8 bytes) scopes a create
retry to the authenticated principal, owning context and project. An equivalent
retry returns the existing active record; a changed command or retired target
returns 409. Without a key a repeated create is not automatically idempotent.

`GET /api/memories/{id}` reads a visible card in its selected context. With no
selector or identity opt-in, an enrolled client defaults to self; other callers
retain the legacy missing-project 400. Explicit self requests still require an
enrolled client, and invalid explicit selectors never fall back. To revise,
send `PATCH /api/memories/{id}` with `{"version":N,"content":"..."}` and the
same context selection. The owner must be authorized to manage the current card.
Project aliases support reads only; a project-context revision requires the card's
unique native canonical membership and revalidates it under the mutation locks.
The version must match; a stale version returns 409. After an uncertain outcome,
read the current card and reconcile before deciding whether to retry. A successful
revision keeps the ID, atomically captures the previous body and increments version.
The newly captured body is subject to the current redaction rules, so it is not
necessarily a byte-exact historical copy. Existing stored history is not rewritten.
When an embedding provider and store are configured, revision starts an asynchronous
re-embedding attempt; provider errors or empty vectors do not establish a retry.
`GET /api/memories/{id}/revisions` returns retained previous bodies after checking
current-card access, in ascending version order. `after_version` selects versions
strictly above its nonnegative value; `limit` defaults to 50 and accepts 1–100.
Typed PATCH refuses legacy project rows with 404 rather than creating incomplete
revision history. Existing legacy reads and edits remain available through their
existing paths. The current body remains on the ordinary read. Retained revisions
are removed with a hard-deleted parent. Neither the revision endpoint nor global
context grants access by itself. Synthetic auth-disabled identity is not a
registered keycard.

Explicit-principal queries filter inaccessible cards before pagination. The
`hidden_count` field is `null` when the number of inaccessible cards is unknown;
it is not a count of matching private cards. This applies to the HTTP principal
query and its MCP query and principal-scoped brief projections. Authorized
administrative private widening remains subject to durable audit.

`POST /api/memories/{id}/transition` takes a JSON object with required `action`:
`suppress` changes active to flagged, `unsuppress` changes flagged to active, and
`archive` changes active or flagged to archived without erasure. Optional query
`project` must be a single canonical UUID and only narrows the ID's live membership;
omission preserves caller visibility and domain-manage authorization. Missing or
denied IDs return 404; state/version conflicts return 409 with transaction rollback.
Success returns `{id, action, status, version, project}`. A transport failure can
leave the outcome unknown and must not be retried automatically.

### Live events and logs

`GET /api/events` and `GET /api/logs` are registered before the database-ready
gate, but ordinary authentication and per-client rate limiting still apply when
enabled. Authentication accepts `X-Auth-Token`, `Authorization: Bearer <token>`,
or the `token` query parameter, in that order, before supported session cookies.
A supplied invalid token does not fall back to a cookie. Query credentials can
leak through URLs; prefer headers or an authenticated cookie when available.
Auth-disabled mode supplies the usual synthetic identity. Before the validator is
wired, a supplied token can receive 401 even though the routes are pre-ready.
Unauthorized requests receive plain-text 401; disallowed keycard classes receive
403; rate limiting can return 429. Authentication-store failure can return 500.
These `http.Error` responses are `text/plain; charset=utf-8`, occur before the
stream starts, and do not imply DB readiness.

`GET /api/events` returns `text/event-stream`. On connection the handler writes
`data: {"type":"connected","clientId":"client-<number>"}\n\n`; the ID is
connection-local, not a replay cursor. Registration occurs before that write, so
a concurrent broadcast can arrive first. Other `data: <JSON>\n\n` frames carry
live dashboard events, whose fields depend on the event type (for example,
`session` or `processing_status`). The server sends `: keepalive\n\n` comments
every 25 seconds. Comments are not application events. A writer without flushing
support gets 500 `streaming not supported`. Disconnecting ends the subscription;
reconnect establishes a new connection. There are no SSE event IDs, Last-Event-ID
handling, or replay, so clients must refresh relevant state after a gap.

Broadcast write errors or timeouts remove the subscriber and signal the handler
to end its stream. The handler waits for any in-flight write before returning,
so a writer that never unblocks can delay termination indefinitely. A timeout
still lets the broadcaster continue serving other clients; it does not cancel
the blocked write. Clients should reconnect and refresh relevant state after a
disconnect; reconnecting cannot recover missed events. A quiet stream alone
does not establish that delivery is healthy or that removal occurred.

`GET /api/logs` normally returns `application/json`: an array of entries with
`timestamp`, `level`, `message`, optional `fields`, and `raw`. RFC3339 timestamps
are converted to Unix seconds; numeric timestamps are retained as integers and
missing or unparseable timestamps may be zero. Entries are chronological, oldest
first. `lines` defaults to 100;
positive integers are capped at 10000, while invalid, zero, or negative values
use the default. It limits the most recent matching records in the in-memory ring;
an empty result is `[]`.

Only `follow=true` (exact, case-sensitive value) selects `text/event-stream`.
It ignores `lines`, subscribes to new entries without an initial snapshot, and
first writes `: connected\n\n`. Each matching new entry is a JSON log entry in a
`data: <JSON>\n\n` frame. There is no periodic follow-mode keepalive. The
subscriber queue holds 64 entries and drops new entries when full; disconnect or
reconnect has no replay or event cursor. Clients can fetch a new snapshot to
resynchronize, but that is not an atomic handoff and cannot guarantee gap-free
delivery. A missing log buffer returns 503; a follow response whose writer cannot
flush returns 500 `streaming not supported`.

Both log modes support `level` as a minimum severity (`trace`, `debug`, `info`,
`warn`, `error`, `fatal`, `panic`); unknown requested or entry levels pass the
filter. `query` is a case-insensitive substring search of the projected message
and raw log, after scoped browser-session redaction. Filtering precedes the
snapshot limit. Projection covers identified browser-session fields, messages,
and raw data, including retained records; it is not a general secret scrubber.
The endpoint does not require the access-administration session-admin guard.

Swagger 2 cannot represent response media types per status or different 200
schemas based on `follow` for the same operation. Both operations list `text/plain`
alongside their success media types at operation scope; that does not mean a
successful stream is plain text. The logs operation does not claim a single 200
schema; this section specifies the conditional
contract. `internal/worker/sse/broadcaster.go`, `handlers_logs.go`,
`internal/logbuf/ring.go`, and `middleware.go` own these behaviors.

### Authentication (v6)

Two-tier token model:
- **Operator token** (`ENGRAM_AUTH_ADMIN_TOKEN`): full admin access
- **Worker keycards** (per-workstation): issued via `/tokens` dashboard page

```
Authorization: Bearer <token>
```

`ENGRAM_AUTH_DISABLED=true` supplies a synthetic identity for ordinary operations;
it is not a browser session and does not pass access-administration reader guards.

`POST /api/auth/setup` requires the operator token and a JSON email and password.
The password must not exceed 72 bytes after UTF-8 encoding. An overlong password
returns HTTP 400 with `{"error":"password must not exceed 72 bytes"}`; the
rejected request does not create a user or complete setup. Unexpected hashing
failures return HTTP 500.

`POST /api/auth/register` also rejects passwords exceeding 72 UTF-8 bytes with
HTTP 400 and `{"error":"password must not exceed 72 bytes"}`. The rejected
request does not create a user or consume the invitation. Unexpected hashing
failures return HTTP 500.

#### Browser session management (issue 535)

The private `sessions.id` remains the `engram_auth` HttpOnly cookie credential.
Cookie authentication, expiry, revocation and logout continue to use that private
value. It is never a management selector or an `AuthSession` JSON ID.

| Method | Path | Session identity contract |
|--------|------|---------------------------|
| `POST` | `/api/auth/user-login` | Issues the credential only in the cookie; records safe audit correlation. |
| `POST` | `/api/auth/user-logout` | Revokes the cookie's session and clears that cookie; existing origin/idempotency checks remain. |
| `GET` | `/api/access/sessions` | `sessions[].id` is a stable public handle. `include_revoked=true` also includes expired/revoked rows. |
| `GET` | `/api/access/users/{id}` | Session IDs and browser-auth audit references use the same public handles. |
| `GET` | `/api/access/log` | Historical and new browser-auth states expose safe correlation, not credentials. |
| `POST` | `/api/access/sessions/{id}/revoke` | Accepts only the public handle; credential and unknown selectors return 404 without mutation. Success repeats the handle. |

All seven access GET templates (`providers`, `invitations`, `users`, `users/{id}`,
`roles`, `sessions`, `log`) retain their session-admin guard. Master/client keycards
and synthetic auth-disabled identities do not pass it. Existing HMAC session-admin
read admission remains; revocation additionally requires the current enabled DB
admin cookie. A public handle never authenticates as a cookie. Refresh an old
Access view before revoking; cached credential selectors are not accepted as a
compatibility route.

Migration `190_browser_session_public_handle` adds one `sessions.public_handle`
TEXT column and the unique `idx_sessions_public_handle` index. Its value is
`bsh1_` followed by lowercase SHA-256 of the exact UTF-8 bytes of
`engram/browser-session-management/v1:` plus the stored credential. No trimming or
case folding occurs. The transform relies on random credential entropy; it does
not strengthen weak values. The original credential/PK and all lifecycle fields
are retained. Backfill and owned DDL run in an explicit transaction and refuse
incompatible retained handles or index state; collisions fail rather than select
another handle. A BEFORE INSERT / UPDATE OF id, public_handle trigger computes
the same value for old SQL writers. No mapping table or expression-index
immutability assertion is involved. Reapplication is idempotent. Destructive down
migration is refused and retains the additive schema. Old-writer compatibility
does **not** make an older credential-exposing binary a safe behavioral rollback.

New `auth_login` and `auth_session_revoked` events write safe handles. Their
historical before/after states are projected recursively without rewriting stored
audit rows, including orphan references whose session rows no longer exist.
Representable metadata is retained; non-object JSON is safely wrapped instead of
echoed as raw text. Browser-specific Operator Code audit provenance uses the same
handle while tab/chooser/authorization identity remains private and cookie-bound.
SDK/agent session IDs, generic source-session attribution and their APIs are not
converted into browser handles.

The separate `GET /api/logs` JSON snapshot and `GET /api/logs?follow=true` SSE
stream apply scoped browser-session projection to matching log fields, messages
and raw JSON. Browser-session management paths are normalized for logging, and
identified browser credentials in browser-auth diagnostics are hidden. Existing
historical in-memory records are projected on read, not deleted or rewritten;
SSE projects queued/new entries before serialization. Text `query` matching uses
the safe projected message/raw JSON, not hidden credential bytes. Snapshot query
filtering also precedes the latest-`lines` limit; existing level filtering and
chronological ordering remain. Ordinary log-route authentication is unchanged;
this route does not acquire the access-administration session-admin guard.
SDK/agent session correlation and unrelated harmless diagnostic fields remain.
This is scoped browser-session projection, not general secret detection or a
guarantee that arbitrary application logs contain no other secrets.


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
| `POST` | `/api/memories/:id/transition` | Suppress, unsuppress or archive a retained memory with optional canonical project narrowing. |
| `GET` | `/api/issues` | List issues. |
| `POST` | `/api/issues` | Create issue. |
| `PATCH` | `/api/issues/:id` | Update issue (status, labels, etc.). |
| `GET` | `/api/tokens` | List API tokens. |
| `POST` | `/api/tokens` | Create worker keycard. |
| `DELETE` | `/api/tokens/:id` | Revoke token. |

For `POST /api/update/apply`, an available update without an asset URL for the
current platform returns HTTP 200 with `success: false` and an error message.
The synchronous response is authoritative for this refusal. The updater
initially records an `error` status with the existing manual update command,
but a subsequent update check may overwrite that in-memory status before it is
read through `GET /api/update/status`. No update is started in this case. An
unavailable update keeps the existing `success: false` response; an update with
an asset continues in the background. Status does not promise crash durability.
The generic manual update command is not guaranteed to work when the same
platform artifact is absent.

### Knowledge readers

`GET /api/projects` returns `{items:[{project_id,name,display_name,repository_qualifier?}],unresolved:[{reference,name,reason,reader_kinds}],logical_groups:[{primary_project_id,alias_project_ids}]}`.
`items` contains live canonical `projects` rows, not session labels or legacy
selectors. `project_id` is the canonical UUID; `name` is a human label that falls
back to `Registered project` when missing or blank. `display_name` is always
present and is the exact stored string or JSON null, without that fallback. The optional
`repository_qualifier` is a normalized repository locator. Duplicate labels remain
distinct UUIDs. Existing ordinary authenticated readers retain their server-wide project
scope. HAP project/legacy credentials remain forbidden by the existing route
policy. The catalog response is `private, no-store`, never publicly cached.
This server always emits `logical_groups` as an array, including `[]` when no
groups exist. Older servers may omit it; an already feature-aware client must not
silently downgrade if it disappears. The relation is not an authorization grant.

For `GET /api/projects/resolve?legacy_id=...`, an existing active UUID uses
the same safe global alias projection as knowledge readers; chains and
conflicting redirects do not select an intermediate project. Non-UUID
historical selectors retain their legacy/session resolution semantics.

`PATCH /api/projects/{id}` changes only the stored label of an existing active,
non-removed canonical UUID. It requires the server's operator/admin identity;
read-only and read-write client keycards cannot mutate this server-wide metadata.
The existing auth-disabled mode uses its synthetic admin identity. The JSON object
must contain exactly `display_name` and `expected_display_name`, each either a
string or null. Both fields must be present; an empty string is distinct from
null. Strings are limited to 255 Unicode code points and cannot contain control
characters; the request body is limited to 4096 bytes. Use the catalog's exact
`display_name` as the precondition, never its presentation `name`.

The server locks the live row and atomically compares the stored nullable value
before changing it. Success returns HTTP 200 with `project_id` and the stored
`display_name`; 409 means the value is stale, and 404 means the project is missing
or inactive. Invalid input is 400, oversized bodies are 413, and unauthorized
callers are denied. The same conditional operation can restore a prior null,
empty or valid string value; it does not create projects or reassign history.
Historical labels containing control characters cannot be supplied as a
precondition, so this endpoint refuses their rename rather than prevent rollback.

`unresolved` describes retained data owners without a canonical project assignment.
Each entry has an opaque `reference`, display `name`, `reason`, and `reader_kinds`
identifying retained `memory`, `document`, `issue` or `code` data. This metadata
neither verifies retained content nor grants reader access.

Administrators explicitly attach confirmed UUID aliases through
`POST /api/projects/consolidation/aliases/prepare`,
`POST /api/projects/consolidation/aliases/apply`,
`GET /api/projects/consolidation/aliases/status` and
`POST /api/projects/consolidation/aliases/rollback`. See
[Explicit project aliases](../operating-engram.md#explicit-project-aliases) for
request and response semantics, database identity limits and unknown-outcome handling.
An existing global target-to-itself redirect is retained as sealed expected
state, not replaced or deleted. Changed baseline or operation-owned redirects
refuse active reconciliation and conditional inverse; session-scoped and other
conflicting redirects are not admitted as that baseline.

#### Bounded retained-record discovery

`GET /api/projects?view=retained_records&attribution_scope=unresolved_history&kind=memory&q=retained&limit=50`
explicitly selects a separate historical projection. Without `view`, the full
catalog above is unchanged. New-mode parameters are closed and scalar: exactly
one `view=retained_records`, `attribution_scope=unresolved_history` and
`kind=memory|document|issue`; optional `q`, `limit`, `cursor`. Unknown, duplicate
or foreign project/reference/offset selectors are `400 INVALID_QUERY`.
`q` is valid UTF-8, at most 1024 bytes, trimmed at its edges and searched as an
escaped case-insensitive literal. `%`, `_` and backslashes are not a language.
Memory searches content; documents search path/content; issues search title/body.
`limit` defaults to 50 and accepts 1..200.

The response is `{view:"retained_records",attribution_scope:"unresolved_history",kind,items:[{reference,id,record}],next_cursor:string|null}`.
`items` is always an array. `record` retains the genuine native family payload,
adds `attribution_scope:"unresolved_history"` and omits `project_key`. The outer
positive ID equals the native record ID. Results are unique by family/native ID
and ordered by native ID DESC, not creation time or group. Document versions
remain separate IDs. The first valid unresolved witness supplies the association;
duplicate roles/groups do not duplicate a record. A separate canonical role does
not disqualify a valid unresolved role. Historical `project`, source/target and
origin fields are provenance, not a canonical owner or automatic-context grant.

There is no `total` or `has_more`. Saved membership-occurrence counts are not
visible-record counts. A cursor means remaining search **work**, not promised
visible results: short/empty pages with a cursor are intermediate. Null means
observed traversal exhaustion. No cross-request snapshot is promised.

Each request selects at most 200 native IDs plus one lookahead in keyset order,
then applies query/privacy within that window without materializing bodies.
One fixed-family membership set proposes at most 200 witnesses plus lookahead.
The shared native validator checks full typed keys, source-key/path/plan and
Vault HMAC references, native liveness, selector and exactly-one-match evidence.
Invalid witnesses are skipped; operational errors abort the entire page.
Validation uses bounded set batches, not per-record/group reads. Only the
consumed prefix advances: an unconfirmed ID and last witness resume before lower
IDs, so the valid witness after a 200-witness boundary is not lost. The sealed
cursor has constant-size prefix/pending-ID/last-witness state. Bodies for at most
the public limit of confirmed IDs are fetched once and restored to ID DESC.
These application bounds do not promise bounded PostgreSQL physical I/O or bytes.

Cursors use retained-specific Vault-derived AES-GCM, binding the normalized query,
limit and actual effective identity/visibility context, including the no-auth
realm. Tampered, different-caller or different-query cursors are refused with
`400 INVALID_CURSOR` before data work. There is no public-checksum, fallback-key,
code-feature or browser-only identity dependency. Vault, randomness and storage
failures return safe `503 RETAINED_RECORDS_UNAVAILABLE`, without partial items or
a false terminal cursor. Success and handler errors are `private, no-store`.
Existing TokenAuth/DB-ready/HAP route restrictions remain; memory read/history
privacy and manage checks remain. Document/issue authority is the existing
authenticated route plus exact validated membership, not a new per-row ACL.

Open the exact issued selection with
`GET /api/projects/unresolved/{reference}/content?attribution_scope=unresolved_history&kind=memory&id=123`;
use `/history` with the same reference/kind/positive ID. These closed readers
accept only the optional historical attribution value. Omitting scope preserves
their old unresolved meaning; omitting content ID preserves whole-group paging.
Responses identify `unresolved_history`; historical records/history omit
`project_key`. The full selected group still fails closed on a stale neighbor,
and a stale/foreign binding is never repaired through another group. Document
history keeps all validated group IDs and the selected historical path/project,
not just one selected version or another group's same path. Keep the search,
cursor, results and selection on return; clear open payload on stale/foreign/
denied/error responses. This HTTP contract is not installed-client/UI acceptance.

Swagger 2 cannot express two conditional 200 schemas on one route. Its ordinary
catalog schema remains intact; the generated `x-retained-records-response`
extension describes the explicitly selected alternate envelope. Conditional
required parameters and native payload variants are stated in route descriptions.


#### Canonical memory and document readers

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
types are literal values, not CSV. Types apply after latest-version selection per
native `(project,path)` lineage, so matching only an older version never admits it.

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

Documents select the latest version of each native `(project,path)` lineage
**before** filtering, without merging independent same-path histories. Search is
limited to public list metadata (`path`, `doc_type`, `author`), not content,
arbitrary JSON metadata or secrets. `doc_type` (128 bytes) and literal
`path_prefix` (4096 bytes) remain supported; ordering is `created_at DESC, id DESC`.
Content and history reads use the same exact live canonical project and lawful
read scope as list/count. The current document schema has no independent
principal/privacy ACL: its reader boundary is the existing authenticated
project-reader scope.

`GET /api/documents/read` and `/api/documents/history` accept an optional positive
`document_id` query parameter issued by list to select a lineage for the requested
`project` and `path`; read also accepts its existing optional `version`. Without
an ID, the requested native lineage wins, then a single authorized lineage;
ambiguity returns 409. A version-write `POST /api/documents` may supply positive
`document_id` and `expected_version` together to bind the latest version of that
lineage; stale, foreign or mismatched targets are refused.

`POST /api/documents/comment` and `GET /api/documents/comments` require
`document_id`; scoped calls additionally supply `project` and positive
`expected_version` together, identifying the exact selected history row, not
necessarily the latest. For scoped export, selection snapshot/current requests
and `POST /api/documents` with `action: export` carry `project`; explicit
selection targets carry `id` and `expected_version`. Execution and the unchanged
`/api/documents/exports/{id}` download reauthorize stored targets. Authenticated
legacy calls omitting project retain their existing server-wide ID-addressed
branch; a supplied scope that fails does not fall back to it. See the linked
operator guidance for the complete contracts and no-replay boundary.

Missing/blank project or invalid parameters: 400. Noncanonical, missing, removed
project or forbidden credential: nondisclosing 403. Authentication remains 401;
unavailable store: 503; unexpected query failure: 500 without SQL diagnostics.
Offsets are stable only while the dataset is unchanged, not snapshot/cursor
tokens. An explicit UI “All projects” context uses descriptor IDs; empty project
never widens server reads and does not trigger an automatic content scan.

For memory and document rows, authoritative consolidation membership overrides
the native project selector; conflicting or unassigned memberships fail closed.
Without membership, native project matching applies. Confirmed unambiguous,
active UUID redirects expand the logical read scope without changing write
authority or stored provenance. Canonical descriptors come from `projects`,
not from session labels or inferred name grouping. Unregistered orphan TEXT rows
without authoritative membership remain outside these canonical readers.

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

HTTP endpoints use different response formats. Many return JSON; the streaming
endpoints above return SSE, and some errors are plain text. Consult each endpoint
rather than assuming a uniform success or error envelope.
