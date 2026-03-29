# Feature: Dashboard v1.0.2 — Bug Fixes + Relation Detection

**Slug:** dashboard-fixes
**Created:** 2026-03-19
**Status:** Mostly Implemented (2 gaps: vault encryption helper, batch-tag endpoint)
**Author:** AI Agent (reviewed by user)

## Overview

Fix all bugs found during production testing of Dashboard v1.0.1, implement missing relation detection (critical for knowledge graph), and add the OpenClaw heartbeat config option. This iteration addresses 3 frontend bugs, 2 critical backend gaps, 1 infra config helper, and 1 plugin feature request.

## Context

Dashboard v1.0.1 was deployed to production (unleashed.lan:37777). Playwright testing revealed:
- "Invalid Date" in Vault and Analytics pages (null date formatting)
- Analytics stats showing zeros (API field naming mismatch)
- Knowledge graph empty because relation detection was never implemented (marked "Phase 4")
- Conflict detection placeholder never wired up
- Vault encryption disabled (no ENGRAM_VAULT_KEY guidance in Docker template)

The relation detection gap is the most impactful: without it, the Graph page, `find_related_observations` MCP tool, and knowledge graph visualization are non-functional despite the infrastructure (FalkorDB, RelationStore, AsyncGraphWriter) being fully built and connected.

## Functional Requirements

### FR-1: Fix "Invalid Date" in Vault and Analytics

All date/time values displayed in the dashboard MUST gracefully handle null, empty, or missing date fields. When a date field is absent, the UI MUST show "—" instead of "Invalid Date".

Affected views: VaultView (credential created_at), AnalyticsView (recent queries last_used).

### FR-2: Fix Analytics Stats Display

The Analytics page MUST correctly display non-zero statistics from the API. All API response fields MUST map correctly to frontend display regardless of naming convention (TitleCase vs camelCase).

### FR-3: Implement Relation Detection on Observation Create

When a new observation is stored, the system MUST analyze existing observations to detect relationships. Detected relations MUST be stored in the `observation_relations` table AND written to FalkorDB (via AsyncGraphWriter) if configured.

Relation types to detect:
- `fixes` — new bugfix obs references same files/concepts as existing problem obs
- `explains` — new discovery/how-it-works obs covers same topic as existing obs
- `supersedes` — new obs has very high similarity (>0.9) to existing obs of same type
- `contradicts` — new decision obs has different conclusion on same topic (uses `rejected[]`)
- `evolves_from` — new obs of same type in same project with high concept overlap

Detection method: detector calls embedding service inline on the new observation's text (~50ms), then vector similarity search → filter by type heuristics → create relations above confidence threshold (0.6). Detector runs async so embedding latency is invisible to the user.

### FR-4: Implement Conflict Detection on Observation Create

When a new observation is stored, the system MUST check for potential conflicts with existing observations (high similarity, same type, different conclusions). Conflicts MUST be stored in the `observation_conflicts` table for later review.

### FR-5: OpenClaw Heartbeat Exclusion Config

The OpenClaw plugin MUST support a configurable option to exclude heartbeat/keep-alive events from engram ingestion. Default: disabled (heartbeats NOT sent). Config key: `heartbeat.ingest` (boolean, default: false).

### FR-6: Vault Encryption Setup Helper

When vault encryption is disabled, the System page MUST show a setup helper with the command to generate a key (`openssl rand -hex 32`) and which env var to set (`ENGRAM_VAULT_KEY`).

## Non-Functional Requirements

### NFR-1: Performance
- Relation detection MUST complete within 500ms per observation (async, non-blocking to the observation store flow)
- Relation detection MUST NOT slow down observation creation (fire-and-forget pattern)

### NFR-2: Backward Compatibility
- Existing observations without relations MUST continue to work
- Graph page MUST gracefully show "no relations" instead of empty state when DB has no relations
- API field naming fix MUST NOT break existing MCP/plugin clients

## User Stories

### US1: See Knowledge Relationships (P1)
**As a** developer using AI agents, **I want** observations to automatically form a knowledge graph, **so that** I can see how bugs relate to fixes, how decisions evolve, and how concepts connect.

**Acceptance Criteria:**
- [ ] New observation triggers relation detection (async)
- [ ] Graph page shows non-zero nodes/edges after observations are created
- [ ] `find_related_observations` MCP tool returns results
- [ ] FalkorDB graph populated when configured

### US2: See Correct Dates (P1)
**As a** dashboard user, **I want** dates to display correctly or show "—" for missing dates, **so that** the UI is not broken with "Invalid Date" text.

**Acceptance Criteria:**
- [ ] Vault credential rows show "—" when created_at is null
- [ ] Analytics recent queries show "—" when last_used is null
- [ ] No "Invalid Date" text visible on any dashboard page

### US3: See Correct Analytics Stats (P1)
**As a** dashboard user, **I want** analytics numbers to reflect actual API data, **so that** I can trust the statistics displayed.

**Acceptance Criteria:**
- [ ] Total Searches shows correct non-zero value
- [ ] Retrieval Stats shows non-zero values matching API response

### US4: Control Heartbeat Ingestion (P2)
**As a** developer, **I want** to disable heartbeat event ingestion, **so that** my observation list is not polluted with keep-alive noise.

**Acceptance Criteria:**
- [ ] OpenClaw plugin config has `heartbeat.ingest` option (default: false)
- [ ] When false, heartbeat events are not sent to engram
- [ ] When true, heartbeats are sent (backward compatible)

### US5: Set Up Vault Encryption (P2)
**As a** developer, **I want** guidance on enabling vault encryption, **so that** my stored credentials are protected.

**Acceptance Criteria:**
- [ ] System page shows "Encryption disabled — run `openssl rand -hex 32` and set ENGRAM_VAULT_KEY" when vault not encrypted
- [ ] Helper text only visible when encryption is disabled

## Edge Cases

- Relation detection on first observation (no existing obs to compare) → no relations created, no error
- Relation detection when vector DB unavailable → log warning, skip relation detection, don't block obs creation
- Very high observation volume (>100 obs/min) → relation detection queue with backpressure, drop oldest
- OpenClaw plugin without heartbeat config → default false, no behavioral change for existing users

## Out of Scope

- Memory block population (MISSING-3 from bugs) — deferred to consolidation v2
- Chunk splitting improvements (MISSING-4) — deferred to collection v2
- Full relation backfill for existing 5000+ observations — separate batch job, not part of this iteration
- ENGRAM_AUTH_ADMIN_TOKEN rename — already done in v1.0.1

## Dependencies

- FalkorDB connected and running (already configured on production)
- pgvector for similarity search during relation detection (already available)
- Embedding service for new observation embeddings (already working)

## Success Criteria

- [ ] Zero "Invalid Date" text on any dashboard page
- [ ] Analytics stats match API response values
- [ ] New observations create relations (verified via Graph page or API)
- [ ] FalkorDB graph has non-zero nodes after new observations
- [ ] OpenClaw heartbeat config option works
- [ ] `go build ./cmd/worker/` passes
- [ ] `cd ui && npm run build` passes
- [ ] All existing tests pass

## Open Questions

None — all findings from production testing, root causes identified.
