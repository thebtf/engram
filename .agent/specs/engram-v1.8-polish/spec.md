# Feature: Engram v1.8 Dashboard & Knowledge Quality Polish

**Slug:** engram-v1.8-polish
**Created:** 2026-03-26
**Status:** Draft
**Author:** AI Agent (reviewed by user)

## Overview

Four improvements to make the engram dashboard genuinely useful for browsing and managing knowledge, rather than a collection of populated-but-useless pages. Patterns get real insights, memories become browsable, observations get lifecycle management, and the system learns from its own mistakes at commit time.

## Context

Engram v1.7.4 is stable with 48 MCP tools, 686 observations, 345 patterns, and full self-learning pipeline. However, user testing revealed that several dashboard features are "decoration" — they show data but provide no actionable value. The Patterns page shows 345 entries with identical generic descriptions. Memories created via `store_memory` are invisible in the UI. Stale observations inject forever with no way to mark them resolved. Agents repeat mistakes that engram already knows about because knowledge is injected at prompt time, not at commit time.

## Functional Requirements

### FR-1: Pattern Insight — LLM Summary + Source Observations
**Current:** Insight button shows "I've encountered this pattern 1816 times. This is a recognized pattern in the codebase." — zero useful content.
**Required:**
- API endpoint `GET /api/patterns/{id}/observations` returns observations constituting the pattern (from `observation_ids` field)
- On Insight click: send source observation titles + narratives to LLM, generate 2-3 sentence summary explaining WHAT the pattern is, WHY it matters, WHEN to apply it
- Frontend: LLM-generated summary at top, collapsible list of source observations below (title, type badge, link to detail)
- Cache LLM summary in pattern `description` field — regenerate only when source observations change (hash of observation_ids)

### FR-2: Pattern Quality Cleanup
**Current:** 345 patterns with confidence 0.5-0.65, many are orphans (source observations deleted by migrations).
**Required:**
- Batch recalculate confidence for all patterns using existing `updateConfidence()` formula
- Detect and purge orphan patterns (observation_ids reference non-existent observations)
- Add bulk action in dashboard: "Archive patterns with confidence < threshold" (user-selectable threshold, default 0.6)

### FR-3: Dashboard Memories View
**Current:** `store_memory` records are observations with `memory_type` field, mixed into general list with no filter.
**Required:**
- Add `memory_type` filter to `GET /api/observations` (alongside existing `type` filter)
- Add dedicated "Memories" section or tab in ObservationsView — filtered to observations where `memory_type IS NOT NULL AND memory_type != ''`
- Each memory card shows: title, content preview, tags, scope (project/global), importance score, created date
- Actions: edit (title, content, tags), delete, change scope

### FR-4: Observation Status Lifecycle
**Current:** Observations are either active (injected into context) or suppressed (hidden forever). No middle ground for temporary facts.
**Required:**
- Add `status` column to observations: `active` (default), `resolved` — two states only (no `conditional`, per multi-model consensus C1)
- Add `status_reason` column (TEXT, nullable) — records why observation was resolved (per consensus C2)
- `resolved` observations completely excluded from context injection (`WHERE status = 'active'`), still searchable via `search` and dashboard
- Extend existing `edit_observation` MCP tool with `status` and `status_reason` fields — no new tools (per consensus C5, reduces tool payload)
- No resolution cascade — each observation resolved independently (per consensus C3)
- No auto-resolution — existing retrieval count decay (FR-19) handles staleness (per consensus C4)
- Dashboard: resolved observations shown with strikethrough/dimmed visual indicator, bulk resolve action
- Add index on `status` column for injection query performance

## Non-Functional Requirements

### NFR-1: Performance
- Pattern insight LLM call < 5s (cached after first generation)
- Orphan pattern detection < 10s for 345 patterns
- Memory filter adds < 50ms to observation list query

### NFR-2: Backward Compatibility
- New `status` column defaults to `active` — zero impact on existing observations
- `memory_type` filter is optional — existing API consumers unaffected
- Pattern description update preserves existing data (only overwrites "Automatically detected...")

### NFR-3: No New Dependencies
- LLM summary uses existing `learning.LLMClient` (same as extraction)
- No new tables — uses existing columns or adds migration columns

