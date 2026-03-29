# Feature: Dashboard Bugfixes v2

**Slug:** dashboard-bugfixes-v2
**Created:** 2026-03-28
**Status:** Implemented
**Author:** AI Agent (reviewed by user)

## Overview

Fix 4 dashboard bugs reported via screenshots (2026-03-28). All affect the home page
Activity Timeline and make the dashboard appear broken despite 894 observations and 7 active sessions.

## Context

Dashboard runs as embedded SPA in engram worker. Vue 3 frontend, Go backend REST API.
Screenshots show: concept filter returns empty for ALL concepts, type filter is client-side
(broken pagination), "50 obs · 50 prompts" hardcoded instead of real counts, Summaries tab
shows "No items to display" despite 24h+ of active sessions.

## Functional Requirements

### FR-1: Concept Filter Must Return Matching Observations
When user selects a concept (e.g., "how-it-works", "architecture") from the dropdown,
observations tagged with that concept must appear. Currently: "No items to display" for ALL concepts.

### FR-2: Type Filter Must Be Server-Side
Type filter (bugfix, feature, decision, etc.) must pass `type` parameter to the API and
paginate server-side. Currently: filters 20 client-side records, showing wrong counts.

### FR-3: Real Observation and Prompt Counts
The "50 obs · 50 prompts" display must show actual counts from the API, not hardcoded values.

### FR-4: Summaries Tab Must Display Session Summaries
Summaries tab must show session summaries. If no summaries exist, investigate why the
summarization pipeline (stop.js → `/api/sessions/{id}/summarize`) produces no results.

## Non-Functional Requirements

### NFR-1: Visual Verification Required (Constitution #14)
Every fix must be verified via screenshot or Playwright snapshot before marking done.

## User Stories

### US1: Concept Filtering Works (P1)
**As a** user browsing the dashboard, **I want** to filter by concept tags,
**so that** I can find observations about specific topics.

**Acceptance Criteria:**
- [ ] Selecting "architecture" shows observations with that concept
- [ ] Selecting "All Concepts" shows all observations (no filter)
- [ ] Count updates correctly when concept is selected

### US2: Type Filtering Works (P1)
**As a** user filtering by type, **I want** server-side filtering with correct pagination,
**so that** I see all matching observations, not just filtered from page 1.

**Acceptance Criteria:**
- [ ] `handleGetObservations` accepts `type` query parameter
- [ ] Pagination counts reflect filtered total
- [ ] Client-side filter removed from ObservationsView

### US3: Real Counts Displayed (P1)
**As a** user, **I want** to see actual observation and prompt counts,
**so that** I know how much data is in the system.

**Acceptance Criteria:**
- [ ] Count display shows real numbers from API
- [ ] Updates when filters change

### US4: Summaries Visible (P2)
**As a** user checking session summaries, **I want** to see them on the Summaries tab,
**so that** I can review what happened in past sessions.

**Acceptance Criteria:**
- [ ] At least 1 summary visible after a session with summarization enabled
- [ ] If pipeline is broken, root cause identified and fixed

## Edge Cases

- Concept filter with no matching observations — show "No observations with this concept" message
- Type filter with 0 results — show empty state, correct count (0)
- API returns error — show error message, not empty state
- Summaries endpoint returns empty array — distinguish "no summaries generated" from "endpoint broken"

## Out of Scope

None.

## Dependencies

- Go backend: `internal/worker/handlers_data.go`
- Vue frontend: `ui/src/views/ObservationsView.vue`, `ui/src/utils/api.ts`
- Stop hook: `plugin/engram/hooks/stop.js` (summarization)
- Server: `internal/worker/handlers_data.go` (summaries endpoint)

## Success Criteria

- [ ] Concept filter returns observations for at least 3 different concepts
- [ ] Type filter shows correct paginated counts
- [ ] Real counts displayed (not 50/50)
- [ ] At least 1 summary visible after session end
- [ ] All fixes verified via screenshot (Constitution #14)
