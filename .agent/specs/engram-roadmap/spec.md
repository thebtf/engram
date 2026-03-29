# Feature: Engram Roadmap — Consolidated Remaining Work

**Slug:** engram-roadmap
**Created:** 2026-03-24
**Status:** Draft
**Author:** AI Agent (reviewed by user)
**Source:** Full spec audit of 17 specs (~200 items verified against code, 2026-03-24)
**Constitution:** .agent/specs/constitution.md v1.0.0

## Overview

Unified specification for all verified gaps remaining after the spec audit. Replaces 17 individual specs with one coherent document. Every requirement here is a confirmed gap — code audit verified it does NOT exist in the current codebase.

Previous specs with Status: Implemented are archived. This spec covers only unfinished work.

## Context

Engram v1.6.5 (server) + v0.6.0 (plugin) is live with:
- 48 MCP tools, 6 hook events, Vue 3 dashboard
- Composite scoring, B_fewshot extraction, FalkorDB graph
- Always-inject tier, PreCompact/PreToolUse hooks, causal chain linking
- Vault (AES-256-GCM), credential management, secret redaction

Gaps fall into 8 categories verified by code audit.

---

## Phase 1: Security & Reliability (P0)

### FR-1: RedactSecrets on LLM Output
**Constitution P9 VIOLATION.** LLM extraction output is stored without secret redaction. `privacy.RedactSecrets` is applied to LLM INPUT but not to the returned text before observations are created.

**Acceptance Criteria:**
- [ ] `callLLM()` return value passes through `privacy.RedactSecrets()` before XML parsing
- [ ] Applied in both `internal/worker/sdk/processor.go` (live extraction) and `internal/backfill/backfill.go` (backfill extraction)
- [ ] Test: inject transcript containing `sk-proj-abc123` → extracted observation narrative contains `[REDACTED:...]` not the raw key

**File:** `internal/worker/sdk/processor.go`, `internal/backfill/backfill.go`

### FR-2: CSP Headers
Dashboard serves with `default-src 'self'` only. Missing granular directives.

**Acceptance Criteria:**
- [ ] `Content-Security-Policy` header set to: `default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; font-src 'self'; frame-ancestors 'none'`
- [ ] Header applied in `internal/worker/middleware.go`

**File:** `internal/worker/middleware.go`

### FR-3: Tool Call Error Logging
MCP tool errors log tool name and error but omit args summary and session ID.

**Acceptance Criteria:**
- [ ] Tool call error log includes: tool name, truncated args (first 200 chars), session ID (from request context), error detail
- [ ] Log level remains `Error`

**File:** `internal/mcp/server.go` (~line 1409)

### FR-4: Diagnostic LLM Error Messages
`callLLM()` returns bare "no LLM backend available" with no diagnostic context.

**Acceptance Criteria:**
- [ ] Error message includes: `llmClient={configured|nil}`, LLM URL (if configured), model name
- [ ] When `llmClient != nil` but retries exhausted, error includes HTTP status code from last attempt

**File:** `internal/worker/sdk/processor.go` (~line 677)

### FR-5: MCP Connection Health Monitoring
Streamable HTTP handler is stateless — no request counting, no error rate tracking.

**Acceptance Criteria:**
- [ ] In-memory counters: total requests, error count, per 5-minute sliding window
- [ ] New endpoint `GET /api/mcp/health` returns: `{ requests_5m, errors_5m, error_rate, uptime_seconds }`
- [ ] Counters reset on server restart (no persistence needed)

**File:** `internal/mcp/streamable.go`, `internal/worker/service.go` (route registration)

### FR-6: Bounded Goroutine Semaphore
Nil-semaphore fallback path spawns unbounded goroutines for vector sync.

**Acceptance Criteria:**
- [ ] Remove nil-semaphore fallback at `service.go:252-258`
- [ ] Always initialize semaphore (fail-fast if sync.Pool unavailable)
- [ ] Log warning if semaphore full and request dropped

