# Component Inventory

## Scope and Source of Truth

This document maps all runtime binaries and internal Go components for `engram`.

- Language/runtime target: Go 1.21+
- Data store: PostgreSQL with pgvector and FTS
- Embeddings: OpenAI-compatible REST API
- Protocols: MCP over stdio, HTTP JSON APIs, SSE
- Constraint: MCP stdio paths keep protocol frames on `stdout` and write logs to `stderr`

## Binaries

### `bin/mcp-server` (`cmd/mcp/main.go`)

- **Role**: MCP stdio server exposing all `nia` tools to Claude Code.
- **Transport**: MCP JSON-RPC over `stdin`/`stdout`.
- **Flags**: `--project PROJECT` (required), `--debug`.
- **Startup order**:
  - Load config
  - Load collections YAML
  - Init `gorm.Store` (auto-migrates DB)
  - Init stores: `ObservationStore`, `SummaryStore`, `PromptStore`, `PatternStore`, `RelationStore`, `SessionStore`
  - Init `SessionIndexer` (background) on `~/.claude/projects/` JSONL
  - Init embedding service (OpenAI-compatible REST API)
  - Init `pgvector.Client`
  - Init `ScoreCalculator` and `Recalculator`
  - Init `SearchManager`
  - Start watcher on `~/.engram/settings.json`; file change triggers `os.Exit(0)`
  - Run `mcp.Server.Run(ctx)`
- **Important note**: `maintenanceService=nil` and `consolidationScheduler=nil`; worker owns these.
- **Logging**: `stderr` only

### `bin/worker` (`cmd/worker/main.go`)

- **Role**: Persistent HTTP worker daemon, API for hooks, indexing, consolidation, dashboard.
- **Transport**: HTTP on `:37777` using `chi`.
- **UI**: Vue dashboard embedded as static assets.
- **Startup**: delegates to `internal/worker.NewService(Version)`.
- **Services started**: consolidation scheduler, maintenance service, SSE event bus.
- **Shutdown**: graceful with 30s timeout.

### `bin/mcp-stdio-proxy` (`cmd/mcp-stdio-proxy/`)

- **Role**: stdio-to-SSE bridge.
- **Behavior**: reads MCP JSON-RPC from `stdin`, `POST`s to remote SSE endpoint, returns response via `stdout`.
- **Use**: remote machines that connect to centralized MCP-SSE worker.

## Hooks (`plugin/engram/hooks/`)

All hooks are JavaScript files executed via `node` by Claude Code's plugin system.
Hook registration is defined in `hooks.json`. Each hook calls the remote worker API via HTTP.

1. `session-start.js`
   - `GET /api/context/inject?project=X&cwd=Y`
   - returns observations as `<engram-context>...</engram-context>`
2. `user-prompt.js`
   - `POST` to worker to record user prompt text and prompt number
3. `post-tool-use.js`
   - `POST` to worker to record tool invocations and outputs
4. `subagent-stop.js`
   - `POST` to worker to record subagent completion event
5. `stop.js`
   - `POST` to worker to generate session summary from session activity
6. `statusline.js`
   - `GET` worker status endpoint
   - injects memory count into Claude Code statusline

## Internal Packages

### `internal/config`

- Defines config schema and defaults.
- Path constants:
  - `DataDir = ~/.engram/`
  - `SettingsPath = ~/.engram/settings.json`
- `Default()` returns defaults.
- `Load()` reads JSON config and applies env overrides.
- Exposes helpers such as worker host/port/token, embedding provider, DB DSN.

### `internal/db/gorm`

- `Store` wraps `*gorm.DB` and `*sql.DB`.
- `NewStore(Config{DSN, MaxConns})` runs **19** `gormigrate` migrations.
- Substores:
  - `ObservationStore`
  - `SummaryStore`
  - `PromptStore`
  - `PatternStore`
  - `RelationStore`
  - `SessionStore`
  - `ConflictStore`
  - `DocumentStore`
  - `ScoringStore`

### `internal/search`

- Provides retrieval orchestration and query cache.
- Cache characteristics:
  - key: FNV-64a
  - TTL: 30 seconds
  - max entries: 200
- Concurrency: `singleflight` for duplicate concurrent search operations.
- Cache warming goroutine: top-5 frequent queries every 20 seconds.
- `hybridSearch` pipeline:
  - run FTS/BM25
  - BM25 short-circuit when `score >= 0.85` and `gap >= 0.15`
  - then pgvector
  - then RRF fusion with `k = 60`
- `filterSearch` used as fallback.
- Specialized query boosts for `Decisions`, `Changes`, `HowItWorks`.

### `internal/embedding`

- `NewServiceFromConfig()` creates the OpenAI-compatible embedding service.
- `openai.go`: OpenAI-compatible API embeddings.
  - configurable model and dimension
  - default model `text-embedding-3-small`
  - default dimension `1536`

### `internal/vector/pgvector`

- `Client` wraps `*gorm.DB` and an embedding service.
- `Query(ctx, text, limit, where)`:
  - embeds query text
  - performs cosine search on vectors table
- `Sync(ctx, id, text, docType, project, scope)`:
  - generates vector and stores it
- `IsConnected()` health check.
- `BuildWhereFilter()` helper for SQL filter construction.

### `internal/collections`

- Manages collection metadata and file routing.
- Types:
  - `Collection{Name, Description, PathContext map[string]string}`
  - `Registry{byName map, order []string}`
- `Load(path)` returns empty registry when file is missing.
- `context.go` maps files into collections by path matching.

### `internal/consolidation`

- Scheduler manages memory lifecycle:
  - daily relevance decay pass
  - weekly creative association pass
  - quarterly forgetting pass (opt-in)
