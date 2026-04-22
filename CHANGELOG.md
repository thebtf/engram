# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [5.0.0] - 2026-04-23

### Added

- **Static session-start gRPC flow (US13)**: added `GetSessionStartContext` and `NegotiateVersion` to the gRPC API, worker compatibility endpoint `/api/context/session-start`, and hook-side local cache fallback at `${ENGRAM_DATA_DIR}/cache/session-start-{project-slug}.json`.

### Changed

- **Static-only product direction**: Engram now treats explicit writes and deterministic reads as the primary product contract. Session-start inject is simplified to issues + behavioral rules + memories.
- **Version compatibility signaling**: session-start path now performs explicit major-version negotiation instead of silently tolerating client/server skew.
- **Plugin/daemon release alignment**: plugin version `5.0.0` and daemon version `v5.0.0` are released together.

### Removed

- `internal/search` package deleted (search.Manager, RRF, MMR, LLM filter, search metrics)
- `internal/search/expansion` package deleted (HyDE query expansion, Expander)
- `recall` MCP tool reduced to trivial SQL filter (memoryStore.List + in-memory substring)
- Dropped MCP tools: `search`, `timeline`, `decisions`, `changes`, `how_it_works`, `find_by_concept`, `find_by_type`, `get_recent_context`, `get_context_timeline`, `get_timeline_by_query`, `explain_search_ranking`
- patterns subsystem
- graph subsystem
- learning / scoring / maintenance / consolidation loops
- reranking / embeddings-era runtime stack
- `ENGRAM_HYDE_ENABLED` env var removed
- `ENGRAM_LLM_FILTER_ENABLED` env var removed

### Breaking

- observations-era dynamic runtime is no longer the primary storage/retrieval model
- session-start uses static composite payloads rather than the old dynamic inject path
- mixed major client/server versions must fail with an explicit compatibility error on the session-start path

### Notes

- Release notes: `docs/release-notes/v5.0.0.md`

## [3.7.1] - 2026-04-12

Post-MVP stabilization: hotfixes, reconciliation, and feedback import.

### Fixed

- **Session outcome identity handling**: finalize canonical session ID resolution for outcome propagation (`21c7f69`)
- **Context by-file project scoping**: enforce project parameter in `/api/context/by-file` to prevent cross-project observation leakage (`c166179`)
- **Hit-rate markdown reparsing**: remove unnecessary markdown parsing in learning hit-rate analytics (`c5f1e81`)
- **Nil command arrays serialization**: serialize nil command arrays as empty JSON arrays instead of null (`3d9178d`)
- **Missing commands_run migration**: add migration for commands_run column in observations table (`213e562`)

### Added

- **Server-side feedback import**: new `POST /api/import/feedback` endpoint + CLI HTTP client for bulk importing historical feedback data. New `cmd/engram-import/main.go` entry point. (`3ab74d6`, `e5a7e60`)

## [v4.x-in-progress] - 2026-04-11

Learning Memory v4 post-MVP feature wave. This entry tracks the FRs shipped after the v3.7.0 MVP foundation and before the final v4 polish/staging sign-off.

### Shipped FRs

- **FR-4 File-scope prefiltering**: inject/search can now narrow observation retrieval to files currently being edited. Added file-path support to `BuildWhereFilter`, tracked edited files in hook-side session signals, and passed `files_being_edited` through inject/search flows.
- **FR-5 Per-type search lanes**: retrieval can now use type-specific `(min_score, top_k, reranker_weight)` lanes when `ENGRAM_TYPE_LANES_ENABLED=true`, allowing guidance/pitfall, decision, wiki/entity, and default classes to rank differently.
- **FR-6 Project briefing**: per-project synthesized briefing lookup/generation and inject wiring are now present behind `ENGRAM_PROJECT_BRIEFING_ENABLED`.
- **FR-7 Alarm model expansion**: file alarm model now covers semantic Edit/Write trigger matching, Bash command prefix warnings, and repeated-Read path context via `/api/memory/triggers` plus hook-side merged rendering.
- **FR-8 Write-time merge decision**: `DecideMerge` is wired into the observation ingest path with `CREATE_NEW`, `UPDATE`, `SUPERSEDE`, and `SKIP` handling. The supersede path now keeps the old observation active until the replacement insert succeeds.
- **FR-8a Contradiction kill-switch**: `ENGRAM_CONTRADICTION_DETECTION_ENABLED` lets operators disable the old supersede path and fall through to `CREATE_NEW`.
- **FR-8b Wrong-supersede audit artifacts**: `.agent/reports/wrong-supersede-audit.md` and `.agent/reports/restore-candidates.sql` capture the known-bad supersede IDs for operator review.
- **FR-9 Entity-seeded graph traversal**: inject path can derive entity seeds from the current session and fuse graph-neighbor observations with vector results through `search.RRF` when `ENGRAM_INJECT_GRAPH_BFS_ENABLED=true`.