**File:** `internal/worker/service.go`

### FR-7: Fire-and-Forget Vault Store
**Constitution P3 VIOLATION.** `vaultStoreDetectedSecrets` blocks the ingest handler synchronously.

**Acceptance Criteria:**
- [ ] Vault storage runs in a goroutine with 3s timeout context
- [ ] Handler returns immediately after spawning goroutine
- [ ] Vault failure logged as warning, never blocks observation storage

**File:** `internal/worker/handlers_backfill.go`

---

## Phase 2: Dashboard Completeness (P1)

### FR-8: Bulk Delete Operations
Only `archive` bulk action exists. Delete, scope change, tag bulk not implemented.

**Acceptance Criteria:**
- [ ] `ObservationsView.vue` supports `batchAction` types: `archive`, `delete`, `scope`, `tag`
- [ ] `DELETE /api/observations/bulk` REST endpoint (wraps existing `bulk_delete_observations` MCP tool logic)
- [ ] Confirmation dialog before destructive actions
- [ ] Success/error toast notification

**Files:** `ui/src/views/ObservationsView.vue`, `internal/worker/handlers_data.go`

### FR-9: Bulk Scope Change
**Acceptance Criteria:**
- [ ] `PATCH /api/observations/bulk-scope` endpoint accepts `{ ids: [], scope: "global"|"project" }`
- [ ] Dashboard wires scope picker for selected observations

**Files:** `ui/src/views/ObservationsView.vue`, `internal/worker/handlers_data.go`

### FR-10: Bulk Tag Operations
**Acceptance Criteria:**
- [ ] `POST /api/observations/batch-tag` endpoint accepts `{ ids: [], tag: string, action: "add"|"remove" }`
- [ ] Dashboard tag picker for batch operations
- [ ] Wired to bulk action dropdown in ObservationsView

**Files:** `ui/src/views/ObservationsView.vue`, `internal/worker/handlers_tags.go`

### FR-11: Tag Cloud
Observation browser has no tag overview.

**Acceptance Criteria:**
- [ ] Sidebar in `ObservationsView.vue` shows top 20 concept tags with observation counts
- [ ] Clicking a tag filters the observation list
- [ ] `GET /api/observations/tag-cloud` endpoint returns `[{ tag: string, count: int }]`

**Files:** `ui/src/views/ObservationsView.vue`, `internal/worker/handlers_tags.go`

### FR-12: Per-Token Usage Stats
Token management page shows tokens but not their usage.

**Acceptance Criteria:**
- [ ] `GET /api/auth/tokens/:id/stats` returns `{ request_count, last_used_at }`
- [ ] Token list in `TokensView.vue` shows request count and last used date

**Files:** `ui/src/views/TokensView.vue`, `internal/worker/handlers_auth.go`

### FR-13: Auth-Disabled Warning Badge
When auth is disabled, dashboard shows no indication.

**Acceptance Criteria:**
- [ ] `AppSidebar.vue` shows yellow "Auth Disabled" badge when server reports auth disabled
- [ ] Token management section hidden when auth disabled
- [ ] `/api/auth/me` response includes `auth_disabled: boolean` field

**Files:** `ui/src/components/layout/AppSidebar.vue`, `internal/worker/handlers_auth.go`

### FR-14: Vault Encryption Setup Helper
SystemView has no guidance for vault setup.

**Acceptance Criteria:**
- [ ] `SystemView.vue` shows vault setup section when vault is not configured
- [ ] Displays `openssl rand -hex 32 > vault.key` command with copy button
- [ ] Shows env var instructions: `ENGRAM_ENCRYPTION_KEY_FILE=/path/to/vault.key`

**File:** `ui/src/views/SystemView.vue`

---

## Phase 3: Self-Learning Completion (P1)

### FR-15: Cross-Session Priming
Observations from concurrent/recent sessions on same project not boosted.

