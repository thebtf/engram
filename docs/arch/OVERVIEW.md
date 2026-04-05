# Architecture Overview

## Project Positioning

`engram` is a fork of [engram](https://github.com/thebtf/engram) with a PostgreSQL-first architecture.
It turns Claude Code conversation history into structured memory that can be searched, scored, summarized, and recalled through MCP tools and an HTTP worker.

The fork targets shared-team usage and production-ready durability:

- It replaces SQLite/sqlite-vec with PostgreSQL + pgvector.
- It adds a multi-workstation access path through MCP SSE.
- It adds lifecycle-driven memory management (decay, association, forgetting).
- It supports OpenAI-compatible REST API embeddings.

## Logical Architecture

```
+---------------------------------------------------------+
|                     Claude Code                          |
|  +----------+  +----------+  +---------------------+    |
|  | JS Hooks |  |   MCP    |  |  MCP SSE Proxy      |    |
|  | (HTTP)   |  | (stdio)  |  | (stdin->POST->SSE)  |    |
|  +----+-----+  +----+-----+  +--------+------------+    |
+-------|--------------|-----------------|-----------------+
        |              |                 |
        v              v                 v
+---------------+ +---------------+ +-------------------+
|   Worker      | |  MCP Server   | |  MCP SSE Server   |
|  :37777       | |  (stdio)      | |  :37778           |
|  HTTP API     | |  nia tools    | |  HTTP SSE         |
|  Dashboard    | |               | |  Token Auth       |
+-------+-------+ +-------+------+ +---------+---------+
        |                 |                   |
        v                 v                   v
+--------------------------------------------------------+
|                PostgreSQL + pgvector                     |
|  +------------+ +----------+ +------------------------+ |
|  | tsvector   | | pgvector | | GORM models            | |
|  | GIN index  | | HNSW idx | | (observations,         | |
|  | (FTS)      | | (cosine) | |  relations, etc.)      | |
|  +------------+ +----------+ +------------------------+ |
+--------------------------------------------------------+
```

## How the System Behaves End-to-End

1. Claude Code lifecycle hooks (JS, executed via node) and tool events write observations through the worker API and session parser.
2. Worker endpoints and background tasks score, route, consolidate, and persist observations and relations in PostgreSQL.
3. MCP tools expose memory operations to Claude on three transport modes:
   - Local stdio transport (`bin/mcp-server`) for per-session operation.
   - HTTP SSE transport (integrated into worker on `:37777/sse`) for remote clients.
   - Streamable HTTP transport (worker on `:37777/mcp`) for Claude Code plugin.
4. Search queries combine lexical search and vector similarity through the hybrid pipeline and return ranked, reranked results.
5. MCP stdio proxy converts hook-style stdio requests into SSE-compatible POSTs when required.
6. The Vue dashboard and status endpoints reflect worker health, memory operations, and consolidation activity.

## Runtime Roles and Binaries

| Binary                | Role |
|-----------------------|------|
| `bin/worker`          | Persistent HTTP daemon on `:37777` with REST API, dashboard, SSE broadcast, consolidation scheduler |
| `bin/mcp-server`      | MCP stdio server exposing `nia` tools (per Claude Code session) |
| `bin/mcp-stdio-proxy` | Stdio bridge that forwards JSON-RPC over POST and SSE |
| `plugin/engram/hooks/*.js` | Six JS lifecycle hooks executed via `node`: `session-start`, `user-prompt`, `post-tool-use`, `subagent-stop`, `stop`, `statusline` |

The MCP stdio server exposes 37 tools across search, memory metadata, and consolidation controls.

## Key Design Decisions

### 1) PostgreSQL over SQLite

**Decision:** Use PostgreSQL + pgvector for all persistence and search.

**Why:**  
The hook-driven and MCP-driven write paths are concurrent and long-running, which requires strong transactional control, better process isolation, and lower contention risk than single-file SQLite.
Hooks run as JS scripts on the client, sending HTTP requests to the remote worker.  
PostgreSQL also provides built-in extension support:
- `tsvector` + GIN index for full-text relevance and filtering.
- `pgvector` + HNSW index for fast approximate nearest-neighbor vector search.

### 2) Hybrid search with RRF

**Decision:** Combine tsvector and vector retrieval, then apply Reciprocal Rank Fusion with `k = 60`.

**Why:**  
FTS and vector retrieval optimize different retrieval goals:
- FTS is precise for keyword recall.
- Vector retrieval captures semantic similarity and conceptual proximity.
Running both and fusing ranks improves robustness and decreases brittle misses across query styles.

### 3) BM25 short-circuit

**Decision:** Skip vector search when FTS indicates a high-confidence answer:
- top FTS score >= 0.85
- and score gap between rank 1 and rank 2 >= 0.15

**Why:**  
This is a latency optimization with a precision bias. Strong lexical matches are often the highest-quality result for exact terms, command names, file names, and IDs.
Vector search is then unnecessary for many routine lookups, reducing tail latency and CPU overhead.

### 4) Hub storage threshold

**Decision:** Persist embeddings only after an observation is accessed `HubThreshold` times (default `5`).

**Why:**  
This delays vector indexing cost and storage writes for fresh or low-value observations while still allowing recall through metadata and FTS.  
The trade-off is intentional: better write efficiency and lower storage pressure early, with delayed semantic visibility for very new items.

### 5) Worker/MCP separation

**Decision:** Keep worker and MCP as separate processes.

**Why:**  
The worker owns background workflows and API responsibilities that are long-lived (dashboard, consolidation scheduler, sessions parser, SSE stream state), while MCP remains lightweight and session-scoped.
This split avoids keeping expensive services alive for each Claude Code session and keeps failure domains isolated.

### 6) Module name unchanged

**Decision:** Retain Go module name `github.com/thebtf/engram`.

**Why:**  
Preserving the import path avoids ecosystem churn and lowers friction for dependent tooling and local automation that still assumes the original module name.

## Why This Fork Exists

The upstream project already demonstrates a useful MCP memory pattern.  
The fork extends that foundation to support:

- shared-memory deployment across machines,
- richer retrieval behavior through hybrid retrieval and consolidation,
- multi-transport tool access (stdio + SSE),
- and configurable persistence/embedding strategies.

## Upstream vs Fork

| Feature | Upstream (engram) | This Fork |
|---------|--------------------------|-----------|
| Database | SQLite + sqlite-vec | PostgreSQL + pgvector |
| FTS | SQLite FTS5 virtual table | PostgreSQL tsvector + GIN |
| Vector search | sqlite-vec | pgvector HNSW (cosine) |
| Multi-workstation | No | Yes (SSE transport + shared DB) |
| Memory lifecycle | No | Decay/associations/forgetting |
| Session indexing | No | JSONL parser (workstation-isolated) |
| Collections | No | YAML-configured namespaces |
| Embedding | ONNX BGE only | OpenAI-compatible REST API |
| Dashboard | No | Vue.js dashboard |
| Auth | No | Bearer token |

## Design Trade-offs and Boundaries

- **Boundary:** The system is optimized around one PostgreSQL cluster per deployment, not eventually-consistent distributed memory.
- **Trade-off:** Hybrid search and consolidation improve answer quality and signal freshness but add complexity in scoring tuning.
- **Assumption:** Worker and MCP instances are trusted per host unless token-based perimeter controls are enabled and secrets are rotated.