- Association generation samples observations and builds relation edges:
  - `CONTRADICTS` from two decision observations with low semantic match
  - `EXPLAINS` from insight+pattern with moderate similarity
  - `SHARES_THEME` when similarity > `0.7`
  - `PARALLEL_CONTEXT` from temporal proximity with low similarity
- Relevance decay formula:
  - `exp(-0.1*ageDays) * (0.3 + 0.3*accessFactor) * relationFactor * (0.5+importance) * (0.7+0.3*confidence)`

### `internal/sessions`

- `Indexer` parses JSONL files from `SESSIONS_DIR` (default `~/.claude/projects/`).
- Uses incremental mtime tracking; unchanged files are skipped.
- Session keys:
  - `workstation_id = sha256(hostname+machine_id)[:8]`
  - `project_id = sha256(cwd_path)[:8]`
  - composite key `workstation_id:project_id:session_id` (`session_id` from filename UUID)

### `internal/chunking`

- `manager.go` routes chunking by extension.
- Implementations:
  - `golang/` AST-based chunking (using `go/ast`, not tree-sitter)
  - `markdown/` header-based chunking
- Shared model: `Chunk{Text, Start, End, Type}`.

### `internal/mcp`

- MCP runtime implementation and SSE handler.
- Registers all `37` `nia` tools.
- Tool handlers delegate into stores, search, scoring, and vector services.

### `internal/worker`

- HTTP route and middleware layer (`auth`, logging).
- Serves embedded Vue static content.
- Exposes SSE event stream for dashboard updates.
- Initializes SDK client and maintenance/consolidation schedulers.

### `internal/scoring`

- `Calculator`: computes importance score from concept weights, observation type, and recency.
- `Recalculator`: periodic background recalculation of scores.

### `internal/pattern`

- Detects patterns from observation sequences.

### `internal/privacy`

- Secrets hardening before persistence.
- Removes API keys, passwords, and tokens from observation text.

### `internal/reranking`

- Cross-encoder reranking layer.
- Blend formula (`RerankingAlpha = 0.7`):
  - final score = `cross_encoder * 0.7 + vector_score * 0.3`

### `internal/watcher`

- fsnotify wrapper.
- Used by MCP stdio server to watch `~/.engram/settings.json` and restart on change.

### `internal/graph`

- Provides observation relationship graph for relation analysis and recomputation.
- In-memory graph structures:
  - `ObservationGraph`
  - `Node`
  - `Edge`
  - CSR representation (`rowPtr`, `colIdx`, `weights`)
- Relation types:
  - `RelationFileOverlap`
  - `RelationSemantic`
  - `RelationTemporal`
  - `RelationConcept`
- Edge detectors:
  - temporal sequence edges
  - concept overlap edges
  - file overlap edges using file Jaccard overlap
  - optional semantic edges with threshold `0.85`
- Controls graph complexity by capping at `MaxEdgesPerNode = 20`.
- Provides neighbor lookup, hub detection by percentile, and aggregate `GraphStats`.

### `internal/maintenance`

- Implements scheduled cleanup operations.
- Executes when enabled in config.
- Operations:
  - clean old observations by retention policy
  - remove stale/superseded observations
  - clean old prompts
  - optimize DB
  - call vector cleanup callback for deleted IDs
- Lifecycle:
  - initial delay: 5 minutes
  - repeating interval from config (minimum 1 hour)
  - stop support and metrics (`Stats`, `RunNow`).

### `internal/update`

- Self-update subsystem:
  - fetch release metadata from GitHub
  - compare versions
  - download checksums/artifacts
  - optional signature verification (`cosign`)
  - tarball extraction and binary replacement
  - optional restart path
- Models: `Release`, `Asset`, `UpdateInfo`, `UpdateStatus`.
- Targets: `worker`, `mcp-server`.

## `pkg` Packages

### `pkg/models`

- Core domain types:
  - `Observation`, `SessionSummary`, `UserPromptWithSession`
  - `ObservationScope`, `ObservationType`, `MemoryType`
  - `RelationType`, `ConflictType`, `PatternType`, `PatternStatus`
- JSON database helpers:
  - `JSONStringArray`
  - `JSONInt64Array`
  - `JSONInt64Map`
  - each supports `sql.Scanner` and `driver.Valuer`

### `pkg/similarity`

- Clustering helpers for pattern-related similarity workflows.

## Component Interaction Diagram

```text
Claude Code
  |
  +-- session-start.js --> GET /api/context/inject ---------> Worker :37777
  |                        <-- observations JSON <-----------+
  |                       builds <engram-context>
  |
  +-- MCP Server (stdio, --project X)
  |     |
  |     +-> ObservationStore -> PostgreSQL (observations table)
  |     +-> SearchManager -> hybridSearch -> [FTS + pgvector + RRF]
  |     +-> SessionIndexer (background) -> JSONL files
  |
  +-- user-prompt.js ----> POST /api/hooks/user-prompt ----> Worker
  +-- post-tool-use.js --> POST /api/hooks/post-tool-use --> Worker
  +-- stop.js -----------> POST /api/hooks/stop -----------> Worker
  +-- statusline.js -----> GET /api/status ----------------> Worker
  |
  +-- (remote) mcp-stdio-proxy --> SSE POST --> mcp-sse :37778 --> PostgreSQL

Worker :37777
  +-> ConsolidationScheduler (daily/weekly/quarterly)
  +-> MaintenanceService
  +-> Vue Dashboard (embedded ui/dist/)
  +-> SSE event bus (real-time dashboard updates)
```

## Component Contracts and Boundaries

- MCP server is session-scoped and lightweight.
- Worker owns long-lived runtime tasks: consolidation, maintenance, dashboard, and SSE state.
- Search pipeline always uses package-level services from config, vector store, and reranker.
- Settings changes are treated as lifecycle events and cause MCP stdio restart.