**Acceptance Criteria:**
- [ ] Search scoring pipeline applies `recentSessionBoost` (1.3x) to observations from sessions active in last 2 hours on same project
- [ ] Boost factor configurable via `ENGRAM_SESSION_BOOST` (default 1.3)
- [ ] Query: join observations → sessions → filter by `last_activity > now - 2h AND project = ?`

**File:** `internal/search/manager.go`

### FR-16: Per-Project Adaptive Relevance Threshold
Relevance threshold is global. Should adapt per-project based on feedback.

**Acceptance Criteria:**
- [ ] New migration: `project_settings` table with columns: `project TEXT PRIMARY KEY, relevance_threshold FLOAT DEFAULT 0.3, feedback_count INT DEFAULT 0, updated_at TIMESTAMP`
- [ ] After each utility feedback signal (used/corrected/ignored), adjust threshold: used → lower by 0.01 (more results), ignored → raise by 0.01 (fewer results). Bounds: [0.1, 0.8]
- [ ] Search pipeline reads project threshold, falls back to global config if no project entry

**Files:** `internal/db/gorm/migrations.go`, `internal/db/gorm/project_settings_store.go` (new), `internal/search/manager.go`

### FR-17: Minimum Injection Floor
When all scores below threshold, zero observations injected — feedback starvation.

**Acceptance Criteria:**
- [ ] Always inject at least N observations (configurable, `ENGRAM_INJECTION_FLOOR`, default 3) even if all composite scores below threshold
- [ ] Floor observations selected by highest composite score (best available)
- [ ] Applied in both `handleContextInject` and `handleSearchByPrompt`

**Files:** `internal/worker/handlers_context.go`

### FR-18: Dedup Similarity Threshold Raise
Current threshold 0.55 is too permissive — near-duplicates pass through.

**Acceptance Criteria:**
- [ ] Default `DedupSimilarityThreshold` raised from 0.55 to 0.7 in `config.go`
- [ ] **Pre-test:** Sample 100 observation pairs in 0.55-0.7 similarity range. If >10% are genuinely distinct, keep at 0.55.

**File:** `internal/config/config.go`

### FR-19: Retrieval Count Decay
Older retrieval counts contribute equally to importance — no temporal decay.

**Acceptance Criteria:**
- [ ] Retrieval contribution formula: `log2(recent_count + 1) * 0.1` where `recent_count` = retrievals in last 30 days (not all-time)
- [ ] Requires `retrieved_at` timestamp per retrieval event, or exponential moving average

**File:** `internal/scoring/calculator.go`

### FR-20: Manual Search Feedback Signal
No detection of "user manually searched for something that should have been injected."

**Acceptance Criteria:**
- [ ] `stop.js` detects pattern: engram search tool called during session for query that overlaps with injected observation content
- [ ] Signal: `insufficient_injection` sent to `/api/observations/{id}/utility` with negative weight
- [ ] Observation that SHOULD have been injected but wasn't → importance boosted

**File:** `plugin/engram/hooks/stop.js`

---

## Phase 4: MCP Tools Polish (P1)

### FR-21: Full Tool Set Access Parameter
Spec says `include_all: true`, implementation uses `cursor: "all"`.

**Acceptance Criteria:**
- [ ] `tools/list` accepts `include_all: boolean` parameter
- [ ] `cursor: "all"` kept as backward-compatible alias
- [ ] Default behavior unchanged (T1+T2 tools only)

**File:** `internal/mcp/server.go`

### FR-22: Vault Tool Aliases
Vault tools lack namespace prefix. Add aliases without breaking existing names.

**Acceptance Criteria:**
- [ ] New aliases registered: `vault_store`, `vault_get`, `vault_list`, `vault_delete`, `vault_status`
- [ ] Existing names (`store_credential`, `get_credential`, etc.) continue to work
- [ ] Aliases route to same handlers

**File:** `internal/mcp/server.go`

### FR-23: Document Tool Aliases
Same as FR-22 for document tools.

