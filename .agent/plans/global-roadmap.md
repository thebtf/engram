# Global Roadmap: engram → Shared Brain

**Status**: Planning
**Created**: 2026-02-26
**Last Updated**: 2026-02-26

---

## Vision

Transform engram from a single-workstation memory tool into a shared knowledge infrastructure for multiple Claude Code agents across multiple workstations. Single Go binary. Single chosen storage backend initially. No separate services.

### Key principles from analyzed repos
- **qmd**: Collection model + BM25+vector hybrid search + context annotation tree + reranking
- **automem**: Typed relation graph (11 edge types) + memory consolidation lifecycle (decay/cluster/forget) + HippoRAG2 association discovery
- **claude-session-index**: Session JSONL indexing + workstation/project deterministic isolation + FTS5 full-text search

---

## Storage Backend Decision

**User constraint**: "avoid multiple simultaneous storage points — support multiple backends but choose ONE initially"

### Chosen initial stack: PostgreSQL + pgvector

| Layer | Phase 0-2 (Initial) | Phase 5+ (Optional) |
|-------|---------------------|---------------------|
| **Relational** | **PostgreSQL** (replaces SQLite) | — |
| **Vector** | **pgvector** (replaces sqlite-vec) | Qdrant (if scale demands) |
| **Graph** | — | FalkorDB (Phase 5) |

**Rationale:**
- PostgreSQL replaces SQLite entirely: GORM driver swap (`gorm.io/driver/postgres`), same ORM code
- pgvector handles both relational + vector in ONE database — no separate vector service needed
- Eliminates CGO dependency (sqlite-vec required CGO + platform .dll/.so files)
- User already has a running PostgreSQL instance
- Works natively for multi-workstation (TCP connection vs embedded file)
- Qdrant deferred: pgvector sufficient for initial scale, add Qdrant if vectors exceed ~10M

### Backend config
```
DATABASE_DSN=postgres://user:pass@host:5432/mnemonic?sslmode=disable
PGVECTOR_DIMENSIONS=384   # match embedding model
```

---

## Phase Breakdown

### Phase 0: Network & Auth Hardening (MVP prerequisite)
**Goal**: Make the server usable from remote workstations
**Complexity**: T2
**Files**: `pkg/hooks/worker.go`, `internal/worker/service.go`, `internal/worker/middleware.go`, `internal/config/config.go`

Tasks:
- [ ] Add `ENGRAM_WORKER_HOST` env var to config (default: `127.0.0.1`)
- [ ] Replace all 4 hardcoded `127.0.0.1` in `pkg/hooks/worker.go` (lines 45, 150, 239, 265)
- [ ] Wire `TokenAuthMiddleware` in `setupMiddleware()` (`service.go:1150-1178`)
- [ ] Add `ENGRAM_API_TOKEN` env var to config
- [ ] Write `Dockerfile` (multi-stage: build + scratch/alpine runtime)
- [ ] Write `docker-compose.yml` (mnemonic + qdrant services)

### Phase 1: OpenAI-Compatible Embedding
**Goal**: Support external embedding models via LiteLLM-compatible API
**Complexity**: T2
**Files**: `internal/embedding/model.go`, `internal/config/config.go`

Tasks:
- [ ] Add `OpenAIEmbeddingModel` implementing `EmbeddingModel` interface
  - Config: `EMBEDDING_PROVIDER=openai|builtin`, `EMBEDDING_BASE_URL`, `EMBEDDING_API_KEY`, `EMBEDDING_MODEL_NAME`, `EMBEDDING_DIMENSIONS`
  - HTTP client calling `/v1/embeddings` endpoint (OpenAI format)
- [ ] Register in `ModelRegistry` with auto-detection
- [ ] Expose `/v1/models` listing (compatible with LiteLLM proxy)
- [ ] Keep builtin ONNX/BGE as default (no breaking change)

### Phase 2: Qdrant Vector Backend
**Goal**: Replace sqlite-vec with Qdrant for multi-workstation vector search
**Complexity**: T3
**Files**: `internal/vector/interface.go`, new `internal/vector/qdrant/`

