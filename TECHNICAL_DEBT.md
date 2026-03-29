# Technical Debt

## ~~2026-03-25: Bulk Import Creates Phantom Sessions (403 openclaw sessions)~~ RESOLVED v2.0.3 (PR #65 + PR #99)
**What:** `handleBulkImport` created a new synthetic session for every call. 403+ phantom `bulk-import-*` sessions.
**Fix:** PR #65 (session_id param) + PR #99 (cleanup migration). Fully resolved.

## ~~2026-03-25: Cleanup Existing Phantom bulk-import-* Sessions~~ RESOLVED v2.0.3 (PR #99)
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

## ~~2026-03-23: T027 Post-Deploy Verification Pending~~ RESOLVED v2.0.2 (scoring active, observations show non-trivial scores)
**What:** Retrospective eval skill (T027) needs manual execution after v1.5.1 deploy to verify >50% observation relevance.
**Why deferred:** Requires server restart with v1.5.1 image (migration 046 fix), then manual `/retrospective-eval` run.
**Impact:** No automated verification that composite scoring + diversity penalty actually improve relevance. Currently based on qualitative assessment only.
**Context:** `.agent/specs/composite-relevance-scoring/tasks.md` T027

## ~~2026-03-23: Vault Credentials Encrypted with Lost Key~~ RESOLVED (Migration 053)
**What:** 15 credentials encrypted with lost key. Migration 053 deleted all dead credentials.
**Fix:** `053_cleanup_dead_vault_credentials` in `migrations.go`. Key storage fixed in v1.4.0.

## ~~2026-03-26: Patterns System — 345 Patterns with Useless Insights~~ RESOLVED v1.8.0 (PRs #71, #72)
**What:** 345 patterns (down from 16k after migration cleanup). Insight button shows "I've encountered this pattern 1816 times. This is a recognized pattern in the codebase." — zero actionable content. User confirmed: "результат insight все еще бесполезен".
**Why it's broken (3 independent problems):**
1. **Generic description** — `promoteCandidate()` hardcodes "Automatically detected pattern from recurring observations" for ALL patterns. No LLM summary, no evidence from source observations.
2. **Template insight** — Frontend renders `{frequency} times across {projects} projects`. No examples, no observation titles, no links to source observations. The user sees a count but has no idea WHAT the pattern actually IS.
3. **Stale confidence** — Most patterns stuck at 0.5-0.65 from creation. `updateConfidence()` only runs on new occurrences, not on existing patterns. No batch recalculation.
**Impact:** Entire Patterns page is decoration — looks populated but provides no value to user or agent.
**Fix plan (2 phases):**
Phase A (Insight redesign — combined LLM summary + source observations):
1. Add API endpoint `GET /api/patterns/{id}/observations` — returns observations that constitute this pattern (from `observation_ids` field)
2. On Insight click: send source observation titles + narratives to LLM → generate 2-3 sentence summary explaining WHAT this pattern is, WHY it matters, and WHEN to apply it
3. Frontend: show LLM-generated summary at top, followed by collapsible list of source observations (title, type badge, link to detail view)
4. Cache LLM summary in pattern `description` field — regenerate only if source observations change
Phase B (quality improvement):
5. Batch recalculate confidence for all 345 patterns
6. Purge orphan patterns (source observations deleted by migrations 040-043)
7. Add bulk action "archive all confidence < 0.6"
**Context:** `pkg/models/pattern.go:165`, `internal/pattern/detector.go:257`, `ui/src/views/PatternsView.vue`

## ~~2026-03-23: ScoreBreakdown Modal — API Response Mismatch~~ RESOLVED v1.6.1 (PR #44)
**What:** Clicking score badge (e.g., "1.31") in ObservationCard triggers ScoreBreakdown modal but shows blank/error. API returns `{id, components, config}` but Vue component expects `{observation: {title, type}, scoring: {final_score, type_weight, recency_decay, ...}, explanation: {...}}`.
**Root cause:** `handleExplainScore` in `handlers_scoring.go` returns raw `scoreCalculator.CalculateComponents()` output. Frontend `ScoreBreakdown.vue` expects a different shape with nested `observation`, `scoring`, `explanation` objects.
**Impact:** Score breakdown feature broken — modal shows loading then nothing.
**Fix plan:** Either reshape API response to match frontend, or update frontend to match API.
**Context:** `internal/worker/handlers_scoring.go:383`, `ui/src/components/ScoreBreakdown.vue:106-196`, `ui/src/utils/api.ts:205`