**Acceptance Criteria:**
- [ ] New aliases: `doc_list_collections`, `doc_list_documents`, `doc_get`, `doc_ingest`, `doc_search`, `doc_remove`
- [ ] Existing names continue to work

**File:** `internal/mcp/server.go`

### FR-24: Tag MCP Tools
`tag_observation` and `batch_tag_by_pattern` exist as REST endpoints but not MCP tools.

**Acceptance Criteria:**
- [ ] `tag_observation` MCP tool: accepts `{ id, tags: [], action: "add"|"remove"|"set" }`
- [ ] `batch_tag_by_pattern` MCP tool: accepts `{ pattern, tag, action, dry_run }`
- [ ] Both tools at tier T2 (useful)

**File:** `internal/mcp/server.go`, `internal/mcp/tools_tags.go` (new)

### FR-25: File Context MCP Tool
`GetObservationsByFile` exists as REST but no MCP tool for agents to manually query.

**Acceptance Criteria:**
- [ ] `find_by_file_context` MCP tool: accepts `{ file_path, limit }`, returns file-specific observations
- [ ] Distinct from `find_by_file` which searches files_modified/files_read in search results

**File:** `internal/mcp/server.go`

---

## Phase 5: Deployment (P2)

### FR-26: Client-Only Install Script
`install.sh` copies `engram-server` binary for client installs — unnecessary.

**Acceptance Criteria:**
- [ ] `install.sh` has `--client-only` flag (default) that skips `engram-server` download
- [ ] `--full` flag downloads everything (for self-hosted single-machine setup)
- [ ] Default behavior changed to client-only

**File:** `install.sh`

### FR-27: Deployment Docs Naming
`docs/DEPLOYMENT.md` Option C uses `cmplus-server` image name.

**Acceptance Criteria:**
- [ ] All references to `cmplus-server` replaced with `engram-server` in DEPLOYMENT.md

**File:** `docs/DEPLOYMENT.md`

---

## Phase 6: Release v1.7.0 (Gate)

### FR-28: PreCompact Verification
PreCompact hook registered but transcript_path not yet verified empirically.

**Acceptance Criteria:**
- [ ] Trigger compaction → `.agent/pre-compact-discovery.json` appears with `transcript_path` field
- [ ] If `transcript_path` present: confirm backfill endpoint receives chunks
- [ ] If absent: confirm fallback path derivation works

### FR-29: PreToolUse Verification
PreToolUse hook registered but file-context injection not tested.

**Acceptance Criteria:**
- [ ] Edit a file that has engram observations → `<file-context>` block appears as systemMessage
- [ ] Edit an unknown file → no delay, no error

### FR-30: PreCompact Dedup Measurement
Need to measure duplicate rate between PreCompact and per-tool extraction.

**Acceptance Criteria:**
- [ ] After 3 real sessions with PreCompact active, compare observation counts
- [ ] If duplicate rate >10%: implement server-side dedup via embedding similarity >0.95
- [ ] Document findings in spec

### FR-31: Release Tag
**Acceptance Criteria:**
- [ ] `gh release create v1.7.0` with structured changelog covering all phases
- [ ] Plugin marketplace updated with v0.6.0

---

## Phase 7: OpenClaw Integration (P2)

### FR-35: OpenClaw SDK Research (PREREQUISITE — blocks FR-32/33/34)
RQ-1 (unit of work definition) unresolved. **No implementation of FR-32/33/34 until this is complete.**

**Acceptance Criteria:**
- [ ] Deep-dive OpenClaw plugin SDK: what is a "session"? How does `kind: "memory"` lifecycle work?
- [ ] Document what metadata fields `ctx`/`input` actually provide in each hook event
- [ ] Determine if metadata-based classification (FR-32) is feasible or requires upstream OpenClaw changes
- [ ] Document findings in `.agent/specs/engram-roadmap/research-openclaw-sdk.md`
- [ ] Decision gate: GO (metadata sufficient) or PIVOT (improved content classification with allowlist)

