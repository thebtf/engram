# Specification: Dashboard Analytics v2 — Persistent Analytics + Bugfixes + Verified Facts TTL

**Status:** Implemented

## Overview

Replace in-memory analytics (search queries, retrieval stats) with PostgreSQL-persisted analytics
that survive server restarts. Fix all broken dashboard pages (Patterns, Graph, Analytics, Sessions).
Add TTL enforcement for verified facts to prevent stale facts from bypassing re-verification.

## Current State Analysis

### What's broken
1. **Patterns page**: Frontend `Pattern` type uses `occurrences` but backend sends `frequency` (JSON tag).
   Sort by frequency compares `undefined - undefined = NaN`. Display shows "undefined occurrences".
   - Files: `ui/src/utils/api.ts:598`, `ui/src/views/PatternsView.vue:60,247`

2. **Graph page**: vis-network renders 0 nodes despite stats showing 341/525. Root cause: `graphContainer`
   div is rendered alongside `v-if` conditions — container exists but may have zero dimensions when
   Network is initialized (loading overlay covers it, but container IS in DOM).
   Actual root cause needs empirical verification — could be data issue, container size, or library bug.
   - File: `ui/src/views/GraphView.vue:400-403`

3. **Analytics page — search misses**: `fetchSearchMisses()` POSTs `{}` to `/api/analytics/search-misses`,
   backend requires `project` field → 400. Frontend silently catches error → empty list.
   - Files: `ui/src/utils/api.ts:572-573`, `internal/worker/handlers_context.go:1230`

4. **Analytics page — retrieval stats**: In-memory `RetrievalStats` struct with atomic counters.
   Resets to zero on every server restart. Useless for long-term analytics.
   - Files: `internal/worker/service.go:1872-1894`

5. **Analytics page — search analytics**: Derived from in-memory ring buffer (`recentQueriesRing`).
   Holds max ~200 queries, resets on restart. Not meaningful analytics.
   - Files: `internal/worker/handlers_data.go:466-470`, `internal/worker/service.go:1938-1970`

### What works but could be better
6. **Sessions page**: Works if sessionIdxStore initialized. Shows "No sessions found" without
   explanation if session indexing is not configured.

7. **StatsCards "Queue Depth"**: Unclear metric, no tooltip.

## Functional Requirements

### Phase 1: Fix Broken UI (frontend-only, except FR3 backend change)

- FR1: Pattern type must use `frequency` field from backend JSON. Also add `last_seen_at` and `last_seen_at_epoch` fields (present in backend model, missing in frontend type). Sort by frequency, confidence, and last_seen must all work. Display: "N occurrences" where N = `frequency` value.
  - **Affected files**: `ui/src/utils/api.ts` (Pattern interface), `ui/src/views/PatternsView.vue` (sort + display), `ui/src/composables/usePatterns.ts` (if references occurrences).
- FR2: Graph page must render vis-network nodes. Debug approach: (1) verify `graphContainer` has non-zero dimensions at render time via `nextTick`, (2) if container is zero-size, set explicit `min-height` + use `v-show` instead of conditional rendering, (3) if data issue, log node/edge counts before render. Success = at least 1 node visible in canvas.
- FR3: Backend `handleSearchMissAnalytics` must make `project` optional. When project is empty, return global search misses across all projects. Frontend `fetchSearchMisses` sends `{}` body — this must work.
  - **Backend change**: `internal/worker/handlers_context.go:1230` — remove `project required` check, adjust SQL to omit `WHERE project = ?` when empty.
  - **Frontend change**: none needed (already sends `{}`).
- FR4: Sessions page must distinguish 503 ("Session indexing not configured on server") from 500 ("Internal error"). Show specific message for 503.

### Phase 2: Persistent Search Query Log (new PostgreSQL table)

- FR5: Create `search_query_log` table:
  ```sql
  CREATE TABLE search_query_log (
    id BIGSERIAL PRIMARY KEY,
    project TEXT,
    query TEXT NOT NULL,
    search_type TEXT NOT NULL,  -- enum: 'vector', 'filter', 'hybrid', 'fts', 'decision'
    results INT NOT NULL DEFAULT 0,
    used_vector BOOL NOT NULL DEFAULT false,
    latency_ms REAL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
  );
  CREATE INDEX idx_search_query_log_created ON search_query_log (created_at DESC);
  CREATE INDEX idx_search_query_log_project ON search_query_log (project, created_at DESC);
  ```
- FR6: Insert a row for every search through these handlers:
  - `handleSearchByPrompt` (`/api/context/search`)
  - `handleContextInject` (`/api/context/inject`)
  - MCP `search` tool handler
  - MCP `decisions` tool handler
  - MCP `how_it_works` tool handler
  - MCP `find_by_concept` tool handler
  Insert is async (goroutine), fire-and-forget. Log warning on insert error, never block request.