## ~~2026-03-23: Observation Status Lifecycle (Future FR)~~ RESOLVED v1.8.0 (PR #69)
**What:** Observations lack a `status` field (active/resolved/conditional). Temporary facts (e.g., "Codex account blocked") can only be suppressed (hidden forever) or downvoted (soft penalty). Neither supports "resolved but re-openable if condition recurs".
**Impact:** Stale observations continue to inject into context. Users must manually suppress and re-create when conditions change.
**Fix plan:** Add `status` column to observations (active/resolved), filter resolved from injection, allow reopen via MCP tool.
**Context:** Discussed 2026-03-23 re: Codex Account Blocker observation (ID 56553)

## ~~2026-03-24: Pre-Commit Quality Guardrails~~ RESOLVED v2.1.3 (PR #116)
**What:** Agent committed hardcoded `max_tokens: 4096` that should have been configurable. No automated check caught it before commit.
**Two scopes:**
1. **Static guardrails (linter)** — magic numbers, hardcoded URLs, missing error checks, TODO without issue. Classic pre-commit hook territory (golangci-lint custom rules). Not engram — fixed rules, no LLM needed.
2. **Context-aware guardrails (engram)** — "this pattern caused issues before", "user prefers config over hardcode". Already partially solved by `<user-behavior-rules>` injection. Gap: injection happens at prompt time, not at commit time. A PostToolUse hook on Write/Edit could check diff against known anti-patterns from engram observations.
**Impact:** Agent ignores rules it already knows. Pre-commit check would catch before it reaches PR.
**Decision needed:** Is this a linter task (golangci-lint) or an engram task (context-aware), or both?
**Context:** Hardcoded 4096 in `internal/learning/llm.go`, caught by user not by system. Fixed in PR #49.

## ~~2026-03-24: Re-benchmark All 12 Models with max_tokens: 4096~~ RESOLVED — script updated to 4096, model list refreshed (13 models)
**What:** Benchmark Rounds 1-2 used max_tokens: 1024 (hardcoded in benchmark script). Thinking models were unfairly penalized — reasoning consumed token budget. With production max_tokens: 4096, results may differ significantly.
**Impact:** Current winner (huihui-qwen3.5-9b-abliterated) may not be the best choice. Thinking models that scored poorly (qwen3.5-9b 5.0, ernie 5.5) could improve dramatically.
**Action:** Update run_benchmark_v2.py to use 4096, re-run all 12 models with B_fewshot. Compare with current results.
**Context:** `.agent/benchmark/run_benchmark_v2.py:44`, `.agent/benchmark/benchmark-v2-design.md`

## ~~2026-03-19: MCP Resources/Prompts Stubs~~ RESOLVED — by design
What: MCP server returns empty lists for resources/list, prompts/list, completion/complete.
MCP spec explicitly allows empty responses for unsupported capabilities. Not a bug.

## ~~2026-03-19: Memory Blocks Table Unpopulated~~ RESOLVED v1.5.2 — dropped via migration 047

## ~~2026-03-25: Dashboard Type Filter — Client-Side Instead of Server-Side~~ RESOLVED v2.1.1 (PR #114)
**What:** Type filter was client-side. Fixed: server-side type + concept params added to handleGetObservations.
**Fix:** PR #114 — backend accepts type/concept params, frontend passes from FilterTabs.

## ~~2026-03-25: SDK Extraction Produces Only guidance/bugfix Types~~ RESOLVED v2.1.1
**What:** LLM extraction prompt biased — "guidance" listed first with broadest description.
**Fix:** Reordered types: specific first (decision, feature, bugfix), general last (guidance). Added "prefer specific over general" instruction. `internal/learning/prompts.go`