### FR-32: Metadata-Based Message Classification
**BLOCKED BY FR-35.** OpenClaw hooks use content regex to filter heartbeat/Telegram messages. Fragile.

**Acceptance Criteria:**
- [ ] Classify messages by `ctx`/`input` metadata (if FR-35 confirms feasibility), OR improved content classification with allowlist (if metadata insufficient)
- [ ] Categories: `user_prompt`, `heartbeat`, `system`, `agent_internal`, `external_channel`
- [ ] Only `user_prompt` and `external_channel` processed for extraction
- [ ] Heartbeat/system silently skipped

**Files:** `plugin/openclaw-engram/src/hooks/`

### FR-33: Cross-Channel Context Injection
**BLOCKED BY FR-35.** Context injection is project-scoped, not channel-aware.

**Acceptance Criteria:**
- [ ] Observations from any channel (Telegram, web, CLI) on same project are searchable
- [ ] Channel metadata stored with observations for filtering
- [ ] Context injection includes observations from all channels by default

### FR-34: Cross-Channel Behavioral Rules
**BLOCKED BY FR-35.** Behavioral rules don't propagate across channels.

**Acceptance Criteria:**
- [ ] Rules tagged `always-inject` appear in all channels (already works via server-side query)
- [ ] Verify OpenClaw hooks consume `always_inject` array from server response

---

## Phase 8: Advanced Features (P3)

### FR-36: Intentional Links
Parse `[[obs:1234]]` syntax in observation narratives into graph edges.

**Acceptance Criteria:**
- [ ] Regex parser detects `[[obs:NNNN]]` in narrative text during observation creation
- [ ] Creates `references` graph edge from current observation to referenced ID
- [ ] Bidirectional: referenced observation gets `referenced_by` edge back

### FR-37: File→Observation Graph Index
Observations mentioning file paths don't create graph edges.

**Acceptance Criteria:**
- [ ] `files_modified` and `files_read` entries create `modifies`/`reads` graph edges
- [ ] Edges created during relation detection (existing pipeline)
- [ ] Enables graph traversal: "what observations touch this file?"

### FR-38: GetCluster Graph Method
GraphStore interface missing `GetCluster` method.

**Acceptance Criteria:**
- [ ] Add `GetCluster(nodeID int64, maxNodes int) ([]int64, error)` to `graph.GraphStore` interface
- [ ] FalkorDB implementation uses community detection or BFS with similarity threshold
- [ ] NoopGraphStore returns empty slice

### FR-39: Orphan Vector Cleanup
After purge, vectors may reference deleted observations.

**Acceptance Criteria:**
- [ ] Maintenance task: find vector IDs with no matching observation → delete from vector store
- [ ] Runs as part of daily maintenance cycle
- [ ] Logs count of orphans found and cleaned

### FR-40: Missing Vector Detection
Observations may exist without embeddings (silent embedding failure).

**Acceptance Criteria:**
- [ ] Maintenance task: find observations with no vector entry → queue for re-embedding
- [ ] Re-embedding uses existing async embedding pipeline
- [ ] Logs count of missing vectors found

### FR-41: Stale Relation Cleanup
Relations may reference deleted observation IDs.

**Acceptance Criteria:**
- [ ] Maintenance task: find relations where source_id or target_id has no matching observation → delete relation
- [ ] Also clean FalkorDB edges pointing to non-existent nodes

### FR-42: FalkorDB↔PostgreSQL Drift Detection
Graph nodes may not match observation set after purge.

**Acceptance Criteria:**
- [ ] Maintenance task: compare FalkorDB node set with observation IDs → report drift count
- [ ] If drift >5%: trigger full re-sync from PostgreSQL relations

### FR-43: Embedding Model Change Detection
Changing embedding model invalidates all existing vectors.

