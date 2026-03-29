# Feature: Engram Dashboard v1.0

**Slug:** engram-dashboard
**Created:** 2026-03-18
**Status:** Mostly Implemented (5 gaps: bulk ops, tag cloud, per-token stats, CSP headers, auth-disabled badge)
**Author:** AI Agent (reviewed by user)
**ADR:** ADR-0003 (Dashboard Deferred — user-participatory)

## Overview

Production-grade embedded web dashboard for Engram, replacing the current PoC single-page timeline view. Provides human operators with visibility into what AI agents have memorized, tools to curate that knowledge, and system monitoring — all through a multi-page Vue 3 SPA served from the Go binary at the root URL.

## Context

Engram stores AI agent memories (observations, decisions, patterns, credentials) in PostgreSQL + pgvector. Today, 48 MCP tools and 91 HTTP endpoints serve AI agents — but humans have no practical way to browse, search, edit, or audit this knowledge without raw API calls. The existing PoC (`ui/` directory) provides a single-page timeline with stats, but lacks search, observation editing, vault access, and routing.

The backend is fully ready: all APIs documented via OpenAPI at `/api/docs`, authentication enforced (v0.9.0), and all data endpoints functional.

### Architectural Decisions (from spec review)

**AD-1: REST-first, MCP wraps REST.** REST HTTP endpoints are the primary implementation. MCP tools are thin wrappers that call REST endpoints internally. Dashboard calls REST directly. Any functionality currently only in MCP (vault, tags, sessions, maintenance) MUST first get REST endpoints, then MCP tools updated to delegate to those endpoints.

**AD-2: Token hierarchy.** `ENGRAM_API_TOKEN` becomes the master admin token (login + admin ops). Admin generates client API tokens via REST/UI. Each token has per-token usage statistics. Agents/plugins use client tokens. Dashboard authenticates master token → httpOnly cookie.

**AD-3: Iterative phasing.** Ship in 3 phases: CRITICAL → HIGH → MEDIUM.

### Existing PoC Inventory (to preserve/extend)

| Component | Status | Upgrade Needed |
|-----------|--------|----------------|
| `Header.vue` — SSE status, update check | Working | Add search bar, navigation |
| `StatsCards.vue` — count cards | Working | Add trend sparklines |
| `Timeline.vue` — activity feed | Working | Add pagination, click-to-detail |
| `Sidebar.vue` — health, graph metrics | Working | Move to dedicated pages |
| `ObservationCard.vue` — card with feedback | Working | Add edit/archive actions |
| `RelationGraph.vue` — vis-network graph | Working | Expand to full-page view |
| `ScoreBreakdown.vue` — score explainer | Working | Reuse in detail view |
| `FilterTabs.vue` — type/concept filter | Working | Extend with search integration |
| `useSSE` composable — real-time events | Working | Keep as-is |
| `useStats`, `useHealth`, etc. | Working | Keep, add new composables |
| `api.ts` — fetch utils with retry | Working | Extend for new endpoints |
| Tailwind + `claude` orange palette | Working | Add blue data + amber accent |
| vis-network, vis-data deps | Working | Use for knowledge graph page |

## Design System

### Visual Identity

| Aspect | Value | Source |
|--------|-------|--------|
| Mode | Dark mode OLED (primary), light mode (optional future) | UI/UX Pro Max |
| Brand color | `claude` orange `#EE7410` | Existing |
| Data accent | Blue `#3B82F6` | UI/UX Pro Max |
| Highlight accent | Amber `#F59E0B` | UI/UX Pro Max |
| Heading font | Fira Sans (400, 500, 600, 700) | UI/UX Pro Max |
| Data/code font | Fira Code (400, 500) | UI/UX Pro Max |
| Icons | FontAwesome 6 (already in deps) | Existing |
| Effects | Minimal glow, dark-to-light transitions, visible focus | UI/UX Pro Max |

### Layout

| Element | Spec |
|---------|------|
| Navigation | Fixed left sidebar (collapsible), 56px when collapsed, 240px expanded |
| Content area | Fluid, max-w-7xl centered, px-6 py-6 |
| Cards | `bg-slate-800/50 border border-slate-700/50 rounded-xl` |
| Modals/dialogs | Centered overlay with `backdrop-blur-sm` |
| Tables | Striped rows, sticky header, horizontal scroll on mobile |

## Functional Requirements

### FR-1: Multi-Page Router

The dashboard MUST have client-side routing with distinct pages. Navigation sidebar MUST show active page. Browser back/forward MUST work. Deep-linking MUST work (e.g., `/observations/42` opens observation detail).