Tasks:
- [ ] Extend `vector.Client` interface (already exists) to cover all sqlite-vec operations
- [ ] Implement `internal/vector/qdrant/client.go` (qdrant-go SDK)
  - Collections → one per namespace (observations, patterns, prompts)
  - Payload filtering for project/workstation scoping
- [ ] Wire Qdrant client in `Service` struct via interface (refactor `vectorClient *sqlitevec.Client` → `vectorClient vector.Client`)
- [ ] Update `setupVectorSyncCallbacks()` to work with interface
- [ ] Add `VECTOR_BACKEND` config + factory function `NewVectorClient(cfg)`
- [ ] Add collection namespace support (collections from qmd concept)

### Phase 3: Collection Model + Hybrid Search (from qmd)
**Goal**: Hierarchical namespacing + BM25+vector+RRF search pipeline
**Complexity**: T3
**Files**: New `internal/collections/`, new `internal/chunking/markdown/`, extend `internal/search/`

qmd concepts to port (architecture only, no TypeScript code):

**Collection model** (YAML-based, NOT in DB):
- Collections defined in `~/.config/engram/collections.yml`
- DB stores `collection TEXT` as denormalized column (no FK to collections table)
- Path-prefix context tree: `{ "/": "Notes vault", "/2024": "Daily notes 2024" }`
- Context resolution: prefix match → sort shortest→longest → concatenate with `\n\n`

**Content-addressable storage** (SHA-256 deduplication):
```sql
CREATE TABLE content (hash TEXT PRIMARY KEY, doc TEXT NOT NULL, created_at TIMESTAMPTZ);
CREATE TABLE documents (
    id BIGSERIAL PRIMARY KEY, collection TEXT NOT NULL, path TEXT NOT NULL,
    title TEXT, hash TEXT REFERENCES content(hash), active BOOLEAN DEFAULT true,
    UNIQUE(collection, path)
);
CREATE TABLE content_chunks (
    hash TEXT NOT NULL, seq INTEGER NOT NULL, pos INTEGER NOT NULL,
    model TEXT NOT NULL, embedding VECTOR(N),
    PRIMARY KEY (hash, seq)
);
```

**BM25 hybrid search** (PostgreSQL full-text search):
- `tsvector` column on documents + GIN index (replaces SQLite FTS5)
- BM25 score normalization: `|score| / (1 + |score|)` → [0,1)
- Field weights: path=10, title=5, body=1

**RRF fusion** (pure Go, zero deps):
- `score = weight / (k + rank + 1)`, k=60
- First 2 ranked lists get 2× weight
- Top-rank bonuses: rank=0 → +0.05, rank≤2 → +0.02

**Strong-signal short-circuit** (immediate latency win):
- If BM25 top score ≥ 0.85 AND gap to #2 ≥ 0.15 → skip vector search entirely

**Chunk-level reranking** (3-10× faster than document-level):
- Rerank best chunk per candidate, not full document
- Use existing `internal/reranking/` ONNX service

**Smart markdown chunking** (from qmd `scanBreakPoints`):
- Scored break types: h1=100, h2=90, h3=80, codeblock=80, hr=60, blank=20
- Squared-distance decay: `score × (1 - (dist/window)² × 0.7)`
- Never split inside code fences
- Port as new `internal/chunking/markdown/` variant

**Dynamic MCP instructions** (zero-cost LLM UX improvement):
- On `initialize`, inject collection list + doc counts into MCP `instructions` field
- Prepend `<!-- Context: {context} -->` to every document in MCP responses

Tasks:
- [ ] `internal/collections/` — YAML loader + context resolver
- [ ] DB migration: `content`, `documents`, `content_chunks` tables with pgvector
- [ ] `internal/chunking/markdown/` — smart chunking algorithm
- [ ] `internal/search/` — BM25 (tsvector/tsquery), RRF fusion, short-circuit
- [ ] Update MCP server: dynamic instructions + context injection
- [ ] `COLLECTION_CONFIG` env var pointing to YAML file