## ~~2026-03-25: Dashboard Memories View — Browse store_memory Records~~ RESOLVED v1.8.0 (PR #70)
**What:** Dashboard has no dedicated view or filter for `store_memory` records. Users create memories via MCP tool but can only find them mixed into the general observations list with no memory_type filter.
**Why deferred:** Not in v1.7.0 roadmap. User explicitly requested.
**Impact:** No way to browse/manage explicitly stored memories from UI. Must use MCP tools (recall_memory, search) only.
**Fix plan:**
1. Add memory_type filter to ObservationsView (minimal) OR dedicated MemoriesView page
2. Show tags, scope, importance for each memory
3. Allow edit/delete from UI
**Context:** `ui/src/views/ObservationsView.vue`, `internal/worker/handlers_data.go`

## ~~2026-03-26: OpenClaw Reports "POST /api/context/inject failed"~~ INVESTIGATED — server OK, client-side issue
**What:** OpenClaw gateway reports: "POST /api/context/inject failed, server marked unavailable after 3 consecutive failures" → 60s cooldown → all engram tools disabled.
**Observed:** Engram server responds 200 OK in 0.27-0.8s when tested directly (10/10 calls stable). No inject errors in engram server logs. Problem reported by OpenClaw, not reproducible from this machine.
**Not investigated:** What OpenClaw actually receives (HTTP status, error message, network path). Whether the issue is DNS, Docker networking, auth, response parsing, or something else entirely.
**Impact:** All engram tools (search, decisions, store_memory, recall_memory) return "unreachable" during 60s cooldown.
**Next step:** Reproduce from OpenClaw side — check OpenClaw gateway logs for the actual error message/stack trace.
**Context:** `plugin/openclaw-engram/src/availability.ts` (STRIKE_THRESHOLD=3, COOLDOWN_MS=60000), `src/client.ts` (request method with AbortController).

## ~~2026-03-26: GPU Contention — SocratiCode Embedding Floods LLM Queue~~ RESOLVED — queue cleared, issue was transient
**What:** SocratiCode codebase indexer sends 65,000+ embedding requests to shared Ollama GPU. Embedding model (qwen3-embedding-8b) and LLM model (qwen3.5-9b-abliterated) share same GPU with Parallel=4. Embedding backlog starves LLM requests — pattern insight and observation extraction timeout.
**Root cause:** SocratiCode re-indexes same files repeatedly (nvmd-devops SKILL.md files appear every 2-3 seconds in Ollama logs). Multiple Claude Code sessions may trigger concurrent `codebase_index(force: true)`.
**Impact:** Pattern insight "Summary unavailable" despite model being loaded and key being correct. Observation LLM extraction falls back to CLI.
**Fix options:**
1. LiteLLM priority queues — separate embedding and LLM traffic (LiteLLM feature, not engram)
2. Deduplicate SocratiCode embedding requests — batch dedup at proxy level
3. Reduce SocratiCode re-index frequency — don't index unchanged files
4. Separate GPU instances for embedding vs LLM (hardware solution)
**Context:** Ollama dashboard showed: embedding model 65,702 queued, LLM model 108 GEN + 4 queued. Both on same GPU.

## ~~2026-03-19: Config Reload via os.Exit(0)~~ RESOLVED v2.1.4 (PR #117)
**What:** reloadConfig used os.Exit(0). **Fix:** config.Reload() swaps global atomically. Port/token warn only.

## ~~2026-03-28: Behavioral Rules for MCP Tool Adoption~~ RESOLVED v2.1.1
**What:** Agents use 2 of 68 MCP tools. Need always_inject behavioral rules that trigger tool usage at the right moments: `find_by_file` before Edit/Write, `decisions` before architectural choices, `rate_memory` after session.
**Why deferred:** Plugin tool consolidation (FR-1 through FR-6) must land first so rules reference stable tool names.
**Impact:** High — this is the actual adoption fix. Tool consolidation reduces noise, behavioral rules drive usage.
**Context:** `.agent/reports/plugin-api-gap-audit-2026-03-28.md`, `.agent/specs/plugin-tool-consolidation/spec.md`

## ~~2026-03-28: MCP Tool Namespace Prefixes~~ RESOLVED v2.1.0 — superseded by consolidation
**What:** Vault/doc tools lacked namespace prefix. Now moot: all tools consolidated into `vault(action=...)` and `docs(action=...)`. Old tool names are backward-compat aliases only.