**Pages:**
- `/` — Home (stats overview + recent activity)
- `/observations` — Observation browser with filters
- `/observations/:id` — Observation detail + edit
- `/search` — Semantic search + decision search
- `/vault` — Credential management
- `/logs` — Live log viewer
- `/graph` — Knowledge graph visualization
- `/patterns` — Pattern browser
- `/sessions` — Session browser
- `/analytics` — Search analytics, miss tracking, trends
- `/system` — System health, vector status, maintenance
- `/tokens` — API token management (FR-15)

### FR-2: Observation Browser

List observations with server-side pagination (`limit`, `offset`). Filter by: project (dropdown), type (tabs), concept (tags), scope (project/global). Sort by: date (default), importance score. Each row MUST be clickable to navigate to detail view.

**API:** `GET /api/observations?project=X&limit=20&offset=0`

### FR-3: Observation Detail + Edit

Display full observation: title, narrative, facts, concepts, files, scope, type, importance score breakdown, relations graph, creation date. Allow editing: title, narrative, type, scope, concepts (tag editor). Save model: explicit save button ("Save" / "Discard"), browser `beforeunload` confirmation on navigate-away with unsaved changes. Allow actions: archive, unarchive, delete (with confirmation), thumbs up/down feedback.

**APIs:** `GET /api/observations/{id}`, `PUT /api/observations/{id}`, `POST /api/observations/{id}/feedback`, `POST /api/observations/archive`

### FR-4: Semantic Search

Search bar in header (global, always visible) + dedicated search page with results. Hybrid search (BM25 + vector + reranker). Results show relevance score, observation type badge, highlighted matching text. Decision search mode: filter by type=decision, show `rejected[]` field.

**APIs:** `POST /api/context/search`, `POST /api/decisions/search`

### FR-5: Vault Credentials

List all credentials (name, scope, created date). Values hidden by default. "Reveal" button — shows decrypted value for 30 seconds with visible countdown ("Hides in 28s..."), then auto-hides (replaced with `•••••••`). "Copy" button next to revealed value. Re-reveal allowed (resets 30s timer). Delete credential with confirmation dialog.

**Backend prerequisite (AD-1):** Add REST endpoints for vault operations. MCP `store_credential`, `get_credential`, `list_credentials`, `delete_credential`, `vault_status` tools MUST delegate to these REST handlers.

**APIs (new):** `GET /api/vault/credentials`, `GET /api/vault/credentials/:name`, `DELETE /api/vault/credentials/:name`, `POST /api/vault/credentials`, `GET /api/vault/status`

### FR-6: Live Log Viewer

Real-time log stream via SSE. Filter by level (trace/debug/info/warn/error/fatal). Text search within logs. Pause/resume stream. Auto-scroll to bottom with "jump to latest" button.

**API:** `GET /api/logs?follow=true&level=warn`

### FR-7: Bulk Operations

Multi-select observations (checkboxes). Batch actions: archive, delete, change scope, add/remove tags. Confirmation dialog showing count and action summary.

**APIs:** `POST /api/observations/archive`, `POST /api/observations/bulk-status`

### FR-8: Tag Management

Tag editor on observation detail (add/remove concept tags). Tag cloud view on observation browser sidebar. Batch tagging: select observations + apply tag pattern.

**Backend prerequisite (AD-1):** Add REST endpoints for tag operations. MCP tools delegate to these.

**APIs (new):** `POST /api/observations/:id/tags`, `DELETE /api/observations/:id/tags/:tag`, `POST /api/observations/batch-tag`, `GET /api/observations/by-tag/:tag`

### FR-9: Search Miss Analytics

