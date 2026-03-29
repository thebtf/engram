# Feature: Engram v1.1 — Quality Hardening

**Slug:** engram-v1.1-quality
**Created:** 2026-03-19
**Status:** Implemented (2 items documented in TECHNICAL_DEBT.md)
**Author:** AI Agent (reviewed by user)
**Source:** 3-auditor parallel stub audit + production Playwright testing + user feedback

## Overview

Eliminate all stubs, hardcoded fakes, and UX gaps discovered during v1.0 production testing. Covers 11 audit findings (2 HIGH, 6 MEDIUM, 3 LOW), 6 dashboard UX bugs, 3 dashboard enhancements, and pattern quality crisis (100K+ junk patterns). Goal: every metric shown to the user reflects real data, every UI element functions correctly.

## Context

Engram v1.0.0-v1.0.2 shipped with dashboard, relation detection, and auth subsystem. Production testing on unleashed.lan:37777 revealed:
- Vector metrics page shows hardcoded zeros (latency, queries, cache — all fake)
- 100,445 patterns with avg confidence 0.50 (half are noise, no auto-cleanup)
- Dashboard UX bugs: vault encryption status wrong, copy doesn't work over HTTP, revoked tokens still visible, graph page unusable, dropdown contrast poor
- Stale code: dormant fields, silently dropped chunks, heuristic limits, os.Exit for reload

Constitution Principle 8 ("Complete Implementations Only") is violated by handleVectorMetrics (15+ hardcoded zeros shown to users as real metrics).

## Functional Requirements

### Section A: Stub Elimination (audit findings)

#### FR-1: Real Vector Query Metrics
The vector database client MUST track real query metrics: total query count, per-query latency, and latency percentiles (p50, p95, p99). The `/api/vector/metrics` endpoint MUST return only measured values. Fields without real instrumentation MUST be removed from the API response (not returned as zeros).

#### FR-2: Search Scope Parameter
The OpenClaw plugin search tool (`engram-search.ts`) advertises a `scope` parameter (personal/shared/all) that is currently ignored. Either: (A) pass scope through to the server and enforce filtering, or (B) remove the parameter from the tool schema until implemented.

#### FR-3: Remove Hardcoded Metric Fields
Fields in `/api/vector/metrics` that have no backing instrumentation (hubOnly, hybrid, onDemand, avgHub, avgRecompute, hubDocuments, savingsPercent, recomputedTotal, graph traversals) MUST be removed from the API response. Dashboard MUST NOT display metrics that don't exist.

#### FR-4: Fix ObservationStore Dormant Fields
`conflictStore` and `relationStore` fields in ObservationStore are typed as `any` and never read. Either integrate them properly with typed interfaces (relation detection now exists via `RelationDetector` interface), or remove the unused fields.

#### FR-5: Handle Large Chunks
When a document chunk exceeds MaxChunkSize, the system MUST NOT silently drop it. Options: split deterministically, or log a warning with the file path and chunk size so data loss is visible.

#### FR-6: Fix Analytics Query Heuristic
The analytics trends endpoint uses `days * 50` as observation limit. Replace with proper `WHERE created_at >= cutoff` filter in the database query.

#### FR-7: Real Graph Node Count
Graph stats endpoint MUST return real node count (distinct observation IDs in relations), not `edges * 1.5` approximation.