### Memory correction

- The previous memory shorthand "stop hook unreliable" is no longer accurate enough and has been corrected.
- The important distinction is: **Claude Code `Stop` was the wrong lifecycle point for realtime outcome propagation; `SessionEnd` is the correct hook for graceful session exit, and the server periodic recorder remains the backup path.**
- Memory index and memory note were updated to reflect this correction so future sessions do not keep repeating the old oversimplified claim.

## [3.7.0] - 2026-04-11

Learning Memory v4 MVP -- empirically-driven rebuild of the retrieval path after baseline
metrics showed 2164 feedback records with 0 positive / 0 negative citations and 20 noise
candidates with 0 high-value observations. The relevant-memory injection was broadcasting
guidance rules that agents never cited. v4 repairs the foundation before adding features.

Full spec set: `.agent/specs/learning-memory-v4/` (spec.md, roadmap.md, tasks.md, baseline-metrics.md,
challenge-report.md, hook-lifecycle-findings.md).

### Fixed (breaking changes, migration path below)

- **Injection floor anti-pattern removed (FR-1)**: `InjectionFloor` default changed from 3 to 0.
  Previously, when composite scoring eliminated every candidate, the code force-filled the
  response with top-importance observations regardless of query relevance. This made the
  relevance threshold cosmetic. Silence is now a legitimate result.
  Files: `internal/config/config.go`, `internal/worker/handlers_context.go`, new `internal/worker/floor_fill.go` helper.
  Migration: operators who relied on always-non-empty responses can set `ENGRAM_INJECTION_FLOOR=3`.
  Commits: `5e1a56c` (T006), `fbd4da0` (T007).

- **LLM filter silence gate (FR-2)**: when `LLMFilterEnabled=true` and the LLM explicitly
  returns an empty set meaning "nothing is relevant", the code previously overrode this
  with "top-5 composite scoring fallback". The LLM said silence; the code injected noise.
  Now the empty set is honored and an Info log line marks the silence event.
  Error/timeout fallback (return all candidates) is unchanged -- only the intentional
  empty-set path changed.
  Files: `internal/search/llm_filter.go`, `internal/worker/handlers_context.go`.
  Commits: `f52b0d4` + `1a01310` + `77bbd41` (T008), `a02c586` + `14f7921` (T009).

- **Hardcoded inject query replaced (FR-3)**: `handleContextInject` relevant section no longer
  uses `query := project + " code development"`. Injection now routes through
  `RetrieveRelevant`, the same pipeline that user-prompt search uses -- hybrid search,
  composite scoring, LLM filter, adaptive threshold, deduplication. Query is derived from
  the last user prompt for the session, falling back to project name for cold starts.
  New `retrievalHooks` extension point prepared for future F5 typed lanes and F8 BFS phases.
  Files: new `internal/worker/retrieval.go`, new `internal/worker/retrieval_helpers.go`,
  `internal/worker/handlers_context.go`.
  Commits: `d9a3c42` (T010), `4b6c999` (T011).

- **MCP `set_session_outcome` bypass fixed**: `internal/mcp/tools_learning.go` previously
  called only `UpdateSessionOutcome` without triggering `PropagateOutcome`. Utility scores
  were never updated from MCP-initiated outcome signals. Now mirrors the HTTP endpoint's
  goroutine-based propagation. Also wires `SetInjectionStore` on the MCP server.
  Commit: `7342ec3` (T019).

### Added