Show top unmatched queries (what agents search for and don't find). Table: query, project, frequency, last seen. This helps the user identify knowledge gaps to fill.

**API:** `POST /api/analytics/search-misses`

### FR-10: Knowledge Graph Visualization

Full-page interactive graph using vis-network (already in deps). Nodes = observations, edges = relations (causes, fixes, explains, contradicts). Click node to open observation detail. Filter by project, relation type. Show graph stats (node/edge counts, density).

**APIs:** `GET /api/observations/{id}/graph`, `GET /api/graph/stats`

### FR-11: Pattern Browser

List detected patterns (workflow, best_practice, anti_pattern) with stats. View pattern detail with insight. Actions: deprecate, delete, merge patterns.

**APIs:** `GET /api/patterns`, `GET /api/patterns/{id}/insight`, `DELETE /api/patterns/{id}`, `POST /api/patterns/{id}/deprecate`, `POST /api/patterns/merge`

### FR-12: Session Browser

List indexed Claude Code sessions with filters (workstation, project, date range). Full-text search across session transcripts.

**Backend prerequisite (AD-1):** Add REST endpoints for session operations. MCP tools delegate to these.

**APIs (new):** `GET /api/sessions?project=X&limit=20`, `GET /api/sessions/search?query=X&project=Y`

### FR-13: System Health & Maintenance

System health dashboard: component status (DB, vector store, embedding, graph, reranker). Vector metrics (query latency, cache hit rate, storage). Maintenance controls: trigger consolidation, run maintenance. Update manager: check for updates, apply.

**Backend prerequisite (AD-1):** Add REST endpoints for maintenance operations. MCP tools delegate to these.

**APIs (existing):** `GET /api/selfcheck`, `GET /api/vectors/health`, `GET /api/vector/metrics`, `GET /api/graph/stats`
**APIs (new):** `POST /api/maintenance/consolidation`, `POST /api/maintenance/run`, `GET /api/maintenance/stats`

### FR-14: Analytics Dashboard

Retrieval stats per project: search requests, context injections, stale excluded. Recent search queries list. Temporal trends: observations per day/week. Search performance: avg latency, cache hits.

**APIs (existing):** `GET /api/stats/retrieval`, `GET /api/search/recent`, `GET /api/search/analytics`
**APIs (new):** `GET /api/analytics/trends?project=X&period=daily|weekly|hourly`

### FR-15: Token Management (AD-2)

Master admin token (`ENGRAM_API_TOKEN`) is used only for dashboard login and admin operations. Admin can generate, list, revoke client API tokens through the dashboard. Each client token has:
- Name (human-readable label, e.g., "workstation-home", "ci-pipeline")
- Scope: read-only or read-write. Read-only blocks all POST/PUT/DELETE except this exhaustive allowlist of semantically-read-only endpoints: `POST /api/context/search`, `POST /api/context/inject`, `POST /api/decisions/search`, `POST /api/analytics/search-misses`. Enforced in middleware by method + path allowlist. Any new read-only POST endpoint MUST be added to this list explicitly.
- Created date, last used date
- Request count, error count
- Active/revoked status

Token management page shows all client tokens with usage statistics. Admin can create new tokens (generates random secure value, shown once — no recovery, lost token = create new + revoke old, standard API token pattern), revoke tokens, and view per-token analytics.

**APIs (new):**
- `POST /api/auth/login` — validate master token, return httpOnly session cookie
- `POST /api/auth/logout` — clear session cookie
- `GET /api/auth/tokens` — list client tokens with stats (admin only)
- `POST /api/auth/tokens` — create new client token (admin only)
- `DELETE /api/auth/tokens/:id` — revoke client token (admin only)
- `GET /api/auth/tokens/:id/stats` — per-token usage analytics

**Backend prerequisite:** Token storage table in PostgreSQL. Middleware updated to accept both master token (header) and session cookie (dashboard). Client tokens validated against DB instead of env var comparison.

### FR-16: Dashboard Authentication Flow (AD-2)

First visit → token input page (no sidebar, no navigation). User enters master admin token. Server validates via `POST /api/auth/login`, returns httpOnly cookie. Subsequent requests authenticated via cookie. Cookie expiry: 30 days (configurable). Logout clears cookie. Invalid/expired cookie → redirect to login page.

**APIs:** `POST /api/auth/login`, `POST /api/auth/logout`, `GET /api/auth/me` (returns current auth status)

## Non-Functional Requirements

### NFR-1: Performance
- Initial page load (SPA bundle) < 500KB gzipped
- Time to first meaningful paint < 1 second on localhost
- Search results rendered < 300ms after API response
- SSE reconnection < 5 seconds after disconnect

### NFR-2: Accessibility
- WCAG 2.1 AA compliance
- Keyboard navigation for all interactive elements
- Focus ring visible on all focusable elements
- Color contrast ratio >= 4.5:1 for text
- `prefers-reduced-motion` respected

### NFR-3: Security
- All API calls MUST include auth token (from login or cookie)
- Vault credential values MUST auto-hide after 30 seconds
- No credential values stored in browser localStorage/sessionStorage
- CSP headers set by Go server: `default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; font-src 'self'; frame-ancestors 'none'` (`unsafe-inline` for style required by Tailwind runtime + Vue scoped styles)
- Constitution Principle 2: secrets never leave the server (dashboard shows, never stores)

### NFR-4: Responsiveness
- Usable at 1024px minimum (desktop-first, developer tool)
- Sidebar collapses to icons at < 1280px
- Tables horizontally scroll on smaller screens
- Not required: mobile (< 768px) optimization

### NFR-5: Embeddability
- SPA built by `make dashboard`, output to `internal/worker/static/`
- Served via `go:embed` from Go binary — no separate server
- Works behind reverse proxy with any base path
- No external CDN dependencies (all assets bundled)

## User Stories

### US1: Browse Memories (P1)
**As a** developer using AI agents, **I want** to browse all stored observations with filters, **so that** I can see what my agents have learned about my projects.

**Acceptance Criteria:**
- [ ] Observation list loads with server-side pagination (20 per page)
- [ ] Filtering by project, type, and concept works without page reload
- [ ] Clicking an observation navigates to detail view with full content
- [ ] Empty state shown when no observations match filters

### US2: Search Knowledge (P1)
**As a** developer, **I want** to search stored knowledge by meaning (not just text), **so that** I can find relevant observations even when I don't remember exact words.

**Acceptance Criteria:**
- [ ] Search bar visible in header on every page
- [ ] Results show relevance score and observation type badge
- [ ] Decision search mode shows `rejected[]` alternatives
- [ ] Search with 0 results shows "no results" message

### US3: Edit Observation (P1)
**As a** developer, **I want** to correct or enhance observations, **so that** agents receive accurate information in future sessions.

**Acceptance Criteria:**
- [ ] Title, narrative, type, scope, and concepts are editable inline
- [ ] Save shows success feedback, reloads updated data
- [ ] Archive/Delete require confirmation dialog
- [ ] Changes are immediately reflected in the observation list

### US4: View Credentials (P2)
**As a** developer, **I want** to see which API keys/secrets my agents have detected, **so that** I can audit what's stored in the vault.

**Acceptance Criteria:**
- [ ] Credential list shows name, scope, created date (no values)
- [ ] "Reveal" button shows decrypted value after confirmation
- [ ] Value auto-hides after 30 seconds
- [ ] Delete removes credential with confirmation

### US5: Monitor System (P2)
**As a** developer, **I want** to see system health at a glance, **so that** I know if engram is working correctly for my agents.

**Acceptance Criteria:**
- [ ] Health page shows all component statuses (green/yellow/red)
- [ ] Live logs stream with level filtering
- [ ] Vector metrics show query latency and cache hit rate

### US6: Find Knowledge Gaps (P2)
**As a** developer, **I want** to see what my agents search for and don't find, **so that** I can create missing observations to improve agent effectiveness.

**Acceptance Criteria:**
- [ ] Search miss table shows query, project, frequency
- [ ] Sorted by frequency (most-missed first)
- [ ] Clicking a miss query pre-fills the search bar

### US7: Explore Knowledge Graph (P3)
**As a** developer, **I want** to visualize how observations relate to each other, **so that** I can understand the structure of stored knowledge.

**Acceptance Criteria:**
- [ ] Interactive graph renders with vis-network
- [ ] Nodes colored by observation type
- [ ] Click node opens observation detail
- [ ] Filter by project and relation type

### US8: Manage Patterns (P3)
**As a** developer, **I want** to review detected patterns and remove false positives, **so that** pattern detection improves over time.

**Acceptance Criteria:**
- [ ] Pattern list shows name, type, occurrence count
- [ ] Deprecate and delete actions available
- [ ] Merge combines two related patterns

### US9: Manage API Tokens (P2)
**As a** developer, **I want** to create scoped API tokens for my agents and see their usage stats, **so that** I can track which agents are active and revoke compromised tokens.

**Acceptance Criteria:**
- [ ] Token list shows name, scope, created date, last used, request count
- [ ] Create token generates random value, shown once with copy button
- [ ] Revoke disables token immediately (agents get 401)
- [ ] Per-token stats show request count and last used timestamp

### US10: Login to Dashboard (P1)
**As a** developer, **I want** to log in with my admin token, **so that** the dashboard is protected from unauthorized access.

**Acceptance Criteria:**
- [ ] First visit shows login page (no sidebar/navigation visible)
- [ ] Entering valid master token → redirects to home page
- [ ] Invalid token shows error message, stays on login page
- [ ] Session persists across browser restarts (30-day httpOnly cookie)
- [ ] Logout clears cookie and returns to login page

## Edge Cases

- Server unreachable: show offline banner (existing SSE reconnection), disable mutation actions
- Auth token expired/invalid: redirect to login page
- Auth disabled (`ENGRAM_AUTH_DISABLED=true`): skip login page, show home directly. Sidebar shows warning badge "Auth disabled". Token management page hidden. Vault page shows warning "Authentication disabled — vault accessible to anyone on the network".
- Observation not found (deleted while viewing): show "not found" with back button
- Empty database (fresh install): show onboarding state with "no observations yet" messaging
- Very long narratives (10K+ chars): truncate with "show more" toggle
- Concurrent edits: last-write-wins (no collaborative editing needed for single-user tool)
- Large observation count (100K+): server-side pagination, virtual scroll not required for MVP

## Out of Scope

- **Mobile optimization** (< 768px) — developer tool, desktop-first
- **Multi-user collaboration** — single admin with shared knowledge
- **OAuth / SSO / OIDC** — master token + generated client tokens only
- **Per-user RBAC** — single admin role; client tokens have scope (read/read-write) but no user identity
- **Observation creation from dashboard** — agents create observations, humans curate
- **Dark/light mode toggle** — dark mode only for MVP (light mode = future)
- **i18n / localization** — English only
- **Offline mode / PWA** — requires server connection
- **Custom dashboard layouts / widgets** — fixed layout for MVP

## Dependencies

- Backend: 91 HTTP endpoints functional (verified via OpenAPI at `/api/docs`)
- **Backend (new — AD-1):** REST endpoints for vault, tags, sessions, maintenance (MCP tools refactored to call these)
- **Backend (new — AD-2):** Auth subsystem: token table, session cookies, `POST /api/auth/*` endpoints, middleware update
- Build: `make dashboard` pipeline (Vue build → `internal/worker/static/`)
- Deps: vue-router (new), existing: Vue 3, Tailwind, vis-network, FontAwesome

## Success Criteria

- [ ] All CRITICAL features (FR-1 through FR-4, FR-16) functional and accessible via browser
- [ ] All HIGH features (FR-5 through FR-9, FR-15) functional
- [ ] Token hierarchy works: master login → create client token → agent uses client token → stats tracked
- [ ] `make dashboard && make worker` produces working binary with embedded SPA
- [ ] Observation CRUD cycle works: browse → detail → edit → save → verify change
- [ ] Search returns relevant results from 1000+ observations in < 300ms
- [ ] Dashboard loads in < 1 second on localhost
- [ ] No console errors during normal usage flow

## Resolved Decisions

1. **REST-first (AD-1):** REST endpoints are primary. MCP tools wrap REST, not the other way around. Dashboard calls REST directly. New REST endpoints needed for vault, tags, sessions, maintenance. **Rejected:** MCP client in Vue, MCP-first architecture.

2. **Token hierarchy (AD-2):** Master admin token for dashboard login + admin ops. Client tokens generated via REST/UI for agents/plugins. Per-token usage stats tracked. httpOnly cookie for dashboard sessions. **Rejected:** Single flat token, URL query param auth.

3. **Iterative phasing (AD-3):** Three phases:
   - **Phase 1 (CRITICAL):** Router + Auth flow + Observation CRUD + Search (FR-1, FR-2, FR-3, FR-4, FR-16)
   - **Phase 2 (HIGH):** Vault + Logs + Token management + Bulk ops + Tags + Analytics (FR-5, FR-6, FR-7, FR-8, FR-9, FR-15)
   - **Phase 3 (MEDIUM):** Knowledge graph + Patterns + Sessions + System health (FR-10, FR-11, FR-12, FR-13, FR-14)

## Clarifications

| # | Category | Question | Resolution | Date |
|---|----------|----------|------------|------|
| C1 | Integration | FR-12/13/14 referenced MCP tools as API | Updated to REST endpoints per AD-1 | 2026-03-19 |
| C2 | UX | Save model for observation editing | Explicit save button + beforeunload warning | 2026-03-19 |
| C3 | Security | Read-only scope enforcement | Method-based: block POST/PUT/DELETE except semantic search endpoints | 2026-03-19 |
| C4 | Edge Cases | Home page stats with 100K+ observations | Non-issue: server-side cache exists (cachedObsCounts, 1-min TTL) [VERIFIED] | 2026-03-19 |
| C5 | Functional | Token recovery when lost | No recovery: create new + revoke old (standard API token pattern) | 2026-03-19 |
| C6 | Security | Read-only POST allowlist | 4 endpoints: context/search, context/inject, decisions/search, analytics/search-misses | 2026-03-19 |
| C7 | UX | Vault reveal details | 30s countdown visible, copy button, re-reveal allowed (resets timer) | 2026-03-19 |
| C8 | Edge Cases | Auth disabled + dashboard | Skip login, show warning badges, hide token management | 2026-03-19 |
| C9 | Security | CSP policy | Strict self-only, unsafe-inline for style (Tailwind/Vue), frame-ancestors none | 2026-03-19 |

## Open Questions

None — all clarified.