#### FR-8: Config Reload Without os.Exit
`reloadConfig` MUST NOT call `os.Exit(0)`. Implement proper hot-reload or document this as an intentional restart in the API response (not pretend it's a reload).

#### FR-9: MCP Resources/Prompts — Document or Implement
MCP stub handlers for resources/list, prompts/list, completion/complete MUST either be implemented or documented in TECHNICAL_DEBT.md as intentional graceful degradation.

#### FR-10: Remove Stale Comments
Remove "Phase 4" comments in observation_store.go (Phase 4 is done), stale edge_detector comment in observation_graph.go, and TODO in engram-search.ts (or implement the TODO).

#### FR-11: Memory Blocks Table — Document Gap
`memory_blocks` table exists (migration 024) but is never populated. Document in TECHNICAL_DEBT.md with clear scope for consolidation-driven population.

### Section B: Dashboard UX Bugs

#### FR-12: Vault Encryption Status Mapping
VaultView shows "Encryption: Disabled" when vault API returns `key_configured: true`. Frontend MUST map `key_configured` → display as "Enabled/Disabled" correctly.

#### FR-13: Vault Credential Dates
Vault credential list MUST show creation date. Backend handler MUST include `created_at` from the observation record in the credential response.

#### FR-14: Clipboard Fallback for HTTP
Copy buttons (token reveal, vault reveal) MUST work over HTTP (not just HTTPS). Implement `document.execCommand('copy')` fallback when `navigator.clipboard` is unavailable.

#### FR-15: Revoked Token Display
Revoked tokens MUST be visually distinct: show dimmed with "Revoked" badge, sorted to bottom. List endpoint SHOULD include revoked tokens for audit trail.

#### FR-16: Memory Matches Badge — Show Real Count
The "10 memory matches" badge on observations in timeline MUST show actual result count from engram search, not the requested limit.

### Section C: Dashboard Enhancements

#### FR-17: Graph Page Redesign
Graph page MUST show the knowledge graph immediately on load (top 50 most-connected observations, expandable by clicking nodes). Floating tooltip on hover (title, type, score). Click node → navigate to detail. Filter by project, relation type. Remove manual ID input UX.

#### FR-18: Patterns Page Overhaul
Patterns page MUST have: pagination (not hardcoded 100), sort options (frequency, confidence, last seen), items per page selector, search by name, explanation header text. Show occurrence count and confidence prominently.

#### FR-19: Pattern Quality Tuning
Pattern detection MUST have quality controls:
- Raise thresholds for new patterns: MinFrequency ≥ 5, MinMatchScore ≥ 0.5
- Time-based decay: patterns not seen in 30+ days auto-deprecated (soft, reversible)
- Quality score combining: frequency, confidence, recency, project diversity
- Bulk cleanup endpoint: deprecate patterns below quality threshold
- Show only patterns above quality threshold by default in dashboard
- Deprecated patterns excluded from API responses by default (`?include_deprecated=true` for audit)

## Non-Functional Requirements

### NFR-1: Zero Fake Data (G1 Compliance)
After implementation, ZERO API fields shown in dashboard may contain hardcoded/fabricated values. Every displayed metric MUST originate from actual measurement. Violation = Constitution Principle 8 breach.

### NFR-2: Performance
- Vector query instrumentation overhead < 1μs per query (atomic counter + clock read)
- Pattern decay calculation < 100ms for 100K patterns (batch SQL update)
- Graph page initial load < 2 seconds for 500 nodes

### NFR-3: Backward Compatibility
- Existing observations, relations, patterns MUST NOT be affected
- API consumers receiving removed fields MUST handle missing fields gracefully
- Pattern decay MUST be soft-delete (deprecated, not deleted) — reversible

## User Stories

### US1: See Real Metrics (P1)
**As a** developer monitoring engram, **I want** vector metrics to show actual query performance, **so that** I can diagnose search issues and optimize configuration.

**Acceptance Criteria:**
- [ ] Query count increments on each vector search
- [ ] Latency shows real p50/p95/p99 values after 10+ queries
- [ ] No "0ms" or "0" values for metrics that have real data
- [ ] Metrics absent from response when no instrumentation exists

### US2: Clean Pattern List (P1)
**As a** developer, **I want** patterns automatically curated by quality, **so that** the 100K pattern list is useful instead of overwhelming.

**Acceptance Criteria:**
- [ ] Low-quality patterns auto-deprecated after 30 days of inactivity
- [ ] New patterns require ≥ 5 occurrences (not 2)
- [ ] Dashboard shows patterns sorted by quality score
- [ ] Bulk cleanup endpoint available

### US3: Working Graph Page (P1)
**As a** developer, **I want** to see the knowledge graph immediately without entering observation IDs, **so that** I can explore how my observations relate.

**Acceptance Criteria:**
- [ ] Graph loads on page open with top connected observations
- [ ] Nodes colored by type, tooltip on hover
- [ ] Click node → observation detail
- [ ] Filter by project

### US4: Correct Vault Display (P2)
**As a** developer, **I want** vault to show correct encryption status and dates, **so that** I can trust the information displayed.

**Acceptance Criteria:**
- [ ] "Encryption: Enabled" when key_configured=true
- [ ] Credential dates show actual creation date (not "—")
- [ ] Copy button works over HTTP

### US5: Proper Token Management (P2)
**As a** developer, **I want** revoked tokens clearly marked and copy to work, **so that** token management is reliable.

**Acceptance Criteria:**
- [ ] Revoked tokens shown dimmed with badge, sorted to bottom
- [ ] Copy button works over HTTP (fallback to execCommand)

### US6: Correct Pattern Browsing (P2)
**As a** developer, **I want** patterns paginated and searchable, **so that** I can find relevant patterns in the 100K list.

**Acceptance Criteria:**
- [ ] Pagination with items per page selector
- [ ] Sort by frequency/confidence/recency
- [ ] Search by name

### US7: No Stale Code (P3)
**As a** maintainer, **I want** all stubs and dead comments removed, **so that** the codebase accurately reflects implemented features.

**Acceptance Criteria:**
- [ ] No hardcoded zeros in API responses shown to users
- [ ] No "Phase 4" comments for completed features
- [ ] No unused constructor parameters
- [ ] TECHNICAL_DEBT.md documents all intentional deferrals

## Edge Cases

- Vector metrics on fresh startup (0 queries): show "No data yet" instead of zeros
- Pattern decay on patterns created today: skip (minimum age before decay applies)
- Graph page with 0 relations: show "No relations found — new observations will build the graph automatically"
- Clipboard unavailable AND execCommand unsupported: show "Copy not available — select and Ctrl+C"
- Bulk pattern cleanup with 100K patterns: batch process (1000 per transaction) to avoid timeout

## Out of Scope

- MCP resources/prompts implementation (documented as intentional graceful degradation)
- Memory blocks population (consolidation v2 — separate feature)
- Hub-and-spoke vector optimization (architecture not implemented — remove metrics, don't implement)
- Full hot-reload for config (document as restart-based in API response)
- Mobile responsive design
- Pattern ML-based quality scoring (use heuristic formula for v1.1)

## Dependencies

- pgvector client: needs instrumentation hooks (internal change, no external deps)
- vis-network: already in deps (graph page redesign)
- No new external dependencies required

## Success Criteria

- [ ] `grep -r '"0ms"\|"0%"' internal/worker/handlers_data.go` returns 0 results
- [ ] `/api/vector/metrics` returns only instrumented fields after 10+ queries
- [ ] Pattern count < 10K after decay cleanup (from 100K+)
- [ ] Graph page loads with nodes on first visit
- [ ] All 11 audit findings resolved or documented in TECHNICAL_DEBT.md
- [ ] All 6 dashboard UX bugs fixed
- [ ] `go build ./cmd/worker/` passes
- [ ] `cd ui && npm run build` passes

## Resolved Questions

1. **Pattern decay formula (from Gemini Deep Research):** Hybrid Gaussian+Linear decay.
   - **Quality baseline:** `Q_base = 0.3 * log_freq + 0.5 * sigmoid_conf + 0.2 * diversity` (log-scaled frequency, sigmoid confidence centered at 0.5, linear project diversity)
   - **Temporal decay:** 7-day grace period → Gaussian decay (7-60 days) → Linear terminal to zero (60-90 days)
   - **Prune threshold:** `Q_dynamic < 0.10` → soft-deprecate; `Q_dynamic = 0.0` → eligible for deletion
   - **Active pruning:** Redis-style background sampler (20 random patterns/cycle, repeat if >25% expired)
   - Sources: Elasticsearch function_score, Redis TTL patterns, Qdrant decay functions, recommendation system freshness scoring

2. **Graph page data source:** Reuse existing endpoints. Current `/api/observations/{id}/graph` + `/api/graph/stats` + `/api/relations/stats`. No new endpoint. Graph page redesign will use client-side aggregation of top-connected observations from existing APIs.

3. **API version bump:** Yes — every change bumps patch version (v1.1.0 for this iteration). Removed vector metric fields are a breaking change for consumers expecting those fields.

## Open Questions

None — all clarified.