## User Stories

### US1: Pattern Insight (P1)
**As a** developer browsing patterns, **I want** to see what a pattern actually means, **so that** I can decide if it's relevant to my current work.

**Acceptance Criteria:**
- [ ] Clicking Insight shows LLM-generated summary (not template text)
- [ ] Source observations listed below summary with clickable links
- [ ] Summary regenerates when source observations change
- [ ] Loading state shown during LLM generation

### US2: Browse Memories (P1)
**As a** user who stores memories via MCP tools, **I want** to see all my stored memories in the dashboard, **so that** I can review, edit, or delete them.

**Acceptance Criteria:**
- [ ] Memories tab/filter shows only store_memory observations
- [ ] Each memory shows title, content, tags, scope, importance
- [ ] Can edit title and content inline
- [ ] Can delete memory from UI

### US3: Resolve Stale Observation (P2)
**As a** user, **I want** to mark an observation as resolved when it's no longer relevant, **so that** it stops being injected into my context but I can still find it later.

**Acceptance Criteria:**
- [ ] "Resolve" action available on observation cards
- [ ] Resolved observations excluded from context injection
- [ ] Resolved observations still appear in search results with visual indicator
- [ ] Can reopen resolved observation if situation recurs

### US4: Clean Up Low-Quality Patterns (P2)
**As a** user, **I want** to remove patterns that are orphaned or low-confidence, **so that** the Patterns page shows only meaningful data.

**Acceptance Criteria:**
- [ ] Orphan patterns (missing source observations) identified and removable
- [ ] Bulk archive for low-confidence patterns with configurable threshold
- [ ] Confidence recalculated after orphan removal

## Edge Cases

- Pattern has 0 valid source observations after orphan check → auto-archive instead of showing empty insight
- LLM unavailable when insight requested → show source observations only, with "Summary unavailable" note
- Observation resolved then re-created (new observation) → old resolved one stays resolved, new one is active
- `memory_type` filter combined with `type` filter → both applied (AND logic)
- Pattern description cache invalidation: compare hash of sorted observation_ids, regenerate only on change

## Out of Scope

- Pre-commit quality guardrails (needs separate design decision: linter vs engram vs both)
- Config hot-reload (large refactor, Docker restart works fine)
- LLM model re-benchmark (operational task, not code change)
- MCP resources/prompts implementation (no functional impact)
- Full E2E test suite for dashboard (future infrastructure)

## Dependencies

- Existing `learning.LLMClient` for pattern insight LLM calls
- Existing `observation_ids` field in patterns table for source observation lookup
- Existing `memory_type` field in observations for memories filter

## Success Criteria

- [ ] Patterns page Insight shows meaningful content (not template text)
- [ ] Memories browsable and manageable from dashboard
- [ ] Stale observations can be resolved without permanent suppression
- [ ] Orphan/low-quality patterns cleaned up
- [ ] All changes backward compatible — existing MCP tools and API consumers unaffected

## Open Questions

None — all design decisions resolved.

## Clarifications

| # | Category | Question | Resolution | Date |
|---|----------|----------|------------|------|
| C1 | Functional | Include `conditional` status or just `active`/`resolved`? | NO — `active`/`resolved` only. YAGNI. Add `conditional` later if real use case emerges. | 2026-03-26 |
| C2 | Domain/Data | Where to store resolution reason? | New `status_reason` TEXT column (nullable). Gemini recommendation adopted. | 2026-03-26 |
| C3 | Functional | Should resolution cascade to related observations? | NO — each observation independent. Graph relations are soft links, not ownership. | 2026-03-26 |
| C4 | Functional | Auto-resolution for old low-retrieval observations? | NO — FR-19 retrieval count decay already handles staleness. Auto-resolution is destructive without user intent. | 2026-03-26 |
| C5 | Integration | New MCP tools vs extend edit_observation? | Extend `edit_observation` with `status` + `status_reason` fields. Zero tool count increase, reduces LLM context payload. | 2026-03-26 |

**Consensus method:** Gemini (architect, for) + Claude/Opus (synthesizer, neutral). Codex failed (sandbox policy block).