- FR7: `/api/search/analytics` derives stats from `search_query_log` via SQL aggregation. In-memory ring buffer kept as real-time supplement for SSE dashboard updates only — not used for analytics endpoint.
- FR8: `/api/search/recent` queries `search_query_log ORDER BY created_at DESC LIMIT ?`. Ring buffer no longer used for this endpoint.
- FR9: Cleanup: maintenance job deletes rows with `created_at < NOW() - INTERVAL '90 days'`. Runs as part of existing `handleRunMaintenance` handler (same trigger as pattern decay and other cleanup).

### Phase 3: Persistent Retrieval Stats (new PostgreSQL table)

- FR10: Create `retrieval_stats_log` table:
  ```sql
  CREATE TABLE retrieval_stats_log (
    id BIGSERIAL PRIMARY KEY,
    project TEXT NOT NULL,
    event_type TEXT NOT NULL,  -- enum: 'request', 'observation_served', 'search', 'context_injection', 'stale_excluded', 'duplicate_removed'
    count INT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
  );
  CREATE INDEX idx_retrieval_stats_project_type_created ON retrieval_stats_log (project, event_type, created_at DESC);
  ```
- FR11: Batched insert: accumulate events in-memory channel (buffered, cap 10000). Flush goroutine runs every 10 seconds OR when buffer reaches 1000 events (whichever first). On server shutdown: flush remaining buffer. On crash: in-flight buffer is lost (acceptable — analytics, not transactional data).
- FR12: `/api/stats/retrieval` aggregates from DB. Optional `since` parameter: ISO8601 timestamp string (e.g., `?since=2026-03-13T00:00:00Z`). Default: no filter (all-time). In-memory atomic counters KEPT as fast-path for SSE real-time updates and `/api/stats` main endpoint. DB is source of truth for analytics.
- FR13: Dashboard Analytics page calls `/api/stats/retrieval?since=...` with time range from selector.

### Phase 4: Enhanced Analytics Dashboard

- FR14: Analytics page top cards: total searches (all-time), searches today (UTC midnight boundary), avg latency (ms), zero-result rate (%) — all from `search_query_log`.
- FR15: Retrieval section: total requests, observations served, context injections — from `retrieval_stats_log` with `since` parameter.
- FR16: Search misses: global view (no project filter). Shows top queries with zero results, aggregated by query text.
- FR17: Time range selector: `today`, `7d`, `30d`, `all`. Frontend computes `since` as ISO8601 with local timezone offset (e.g., `2026-03-20T00:00:00+03:00`). Server parses with `time.Parse(time.RFC3339, since)` — Go handles timezone conversion to UTC automatically. No server-side timezone logic. Perset computation in frontend: `today` = start of day in user's local TZ, `7d` = now minus 7 days, `30d` = now minus 30 days, `all` = omit `since` parameter. Default: `7d`. Applied to all analytics sections uniformly. Each section shows appropriate empty state when no data exists for selected range.
- FR17b: Loading states: each section shows spinner independently while its API call is in flight.

### Phase 5: Verified Facts TTL (expires_at on observations)

- FR18: Migration adds two columns to observations table:
  ```sql
  ALTER TABLE observations ADD COLUMN expires_at TIMESTAMPTZ NULL;
  ALTER TABLE observations ADD COLUMN ttl_days INT NULL;
  CREATE INDEX idx_observations_expires ON observations (expires_at) WHERE expires_at IS NOT NULL;
  ```
  NULL = no expiration (default). All existing 13K+ observations remain unaffected. Migration is safely rollback-able: `ALTER TABLE observations DROP COLUMN expires_at, DROP COLUMN ttl_days;` with zero data loss.

- FR19: `store_memory` MCP tool gets optional `ttl_days INT` parameter. TTL computation:
  1. If `ttl_days` provided by agent → use it (agent override, highest priority)
  2. Else if observation has `verified` tag → apply auto-TTL by concept tags:
     - Exact tag match (not substring): `api` or `endpoint` → 7d, `library` or `framework` → 30d, `language-feature` → 90d, `architecture` or `pattern` → 180d
     - If no tag matches any auto-TTL rule → **default 30 days**
  3. Else (no `verified` tag, no `ttl_days`) → no TTL, `expires_at = NULL`

  `expires_at = NOW() + ttl_days * INTERVAL '1 day'`. Application code sets it, not a DB trigger.
  Guard: `expires_at` is ONLY set when `ttl_days` is non-NULL. No code path sets `expires_at` without `ttl_days`.

- FR20: Real-time computation only. Search/recall_memory responses include computed `is_expired` field: `expires_at IS NOT NULL AND expires_at < NOW()`. No materialized boolean column. This avoids divergence between maintenance runs and real-time state.

  Implementation: add `is_expired` as a computed/virtual field in Go response serialization (not a DB column). SQL queries that return observations add `CASE WHEN expires_at IS NOT NULL AND expires_at < NOW() THEN true ELSE false END AS is_expired`.