- **Realtime outcome propagation via SessionEnd hook (FR-5)**: engram's `hooks.json` now
  registers `SessionEnd`, which fires during Claude Code `gracefulShutdown()` with a 1.5s
  budget (SIGINT/SIGTERM/`/exit`/`/clear`). A new `plugin/engram/hooks/session-end.js`
  posts to the new endpoint `POST /api/sessions/{id}/propagate-outcome` fire-and-forget
  with a 1200ms client timeout (300ms headroom under Claude's 1500ms cap).
  `PropagateOutcome` updates `utility_score` for all injected observations in the session
  within seconds of session exit, instead of hours via the maintenance cycle.
  Maintenance `recordPendingOutcomes` remains as crash-proof fallback (catches sessions that
  missed graceful shutdown via SIGKILL, uncaught exceptions, etc.) and skips sessions that
  were already propagated within the last 2 hours.
  The previous CONTINUITY note "stop hook unreliable, never fires" was based on a
  misunderstanding: `Stop` fires per-turn, not at session exit. That is what `SessionEnd`
  is for. engram's `hooks.json` had simply never registered `SessionEnd`.
  Files: new migration `072_sessions_utility_propagated_at`, new handler in
  `internal/worker/handlers_learning.go`, new file `plugin/engram/hooks/session-end.js`,
  `plugin/engram/hooks/hooks.json`, `internal/maintenance/service.go`.
  Commits: `345efcb` (T014), `9fb0b3b` (T015), `6c266de` (T016), `bd46ca6` (T017), `f60a241` (T018).

- **`ENGRAM_INJECT_UNIFIED` rollback flag**: emergency escape hatch to revert to the
  legacy hardcoded-query path (default true; set false for rollback). To be removed
  after two release cycles once the unified path is proven in production.
  Commit: `c69a51b` (T012).

- **Inject latency benchmark script**: `scripts/bench-inject.sh` runs 100 HTTP calls against
  `/api/context/inject` and reports p50/p95/p99 to `.agent/reports/f1-latency-delta.json`.
  Baseline comparison deferred until production p99 is captured (tracked as T005 in the spec).
  Commit: `8856864` (T013).

- **6 new integration tests** covering the unified inject path:
  `TestInjectRelevant_UnifiedPath_UsesLastUserPrompt`,
  `TestInjectRelevant_UnifiedPath_FallsBackToProjectName`,
  `TestInjectRelevant_TwoSessionsDifferentPrompts` (anti-stub proof),
  `TestInjectRelevant_LegacyPath_WhenFlagFalse`,
  plus 2 config tests (`TestInjectUnifiedDefaultTrue`, `TestInjectUnifiedEnvOverride`).
  Plus 3 LLM filter tests (`EmptyResponseSilencesInjection`,
  `ParseFailureFallsBackToAllCandidates`, `TimeoutFallsBackToAllCandidates`).
  Plus 5 floor-fill tests covering silence and backward-compat paths.

### Config

- `ENGRAM_INJECTION_FLOOR` -- default changed **3 -> 0** (breaking if you relied on the floor)
- `ENGRAM_INJECT_UNIFIED` -- new, default **true** (rollback flag)

### Schema

- Migration `072_sessions_utility_propagated_at`: `ALTER TABLE sessions ADD COLUMN IF NOT EXISTS utility_propagated_at TIMESTAMPTZ`. Idempotent.

### Plugin

- Plugin version bumped to **3.7.0** across all three manifests (`plugin/engram/.claude-plugin/plugin.json`, `plugin/openclaw-engram/package.json`, `plugin/openclaw-engram/openclaw.plugin.json`).
- New hook file: `plugin/engram/hooks/session-end.js`.
- `hooks.json` registers `SessionEnd` with 1500ms timeout.

### Empirical baseline (pre-v4)

Captured before the v4 code changes for regression detection. See `.agent/specs/learning-memory-v4/baseline-metrics.md`.
- 2164 feedback records, **100% neutral** -- no user or heuristic ratings ever registered as positive or negative
- **20 noise candidates** with 10+ injections and 0 citations; **0 high-value candidates**
- Top 10 most-retrieved observations: all `guidance` type, 888-1038 retrievals each, 0 citations
- 30-day corpus: 1438 observations (decision 44.5% / discovery 33.6% / guidance 9.0%)
- Near-dedup total merges: 0 (periodic dedup dormant)

### Post-shipment validation

Validation protocol per spec.md sectionValidation Protocol. After deployment:
1. Re-run `admin(action="hit_rate")` and compare noise/value counts to baseline
2. Run a live session with `/exit` and verify `utility_propagated_at` updates within 3s
3. Check inject silence rate: `% of sessions with 0 relevant observations injected` -- target 40% acceptable
4. Watch `learning_llm_calls_total` to ensure LLM filter does not spike cost

## [3.4.1] - 2026-04-10

### Fixed

- **Issues tool not discoverable**: `issues` tool was registered in secondary tools list but not in `primaryTools()`, so `tools/list` never returned it. Agents could not see or use the issues tool. Now included in primary tools (9 consolidated tools).
- **MCP instructions missing issues**: `buildInstructions()` described "7 Tools" without mentioning issues. Updated to "8 Tools" with dedicated Issues section, workflow examples, and anti-pattern guidance ("Do NOT use store or docs for issues").
- **PATCH /api/issues/{id} missing reopen support**: Handler only accepted `status=resolved`. Added `status=reopened` which calls `ReopenIssue` -- needed for openclaw-engram REST-based reopen.

### Added

- **`include_all` parameter for tools/list**: `tools/list` with `include_all: true` or `cursor=all` returns all 50+ expanded tools alongside primary tools. Default remains primary-only for context efficiency.
- **openclaw-engram issues tool**: New `engram_issues` tool with 6 actions (create, list, get, update, comment, reopen) via REST API. Includes client methods, Zod validation, TypeBox schema.
- **Plugin memory skill updated**: Issues section added to `plugin/engram/skills/memory/SKILL.md` with when-to-use guidance.

## [3.0.0] - 2026-04-06

### Added

- **Learning Memory** -- engram now learns from every session which observations are useful
  - **Citation signal wiring**: stop hook detects which injected observations were referenced by the agent (via existing `detectUtilitySignal`), sends citation data to new `POST /api/sessions/{id}/mark-cited` endpoint. `PropagateCitation` updates effectiveness_score per-observation: cited = +0.03, uncited = -0.01.
  - **Observation enrichment**: user prompts stored server-side as context for tool calls. `BuildObservationPrompt` now includes `<user_intent>` tag -- extraction LLM sees WHY the agent acted, not just WHAT it did.
  - **Mid-session extract-learnings**: PreCompact hook sends last 20 messages (4000 token budget) to extract-learnings endpoint. Reliable trigger (replaces unreliable stop hook). Idempotent.
  - **Contradiction detection on write** (Mem0 Algorithm 1 adapted): cosine >= 0.92 = NOOP (near-duplicate), 0.75-0.92 = UPDATE (supersede with EVOLVES_FROM), < 0.75 = ADD. Synchronous, ~3-5ms.
  - **Adaptive per-project threshold**: maintenance Task 20 reads citation rates from injection_log, adjusts relevance threshold +- 0.05 per project. Bounds [0.15, 0.60]. Window: 50 sessions.
  - **Migration 066**: `cited` BOOLEAN column on injection_log with composite index

### Changed

- Store response now includes `action` field (ADD/UPDATE/NOOP) and `superseded_id` when applicable

## [2.5.0] - 2026-04-06

### Added

- **Minimum Viable Learning Loop** -- first production system to close the retrieve -> measure -> adjust -> re-retrieve feedback loop
  - Bayesian effectiveness multiplier in `ApplyCompositeScoring`: `(successes + 1) / (injections + 2)`. No minimum injection gate.
  - Project-only vector search: removed `includeGlobal=true` from 3 context search call sites
  - Project filter on `GetAlwaysInjectObservations`
  - Client min similarity filter > 0.10 in user-prompt.js

## [2.4.1] - 2026-04-06

### Added

- **Stronger MCP instructions**: exclusivity claim ("Your ONLY Persistent Memory"), mandatory AFTER workflow

### Changed

- PostToolUse hook matcher narrowed `*` -> `Write|Edit|Bash|Agent|mcp__aimux` (~50+ fewer node process spawns)
- Behavioral rules de-duplicated (session-start only, removed from user-prompt.js)
- Documentation rewrite (README, CHANGELOG, translations)

## [2.4.0] - 2026-03-29

### Added

- **LLM-Driven Memory Extraction** (ADR-005): `store(action="extract", content="...")` -- agent dumps raw content, LLM extracts structured observations autonomously
- Each extracted observation: type, title, narrative, concepts (from 20 valid concepts)
- Privacy: content redacted via `privacy.RedactSecrets` before LLM call
- Returns: `{extracted, stored, duplicates, titles}`

## [2.3.1] - 2026-03-29

### Added

- **Embedding Resilience Layer** (ADR-004): independent circuit breaker for embeddings
- 4 health states: HEALTHY -> DEGRADED -> DISABLED -> RECOVERING
- Background health check goroutine (30s probe interval)
- Automatic recovery within 60s of API returning
- Selfcheck reports embedding status with failure counts

## [2.3.0] - 2026-03-29

### Added

- **Reasoning Traces -- System 2 Memory** (ADR-003): structured reasoning chains (thought->action->observation->decision->conclusion)
- Quality scoring (0-1) via LLM evaluation -- only traces >= 0.5 stored
- Auto-detection of reasoning patterns in tool events
- `recall(action="reasoning")` retrieves past reasoning by project
- `reasoning_traces` database table with session/project indexes

## [2.2.1] - 2026-03-29

### Fixed

- P1+P2 findings from 13-area investigation report
- Summary observation fallback when assistant message empty
- userPrompt fallback threshold lowered 50->10 chars
- Circuit breaker recovery logging
- BeforeToolCallResult type added to OpenClaw HookResult
- Missing concept keywords backfill migration

## [2.2.0] - 2026-03-28

### Added

- **Server-side periodic summarizer** (maintenance Task 19): sessions summarized automatically, no client hook dependency

### Fixed

- Pre-edit guardrails: guidance rules no longer shown as warnings
- Removed broken client-side summarizer from session-start.js

### Changed

- Summary generation moved from client to server

## [2.1.9] - 2026-03-28

### Added

- Dashboard search miss handling with frequency display
- Session views with date filtering and min_prompts filter

### Fixed

- Search miss API response unwrapping (miss_stats envelope)
- Session list filtering (min_prompts, from, to query params)

## [2.1.8] - 2026-03-28

### Added

- Dashboard UX polish: tooltips on observation cards, cursor-pointer, hover transitions, color coding by type

## [2.1.7] - 2026-03-28

### Added

- Dashboard pattern insights view with LLM-generated descriptions
- Background pattern insight generation (maintenance Task 18, 5 per cycle)
- Session detail view with metadata, observations, injections

### Fixed

- Summary content built from observations when no transcript available

## [2.1.6] - 2026-03-28

### Added

- Knowledge graph local mode (per-observation neighborhood view)
- Graph node search functionality
- Visual styling improvements for graph visualization

## [2.1.5] - 2026-03-28

### Added

- "Sessions Today" counter on dashboard (replaced always-0 "Active Sessions")
- Consistency check endpoint: `GET /api/maintenance/consistency`
- `memory_get` import bridge: read file AND store as observation

## [2.1.4] - 2026-03-28

### Added

- Config hot-reload: atomic config swap via `config.Reload()`, no process restart needed

## [2.1.3] - 2026-03-28

### Added

- Pre-edit guardrails hook (recall by_file before file modifications)
- Session summarization on session start
- Statusline hook: learning effectiveness metric with 60s cache

## [2.1.2] - 2026-03-28

### Added

- 4 user slash commands: `/retro` (session analysis), `/stats` (memory analytics), `/cleanup` (observation curation), `/export` (data export)

## [2.1.1] - 2026-03-28

### Fixed

- Dashboard concept filter (JSONB @> operator replaces LIKE)
- Dashboard type filter
- Hardcoded observation/prompt counts replaced with real API data

## [2.1.0] - 2026-03-28

### Changed

- **MCP Tool API Consolidation**: 61 tools -> 7 primary tools (recall, store, feedback, vault, docs, admin, check_system_health)
- >80% context window reduction (~6100 -> ~900 tokens per session)
- All 61 original tool names work as backward-compatible dispatch aliases
- Updated MCP server instructions for consolidated API
- 6 new router files for action-based dispatch

## [2.0.9] - 2026-03-28

### Added

- OpenClaw plugin expanded from 8 -> 17 tools with lifecycle hooks
- Tool descriptions include trigger conditions
- Stop hook: switched to retrospective injection API
- Statusline: learning effectiveness metric

### Fixed

- `engram_decisions` uses dedicated endpoint
- `memory_forget` defaults to suppress (reversible)
- Suppress action: RowsAffected check + cache invalidation
- Session outcome recording uses Claude session ID string

### Changed

- Removed 7 redundant MCP tool registrations (68 -> 61)

## [2.0.8] - 2026-03-28

### Added

- Session injection retrospective API (`GET /api/sessions/:id/injections`)

### Fixed

- Effectiveness distribution excludes never-injected observations

## [2.0.7] - 2026-03-27

### Note

Releases v0.9.0 through v2.0.7 included incremental improvements to search quality, scoring algorithms, session indexing, knowledge graph, and infrastructure. See [GitHub Releases](https://github.com/thebtf/engram/releases) for detailed per-version notes.

## [0.5.1] - 2026-03-08

### Added

- **MCP instructions** -- `buildInstructions()` returns comprehensive usage guide for all 48+ tools on `initialize` -- any MCP client instantly knows how to use engram
- **Marketplace auto-sync** -- GitHub Actions workflow syncs `plugin/` to `thebtf/engram-marketplace` on push to main

### Fixed

- **Observation extraction in Docker** -- replaced Claude CLI dependency (`claude --print`) with OpenAI-compatible LLM API (`ENGRAM_LLM_URL`). Observation pipeline was completely non-functional in Docker deployments where Claude CLI is not installed.
- **MCP panic recovery** -- added panic recovery with zerolog logging in Streamable HTTP handler
- **FalkorDB int64 panic** -- convert int64 to int for falkordb-go ParameterizedQuery params
- LLM client URL normalization -- handles both `http://host:port` and `http://host:port/v1` formats
- LLM client fallback env var -- now correctly reads `ENGRAM_EMBEDDING_BASE_URL` (was `ENGRAM_EMBEDDING_URL`)
- Configurable LLM concurrency (`ENGRAM_LLM_CONCURRENCY`), timeout, and retry with backoff for transient errors
- Reranking API key optional for TEI/direct backends; batch size configurable via `ENGRAM_RERANKING_BATCH_SIZE`

### Changed

- Plugin version bumped to 0.5.1

## [0.3.0] - 2026-03-07

### Added

- Collection MCP tools: `list_collections`, `list_documents`, `get_document`, `ingest_document`, `search_collection`, `remove_document` -- YAML-configurable knowledge bases with smart chunking
- `import_instincts` MCP tool -- import ECC instinct files as guidance observations with semantic dedup
- Unified document search integration -- `search` tool now includes document results when `type="documents"` or empty
- Per-session utility signal detection for self-learning

### Fixed

- AI review findings for collection tools and instinct import

### Changed

- README complete documentation rewrite

## [0.2.0] - 2026-03-07

### Added

- HTTP logs endpoint (`/api/logs`)
- JavaScript plugin hooks replacing Go binaries -- simpler deployment, no build needed

### Fixed

- Increase embedding timeout for high-dimension models
- Setup command now edits `settings.json` instead of OS environment variables
- Downgrade SDK processor log from Warn to Debug
- Skip session indexing when directory does not exist

## [0.1.0] - 2026-03-07

Initial release with full feature set.

### Added

- **Core Memory System**
  - PostgreSQL 17 + pgvector storage with HNSW cosine vector index
  - Hybrid search: tsvector GIN + vector similarity + BM25, RRF fusion
  - Cross-encoder reranking (ONNX or API-based)
  - BM25 short-circuit optimization for strong text matches
  - HyDE query expansion with template fast path and LLM fallback

- **MCP Server (44 tools)**
  - Search & Discovery (11): hybrid search, timeline, decisions, changes, concept/file/type filters
  - Context Retrieval (4): recent context, timeline views, pattern detection
  - Observation Management (9): CRUD, tagging, merging, bulk operations
  - Analysis & Quality (11): stats, quality scores, trends, scoring breakdowns
  - Sessions (2): full-text session search, listing with filters
  - Graph (2): neighbor traversal, graph statistics
  - Consolidation & Maintenance (3): decay, associations, forgetting

- **Knowledge Graph**
  - 17 relation types: causes, fixes, supersedes, contradicts, explains, shares_theme, etc.
  - In-memory CSR graph traversal
  - Optional FalkorDB integration with async dual-write
  - Graph-augmented search expansion after RRF fusion

- **Memory Consolidation**
  - Relevance decay (daily): exponential time decay with access frequency boost
  - Creative associations (daily): embedding similarity discovery
  - Forgetting (quarterly, opt-in): archives low-relevance observations
  - Stratified sampling and EVOLVES_FROM relation

- **Scoring System**
  - Importance scoring: type-weighted with concept, feedback, retrieval, utility bonuses
  - Relevance scoring: decay x access x relations x importance x confidence
  - Belief revision: telemetry, provenance tracking, smart GC

- **Session Indexing**
  - JSONL parser with workstation isolation
  - Composite key: `workstation_id:project_id:session_id`
  - Incremental indexing

- **Self-Learning**
  - Guidance observation type with context partitioning
  - Utility tracking infrastructure
  - Utility signal detection in hooks
  - LLM-based learning extraction

- **Embeddings**
  - Local ONNX BGE (384D) provider
  - OpenAI-compatible REST API provider
  - Tiered vector indexing (DiskANN for dims > 2000)

- **Infrastructure**
  - Single-port server (37777): HTTP API + MCP SSE + MCP Streamable HTTP
  - MCP stdio proxy for clients that only support stdio
  - Docker deployment with docker-compose
  - GitHub Actions CI: Docker image publishing to ghcr.io
  - Claude Code plugin with marketplace support
  - `/engram:setup` command with doctor diagnostics
  - Token-based authentication for all endpoints
  - Context injection optimization with compact format and token budgeting
  - Install scripts for macOS/Linux

### Attribution

Originally based on [claude-mnemonic](https://github.com/lukaszraczylo/claude-mnemonic) by Lukasz Raczylo.

[Unreleased]: https://github.com/thebtf/engram/compare/v5.0.0...HEAD
[5.0.0]: https://github.com/thebtf/engram/compare/v3.7.1...v5.0.0
[3.7.0]: https://github.com/thebtf/engram/releases/tag/v3.7.0
[2.4.0]: https://github.com/thebtf/engram/compare/v2.3.1...v2.4.0
[2.3.1]: https://github.com/thebtf/engram/compare/v2.3.0...v2.3.1
[2.3.0]: https://github.com/thebtf/engram/compare/v2.2.1...v2.3.0
[2.2.1]: https://github.com/thebtf/engram/compare/v2.2.0...v2.2.1
[2.2.0]: https://github.com/thebtf/engram/compare/v2.1.9...v2.2.0
[2.1.9]: https://github.com/thebtf/engram/compare/v2.1.8...v2.1.9
[2.1.8]: https://github.com/thebtf/engram/compare/v2.1.7...v2.1.8
[2.1.7]: https://github.com/thebtf/engram/compare/v2.1.6...v2.1.7
[2.1.6]: https://github.com/thebtf/engram/compare/v2.1.5...v2.1.6
[2.1.5]: https://github.com/thebtf/engram/compare/v2.1.4...v2.1.5
[2.1.4]: https://github.com/thebtf/engram/compare/v2.1.3...v2.1.4
[2.1.3]: https://github.com/thebtf/engram/compare/v2.1.2...v2.1.3
[2.1.2]: https://github.com/thebtf/engram/compare/v2.1.1...v2.1.2
[2.1.1]: https://github.com/thebtf/engram/compare/v2.1.0...v2.1.1
[2.1.0]: https://github.com/thebtf/engram/compare/v2.0.9...v2.1.0
[2.0.9]: https://github.com/thebtf/engram/compare/v2.0.8...v2.0.9
[2.0.8]: https://github.com/thebtf/engram/compare/v2.0.7...v2.0.8
[2.0.7]: https://github.com/thebtf/engram/compare/v0.5.1...v2.0.7
[0.5.1]: https://github.com/thebtf/engram/compare/v0.3.0...v0.5.1
[0.3.0]: https://github.com/thebtf/engram/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/thebtf/engram/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/thebtf/engram/releases/tag/v0.1.0