### Phase 4: Session Indexing (from claude-session-index)
**Goal**: Index Claude Code JSONL session files for cross-session search
**Complexity**: T3
**Files**: New `internal/sessions/`, new `cmd/session-indexer/`

claude-session-index concepts to port:

**Session isolation key** (deterministic, "don't mix into mush"):
```
workstation_id = sha256(hostname + machine_id)[:8]  // stable across reboots
session_id     = UUID from JSONL filename
project_id     = sha256(cwd_path)[:8]               // project = working directory
composite_key  = workstation_id:project_id:session_id
```

**JSONL schema** (from claude-session-index analysis):
- Each line: `{type, uuid, timestamp, message: {role, content}, sessionId, cwd, gitBranch}`
- Sessions are UUID-named files in `~/.claude/projects/<encoded-path>/`
- Exchange = consecutive (user, assistant) message pair

**DB schema** (new tables):
```sql
CREATE TABLE sessions (
    id TEXT PRIMARY KEY,           -- session UUID
    workstation_id TEXT NOT NULL,  -- hostname-based hash
    project_id TEXT NOT NULL,      -- cwd hash
    project_path TEXT,             -- human-readable path
    git_branch TEXT,
    first_msg_at DATETIME,
    last_msg_at DATETIME,
    exchange_count INTEGER,
    tool_counts TEXT,              -- JSON: {"Bash": 42, "Edit": 18}
    topics TEXT                    -- JSON array of captured topics
);

CREATE VIRTUAL TABLE session_content USING fts5(
    session_id UNINDEXED,
    content,                       -- concatenated exchange text
    tokenize="porter unicode61"
);
```

**Incremental indexing**: compare file mtime vs indexed_at, re-index only changed sessions

Tasks:
- [ ] `internal/sessions/parser.go` — JSONL parser (exchanges, tool extraction)
- [ ] `internal/sessions/indexer.go` — discover + index sessions
- [ ] `internal/sessions/search.go` — FTS5 search + vector search
- [ ] DB migration: `sessions`, `session_content` tables
- [ ] Background indexing goroutine (runs on startup + via API trigger)
- [ ] MCP tools: `search_sessions`, `get_session_context`, `list_sessions`
- [ ] `SESSIONS_DIR` env var (default: `~/.claude/projects/`)
- [ ] `WORKSTATION_ID` env var (override for remote agents)

### Phase 5: Memory Graph Layer (from automem)
**Goal**: Typed relation graph for associative memory
**Complexity**: T4
**Backend**: FalkorDB (first external graph backend)

automem concepts to port (HippoRAG2 + A-MEM):

**15 typed edge types** (from automem config.py lines 170-218):
```
RELATES_TO, LEADS_TO, OCCURRED_BEFORE, SIMILAR_TO, PRECEDED_BY,
PREFERS_OVER, EXEMPLIFIES, CONTRADICTS, REINFORCES, INVALIDATED_BY,
EVOLVED_INTO, DERIVED_FROM, PART_OF, EXPLAINS, SHARES_THEME, PARALLEL_CONTEXT
```
Consolidation adds: `SUMMARIZES` (MetaMemory → Memory), `CONTRASTS_WITH` (mapped to CONTRADICTS).
Total: **16 relationship types** in the full implementation.

**7 canonical memory types**: Decision, Pattern, Preference, Style, Habit, Insight, Context
Classification: regex-based first (fast, free, covers ~60%) → LLM fallback (gpt-4o-mini)

**Relevance score formula** (portable to Go):
```go
decayFactor     = exp(-0.1 * ageDays)           // base_decay_rate = 0.1/day
accessFactor    = exp(-0.05 * accessRecencyDays)  // 1.0 if recently accessed
relFactor       = 1.0 + 0.3*log1p(relCount)     // log-scaled relationship bonus
relevance       = decayFactor * (0.3 + 0.3*accessFactor) * relFactor * (0.5 + importance) * (0.7 + 0.3*confidence)
```

**Hybrid recall search weights** (all configurable via env):
```
vector=0.35, keyword=0.35, tag=0.20, importance=0.10,
confidence=0.05, recency=0.10, exact=0.20, relation=0.25
```