- FR21: Maintenance job: NO materialized boolean, NO deletion, NO score changes. Maintenance only logs metrics: count of expired verified facts for monitoring. Future iteration may add decay — we observe scale first.

- FR22: Update confidence-check SKILL.md:
  - Step 0: search engram for previously verified facts (existing behavior)
  - New: check `is_expired` field on results. If `is_expired: true` → re-verify via full cascade
  - After successful re-verification: call `store_memory` with updated `ttl_days` to refresh `expires_at`
  - This is a prompt-level instruction change (SKILL.md), not enforceable by server. Observable only through agent behavior in session logs.

## Non-Functional Requirements

- NFR1: Search query logging adds <2ms P99 latency. Measured by: comparing search handler duration with/without logging goroutine in load test (100 concurrent searches). Async goroutine = near-zero on happy path.
- NFR2: Retrieval stat batching: 10s flush interval, max 1000 events per batch, max 10000 events in buffer. Buffer overflow: drop oldest events with warning log. No unbounded memory growth.
- NFR3: 90-day retention for `search_query_log` and `retrieval_stats_log`. Cleanup runs inside `handleRunMaintenance` (existing maintenance endpoint). Frequency: on-demand (called by maintenance cron or manual trigger).
- NFR4: `expires_at` column safety: NULL default, partial index (`WHERE expires_at IS NOT NULL`), TTL logic only activates via `store_memory` with `verified` tag or explicit `ttl_days`. No migration or code path sets `expires_at` on existing observations.
- NFR5: No cascading deletions — expired facts are flagged in response, never removed or score-decayed.

## Acceptance Criteria

- [ ] AC1: Patterns page sorts correctly by frequency, confidence, and last seen
- [ ] AC2: Patterns page shows "5 occurrences" where backend frequency=5
- [ ] AC3: Graph page renders vis-network with at least 1 node visible in canvas (verified via Playwright screenshot or manual inspection)
- [ ] AC4: Analytics page shows non-zero search stats after server restart (query `search_query_log` has rows)
- [ ] AC5: Analytics page shows retrieval stats that survive restart (query `retrieval_stats_log` has rows)
- [ ] AC6: Analytics search misses populated with global data (no project filter needed)
- [ ] AC7: Time range selector (today/7d/30d/all) filters all analytics sections
- [ ] AC8: Search query logging: P99 overhead < 2ms (async goroutine)
- [ ] AC9: Rows older than 90 days deleted from analytics tables after maintenance run
- [ ] AC10: `store_memory(tags=["verified","api"], ttl_days=7)` → observation has `expires_at` = now + 7 days
- [ ] AC11: Search results include `is_expired: true` for observations where `expires_at < NOW()`
- [ ] AC12: confidence-check SKILL.md instructs re-verification for expired facts (prompt change verified by reading SKILL.md)
- [ ] AC13: Maintenance job logs count of expired verified observations (no mutations)
- [ ] AC14: `store_memory(tags=["verified","architecture"], ttl_days=180)` → agent override: expires_at = now + 180d (not auto-TTL 180d from tag — same result but different code path)
- [ ] AC15: Existing 13K+ observations have `expires_at = NULL` and `ttl_days = NULL` after migration

## Migration Ordering

Three separate GORM migrations, executed in order:
1. **Migration N**: `search_query_log` table + indexes (Phase 2)
2. **Migration N+1**: `retrieval_stats_log` table + indexes (Phase 3)
3. **Migration N+2**: `ALTER TABLE observations ADD COLUMN expires_at/ttl_days` + partial index (Phase 5)

Each migration is independently rollback-able. Phase 4 (frontend) has no migration.

## Maintenance Job Scope

Single existing maintenance handler (`handleRunMaintenance`) gets three new cleanup steps:
1. Delete `search_query_log` rows older than 90 days
2. Delete `retrieval_stats_log` rows older than 90 days
3. Log count of expired verified observations (`WHERE expires_at IS NOT NULL AND expires_at < NOW() AND concepts @> '["verified"]'`)

These run alongside existing maintenance tasks (pattern decay, DB optimization).

## Out of Scope

- Real-time streaming analytics (SSE push of analytics updates)
- Grafana/Prometheus export
- Per-token analytics (which API token made which queries)
- Pattern analytics (pattern detection metrics)
- Dashboard performance profiling
- Verified facts dashboard UI (future iteration — view/manage/extend TTL from dashboard)
- Automatic importance score decay for expired facts (future — observe scale first, Phase 5 does NOT preclude this)
- Materialized `is_expired` boolean column (real-time computation chosen to avoid divergence)

## Dependencies

- PostgreSQL 17 (existing)
- GORM migrations (existing pattern)
- Vue 3 + TypeScript (existing frontend)
- vis-network / vis-data (already installed)
- MCP tool registration (existing pattern in internal/mcp/server.go)
