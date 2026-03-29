# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [2.4.0] - 2026-03-29

### Added

- **LLM-Driven Memory Extraction** (ADR-005): `store(action="extract", content="...")` — agent dumps raw content, LLM extracts structured observations autonomously
- Each extracted observation: type, title, narrative, concepts (from 20 valid concepts)
- Privacy: content redacted via `privacy.RedactSecrets` before LLM call
- Returns: `{extracted, stored, duplicates, titles}`

## [2.3.1] - 2026-03-29

### Added

- **Embedding Resilience Layer** (ADR-004): independent circuit breaker for embeddings
- 4 health states: HEALTHY → DEGRADED → DISABLED → RECOVERING
- Background health check goroutine (30s probe interval)
- Automatic recovery within 60s of API returning
- Selfcheck reports embedding status with failure counts

## [2.3.0] - 2026-03-29

### Added

- **Reasoning Traces — System 2 Memory** (ADR-003): structured reasoning chains (thought→action→observation→decision→conclusion)
- Quality scoring (0-1) via LLM evaluation — only traces ≥ 0.5 stored
- Auto-detection of reasoning patterns in tool events
- `recall(action="reasoning")` retrieves past reasoning by project
- `reasoning_traces` database table with session/project indexes

## [2.2.1] - 2026-03-29

### Fixed

- P1+P2 findings from 13-area investigation report
- Summary observation fallback when assistant message empty
- userPrompt fallback threshold lowered 50→10 chars
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

- **MCP Tool API Consolidation**: 61 tools → 7 primary tools (recall, store, feedback, vault, docs, admin, check_system_health)
- >80% context window reduction (~6100 → ~900 tokens per session)
- All 61 original tool names work as backward-compatible dispatch aliases
- Updated MCP server instructions for consolidated API
- 6 new router files for action-based dispatch

## [2.0.9] - 2026-03-28

### Added

- OpenClaw plugin expanded from 8 → 17 tools with lifecycle hooks
- Tool descriptions include trigger conditions
- Stop hook: switched to retrospective injection API
- Statusline: learning effectiveness metric

### Fixed

- `engram_decisions` uses dedicated endpoint
- `memory_forget` defaults to suppress (reversible)
- Suppress action: RowsAffected check + cache invalidation
- Session outcome recording uses Claude session ID string

### Changed

- Removed 7 redundant MCP tool registrations (68 → 61)

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

- **MCP instructions** — `buildInstructions()` returns comprehensive usage guide for all 48+ tools on `initialize` — any MCP client instantly knows how to use engram
- **Marketplace auto-sync** — GitHub Actions workflow syncs `plugin/` to `thebtf/engram-marketplace` on push to main

### Fixed

- **Observation extraction in Docker** — replaced Claude CLI dependency (`claude --print`) with OpenAI-compatible LLM API (`ENGRAM_LLM_URL`). Observation pipeline was completely non-functional in Docker deployments where Claude CLI is not installed.
- **MCP panic recovery** — added panic recovery with zerolog logging in Streamable HTTP handler
- **FalkorDB int64 panic** — convert int64 to int for falkordb-go ParameterizedQuery params
- LLM client URL normalization — handles both `http://host:port` and `http://host:port/v1` formats
- LLM client fallback env var — now correctly reads `ENGRAM_EMBEDDING_BASE_URL` (was `ENGRAM_EMBEDDING_URL`)
- Configurable LLM concurrency (`ENGRAM_LLM_CONCURRENCY`), timeout, and retry with backoff for transient errors
- Reranking API key optional for TEI/direct backends; batch size configurable via `ENGRAM_RERANKING_BATCH_SIZE`

### Changed

- Plugin version bumped to 0.5.1

## [0.3.0] - 2026-03-07

### Added

- Collection MCP tools: `list_collections`, `list_documents`, `get_document`, `ingest_document`, `search_collection`, `remove_document` — YAML-configurable knowledge bases with smart chunking
- `import_instincts` MCP tool — import ECC instinct files as guidance observations with semantic dedup
- Unified document search integration — `search` tool now includes document results when `type="documents"` or empty
- Per-session utility signal detection for self-learning

### Fixed

- AI review findings for collection tools and instinct import

### Changed

- README complete documentation rewrite

## [0.2.0] - 2026-03-07

### Added

- HTTP logs endpoint (`/api/logs`)
- JavaScript plugin hooks replacing Go binaries — simpler deployment, no build needed

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
  - Relevance scoring: decay × access × relations × importance × confidence
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

[Unreleased]: https://github.com/thebtf/engram/compare/v2.4.0...HEAD
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
