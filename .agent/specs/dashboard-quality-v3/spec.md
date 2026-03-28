# Feature: Dashboard Quality v3

**Slug:** dashboard-quality-v3
**Created:** 2026-03-28
**Status:** Draft

## Overview

Fix 3 dashboard issues: pattern insights never pre-generated, search misses display broken,
sessions page is a useless flat list.

## Functional Requirements

### FR-1: Background Pattern Insight Generation
Add Task 18 to maintenance service cycle: query patterns with generic/null descriptions,
generate LLM insights in background (cap 10 per cycle), persist to pattern.description.
Users see cached summaries immediately instead of "Summary unavailable".

### FR-2: Fix Search Misses Display
Frontend `fetchSearchMisses` expects bare `SearchMiss[]` array but API returns
`{ miss_stats: [...] }` envelope. Field name mismatch: API returns `miss_count`,
UI expects `frequency`. Fix: unwrap envelope + map fields in `api.ts`.

### FR-3: Sessions Page Redesign
**FR-3a:** Filter empty sessions — add `min_prompts` param to `ListSDKSessions`,
default to 1 (hide 0-prompt sessions).
**FR-3b:** Wire date filters — pass `filterFrom`/`filterTo` to API (currently silently ignored).
**FR-3c:** Session detail view — click session → show observations, injections, outcome, summary.

## Success Criteria

- [ ] Patterns show LLM insight without clicking (background-generated)
- [ ] Search Misses section shows actual queries with miss counts
- [ ] Sessions page hides 0-prompt sessions by default
- [ ] Date filters work
- [ ] Clicking a session shows detail (observations, outcome)
