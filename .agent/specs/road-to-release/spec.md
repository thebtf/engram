# Feature: Road to Release — Engram v1.0.0

**Slug:** road-to-release
**Created:** 2026-03-17
**Status:** Implemented
**Author:** AI Agent (reviewed by user)
**Constitution:** `.agent/specs/constitution.md` v1.0.0

## Overview

Close all HIGH and MEDIUM release blockers identified in the production readiness report
(`.agent/reports/production-readiness-2026-03-17.md`) to prepare engram for open-source release
as v1.0.0. Core infrastructure is production-grade — this release focuses on the user-facing
layer: dashboard, documentation, auth hardening, and completing partially-shipped features.

## Context

Engram has been in internal production use for 6+ weeks. 11 components are graded Production,
8 are Beta, 3 are Alpha, 1 is PoC (dashboard). The core memory pipeline (search, storage,
embedding, MCP, scoring, consolidation) is battle-tested. What's missing is the layer that
makes engram usable by someone who isn't the author: visual UI, API docs, secure defaults,
and feature completeness for recently shipped capabilities.

The project constitution (10 principles) governs all implementation decisions.

## Functional Requirements

### FR-1: Web Dashboard
The system MUST provide a browser-based UI served at the server root (`/`) that allows users
to browse, search, and inspect observations, credentials, sessions, and analytics without
requiring MCP tool access.

### FR-2: Observation Browser
The dashboard MUST display a paginated list of observations with filtering by type, project,
scope, date range, and concept tags. Each observation MUST be viewable in detail with full
narrative, metadata, related observations, and importance score.

### FR-3: Search Interface
The dashboard MUST provide a search bar that queries the hybrid search engine (same as MCP
`search` tool) and displays ranked results with relevance scores and highlighting.

### FR-4: Vault Credential Viewer
The dashboard MUST list stored credentials (name, scope, creation date) without displaying
decrypted values. A "reveal" action MUST require explicit user confirmation and MUST be
gated behind authentication.

### FR-5: Analytics View
The dashboard MUST display: observation count by type/project, search miss statistics
(from `search_misses` table), recent search queries, and temporal activity trends.

### FR-6: API Documentation
The server MUST serve an OpenAPI 3.0 specification at `/api/docs` or `/api/openapi.json`
that documents all HTTP endpoints with request/response schemas. A human-readable UI
(Swagger UI or equivalent) MUST be accessible at `/api/docs`.

### FR-7: Auth Hardening
When `ENGRAM_API_TOKEN` is not set, the server MUST refuse to start unless an explicit
opt-out flag is provided (`ENGRAM_AUTH_DISABLED=true`). The current behavior (warning only)
is insufficient for a public release.

### FR-8: Structured Decision Schema
Observations of type `decision` MUST support a structured `rejected` field — an array of
alternatives that were considered and dismissed. This enables reliable contradiction detection
instead of fragile narrative text parsing.

### FR-9: Redaction Hash Mapping (FR3 from vault integration spec)
`RedactSecrets()` MUST replace secrets with `[REDACTED:hash]` where `hash` is the first 8
characters of the SHA-256 hash of the secret value. This enables reverse lookup from redacted
text to the corresponding vault entry via `get_credential(name="auto:{hash}")`.

### FR-10: Dependency Cleanup
The `go-tree-sitter` dependency MUST either be pinned to a tagged release (if one exists)
or documented in TECHNICAL_DEBT.md with justification for the pseudo-version. No untagged
commit references in go.mod for v1.0.0.

## Non-Functional Requirements

### NFR-1: Performance
- Dashboard initial load: < 2 seconds on localhost
- Search results: < 500ms (same as current API performance)
- Dashboard MUST NOT degrade server API performance

### NFR-2: Security
- Dashboard MUST be served behind the same token authentication as API endpoints
- No credentials or secrets exposed in dashboard HTML/JS source
- CSP headers MUST be verified to work with the Vue app
- Constitution Principle 2 (Secrets never leave the server) applies to all dashboard views

### NFR-3: Compatibility
- Dashboard MUST work in modern browsers (Chrome 120+, Firefox 120+, Safari 17+)
- API docs MUST be accessible without authentication (read-only spec)
- OpenAPI spec MUST be machine-readable for SDK generation

### NFR-4: Maintainability
- Dashboard MUST be a standard Vue 3 SPA built by `make dashboard`
- No additional runtime dependencies on the server (embedded static files)
- Dashboard code in `ui/` directory, built and embedded at compile time

## User Stories

### US1: Browse Observations (P1)
**As a** developer using engram, **I want** to see all stored observations in a web UI,
**so that** I can verify what my agent has learned and correct inaccuracies.

**Acceptance Criteria:**
- [ ] Paginated list with 20 items per page
- [ ] Filter by type (decision, bugfix, feature, etc.)
- [ ] Filter by project
- [ ] Sort by date or importance score
- [ ] Click to view full observation detail

### US2: Search from Dashboard (P1)
**As a** developer, **I want** to search observations from the browser,
**so that** I don't need an MCP client to find stored knowledge.

