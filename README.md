# Engram

**Give Claude Code a memory that actually remembers — now with shared brain infrastructure.**

[![Go](https://img.shields.io/badge/Go-1.24+-00ADD8?style=flat-square&logo=go)](https://go.dev)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-15+-336791?style=flat-square&logo=postgresql)](https://www.postgresql.org)
[![Docker](https://img.shields.io/badge/Docker-ready-2496ED?style=flat-square&logo=docker)](https://www.docker.com)
[![License](https://img.shields.io/github/license/thebtf/engram?style=flat-square)](LICENSE)

---

Fork of [engram](https://github.com/thebtf/engram) extended with PostgreSQL+pgvector backend, hybrid search, memory consolidation lifecycle, session indexing, and MCP SSE transport for multi-workstation shared knowledge.

## How It Works

Engram uses a **client-server architecture**. The heavy lifting (database, search, embedding, consolidation) runs on a server — your workstations only need lightweight hooks and an MCP proxy.

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
            │   Server (Docker)   │
            │  ┌───────────────┐  │
            │  │ Worker :37777 │  │
            │  │ (API+MCP SSE) │  │
            │  └───────┬───────┘  │
            │          │          │
            │  ┌───────▼───────┐  │
            │  │  PostgreSQL   │  │
            │  │  + pgvector   │  │
            │  └───────────────┘  │
            └─────────────────────┘
```

**Server** (Docker on remote host / Unraid / NAS):
- PostgreSQL 15+ with pgvector extension
- Worker — HTTP API, dashboard, MCP SSE, consolidation scheduler (:37777)

**Client** (each workstation):
- Hooks — capture observations from Claude Code sessions, POST to remote worker
- MCP Stdio Proxy — bridges Claude Code's stdio MCP protocol to the remote SSE server

---

## Quick Start

### 1. Deploy the Server

#### Docker Compose (recommended)

```bash
git clone https://github.com/thebtf/engram.git
cd engram

# Optional: configure in .env file
echo 'POSTGRES_PASSWORD=your-secure-password' > .env
echo 'API_TOKEN=your-api-token' >> .env

docker compose up -d
```

This starts two containers:
- **postgres** — PostgreSQL 17 with pgvector (data persisted in Docker volume)
- **server** — Worker (HTTP API + MCP SSE + dashboard) at `http://your-server:37777`

Verify:

```bash
curl http://your-server:37777/health
```

#### Existing PostgreSQL

If you already have PostgreSQL with pgvector, just run the server container:

```bash
# Pull and run directly
docker compose up -d server

# Or override DATABASE_DSN to point to your existing PostgreSQL:
DATABASE_DSN="postgres://user:pass@your-pg-host:5432/engram?sslmode=disable" \
  docker compose up -d server
```

Make sure pgvector extension is enabled:

```sql
\c engram
CREATE EXTENSION IF NOT EXISTS vector;
```

#### Unraid

Install via Docker template:

1. **Add container** in Unraid Docker tab
2. Set **Repository**: `ghcr.io/thebtf/engram:latest` (or build locally)
3. Map port **37777** (worker + MCP SSE)
4. Add path mapping for PostgreSQL data or point `DATABASE_DSN` to your existing PostgreSQL instance
5. Set environment variables:
   - `DATABASE_DSN` = `postgres://user:pass@your-pg:5432/engram?sslmode=disable`
   - `ENGRAM_API_TOKEN` = your token (optional but recommended)
   - `ENGRAM_EMBEDDING_PROVIDER` = `onnx` (default) or `openai`

> **Tip:** If you run PostgreSQL as a separate Unraid container (e.g., the official `postgres` or `pgvector/pgvector` image), use its container IP or Unraid bridge network hostname in `DATABASE_DSN`.

### 2. Set Up the Client

The client runs on each workstation where you use Claude Code. It connects to the server and provides hooks (to capture observations) and MCP tools (to access `nia` tools).

#### Plugin Install (Recommended)

The simplest way to install — works on all platforms (macOS, Linux, Windows):

```
/plugin marketplace add thebtf/engram-marketplace
/plugin install engram
```

Then set environment variables:

```bash
export ENGRAM_URL="http://your-server:37777/mcp"
export ENGRAM_API_TOKEN="your-api-token"
```

Restart Claude Code and verify with `/doctor`.

#### Automatic Install (macOS / Linux)

```bash
curl -sSL https://raw.githubusercontent.com/thebtf/engram/main/scripts/install.sh | bash
```

This downloads pre-built binaries, registers hooks and MCP with Claude Code, and starts the local components.

After install, configure the server connection:

```bash
# Point hooks to your remote worker
export ENGRAM_WORKER_HOST=your-server
export ENGRAM_WORKER_PORT=37777

# Point MCP to your remote SSE server
# (configured automatically if you edit ~/.claude/settings.json — see Manual Setup)
```

#### Automatic Install (Windows PowerShell)

```powershell
# Clone and build client binaries
git clone https://github.com/thebtf/engram.git
cd engram

$env:CGO_ENABLED = "1"
go build -tags fts5 -ldflags "-s -w" -o bin\mcp-stdio-proxy.exe .\cmd\mcp-stdio-proxy
go build -tags fts5 -ldflags "-s -w" -o bin\mcp-server.exe .\cmd\mcp
go build -tags fts5 -ldflags "-s -w" -o bin\hooks\session-start.exe .\cmd\hooks\session-start
go build -tags fts5 -ldflags "-s -w" -o bin\hooks\user-prompt.exe .\cmd\hooks\user-prompt
go build -tags fts5 -ldflags "-s -w" -o bin\hooks\post-tool-use.exe .\cmd\hooks\post-tool-use
go build -tags fts5 -ldflags "-s -w" -o bin\hooks\subagent-stop.exe .\cmd\hooks\subagent-stop
go build -tags fts5 -ldflags "-s -w" -o bin\hooks\stop.exe .\cmd\hooks\stop
go build -tags fts5 -ldflags "-s -w" -o bin\hooks\statusline.exe .\cmd\hooks\statusline

# Install to Claude Code plugin directory
$PluginDir = "$env:USERPROFILE\.claude\plugins\marketplaces\engram"
New-Item -ItemType Directory -Force -Path "$PluginDir\hooks"
New-Item -ItemType Directory -Force -Path "$PluginDir\.claude-plugin"
New-Item -ItemType Directory -Force -Path "$PluginDir\commands"

Copy-Item bin\mcp-stdio-proxy.exe $PluginDir\
Copy-Item bin\mcp-server.exe $PluginDir\
Copy-Item bin\hooks\*.exe "$PluginDir\hooks\"
Copy-Item plugin\hooks\hooks.json "$PluginDir\hooks\"
Copy-Item plugin\commands\* "$PluginDir\commands\" -ErrorAction SilentlyContinue
```

Then follow the manual MCP configuration below.

#### Manual Client Setup (all platforms)

After building or downloading client binaries, register the MCP server in Claude Code.

**Option A: MCP SSE Proxy (recommended for remote server)**

Add to `~/.claude/settings.json` (macOS/Linux) or `%USERPROFILE%\.claude\settings.json` (Windows):

```json
{
  "mcpServers": {
    "engram": {
      "command": "/path/to/mcp-stdio-proxy",
      "args": ["--url", "http://your-server:37777"],
      "env": {}
    }
  }
}
```

This bridges Claude Code's stdio MCP protocol to the remote SSE server. All `nia` tools work transparently.

**Option B: Direct MCP server (for local-only setups)**

If running everything locally (server + client on same machine):

```json
{
  "mcpServers": {
    "engram": {
      "command": "/path/to/mcp-server",
      "args": ["--project", "${CLAUDE_PROJECT}"],
      "env": {
        "DATABASE_DSN": "postgres://user:pass@localhost:5432/engram?sslmode=disable"
      }
    }
  }
}
```

**Hooks configuration** is handled automatically by the install script. For manual setup, copy `plugin/hooks/hooks.json` to your plugin directory and ensure hook binaries are in the `hooks/` subdirectory.

---

## Features

| Feature | Description |
|---------|-------------|
| **PostgreSQL + pgvector** | Scalable, concurrent storage with vector similarity search |
| **Hybrid Search** | Full-text search (tsvector) + vector similarity (pgvector) + RRF fusion |
| **37+ MCP Tools** | Search, timeline, decisions, changes, find_by_concept, bulk ops, health checks |
| **Memory Consolidation** | Automated relevance decay, creative associations, and forgetting lifecycle |
| **Session Indexing** | JSONL session parser with workstation isolation and incremental indexing |
| **Collections** | YAML-configurable collection model with path-based context routing |
| **Smart Chunking** | AST-aware Go chunker + regex-based Python/TypeScript chunkers |
| **MCP SSE Transport** | HTTP SSE server for remote MCP access across workstations |
| **OpenAI-Compatible Embeddings** | Local ONNX BGE (384D) or any OpenAI-compatible REST API |
| **Token Authentication** | Bearer token auth for worker and SSE endpoints |
| **Dashboard** | Vue-based web dashboard at worker port |
| **17 Relation Types** | Knowledge graph edges (causes, fixes, supersedes, contradicts, explains, etc.) |

---

## Configuration

All variables use the `ENGRAM_` prefix. Environment variables override config file values.

Config file location: `~/.engram/settings.json`

### Server Settings

| Variable | Default | Description |
|----------|---------|-------------|
| `DATABASE_DSN` | — | PostgreSQL connection string (**required**) |
| `DATABASE_MAX_CONNS` | `10` | Maximum database connections |
| `WORKER_PORT` | `37777` | Worker HTTP API and dashboard port |
| `WORKER_HOST` | `0.0.0.0` | Worker bind address |
| `API_TOKEN` | — | Bearer token for API authentication (recommended for remote) |
| `EMBEDDING_PROVIDER` | `onnx` | Provider: `onnx` (local BGE) or `openai` (REST API) |
| `EMBEDDING_BASE_URL` | `https://api.openai.com/v1` | OpenAI-compatible endpoint URL |
| `EMBEDDING_API_KEY` | — | API key for OpenAI provider |
| `EMBEDDING_MODEL_NAME` | `text-embedding-3-small` | Model name for OpenAI provider |
| `EMBEDDING_DIMENSIONS` | `384` | Embedding vector dimensions |
| `RERANKING_ENABLED` | `true` | Enable cross-encoder reranking |
| `RERANKING_CANDIDATES` | `100` | Candidate results before reranking |
| `RERANKING_RESULTS` | `10` | Final results after reranking |

### Client Settings

| Variable | Default | Description |
|----------|---------|-------------|
| `WORKER_HOST` | `127.0.0.1` | Worker address (set to server IP for remote) |
| `WORKER_PORT` | `37777` | Worker port |
| `API_TOKEN` | — | Must match server token |
| `SESSIONS_DIR` | `~/.claude/projects/` | Claude Code session JSONL directory |
| `WORKSTATION_ID` | auto-generated | Override workstation identifier (8-char hex) |
| `CONTEXT_OBSERVATIONS` | `100` | Maximum memories returned per session |
| `CONTEXT_FULL_COUNT` | `25` | Memories with full detail (rest condensed) |

### Docker Compose `.env` File

```env
POSTGRES_PASSWORD=your-secure-password
POSTGRES_PORT=5432
API_TOKEN=your-api-token
WORKER_PORT=37777
EMBEDDING_PROVIDER=onnx
# For OpenAI-compatible embeddings:
# EMBEDDING_PROVIDER=openai
# EMBEDDING_BASE_URL=https://api.openai.com/v1
# EMBEDDING_API_KEY=sk-...
# EMBEDDING_MODEL_NAME=text-embedding-3-small
# EMBEDDING_DIMENSIONS=384
```

---

## MCP Tools

The `nia` MCP server exposes 37+ tools organized into six categories.

### Search & Discovery

| Tool | Description |
|------|-------------|
| `search` | Hybrid semantic + full-text search across all memories |
| `timeline` | Browse observations by time range |
| `decisions` | Find architecture and design decisions |
| `changes` | Find code modifications and changes |
| `how_it_works` | System understanding queries |
| `find_by_concept` | Find observations matching a concept |
| `find_by_file` | Find observations related to a file |
| `find_by_type` | Find observations by type |
| `find_similar_observations` | Vector similarity search |
| `find_related_observations` | Graph-based relation traversal |
| `explain_search_ranking` | Debug search result ranking |

### Context Retrieval

| Tool | Description |
|------|-------------|
| `get_recent_context` | Recent observations for a project |
| `get_context_timeline` | Context organized by time periods |
| `get_timeline_by_query` | Query-filtered timeline |
| `get_patterns` | Detected recurring patterns |

### Observation Management

| Tool | Description |
|------|-------------|
| `get_observation` | Get a single observation by ID |
| `edit_observation` | Modify observation fields |
| `tag_observation` | Add tags to an observation |
| `get_observations_by_tag` | Find observations by tag |
| `merge_observations` | Merge duplicate observations |
| `bulk_delete_observations` | Batch delete |
| `bulk_mark_superseded` | Mark observations as superseded |
| `bulk_boost_observations` | Boost importance scores |
| `export_observations` | Export observations as JSON |

### Analysis & Quality

| Tool | Description |
|------|-------------|
| `get_memory_stats` | Overall memory statistics |
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

### Sessions

| Tool | Description |
|------|-------------|
| `search_sessions` | Full-text search across indexed sessions |
| `list_sessions` | List sessions with filtering |

### Memory Consolidation

| Tool | Description |
|------|-------------|
| `run_consolidation` | Trigger consolidation cycle (all/decay/associations/forgetting) |
| `trigger_maintenance` | Run maintenance tasks |
| `get_maintenance_stats` | Maintenance statistics |

---

## Memory Consolidation Lifecycle

The consolidation scheduler runs three automated cycles on the server:

### Relevance Decay (daily)

```
relevance = decay * (0.3 + 0.3*access) * relations * (0.5 + importance) * (0.7 + 0.3*confidence)
```

Where `decay = exp(-0.1 * ageDays)` and `access = exp(-0.05 * accessRecencyDays)`.

### Creative Associations (weekly)

Samples observations, computes embedding similarity, discovers relations:

| Relation | Condition |
|----------|-----------|
| **CONTRADICTS** | Two decisions with low similarity |
| **EXPLAINS** | Insight/pattern pair with moderate similarity |
| **SHARES_THEME** | Any pair with high similarity (>0.7) |
| **PARALLEL_CONTEXT** | Temporal proximity with low similarity |

### Forgetting (quarterly, opt-in)

Archives observations below relevance threshold. Protected observations are never archived:

- Importance score >= 0.7
- Age < 90 days
- Type: `decision` or `discovery`

---

## Session Indexing

Sessions are indexed from Claude Code JSONL files with workstation isolation:

```
workstation_id = sha256(hostname + machine_id)[:8]
project_id     = sha256(cwd_path)[:8]
session_id     = UUID from JSONL filename
composite_key  = workstation_id:project_id:session_id
```

Each workstation generates a unique ID automatically. Multiple workstations sharing one database keep their sessions isolated while search works across all of them.

---

## Advanced: Full Local Development

For development or running everything on a single machine without Docker:

```bash
git clone https://github.com/thebtf/engram.git
cd engram
make build      # Build all binaries
make install    # Install plugin, register MCP, start worker
```

This requires PostgreSQL + pgvector running locally. Set `DATABASE_DSN` before running.

### Make Targets

```bash
make build            # Build all binaries
make install          # Install to Claude Code, register plugin, start worker
make uninstall        # Remove plugin and stop worker
make test             # Run tests with race detector
make test-coverage    # Run tests with coverage report
make start-worker     # Start worker in background
make stop-worker      # Stop running worker
make restart-worker   # Restart worker
make dev              # Run worker in foreground (development mode)
make clean            # Clean build artifacts
```

### Project Structure

```
cmd/
  mcp/                MCP stdio server (local direct access)
  mcp-sse/            MCP SSE HTTP server (remote access)
  mcp-stdio-proxy/    stdio → SSE bridge (client-side)
  worker/             HTTP API + dashboard (server-side)
  hooks/              Claude Code lifecycle hooks (client-side)
internal/
  chunking/           Smart document chunking (markdown, Go, Python, TypeScript)
  collections/        YAML collection config + context routing
  config/             Configuration management
  consolidation/      Memory consolidation lifecycle
  db/gorm/            PostgreSQL GORM stores + migrations
  embedding/          ONNX BGE + OpenAI REST embedding providers
  mcp/                MCP server + SSE handler
  scoring/            Importance + relevance scoring
  search/             Hybrid search manager + RRF fusion
  sessions/           JSONL session parser + indexer
  vector/pgvector/    pgvector client + sync
  worker/             HTTP handlers, middleware, SSE
pkg/
  hooks/              Hook event client
  models/             Domain models
plugin/               Claude Code plugin definition
```

---

## Uninstall

### Server

```bash
docker compose down -v    # Stop and remove containers + data
docker compose down       # Stop and remove containers (keep data)
```

### Client (macOS / Linux)

```bash
# Via install script:
curl -sSL https://raw.githubusercontent.com/thebtf/engram/main/scripts/install.sh | bash -s -- --uninstall

# Or via make (if built from source):
make uninstall
```

### Client (Windows PowerShell)

```powershell
$PluginDir = "$env:USERPROFILE\.claude\plugins\marketplaces\engram"
Remove-Item -Recurse -Force $PluginDir -ErrorAction SilentlyContinue

# Remove MCP from settings
$SettingsFile = "$env:USERPROFILE\.claude\settings.json"
if (Test-Path $SettingsFile) {
    $s = Get-Content $SettingsFile | ConvertFrom-Json
    $s.mcpServers.PSObject.Properties.Remove('engram')
    $s | ConvertTo-Json -Depth 10 | Set-Content $SettingsFile
}
```

---

## Platform Support

| Platform | Server (Docker) | Client Install Script | Client Build from Source |
|----------|----------------|----------------------|------------------------|
| macOS Intel | Yes | Yes | Yes |
| macOS Apple Silicon | Yes | Yes | Yes |
| Linux amd64 | Yes | Yes | Yes |
| Linux arm64 | Yes | Yes | Yes |
| Windows amd64 | Via WSL2/Docker Desktop | Build from source | Yes (PowerShell) |
| Unraid | Yes (Docker template) | N/A | N/A |

---

## License

MIT

---

**Originally based on:** [claude-mnemonic](https://github.com/lukaszraczylo/claude-mnemonic) by Lukasz Raczylo