**Consolidation lifecycle** (scheduled, not per-request):
- **Decay** (daily): recalculates `relevance_score` for all non-archived memories
- **Creative associations** (weekly): random sample 20 memories, apply type-pair rules:
  - Two Decisions + low similarity → CONTRADICTS (confidence 0.6)
  - {Insight, Pattern} + similarity > 0.5 → EXPLAINS (confidence 0.7)
  - Any + similarity > 0.7 → SHARES_THEME (confidence = similarity)
  - Within 7 days + similarity < 0.4 → PARALLEL_CONTEXT (confidence 0.5)
- **Clustering** (monthly): DBSCAN-like connected components → MetaMemory nodes for clusters ≥5
- **Controlled forgetting** (quarterly, disabled by default): archive/delete below threshold

**Protection rules** (never auto-delete):
- `importance >= 0.7`
- Age < 90 days (grace period)
- Type in `{Decision, Insight}`
- `protected = true` flag

Tasks:
- [ ] Add FalkorDB client (`internal/graph/falkordb/`)
- [ ] `GRAPH_BACKEND=none|falkordb` config
- [ ] Relation model: typed edges between observations
- [ ] Consolidation scheduler + worker
- [ ] `discover_associations` API endpoint
- [ ] MCP tools: `find_related`, `get_relations`, `consolidate`

### Phase 6: MCP SSE Transport (thin stdio wrapper)
**Goal**: Enable remote MCP clients while keeping existing stdio hooks working
**Complexity**: T2
**Files**: New `cmd/mcp-sse/`, new `cmd/mcp-stdio-proxy/`

Strategy: SSE server + thin stdio-to-SSE proxy (NOT replacing existing stdio MCP)

```
[Claude hooks] → [stdio proxy cmd] → [HTTP SSE endpoint at :37778]
[Remote Claude] → [HTTP SSE endpoint at :37778 directly]
```

Tasks:
- [ ] SSE MCP server in `internal/mcp/sse.go` (parallel to existing stdio)
- [ ] `cmd/mcp-sse/main.go` — starts HTTP SSE MCP server
- [ ] `cmd/mcp-stdio-proxy/main.go` — thin stdio→SSE bridge for existing hooks
- [ ] Auth header passthrough (token from config)
- [ ] Port: `ENGRAM_MCP_SSE_PORT` (default: 37778)

---

## Implementation Order

```
Phase 0 → Phase 1 → Phase 2 → Phase 3 → Phase 4 → Phase 5 → Phase 6
```

Phases 0-2 are prerequisites for Docker deployment (Phase 0) and multi-workstation use (Phase 2).
Phases 3-5 can be parallelized after Phase 2.

---

## Architecture After All Phases

```
                     engram (single Go binary)
                     ┌────────────────────────────────────────┐
                     │                                        │
  Claude hooks ──────▶  HTTP Worker API (:37777)              │
  Remote agents ─────▶  MCP SSE (:37778)       ┌─────────────┤
  Local MCP ──────────▶  MCP stdio              │  Storage    │
                     │                          │  ─────────  │
                     │  Capabilities:           │  SQLite     │
                     │  ✓ Observations          │  + Qdrant   │
                     │  ✓ Sessions (JSONL)      │  (vectors)  │
                     │  ✓ Collections           │  + FalkorDB │
                     │  ✓ BM25 + Vector hybrid  │  (graph)    │
                     │  ✓ Typed relations       └─────────────┤
                     │  ✓ Memory consolidation               │
                     │  ✓ Cross-workstation search           │
                     └────────────────────────────────────────┘
```

---

## Open Questions (resolved)

1. **Which backend first?** → SQLite + Qdrant (vector only swap, relational stays SQLite)
2. **Code port or concept port?** → Concept port only. No Python/TypeScript code.
3. **Separate services?** → No. Single Go binary.
4. **MCP SSE approach?** → Thin stdio wrapper + new SSE endpoint. Both coexist.
5. **Session isolation key?** → `workstation_id:project_id:session_id` (all hash-based, deterministic)
