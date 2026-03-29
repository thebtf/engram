# Dashboard Bugfixes — Post-Deploy Smoke Test Findings

## Context

Smoke test of engram v1.7.0 dashboard revealed 11 bugs across security, filtering, API, and data quality.

## Functional Requirements

### FR-1: Server-Side Type Filter (B2+B3) — P1
**Current:** Type filter buttons filter client-side on 20 pre-fetched records. Total count and pagination ignore filter.
**Required:**
- `GET /api/observations` accepts `type` query param
- `GetAllRecentObservationsPaginated` filters by type in SQL WHERE clause
- `fetchObservationsPaginated` in api.ts sends `type` param
- ObservationsView passes `currentType` to fetchPage(), removes client-side filter
- Total and pagination reflect filtered count
- Add `guidance` to `ObservationType` union, `OBSERVATION_TYPES` array, and `TYPE_CONFIG` record

**Files:**
- `internal/worker/handlers_data.go` — add type param parsing
- `internal/db/gorm/observation_store.go` — add type filter to paginated query
- `ui/src/utils/api.ts` — add type to fetchObservationsPaginated params
- `ui/src/views/ObservationsView.vue` — pass type to API, remove filteredObservations
- `ui/src/types/observation.ts` — add guidance type + config

### FR-2: Tag Cloud API Fix (B4) — P2
**Current:** `GET /api/observations/tag-cloud` returns empty response.
**Required:** Returns top N tags with counts. Investigate why empty — likely missing project param or query bug.

**Files:**
- `internal/worker/handlers_tags.go` — handleTagCloud
- `ui/src/views/ObservationsView.vue` — loadTagCloud

### FR-3: Observation Count Consistency (B8) — P2
**Current:** Sidebar shows 636, header shows 662, API returns 679.
**Required:** All counts use same source (API total from paginated endpoint).

**Files:**
- `ui/src/views/HomeView.vue` or sidebar component — check count source
- `ui/src/views/ObservationsView.vue` — header count

### FR-4: SSE Reconnect Loop (B7) — P2
**Current:** "Connection lost. Reconnecting in 8s..." banner persists.
**Required:** SSE connects or gracefully degrades without persistent error banner.

**Files:**
- `ui/src/utils/sse.ts` or equivalent — SSE client
- `internal/worker/service.go` — SSE endpoint, CSP headers

### FR-5: Sidebar Metrics (B11) — P3
**Current:** Requests: 0, Injections: 0 in sidebar despite MCP health showing 37 requests.
**Required:** Sidebar metrics pull from MCP health endpoint or correct data source.

**Files:**
- `ui/src/components/layout/AppSidebar.vue` or equivalent

## Out of Scope (separate tracks)

- **B1 (Token in timeline):** This is a client-side display issue — the user typed the token into the search/prompt field, and it appears in the session transcript. Not a server-side leak. The token was already visible to the authenticated user. Low risk — document but don't fix in this batch.
- **B5 (Vault key mismatch):** Known, documented in TECHNICAL_DEBT.md. Old key lost.
- **B6 (vault/list 404):** Dashboard may use wrong endpoint. Check and fix if trivial.
- **B9 (Analytics stats empty):** Endpoint may not exist. Check and wire if trivial.
- **B10 (LLM extraction types):** Server-side prompt issue, separate investigation.

## Non-Functional Requirements

- NFR-1: No new dependencies
- NFR-2: Backward compatible — existing API consumers unaffected (type param is optional)
- NFR-3: Go build + go test must pass after changes
