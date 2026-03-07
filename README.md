[English](README.md) | [Русский](README.ru.md)

[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go)](https://go.dev/)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-17-4169E1?logo=postgresql)](https://www.postgresql.org/)
[![Docker](https://img.shields.io/badge/Docker-ready-2496ED?logo=docker)](https://www.docker.com/)
[![CI](https://github.com/thebtf/engram/actions/workflows/docker-publish.yml/badge.svg)](https://github.com/thebtf/engram/actions/workflows/docker-publish.yml)
[![License](https://img.shields.io/github/license/thebtf/engram)](LICENSE)

# Engram

Persistent shared memory infrastructure for Claude Code workstations.

Engram captures observations from coding sessions, stores them in PostgreSQL with pgvector, and exposes **48 MCP tools** — hybrid search, knowledge graph, memory consolidation, and session indexing across multiple workstations.

---

## Architecture

Single server port (`37777`) serves the HTTP API, MCP transports, and web dashboard placeholder.

```mermaid
graph TB
    subgraph "Workstation A"
        CC_A[Claude Code]
        H_A[Hooks / MCP Plugin]
        CC_A --> H_A
    end

    subgraph "Workstation B"
        CC_B[Claude Code]
        H_B[Hooks / MCP Plugin]
        CC_B --> H_B
    end

    H_A -- "Streamable HTTP / SSE" --> Server
    H_B -- "Streamable HTTP / SSE" --> Server

    subgraph "Engram Server :37777"
        Server[Worker]
        Server --> |HTTP API| API[REST Endpoints]
        Server --> |MCP| MCP_T["SSE + Streamable HTTP"]
        Server --> |Web| Dash["Dashboard *"]
    end

    Server --> PG[(PostgreSQL 17\n+ pgvector)]

    style Dash stroke-dasharray: 5 5
```

\* Dashboard is a placeholder — planned for a future release.

**Server** (Docker on remote host / Unraid / NAS):
- PostgreSQL 17 with pgvector extension
- Worker — HTTP API, MCP SSE, MCP Streamable HTTP (`POST /mcp`), dashboard, consolidation scheduler

**Client** (each workstation):
- Hooks — capture observations from Claude Code sessions
- MCP Plugin — connects Claude Code to the remote server via Streamable HTTP or SSE

---

## Features

| Feature | Description |
|---------|-------------|
| **PostgreSQL + pgvector** | Concurrent storage with HNSW cosine vector index |
| **Hybrid Search** | tsvector GIN + vector similarity + BM25, RRF fusion |
| **48 MCP Tools** | Search, context, relations, bulk ops, sessions, maintenance, collections |
| **Memory Consolidation** | Daily decay, daily associations, quarterly forgetting |
| **17 Relation Types** | Knowledge graph: causes, fixes, supersedes, contradicts, explains, shares_theme... |
| **Session Indexing** | JSONL parser with workstation isolation, incremental indexing |
| **Collections** | YAML-configurable knowledge bases with smart chunking (Markdown, Go, Python, TypeScript via tree-sitter) |
| **MCP Transports** | SSE + Streamable HTTP (`POST /mcp`) on single port |
| **Embeddings** | Local ONNX BGE (384D) or OpenAI-compatible REST API |
| **Cross-encoder Reranking** | ONNX reranker for search result quality |
| **Token Auth** | Bearer authentication for all endpoints |
| **Instinct Import** | Import ECC instincts as guidance observations with semantic dedup |
| **Self-Learning** | Per-session utility signal detection for adaptive memory |
| **Dashboard** | Web dashboard at worker port *(placeholder — planned)* |

---

## Quick Start

```bash
git clone https://github.com/thebtf/engram.git
cd engram

# Configure
cp .env.example .env   # edit with your settings

docker compose up -d
```

This starts PostgreSQL 17 + pgvector and the Engram server at `http://your-server:37777`.

Verify:

```bash
curl http://your-server:37777/health
```

**Existing PostgreSQL?** Run only the server container and set `DATABASE_DSN`:

```bash
DATABASE_DSN="postgres://user:pass@your-pg:5432/engram?sslmode=disable" \
  docker compose up -d server
```

Then configure MCP (see [Installation](#installation) below).

---

## Installation

### Plugin Install (recommended)

The plugin registers the MCP server automatically. Set two environment variables and install:

```bash
# Set environment variables (read by Claude Code at runtime)
# Linux/macOS: add to shell profile; Windows: set as System Environment Variables
ENGRAM_URL=http://your-server:37777/mcp
ENGRAM_API_TOKEN=your-api-token
```

```
/plugin marketplace add thebtf/engram-marketplace
/plugin install engram
```

Restart Claude Code. The plugin provides hooks, skills, and MCP connection — all configured.

### Manual MCP Configuration

If not using the plugin, configure MCP directly. Engram exposes three transports on the same port:

| Transport | Endpoint | Protocol | Best For |
|-----------|----------|----------|----------|
| **Streamable HTTP** | `POST /mcp` | JSON-RPC over HTTP | Direct connection (recommended) |
| **SSE** | `GET /sse` + `POST /message` | Server-Sent Events | Long-lived streaming |
| **Stdio Proxy** | local binary | stdio to SSE bridge | Clients that only support stdio |

#### Streamable HTTP (recommended)

Add to `~/.claude/settings.json` (user scope) or `.claude/settings.json` (project scope):

```json
{
  "mcpServers": {
    "engram": {
      "type": "url",
      "url": "http://your-server:37777/mcp",
      "headers": {
        "Authorization": "Bearer ${ENGRAM_API_TOKEN}"
      }
    }
  }
}
```

Claude Code expands `${VAR}` references from your environment at runtime. You can also use literal values.

**CLI shortcut:**

```bash
claude mcp add-json engram '{"type":"http","url":"http://your-server:37777/mcp","headers":{"Authorization":"Bearer ${ENGRAM_API_TOKEN}"}}' -s user
```

#### SSE Transport

Use `http://your-server:37777/sse` as the URL:

```json
{
  "mcpServers": {
    "engram": {
      "type": "url",
      "url": "http://your-server:37777/sse",
      "headers": {
        "Authorization": "Bearer ${ENGRAM_API_TOKEN}"
      }
    }
  }
}
```

#### Stdio Proxy (legacy)

For clients that only support stdio. Requires `mcp-stdio-proxy` binary:

```json
{
  "mcpServers": {
    "engram": {
      "command": "/path/to/mcp-stdio-proxy",
      "args": ["--url", "http://your-server:37777", "--token", "your-api-token"]
    }
  }
}
```

### Client Binaries (optional)

Only needed if using the **stdio proxy** or **hooks**. Streamable HTTP / SSE transports work without any client-side binaries.

**Script install (macOS / Linux):**

```bash
curl -sSL https://raw.githubusercontent.com/thebtf/engram/main/scripts/install.sh | bash
```

**Build from source (Windows PowerShell):**

```powershell
git clone https://github.com/thebtf/engram.git && cd engram

$env:CGO_ENABLED = "1"
go build -tags fts5 -ldflags "-s -w" -o bin\mcp-stdio-proxy.exe .\cmd\mcp-stdio-proxy
```

Hooks are JavaScript and come pre-configured with the plugin. No build needed.

---

## Configuration

### Server

| Variable | Default | Description |
|----------|---------|-------------|
| `DATABASE_DSN` | — | PostgreSQL connection string **(required)** |
| `DATABASE_MAX_CONNS` | `10` | Maximum database connections |
| `WORKER_PORT` | `37777` | Worker port |
| `WORKER_HOST` | `0.0.0.0` | Worker bind address |
| `API_TOKEN` | — | Bearer token (recommended for remote) |
| `EMBEDDING_PROVIDER` | `onnx` | `onnx` (local BGE) or `openai` (REST API) |
| `EMBEDDING_BASE_URL` | — | OpenAI-compatible endpoint URL |
| `EMBEDDING_API_KEY` | — | API key for OpenAI provider |
| `EMBEDDING_MODEL_NAME` | — | Model name for OpenAI provider |
| `EMBEDDING_DIMENSIONS` | `384` | Embedding vector dimensions |
| `RERANKING_ENABLED` | `true` | Enable cross-encoder reranking |
| `ENGRAM_LLM_URL` | — | OpenAI-compatible LLM endpoint for observation extraction |
| `ENGRAM_LLM_API_KEY` | — | API key for LLM endpoint |
| `ENGRAM_LLM_MODEL` | `gpt-4o-mini` | Model name for observation extraction |

### Client (hooks only)

These variables are used by the client-side hooks, **not** for MCP transport configuration. MCP connection is configured in `settings.json` (see [Installation](#installation)).

| Variable | Default | Description |
|----------|---------|-------------|
| `ENGRAM_URL` | — | Full MCP endpoint URL for plugin |
| `ENGRAM_API_TOKEN` | — | API token for plugin |
| `ENGRAM_WORKER_HOST` | `127.0.0.1` | Worker address for hooks |
| `ENGRAM_WORKER_PORT` | `37777` | Worker port for hooks |
| `ENGRAM_SESSIONS_DIR` | `~/.claude/projects/` | Session JSONL directory |
| `ENGRAM_WORKSTATION_ID` | auto-generated | Override workstation ID (8-char hex) |
| `ENGRAM_CONTEXT_OBSERVATIONS` | `100` | Max memories per session |
| `ENGRAM_CONTEXT_FULL_COUNT` | `25` | Memories with full detail |

---

## MCP Tools (48)

44 always-available tools, 4 conditional (require document store), plus `import_instincts` (always available, uses embeddings for dedup).

<details>
<summary><strong>Search & Discovery (11)</strong></summary>

| Tool | Description |
|------|-------------|
| `search` | Hybrid semantic + full-text search across all memories |
| `timeline` | Browse observations by time range |
| `decisions` | Find architecture and design decisions |
| `changes` | Find code modifications and changes |
| `how_it_works` | System understanding queries |
| `find_by_concept` | Find observations by concept tags |
| `find_by_file` | Find observations related to a file |
| `find_by_type` | Find observations by type |
| `find_similar_observations` | Vector similarity search |
| `find_related_observations` | Graph-based relation traversal |
| `explain_search_ranking` | Debug search result ranking |

</details>

<details>
<summary><strong>Context Retrieval (4)</strong></summary>

| Tool | Description |
|------|-------------|
| `get_recent_context` | Recent observations for a project |
| `get_context_timeline` | Context organized by time periods |
| `get_timeline_by_query` | Query-filtered timeline |
| `get_patterns` | Detected recurring patterns |

</details>

<details>
<summary><strong>Observation Management (9)</strong></summary>

| Tool | Description |
|------|-------------|
| `get_observation` | Get observation by ID |
| `edit_observation` | Modify observation fields |
| `tag_observation` | Add/remove concept tags |
| `get_observations_by_tag` | Find observations by tag |
| `merge_observations` | Merge duplicates |
| `bulk_delete_observations` | Batch delete |
| `bulk_mark_superseded` | Mark as superseded |
| `bulk_boost_observations` | Boost importance scores |
| `export_observations` | Export as JSON |

</details>

<details>
<summary><strong>Analysis & Quality (11)</strong></summary>

| Tool | Description |
|------|-------------|
| `get_memory_stats` | Memory system statistics |
| `get_observation_quality` | Quality score for an observation |
| `suggest_consolidations` | Suggest observations to merge |
| `get_temporal_trends` | Trend analysis over time |
| `get_data_quality_report` | Data quality metrics |
| `batch_tag_by_pattern` | Auto-tag by pattern matching |
| `analyze_search_patterns` | Search usage analytics |
| `get_observation_relationships` | Relation graph for an observation |
| `get_observation_scoring_breakdown` | Scoring formula breakdown |
| `analyze_observation_importance` | Importance analysis |
| `check_system_health` | System health check |

</details>

<details>
<summary><strong>Sessions (2)</strong></summary>

| Tool | Description |
|------|-------------|
| `search_sessions` | Full-text search across indexed sessions |
| `list_sessions` | List sessions with filtering |

</details>

<details>
<summary><strong>Graph (2)</strong></summary>

| Tool | Description |
|------|-------------|
| `get_graph_neighbors` | Get neighboring nodes in the knowledge graph |
| `get_graph_stats` | Knowledge graph statistics |

</details>

<details>
<summary><strong>Collections & Documents (7)</strong></summary>

| Tool | Description |
|------|-------------|
| `list_collections` | List configured collections with document counts |
| `list_documents` | List documents in a collection |
| `get_document` | Retrieve full document content |
| `ingest_document` | Ingest document: chunk, embed, store |
| `search_collection` | Semantic search across document chunks |
| `remove_document` | Deactivate a document |
| `import_instincts` | Import instinct files as guidance observations |

</details>

<details>
<summary><strong>Consolidation & Maintenance (3)</strong></summary>

| Tool | Description |
|------|-------------|
| `run_consolidation` | Trigger consolidation cycle |
| `trigger_maintenance` | Run maintenance tasks |
| `get_maintenance_stats` | Maintenance statistics |

</details>

---

## Memory Consolidation

### Importance Score (write-time)

Each observation receives an importance score when created:

```
importance = typeWeight * (1 + conceptBonus + feedbackBonus + retrievalBonus + utilityBonus)
```

Type weights: `discovery=0.9`, `decision=0.85`, `pattern=0.8`, `insight=0.75`, `guidance=0.7`, `observation=0.5`, `question=0.4`

### Relevance Score (consolidation)

The consolidation scheduler recalculates relevance periodically:

```
relevance = decay * (0.3 + 0.3*access) * relations * (0.5 + importance) * (0.7 + 0.3*confidence)
```

Where `decay = exp(-0.01 * daysSinceCreation)`.

### Consolidation Cycles

| Cycle | Frequency | Description |
|-------|-----------|-------------|
| **Relevance Decay** | Every 24h | Exponential time decay with access frequency boost |
| **Creative Associations** | Every 24h | Samples observations, computes embedding similarity, discovers relations (CONTRADICTS, EXPLAINS, SHARES_THEME, PARALLEL_CONTEXT) |
| **Forgetting** | Every 90 days | Archives observations below relevance threshold (disabled by default) |

**Forgetting protections** — observations are never archived if:
- Importance score >= 0.7
- Age < 90 days
- Type is `decision` or `discovery`

---

## Session Indexing

Sessions are indexed from Claude Code JSONL files with workstation isolation:

```
workstation_id = sha256(hostname + machine_id)[:8]
project_id     = sha256(cwd_path)[:8]
session_id     = UUID from JSONL filename
composite_key  = workstation_id:project_id:session_id
```

Multiple workstations sharing one server keep sessions isolated while search works across all of them.

---

## Development

```bash
make build            # Build all binaries
make test             # Run tests with race detector
make test-coverage    # Coverage report
make dev              # Run worker in foreground
make install          # Install plugin, register MCP
make uninstall        # Remove plugin
make clean            # Clean build artifacts
```

<details>
<summary><strong>Project Structure</strong></summary>

```
cmd/
  mcp/                MCP stdio server (local direct access)
  mcp-sse/            MCP SSE HTTP server (standalone)
  mcp-stdio-proxy/    stdio -> SSE bridge (client-side)
  worker/             HTTP API + MCP SSE + MCP Streamable HTTP + dashboard
  hooks/              Claude Code lifecycle hooks (legacy Go, see plugin/hooks/)
internal/
  chunking/           AST-aware document chunking (md, Go, Python, TS)
  collections/        YAML collection config + context routing
  instincts/          Instinct parser and import
  config/             Configuration management
  consolidation/      Decay, associations, forgetting
  db/gorm/            PostgreSQL stores + auto-migrations
  embedding/          ONNX BGE + OpenAI REST providers
  graph/              In-memory CSR graph traversal
  mcp/                MCP protocol (server, SSE, Streamable HTTP)
  reranking/          ONNX cross-encoder reranker
  scoring/            Importance + relevance scoring
  search/             Hybrid retrieval + RRF fusion
  sessions/           JSONL parser + indexer
  vector/pgvector/    pgvector client + sync
  worker/             HTTP handlers, middleware, service
pkg/
  hooks/              Hook event client
  models/             Domain models + relation types
  strutil/            Shared string utilities
plugin/               Claude Code plugin definition + marketplace
```

</details>

---

## Platform Support

| Platform | Server (Docker) | Client Plugin | Client Build |
|----------|:-:|:-:|:-:|
| macOS Intel | Yes | Yes | Yes |
| macOS Apple Silicon | Yes | Yes | Yes |
| Linux amd64 | Yes | Yes | Yes |
| Linux arm64 | Yes | Yes | Yes |
| Windows amd64 | WSL2/Docker Desktop | Build from source | Yes |
| Unraid | Docker template | N/A | N/A |

---

## Uninstall

**Server:**

```bash
docker compose down       # stop containers
docker compose down -v    # stop containers and remove data
```

**Client (plugin):**

```
/plugin uninstall engram
```

**Client (script install, macOS/Linux):**

```bash
curl -sSL https://raw.githubusercontent.com/thebtf/engram/main/scripts/install.sh | bash -s -- --uninstall
```

**Client (Windows):**

```powershell
Remove-Item -Recurse -Force "$env:USERPROFILE\.claude\plugins\marketplaces\engram"
```

---

## License

[MIT](LICENSE)

---

Originally based on [claude-mnemonic](https://github.com/lukaszraczylo/claude-mnemonic) by Lukasz Raczylo.