**Acceptance Criteria:**
- [ ] Search bar on dashboard home
- [ ] Results show title, type, relevance score, snippet
- [ ] Results link to full observation view

### US3: View API Documentation (P1)
**As a** developer integrating with engram, **I want** to browse API docs in my browser,
**so that** I can build clients without reading Go source code.

**Acceptance Criteria:**
- [ ] OpenAPI 3.0 spec served at `/api/openapi.json`
- [ ] Swagger UI at `/api/docs`
- [ ] All 94 HTTP routes documented with request/response schemas

### US4: Secure Default Setup (P1)
**As a** new user deploying engram, **I want** the server to refuse to start without auth,
**so that** I don't accidentally expose my memory store to the network.

**Acceptance Criteria:**
- [ ] Server exits with error if `ENGRAM_API_TOKEN` unset and `ENGRAM_AUTH_DISABLED` is not `true`
- [ ] Error message explains how to set the token
- [ ] Docker documentation updated with token setup

### US5: Manage Vault Credentials (P2)
**As a** developer, **I want** to see which credentials are stored in the vault,
**so that** I can audit auto-detected secrets and manually-stored entries.

**Acceptance Criteria:**
- [ ] List view: name, scope, created date
- [ ] No decrypted values visible by default
- [ ] "Reveal" button with confirmation dialog
- [ ] Delete action with confirmation

### US6: View Analytics (P2)
**As a** developer, **I want** to see usage analytics,
**so that** I can understand what engram remembers and where retrieval fails.

**Acceptance Criteria:**
- [ ] Observation count by type (bar chart)
- [ ] Search miss frequency (top 10 unmatched queries)
- [ ] Activity timeline (observations per day)

### US7: Record Decision Rejections (P2)
**As an** AI agent, **I want** to store what alternatives were rejected in a decision,
**so that** contradiction detection works reliably.

**Acceptance Criteria:**
- [ ] `store_memory` MCP tool accepts optional `rejected` array
- [ ] `decisions` search returns `rejected` field
- [ ] Contradiction detection uses structured `rejected` instead of narrative parsing

### US8: Trace Redacted Secrets (P3)
**As a** developer reviewing redacted transcripts, **I want** to see which vault entry
a `[REDACTED:hash]` maps to, **so that** I can retrieve the original if needed.

**Acceptance Criteria:**
- [ ] `RedactSecrets()` outputs `[REDACTED:a1b2c3d4]` format
- [ ] Hash matches vault entry name `auto:a1b2c3d4`
- [ ] `get_credential(name="auto:a1b2c3d4")` retrieves the secret

## Edge Cases

- Dashboard accessed without auth token: MUST return 401, not a broken page
- Search with empty query: MUST show recent observations, not an error
- Vault with no credentials: MUST show empty state, not crash
- Server started with `ENGRAM_AUTH_DISABLED=true`: MUST log prominent warning every 60 seconds
- OpenAPI spec for endpoints with no typed request body: MUST document as `{}` not omit
- `rejected` field on non-decision observations: MUST be silently ignored

## Out of Scope

- User management / multi-user auth (single-token model is sufficient for v1.0.0)
- Real-time dashboard updates via WebSocket (polling is sufficient)
- Dashboard dark mode / theming (functional > aesthetic for v1.0.0)
- Mobile-responsive dashboard (desktop-only is acceptable)
- Automated self-tuning from search miss data (analytics display only)
- Plugin marketplace UI (CLI-only is sufficient)
- Migration tools from other memory systems

## Dependencies

- Vue 3 + Vite (already scaffolded in Makefile `dashboard` target)
- OpenAPI spec generation library (Go: swaggo/swag, or manual spec)
- Swagger UI static bundle (CDN or embedded)
- Existing server API endpoints (all exist, dashboard is a consumer)

## Success Criteria

- [ ] Dashboard replaces placeholder at `/` with functional observation browser + search
- [ ] `curl /api/openapi.json` returns valid OpenAPI 3.0 spec
- [ ] Fresh Docker deploy without `ENGRAM_API_TOKEN` fails with clear error message
- [ ] `store_memory` with `rejected` array stores and returns structured data
- [ ] `RedactSecrets("sk-abc123...")` outputs `[REDACTED:a1b2c3d4]` matching vault entry
- [ ] `go mod tidy` shows no pseudo-versioned dependencies (or documented exception)
- [ ] All components in readiness matrix graded Beta or higher (no Alpha, no PoC)

## Resolved Questions

- **Dashboard tech:** Embedded Vue SPA (`make dashboard` → `internal/worker/static/`).
  **Decision:** Last phase — user participates directly. Do NOT implement autonomously.
- **OpenAPI generation:** swaggo annotations — auto-sync with Go handler code.
  **Decision:** Accepted.
- **Auth hardening:** Hard fail — server exits without `ENGRAM_API_TOKEN` unless
  `ENGRAM_AUTH_DISABLED=true`. `/health` and `/api/version` remain public.
  **Decision:** Accepted.
