# Engram

[![Go](https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go)](https://go.dev/)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-17-4169E1?logo=postgresql)](https://www.postgresql.org/)
[![Docker](https://img.shields.io/badge/Docker-ready-2496ED?logo=docker)](https://www.docker.com/)
[![License](https://img.shields.io/github/license/thebtf/engram)](LICENSE)

Persistent shared memory infrastructure for Claude Code workstations.

Engram captures observations from coding sessions, stores them in PostgreSQL + pgvector, and exposes **40 MCP tools** through the `nia` server — hybrid search, knowledge graph, memory consolidation, and session indexing across multiple workstations.

---

## Architecture

Single server port (`37777`) serves HTTP API, MCP transports, and web dashboard.

```
  Workstation A                 Workstation B
  ┌──────────────┐              ┌──────────────┐
  │  Claude Code  │              │  Claude Code  │
  │  ┌─────────┐  │              │  ┌─────────┐  │
  │  │  Hooks  │  │              │  │  Hooks  │  │
  │  │MCP Proxy│  │              │  │MCP Proxy│  │
  │  └────┬────┘  │              │  └────┬────┘  │
  └───────┼───────┘              └───────┼───────┘
          │ HTTP                         │ HTTP
          └──────────┐    ┌──────────────┘
                     ▼    ▼
            ┌─────────────────────┐
            │   Engram Server     │
            │  ┌───────────────┐  │
            │  │ Worker :37777 │  │
            │  │  HTTP API     │  │
            │  │  MCP SSE      │  │
            │  │  MCP HTTP     │  │
            │  │  Dashboard    │  │
            │  └───────┬───────┘  │
            │          │          │
            │  ┌───────▼───────┐  │
            │  │  PostgreSQL   │  │
            │  │  + pgvector   │  │
            │  └───────────────┘  │
            └─────────────────────┘
```

**Server** (Docker on remote host / Unraid / NAS):
- PostgreSQL 17 with pgvector extension
- Worker — HTTP API, MCP SSE, MCP Streamable HTTP (`POST /mcp`), dashboard, consolidation scheduler

**Client** (each workstation):
- Hooks — capture observations from Claude Code sessions
- MCP Stdio Proxy — bridges Claude Code's stdio MCP to the remote server

---

## Quick Start

### 1. Deploy the Server

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

### 2. Configure MCP

#### With Plugin (recommended)

The plugin registers the MCP server automatically via `.mcp.json`. Set two environment variables and install:

```bash
# Set environment variables (these are read by Claude Code at runtime)
# Linux/macOS: add to shell profile; Windows: set as System Environment Variables
ENGRAM_URL=http://your-server:37777/mcp
ENGRAM_API_TOKEN=your-api-token
```

```
/plugin marketplace add thebtf/engram-marketplace
/plugin install engram
```

Restart Claude Code. The plugin provides hooks, skills, and MCP connection — all configured.

#### Without Plugin (manual)

If not using the plugin, configure MCP directly. Engram exposes three transports on the same port:

| Transport | Endpoint | Protocol | Best For |
|-----------|----------|----------|----------|
| **Streamable HTTP** | `POST /mcp` | JSON-RPC over HTTP | Direct connection (recommended) |
| **SSE** | `GET /sse` + `POST /message` | Server-Sent Events | Long-lived streaming |
| **Stdio Proxy** | local binary | stdio ↔ SSE bridge | Clients that only support stdio |

**Streamable HTTP** — add to `~/.claude/settings.json` (user scope) or `.claude/settings.json` (project scope):

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

**SSE Transport** — use `http://your-server:37777/sse` as the URL instead.

**Stdio Proxy (legacy)** — for clients that only support stdio. Requires `mcp-stdio-proxy` binary:

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

### 3. Install Client Binaries (optional)

Only needed if using the **stdio proxy** or **hooks**. Streamable HTTP / SSE transports work without any client-side binaries.

#### Plugin Install

```
/plugin marketplace add thebtf/engram-marketplace
/plugin install engram
```

#### Script Install (macOS / Linux)

```bash
curl -sSL https://raw.githubusercontent.com/thebtf/engram/main/scripts/install.sh | bash
```

#### Build from Source (Windows PowerShell)

```powershell
git clone https://github.com/thebtf/engram.git && cd engram

$env:CGO_ENABLED = "1"
go build -tags fts5 -ldflags "-s -w" -o bin\mcp-stdio-proxy.exe .\cmd\mcp-stdio-proxy
go build -tags fts5 -ldflags "-s -w" -o bin\hooks\session-start.exe .\cmd\hooks\session-start
go build -tags fts5 -ldflags "-s -w" -o bin\hooks\user-prompt.exe .\cmd\hooks\user-prompt
go build -tags fts5 -ldflags "-s -w" -o bin\hooks\post-tool-use.exe .\cmd\hooks\post-tool-use
go build -tags fts5 -ldflags "-s -w" -o bin\hooks\stop.exe .\cmd\hooks\stop
```

---

## Features

| Feature | Description |
|---------|-------------|
| **PostgreSQL + pgvector** | Concurrent storage with HNSW cosine vector index |
| **Hybrid Search** | tsvector GIN + vector similarity + BM25, RRF fusion |
| **40 MCP Tools** | Search, context, relations, bulk ops, sessions, maintenance |
| **Memory Consolidation** | Daily decay, weekly associations, quarterly forgetting |
| **17 Relation Types** | Knowledge graph: causes, fixes, supersedes, contradicts, explains, shares_theme... |
| **Session Indexing** | JSONL parser with workstation isolation, incremental indexing |
| **Collections** | YAML-configurable knowledge bases with smart chunking |
| **MCP Transports** | SSE + Streamable HTTP (`POST /mcp`) |
| **Embeddings** | Local ONNX BGE (384D) or OpenAI-compatible REST API |
| **Token Auth** | Bearer authentication for all endpoints |
| **Dashboard** | Vue-based web dashboard at worker port |

---

## MCP Tools (40)

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
<summary><strong>Consolidation & Maintenance (3)</strong></summary>

| Tool | Description |
|------|-------------|
| `run_consolidation` | Trigger consolidation cycle |
| `trigger_maintenance` | Run maintenance tasks |
| `get_maintenance_stats` | Maintenance statistics |

</details>

---

## Memory Consolidation

The consolidation scheduler runs three automated cycles:

### Relevance Decay (daily)

```
relevance = decay * (0.3 + 0.3*access) * relations * (0.5 + importance) * (0.7 + 0.3*confidence)
```

### Creative Associations (weekly)

Samples observations, computes embedding similarity, discovers relations:

| Relation | Condition |
|----------|-----------|
| **CONTRADICTS** | Two decisions with low similarity |
| **EXPLAINS** | Insight/pattern pair with moderate similarity |
| **SHARES_THEME** | Any pair with high similarity (>0.7) |
| **PARALLEL_CONTEXT** | Temporal proximity with low similarity |

### Forgetting (quarterly, opt-in)

Archives observations below relevance threshold. Protected:
- Importance score >= 0.7
- Age < 90 days
- Type: `decision` or `discovery`

---

## Configuration

All server variables use the `ENGRAM_` prefix. Config file: `~/.engram/settings.json`.

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

### Client (hooks only)

These variables are used by the client-side hooks, **not** for MCP transport configuration. MCP connection is configured in `settings.json` (see [Configure MCP](#2-configure-mcp) above).

| Variable | Default | Description |
|----------|---------|-------------|
| `ENGRAM_WORKER_HOST` | `127.0.0.1` | Worker address for hooks |
| `ENGRAM_WORKER_PORT` | `37777` | Worker port for hooks |
| `ENGRAM_SESSIONS_DIR` | `~/.claude/projects/` | Session JSONL directory |
| `ENGRAM_WORKSTATION_ID` | auto-generated | Override workstation ID (8-char hex) |
| `ENGRAM_CONTEXT_OBSERVATIONS` | `100` | Max memories per session |
| `ENGRAM_CONTEXT_FULL_COUNT` | `25` | Memories with full detail |

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
  mcp-stdio-proxy/    stdio → SSE bridge (client-side)
  worker/             HTTP API + MCP SSE + MCP Streamable HTTP + dashboard
  hooks/              Claude Code lifecycle hooks
internal/
  chunking/           AST-aware document chunking (md, Go, Python, TS)
  collections/        YAML collection config + context routing
  config/             Configuration management
  consolidation/      Decay, associations, forgetting
  db/gorm/            PostgreSQL stores + auto-migrations
  embedding/          ONNX BGE + OpenAI REST providers
  graph/              In-memory CSR graph traversal
  mcp/                MCP protocol (server, SSE, Streamable HTTP)
  scoring/            Importance + relevance scoring
  search/             Hybrid retrieval + RRF fusion
  sessions/           JSONL parser + indexer
  vector/pgvector/    pgvector client + sync
  worker/             HTTP handlers, middleware, service
pkg/
  hooks/              Hook event client
  models/             Domain models + relation types
plugin/               Claude Code plugin definition + marketplace
```

</details>

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

**Server:** `docker compose down` (add `-v` to remove data)

**Client (macOS/Linux):**
```bash
curl -sSL https://raw.githubusercontent.com/thebtf/engram/main/scripts/install.sh | bash -s -- --uninstall
```

**Client (Windows):**
```powershell
Remove-Item -Recurse -Force "$env:USERPROFILE\.claude\plugins\marketplaces\engram"
```

---

## License

MIT

---

Originally based on [claude-mnemonic](https://github.com/lukaszraczylo/claude-mnemonic) by Lukasz Raczylo
