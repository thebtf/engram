# Technical Debt

## ~~2026-03-25: Bulk Import Creates Phantom Sessions (403 openclaw sessions)~~ PARTIALLY RESOLVED v1.7.2 (PR #65)
**What:** `handleBulkImport` created a new synthetic session for every call. 403+ phantom `bulk-import-*` sessions.
**Fix (PR #65):** Steps 1-4 done — `session_id` param added, openclaw tools pass `ctx.sessionId`.
**Remaining:** Step 5 — cleanup existing 403 phantom sessions via migration or maintenance DELETE.
**Context:** `internal/worker/handlers_import_export.go`, `internal/db/gorm/migrations.go`

## 2026-03-25: Cleanup Existing Phantom bulk-import-* Sessions
**What:** 403+ sessions with `claude_session_id LIKE 'bulk-import-%'` exist from before PR #65 fix. New phantom sessions no longer created, but old ones pollute Sessions page.
**Fix plan:** Add migration or maintenance SQL: `DELETE FROM sdk_sessions WHERE claude_session_id LIKE 'bulk-import-%' AND prompt_counter = 0`
**Impact:** Cleaner Sessions page. ~11% of sessions are phantom.
**Context:** `internal/db/gorm/migrations.go` — add cleanup migration

## ~~2026-03-23: Sessions View Shows Indexed Transcripts, Not SDK Sessions~~ RESOLVED v1.5.2 (PR #42)
**What:** Dashboard "Sessions" page queries `sessions-index` API (indexed transcripts via `POST /api/sessions/index`) but users expect to see their actual Claude Code sessions (stored in `sdk_sessions` table via session-start hook).
**Why deferred:** Requires new REST endpoint to list SDK sessions with pagination/project filter, plus frontend refactor of SessionsView to use the new endpoint instead of `fetchIndexedSessions`. The `sync-sessions.js` hook (added in v1.5.0) indexes new sessions automatically, but historical sessions remain unindexed.
**Impact:** "No sessions found" on Sessions page even when sessions exist. UX confusion — project filter dropdown works (populated from observations) but session list is empty.
**Root cause:**
- `GET /api/sessions` requires `claudeSessionId` param — lookup by ID, not listing
- `GET /api/sessions-index` returns indexed transcripts (separate table), not SDK sessions
- `ui/src/composables/useSessions.ts` calls `fetchIndexedSessions` → empty result
- SDK sessions exist in `sdk_sessions` table but have no list endpoint
**Fix plan:**
1. Add `GET /api/sessions/list?project=X&limit=N&offset=M` endpoint in `handlers_sessions.go` querying `sdk_sessions` table
2. Add `ListSDKSessions(ctx, project, limit, offset)` method to `SessionStore`
3. Update `useSessions.ts` to call new endpoint
4. Keep `sessions-index` as secondary "transcript search" feature
**Context:** `internal/worker/handlers_sessions.go:416`, `ui/src/composables/useSessions.ts`, `internal/db/gorm/session_store.go`

## 2026-03-23: T027 Post-Deploy Verification Pending
**What:** Retrospective eval skill (T027) needs manual execution after v1.5.1 deploy to verify >50% observation relevance.
**Why deferred:** Requires server restart with v1.5.1 image (migration 046 fix), then manual `/retrospective-eval` run.
**Impact:** No automated verification that composite scoring + diversity penalty actually improve relevance. Currently based on qualitative assessment only.
**Context:** `.agent/specs/composite-relevance-scoring/tasks.md` T027

## 2026-03-23: Vault Credentials Encrypted with Lost Key
**What:** 15 credentials in DB encrypted with auto-generated AES-256-GCM key that was stored in Docker ephemeral filesystem (`~/.engram/vault.key`). Container was recreated, key lost.
**Why deferred:** Credentials cannot be recovered — AES-256-GCM has no backdoor. Users need to re-create credentials with current key.
**Impact:** `vault_status` shows credentials exist but `get_credential` fails for old entries. Fixed in v1.4.0: auto-generate now writes to `/data/` (persistent volume).
**Context:** `internal/crypto/vault.go`, migration history

## 2026-03-23: Patterns System — 16k+ Low-Value Patterns
**What:** Patterns page shows 16,264 patterns. Most are noise from pre-v1.3.4 SDK extraction (before whitelist mode). "Insight" button shows generic text ("I've encountered this pattern N times across M projects") with zero actionable information.
**Root cause analysis:**
1. **Confidence = 0.5 for 99% of patterns** — `NewPattern()` sets `Confidence: 0.5` (hardcoded initial). `updateConfidence()` only runs during `AddOccurrence()`, but most patterns were created during bulk backfill/extraction where AddOccurrence was never called. Formula: `freqConfidence = 0.3 + 0.5*(min(freq,10)/10)` + project bonus. But this runs per-occurrence, not on creation.
2. **No confidence recalculation** — there is no batch recalculation path. Patterns created at 0.5 confidence stay at 0.5 forever unless new observations match them. Migration 042 purged `frequency < 5` but didn't recalculate confidence.
3. **Description is generic** — `promoteCandidate()` sets description to "Automatically detected pattern from recurring observations" for all patterns. No LLM summarization, no source evidence.
4. **Insight text is template** — Dashboard shows `{frequency} times across {projects} projects` — no examples, no observation links, no recommendations.
5. **No purge for garbage patterns** — 16k patterns with frequency ≥ 5 but 0.5 confidence indicates they were mass-created from garbage observations (since cleaned up in migrations 040-043) but the patterns themselves were never cleaned.
**Impact:** Patterns page unusable. 16k entries with no way to evaluate quality. Agents can't use patterns for decision-making.
**Fix plan (future sprint):**
1. Batch recalculate confidence for all patterns using `updateConfidence()` logic
2. Purge patterns whose source observations no longer exist (orphan patterns)
3. Add LLM-generated description when confidence > 0.7 (replace generic text)
4. Show source observations in "Insight" view (links to observation detail)
5. Add "archive all with confidence < 0.6" bulk action
**Context:** `pkg/models/pattern.go:165` (hardcoded 0.5), `internal/pattern/detector.go:257` (generic description), `internal/pattern/quality.go` (scoring formula), `ui/src/views/PatternsView.vue`

## ~~2026-03-23: ScoreBreakdown Modal — API Response Mismatch~~ RESOLVED v1.6.1 (PR #44)
**What:** Clicking score badge (e.g., "1.31") in ObservationCard triggers ScoreBreakdown modal but shows blank/error. API returns `{id, components, config}` but Vue component expects `{observation: {title, type}, scoring: {final_score, type_weight, recency_decay, ...}, explanation: {...}}`.
**Root cause:** `handleExplainScore` in `handlers_scoring.go` returns raw `scoreCalculator.CalculateComponents()` output. Frontend `ScoreBreakdown.vue` expects a different shape with nested `observation`, `scoring`, `explanation` objects.
**Impact:** Score breakdown feature broken — modal shows loading then nothing.
**Fix plan:** Either reshape API response to match frontend, or update frontend to match API.
**Context:** `internal/worker/handlers_scoring.go:383`, `ui/src/components/ScoreBreakdown.vue:106-196`, `ui/src/utils/api.ts:205`

## 2026-03-23: Observation Status Lifecycle (Future FR)
**What:** Observations lack a `status` field (active/resolved/conditional). Temporary facts (e.g., "Codex account blocked") can only be suppressed (hidden forever) or downvoted (soft penalty). Neither supports "resolved but re-openable if condition recurs".
**Impact:** Stale observations continue to inject into context. Users must manually suppress and re-create when conditions change.
**Fix plan:** Add `status` column to observations (active/resolved), filter resolved from injection, allow reopen via MCP tool.
**Context:** Discussed 2026-03-23 re: Codex Account Blocker observation (ID 56553)

## 2026-03-24: Pre-Commit Quality Guardrails (Future FR)
**What:** Agent committed hardcoded `max_tokens: 4096` that should have been configurable. No automated check caught it before commit.
**Two scopes:**
1. **Static guardrails (linter)** — magic numbers, hardcoded URLs, missing error checks, TODO without issue. Classic pre-commit hook territory (golangci-lint custom rules). Not engram — fixed rules, no LLM needed.
2. **Context-aware guardrails (engram)** — "this pattern caused issues before", "user prefers config over hardcode". Already partially solved by `<user-behavior-rules>` injection. Gap: injection happens at prompt time, not at commit time. A PostToolUse hook on Write/Edit could check diff against known anti-patterns from engram observations.
**Impact:** Agent ignores rules it already knows. Pre-commit check would catch before it reaches PR.
**Decision needed:** Is this a linter task (golangci-lint) or an engram task (context-aware), or both?
**Context:** Hardcoded 4096 in `internal/learning/llm.go`, caught by user not by system. Fixed in PR #49.

## 2026-03-24: Re-benchmark All 12 Models with max_tokens: 4096
**What:** Benchmark Rounds 1-2 used max_tokens: 1024 (hardcoded in benchmark script). Thinking models were unfairly penalized — reasoning consumed token budget. With production max_tokens: 4096, results may differ significantly.
**Impact:** Current winner (huihui-qwen3.5-9b-abliterated) may not be the best choice. Thinking models that scored poorly (qwen3.5-9b 5.0, ernie 5.5) could improve dramatically.
**Action:** Update run_benchmark_v2.py to use 4096, re-run all 12 models with B_fewshot. Compare with current results.
**Context:** `.agent/benchmark/run_benchmark_v2.py:44`, `.agent/benchmark/benchmark-v2-design.md`

## 2026-03-19: MCP Resources/Prompts Stubs
What: MCP server returns empty lists for resources/list, prompts/list, completion/complete
Why deferred: MCP spec allows graceful empty responses for unsupported capabilities
Impact: No functional impact — clients handle empty lists

## ~~2026-03-19: Memory Blocks Table Unpopulated~~ RESOLVED v1.5.2 — dropped via migration 047

## 2026-03-25: Dashboard Type Filter — Client-Side Instead of Server-Side
**What:** Observation type filter buttons (bugfix, feature, refactor, discovery, decision, change) filter client-side on the 20 records returned per page instead of sending `type` param to API. Result: "Showing 1-20 of 662" with 0-5 visible items, pagination counts wrong.
**Root cause:** `handleGetObservations` in `handlers_data.go` doesn't accept `type` query param. `fetchObservationsPaginated` in `api.ts` doesn't send it. `filteredObservations` computed in `ObservationsView.vue:165` filters locally after fetch.
**Impact:** Type filters unusable — show wrong counts and missing data.
**Fix plan:**
1. Add `type` param to `handleGetObservations` and `GetAllRecentObservationsPaginated`
2. Add `type` param to `fetchObservationsPaginated` in `api.ts`
3. Pass `currentType` to `fetchPage()` in ObservationsView
4. Remove client-side `filteredObservations` filter (server does it)
**Context:** `internal/worker/handlers_data.go:32`, `ui/src/utils/api.ts:402`, `ui/src/views/ObservationsView.vue:165`

## 2026-03-25: SDK Extraction Produces Only guidance/bugfix Types
**What:** Over 2 days of active use (657→662 observations), all new observations are type `guidance` or `bugfix`. Zero `feature`, `refactor`, `discovery`, `decision`, `change` observations created. LLM extraction prompt in stop hook may be biased toward these two types.
**Impact:** Type filters in dashboard are empty for most types. Knowledge base lacks diversity.
**Fix plan:** Review LLM extraction prompt in server-side extract-learnings endpoint — verify all observation types are in the prompt and equally reachable.
**Context:** `plugin/engram/hooks/stop.js` (calls `/api/sessions/{id}/extract-learnings`), server-side extraction prompt

## 2026-03-25: Dashboard Memories View — Browse store_memory Records
**What:** Dashboard has no dedicated view or filter for `store_memory` records. Users create memories via MCP tool but can only find them mixed into the general observations list with no memory_type filter.
**Why deferred:** Not in v1.7.0 roadmap. User explicitly requested.
**Impact:** No way to browse/manage explicitly stored memories from UI. Must use MCP tools (recall_memory, search) only.
**Fix plan:**
1. Add memory_type filter to ObservationsView (minimal) OR dedicated MemoriesView page
2. Show tags, scope, importance for each memory
3. Allow edit/delete from UI
**Context:** `ui/src/views/ObservationsView.vue`, `internal/worker/handlers_data.go`

## 2026-03-19: Config Reload via os.Exit(0)
What: reloadConfig calls os.Exit(0) instead of hot-reload
Why deferred: Hot-reload requires significant refactoring of service initialization
Impact: Docker restart policy handles the restart automatically
