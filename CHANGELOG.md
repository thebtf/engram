# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/thebtf/engram/compare/v0.5.1...HEAD
[0.5.1]: https://github.com/thebtf/engram/compare/v0.3.0...v0.5.1
[0.3.0]: https://github.com/thebtf/engram/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/thebtf/engram/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/thebtf/engram/releases/tag/v0.1.0