**Acceptance Criteria:**
- [ ] Store embedding model name in server metadata (new `system_config` table or config endpoint)
- [ ] On startup: compare current model with stored model → if different, flag for re-embed
- [ ] `GET /api/system/embedding-status` reports: model match, vector count, stale count

### FR-44: Error→Fix Causal Linking
Observations describing errors not linked to observations describing fixes.

**Acceptance Criteria:**
- [ ] LLM classification in `Detect()` pipeline (event-driven async, same as similarity detection)
- [ ] Trigger: only for observations with `type IN (bugfix, correction, guidance)` AND top-3 similarity candidates
- [ ] LLM receives both observation contents, classifies: `fixed_by`, `corrects`, `unrelated`
- [ ] New relation type: `fixed_by` (A → B)
- [ ] ~1 LLM call per 5 observations (not every observation)

### FR-45: Correction→Corrected Linking
User corrections not linked to the corrected behavior.

**Acceptance Criteria:**
- [ ] Same `Detect()` pipeline as FR-44 — LLM classifies `corrects` relationship
- [ ] When USER_BEHAVIOR rule created from correction, link to the observation that was corrected
- [ ] New relation type: `corrects` (new rule → old incorrect behavior)
- [ ] Detection: same session, correction signal in dialog, temporal proximity
- [ ] Combined with FR-44: one LLM call classifies both `fixed_by` and `corrects`

### FR-46: Document Storage Layer (Foundation for Agent Collaboration Platform)
Full document storage designed as foundation for future AI agent task/issue management. Must support multi-agent concurrent access, attribution, and structured metadata — not just Markdown blobs.

**Schema:**
```sql
documents (
  id BIGSERIAL PRIMARY KEY,
  path TEXT NOT NULL,               -- logical path (e.g., "tasks/fix-auth.md", "reviews/pr-53.md")
  project TEXT NOT NULL,
  version INT NOT NULL DEFAULT 1,
  content TEXT NOT NULL,
  content_hash TEXT NOT NULL,        -- SHA-256 for dedup
  doc_type TEXT DEFAULT 'markdown',  -- markdown, task, review, decision (extensible)
  metadata JSONB DEFAULT '{}',       -- structured: assignee, status, priority, labels, parent_id
  author TEXT NOT NULL,              -- agent ID or user ID (attribution)
  created_at TIMESTAMPTZ NOT NULL,
  UNIQUE(path, project, version)
)

document_comments (
  id BIGSERIAL PRIMARY KEY,
  document_id BIGINT REFERENCES documents(id),
  author TEXT NOT NULL,
  content TEXT NOT NULL,
  line_start INT,                    -- optional: inline comment on specific lines
  line_end INT,
  status TEXT DEFAULT 'open',        -- open, resolved, dismissed
  created_at TIMESTAMPTZ NOT NULL
)
```

**Acceptance Criteria:**
- [ ] Documents stored with full version history (each write = new version row)
- [ ] Read latest: `SELECT ... WHERE path=$1 AND project=$2 ORDER BY version DESC LIMIT 1`
- [ ] Read specific version: `SELECT ... WHERE path=$1 AND project=$2 AND version=$3`
- [ ] Version diff: return content of two versions for client-side diff
- [ ] `doc_type` supports extensible types (markdown, task, review, decision)
- [ ] `metadata` JSONB supports structured fields: assignee, status, priority, labels, parent_id (for subtasks)
- [ ] `author` field on every write — full attribution trail
- [ ] `document_comments` table for inline and general comments (review workflow foundation)
- [ ] `memory_get(path, from, lines)` compatibility: reads document by path, returns content slice
- [ ] MD bridge: first access imports from disk → engram; subsequent reads from PostgreSQL
- [ ] MCP tools: `doc_create`, `doc_read`, `doc_update`, `doc_list`, `doc_history`, `doc_comment`
- [ ] Embedding: document content chunked and embedded for semantic search (reuse existing collection pipeline)
- [ ] Concurrent writes: last-write-wins with version tracking (no merge conflicts — agents work on distinct documents or sections)

