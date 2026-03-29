# Feature: Technical Debt Sprint

**Slug:** tech-debt-sprint
**Created:** 2026-03-23
**Status:** Implemented
**Author:** AI Agent (reviewed by user)

## Overview

Resolve all actionable technical debt items accumulated during engram v1.3–v1.5 development. Six items total: three require code changes, one requires server action, two can be deferred or dropped.

## Context

TECHNICAL_DEBT.md contains 6 items from sessions spanning 2026-03-19 to 2026-03-23. The highest-impact item (Sessions view) is a core UX bug — the dashboard page is effectively broken. Others range from orphaned DB data to graceful stubs that are spec-compliant.

## Functional Requirements

### FR-1: SDK Session Listing Endpoint
The system must provide an API endpoint that returns a paginated list of SDK sessions (from `sdk_sessions` table), filterable by project. The endpoint must return session metadata: ID, claude_session_id, project, status, started_at, completed_at, prompt_counter, user_prompt. Total count must be included for pagination.

### FR-2: Dashboard Sessions View
The Sessions page must display actual Claude Code sessions, not indexed transcripts. The project filter must work correctly. Sessions must be sorted by recency (newest first). The existing transcript search feature must remain accessible.

### FR-3: Orphaned Credential Cleanup
The system must provide a way to purge credentials encrypted with a key that no longer matches the current vault key fingerprint. The operation must be idempotent and safe (no data loss beyond already-unrecoverable credentials).

### FR-4: Memory Blocks Table Resolution
The unused `memory_blocks` table must either be populated by the consolidation scheduler or dropped via migration. No empty tables should remain in the schema.

### FR-5: Post-Deploy Relevance Verification
After deploying v1.5.1+, the retrospective evaluation skill must be executed to verify that >50% of injected observations are relevant to the current project context.

### FR-6: Info Sidebar in Navigation Panel
The info sidebar (System Health, Memory Contents, Retrieval Stats, Worker Info) must render inside the left navigation panel below the nav links, not as a separate collapsible column between the nav and timeline. Currently it renders as a collapsed `w-12` strip via `components/Sidebar.vue` positioned between `AppSidebar` and the timeline content area — invisible and inaccessible. The stats content must be moved into the bottom portion of `AppSidebar.vue` (below nav items, above Connected/Logout), always visible when nav sidebar is expanded.

## Non-Functional Requirements

### NFR-1: Performance
Session listing endpoint must respond in <500ms for up to 10,000 sessions.

### NFR-2: Backward Compatibility
All API changes must be additive. No existing endpoints may change behavior.

## User Stories

### US1: View My Sessions (P1)
**As a** user browsing the dashboard, **I want** to see my Claude Code sessions listed when I open the Sessions page, **so that** I can review session history and find past work.

**Acceptance Criteria:**
- [ ] Sessions page shows SDK sessions from `sdk_sessions` table
- [ ] Project filter narrows results to selected project
- [ ] Sessions are sorted newest-first
- [ ] Each session shows: project name, start time, prompt count
- [ ] "No sessions found" only appears when genuinely no sessions exist

### US2: See System Stats in Sidebar (P1)
**As a** user on the Home page, **I want** to see System Health, Memory Contents, Retrieval Stats, and Worker Info in the left navigation panel, **so that** I have a quick overview without scrolling.

**Acceptance Criteria:**
- [ ] Info sidebar content renders below nav links in AppSidebar
- [ ] Content visible when nav sidebar is expanded, hidden when collapsed
- [ ] Old Sidebar.vue component removed from HomeView layout
- [ ] No invisible collapsed strip between nav and timeline

### US3: Clean Up Orphaned Vault Entries (P2)
**As an** administrator, **I want** orphaned credentials (encrypted with a lost key) to be purged, **so that** vault_status reflects accurate credential count.

**Acceptance Criteria:**
- [ ] Credentials with non-matching key fingerprint are identified
- [ ] Purge operation removes only orphaned credentials
- [ ] vault_status shows correct count after cleanup

### US4: Verify Memory Quality (P2)
**As a** system operator, **I want** to verify that composite scoring improvements produce >50% relevant observations, **so that** the scoring changes are validated in production.

**Acceptance Criteria:**
- [ ] Retrospective eval skill executes successfully
- [ ] Results show relevance percentage
- [ ] Verdict is documented

## Edge Cases

- Session listing with 0 sessions: show "No sessions found" message
- Session listing with deleted project: sessions with non-existent project path still appear (project is a string, not FK)
- Vault cleanup with 0 orphaned credentials: no-op, success response
- Vault cleanup when no vault key is configured: error with clear message
- Memory blocks table already dropped by manual intervention: migration is idempotent (IF EXISTS)

## Out of Scope

- Implementing MCP resources/prompts (D4) — spec-compliant empty responses, no user impact
- Hot-reload config (D6) — Docker restart policy is adequate
- Historical session transcript indexing — only new sessions are indexed by sync-sessions hook
- Session detail view with full transcript — separate future feature

## Dependencies

- v1.5.1+ deployed on server (migration 046 fix required for FR-5)
- Current vault key configured (`ENGRAM_VAULT_KEY` or auto-generated in `/data/`)

## Success Criteria

- [ ] Sessions page shows real sessions (not "No sessions found")
- [ ] Info sidebar (System Health, Memory Contents, etc.) visible in nav panel
- [ ] vault_status shows 0 orphaned credentials
- [ ] memory_blocks table resolved (dropped)
- [ ] Retrospective eval executed with documented results
- [ ] TECHNICAL_DEBT.md items marked resolved

## Future FR: Observation Status Lifecycle
Observations need a `status` field (active/resolved/conditional) so that temporary facts (e.g., "Codex account blocked") can be marked resolved without suppression, and automatically reactivated if the condition recurs. Currently the only options are suppress (hidden forever) or rate_memory (soft penalty). Neither supports "resolved but re-openable". This is a future feature — not in scope for this sprint.

## Open Questions

None — all items are well-defined in TECHNICAL_DEBT.md with root cause analysis and fix plans.

## Clarifications

| # | Category | Question | Resolution | Date |
|---|----------|----------|------------|------|
| C1 | UX | Should sessions show observation_count (requires JOIN) or prompt_counter (on table)? | Show prompt_counter — already on sdk_sessions. Observation count deferred to session detail view. | 2026-03-23 |
| C2 | Functional | Should transcript search bar remain in SessionsView? | Yes — sessions were indexed previously, search works. The bug is list view using indexed sessions instead of SDK sessions, not missing data. Keep search bar for transcript search alongside SDK session list. | 2026-03-23 |
| C3 | UX | Should modal triggers (expand icons) be preserved when moving sidebar into AppSidebar? | Yes — keep modal triggers (fa-expand, fa-chart-line, fa-trophy). They open overlay modals, no layout impact. | 2026-03-23 |