---

## Non-Functional Requirements

### NFR-1: Phase 1 Changes Non-Breaking
All Phase 1 changes must be additive — no API contract changes, no migration conflicts.

### NFR-2: Dashboard Changes Backward-Compatible
New dashboard features must not break existing views. New endpoints only.

### NFR-3: MCP Tool Aliases Backward-Compatible
Old tool names must continue working. Aliases are additions, not renames.

### NFR-4: Self-Learning Changes Configurable
All Phase 3 features must be opt-in via config flags with safe defaults that match current behavior.

### NFR-5: Phase 8 Features Incremental
Each Phase 8 feature must be independently deployable — no "all or nothing" dependency.

---

## Edge Cases

- FR-1: LLM returns empty string → skip redaction, proceed normally
- FR-7: Vault goroutine panics → recover, log, don't crash handler
- FR-10: Batch tag with 0 IDs → return 400, not silent success
- FR-15: No recent sessions → recentSessionBoost = 1.0 (no effect)
- FR-16: Project with 0 feedback signals → use global threshold
- FR-17: Injection floor > available observations → inject all available
- FR-18: Threshold change while observations in flight → apply to next search only
- FR-30: Duplicate rate exactly 10% → don't add dedup (threshold is >10%)
- FR-39/40/41: Maintenance runs while observations being created → use transaction isolation

---

## Out of Scope

None. Everything discussed in ADR-001 D1-D10 is covered in Phases 1-8.
If something needs to be deferred, it must be explicitly approved by the user.

---

## Dependencies

```
Phase 1 (P0): Independent — do first
Phase 2 (P1): Independent — parallel with 1
Phase 3 (P1): After Phase 1 (scoring changes need stable pipeline)
Phase 4 (P1): Independent — parallel with 1,2
Phase 5 (P2): Independent — parallel with 1,2,4
Phase 6 (Gate): After Phases 1-5
Phase 7 (P2): After Phase 6
Phase 8 (P3): After Phase 6
```

## Clarifications

| # | Category | Question | Resolution | Date |
|---|----------|----------|------------|------|
| C1 | Non-Functional | FR-16 (adaptive threshold) and FR-17 (injection floor) overlap — need both? | Both needed. Floor = safety net (prevents 0 observations). Adaptive threshold = optimization (right quantity per project). Different problems. | 2026-03-24 |
| C2 | Integration | FR-32/33/34 depend on OpenClaw metadata fields — do we know what's available? | FR-35 (SDK research) is MANDATORY prerequisite. No blind implementation. If metadata insufficient → pivot to improved content classification with allowlist. | 2026-03-24 |
| C3 | Edge Cases | FR-44/45 LLM classification — hot path or batch? | Event-driven async in existing `Detect()` pipeline. Filter: only type IN (bugfix, correction, guidance) + top-3 candidates. ~1 LLM call per 5 observations. Combined classification for both fixed_by and corrects. | 2026-03-24 |
| C4 | Functional | FR-15 cross-session priming: is sdk_sessions.last_activity updated in real-time? | Verify at implementation. If not real-time → use MAX(observations.created_at_epoch) by session_id as proxy. Does not block spec. | 2026-03-24 |
| C5 | Domain/Data | FR-46 scope: simple key-value or full document platform? | Full platform. User plans AI agent collaboration (task assignment, code review between agents). Schema includes: versioned documents, doc_type (markdown/task/review/decision), metadata JSONB (assignee/status/priority), author attribution, document_comments table for review workflow. Last-write-wins concurrency. | 2026-03-24 |

## Success Criteria

- [ ] All 7 Phase 1 items pass code review + tests
- [ ] Dashboard bulk operations work end-to-end
- [ ] Self-learning loop measurably improves injection quality (before/after comparison on 10 sessions)
- [ ] v1.7.0 released with full changelog
- [ ] Zero Constitution violations in codebase
