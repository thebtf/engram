<!-- redoc:start:header -->
[English](README.md) | [Русский](README.ru.md) | [中文](README.zh.md)

[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go)](https://go.dev/)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-17-4169E1?logo=postgresql)](https://www.postgresql.org/)
[![Docker](https://img.shields.io/badge/Docker-ready-2496ED?logo=docker)](https://www.docker.com/)
[![CI](https://github.com/thebtf/engram/actions/workflows/docker-publish.yml/badge.svg)](https://github.com/thebtf/engram/actions/workflows/docker-publish.yml)
[![License](https://img.shields.io/github/license/thebtf/engram)](LICENSE)
<!-- redoc:end:header -->

<!-- redoc:start:intro -->
# Engram

**Persistent shared memory infrastructure for AI coding agents.**

AI coding agents forget everything between sessions. Every new conversation starts from zero — past decisions, bug fixes, architectural choices, and learned patterns are lost. You waste time re-explaining context, and agents repeat the same mistakes.

Engram fixes this. It captures observations from coding sessions, stores them in PostgreSQL with vector embeddings, and automatically injects relevant memories into new sessions. One server, multiple workstations, zero context loss.

Since learning-memory-v4, context injection treats **silence as valid**: when no observation passes the relevance gate, Engram returns an empty relevant-memory block instead of force-filling the prompt with top-importance noise. The inject path also defaults to the **unified retrieval pipeline** (`ENGRAM_INJECT_UNIFIED=true`), so inject and search now share the same scoring/filtering semantics.

**7 consolidated MCP tools** replace 61 legacy tools, cutting context window usage by over 80%. Hybrid search combines full-text, vector similarity, and BM25 with cross-encoder reranking to surface exactly the memories that matter.
<!-- redoc:end:intro -->

---

<!-- redoc:start:whats-new -->
## What's New in v2.4.0

| Version | Highlight |
|---------|-----------|
| **v2.4.0** | LLM-Driven Memory Extraction — `store(action="extract")` from raw content (ADR-005) |
| **v2.3.1** | Embedding Resilience Layer — 4-state circuit breaker with auto-recovery (ADR-004) |
| **v2.3.0** | Reasoning Traces / System 2 Memory — structured reasoning chains with quality scores (ADR-003) |
| **v2.2.0** | Server-side periodic summarizer — no client dependency for consolidation |
| **v2.1.6** | Knowledge graph UX — local mode, search, visual styling |
| **v2.1.4** | Config hot-reload without restart |
| **v2.1.2** | User commands — `/retro`, `/stats`, `/cleanup`, `/export` |
| **v2.1.0** | MCP tool consolidation — 61 to 7 primary tools, >80% context window reduction |

See [Releases](https://github.com/thebtf/engram/releases) for full changelog.
<!-- redoc:end:whats-new -->

---

<!-- redoc:start:architecture -->
## Architecture

Single server on port `37777` serves the HTTP REST API, MCP transports, Vue 3 dashboard, and background workers. Multiple workstations connect via hooks and the MCP plugin.

```mermaid
graph TB
    subgraph "Workstation A"
        CC_A[Claude Code]
        H_A[Hooks + MCP Plugin]
        CC_A --> H_A
    end

    subgraph "Workstation B"
        CC_B[Claude Code]
        H_B[Hooks + MCP Plugin]
        CC_B --> H_B
    end

    H_A -- "Streamable HTTP / SSE" --> Server
    H_B -- "Streamable HTTP / SSE" --> Server

    subgraph "Engram Server :37777"
        Server[Worker]
        Server --> |HTTP API| API[REST Endpoints]
        Server --> |MCP| MCP_T["SSE + Streamable HTTP"]
        Server --> |Web| Dash["Vue 3 Dashboard"]
        Server --> |Background| BG["Summarizer + Insights"]
    end

    Server --> PG[(PostgreSQL 17\n+ pgvector)]
    Server -.-> LLM["LLM API\n(extraction/summarization)"]
    Server -.-> EMB["Embedding API"]
    Server -.-> RR["Reranker API"]
```

**Server** (Docker on remote host / Unraid / NAS):
- PostgreSQL 17 with pgvector (HNSW cosine index)
- Worker — HTTP API, MCP SSE, MCP Streamable HTTP (`POST /mcp`), Vue 3 dashboard, consolidation scheduler, periodic summarizer

**Client** (each workstation):
- Hooks — capture observations from Claude Code sessions (11 lifecycle hooks)
- MCP Plugin — connects Claude Code to the remote server
- Slash commands — `/retro`, `/stats`, `/cleanup`, `/export`, `/setup`, `/doctor`, `/restart`
<!-- redoc:end:architecture -->

---

<!-- redoc:start:features -->
## Features

### Search and Retrieve
- **Hybrid search** — tsvector full-text + pgvector cosine similarity + BM25, fused with Reciprocal Rank Fusion
- **Cross-encoder reranking** — API-based reranker for precision
- **HyDE query expansion** — hypothetical document embeddings for better recall
- **Knowledge graph** — 17 relation types, optional FalkorDB backend, visual explorer
- **Preset queries** — `decisions`, `changes`, `how_it_works` for common lookups

### Store and Organize
- **LLM-driven extraction** — feed raw content, get structured observations (ADR-005)
- **Reasoning traces** — System 2 Memory with structured chains and quality scores (ADR-003)
- **Versioned documents** — collections with history, comments, and semantic search
- **Encrypted vault** — AES-256-GCM credential storage with scoped access
- **Observation merging** — deduplicate and consolidate related memories

### Consolidate and Maintain
- **Memory decay** — daily exponential decay with access frequency boost
- **Creative associations** — discovers CONTRADICTS, EXPLAINS, SHARES_THEME relations
- **Quarterly forgetting** — archives low-relevance observations (protected types exempt)
- **Periodic summarizer** — server-side pattern insight generation, no client dependency
- **Importance scoring** — type-weighted scoring with concept, feedback, and retrieval bonuses

### Resilience and Operations
- **Embedding resilience** — 4-state circuit breaker with auto-recovery (ADR-004)
- **Config hot-reload** — change settings without restart
- **Token budgeting** — context injection respects configurable token limits
- **Closed-loop learning** — A/B injection strategies with outcome tracking
- **Pre-edit guardrails** — recall by_file before modifications

### Dashboard and UX
- **Vue 3 dashboard** — 15 views: observations, search, graph, patterns, sessions, analytics, vault, learning, system health
- **7 slash commands** — `/retro`, `/stats`, `/cleanup`, `/export`, `/setup`, `/doctor`, `/restart`
- **11 lifecycle hooks** — session-start through stop
- **Multi-workstation isolation** — workstation ID scoping with cross-workstation search
<!-- redoc:end:features -->

---

<!-- redoc:start:use-cases -->
## Use Cases

- **Context continuity** — Start a new session and automatically recall relevant decisions, patterns, and prior work
- **Silence over noise** — if nothing is relevant, inject returns an empty relevant-memory block instead of filler observations
- **Unified inject/search semantics** — inject now uses the same retrieval path as search by default (`ENGRAM_INJECT_UNIFIED=true`)
- **Architectural memory** — Query past design decisions before making new ones
- **Pre-edit awareness** — Check what is known about a file before modifying it
- **Pattern detection** — Surface recurring patterns across sessions and workstations
- **Team knowledge sharing** — Multiple workstations sharing one memory server
- **Credential management** — Store and retrieve API keys and secrets without .env files
- **Session retrospectives** — Analyze past sessions for productivity insights
<!-- redoc:end:use-cases -->

---

<!-- redoc:start:quick-start -->
## Quick Start

```bash
git clone https://github.com/thebtf/engram.git
cd engram

# Configure
cp .env.example .env   # edit with your settings

# Start
docker compose up -d
```

This starts PostgreSQL 17 + pgvector and the Engram server at `http://your-server:37777`.

Verify:

```bash
curl http://your-server:37777/health
```

Then install the plugin in Claude Code:

```
/plugin marketplace add thebtf/engram-marketplace
/plugin install engram
```

Set environment variables (read by Claude Code at runtime):

```bash
# Linux/macOS: add to shell profile
# Windows: set as System Environment Variables
ENGRAM_URL=http://your-server:37777/mcp
ENGRAM_AUTH_ADMIN_TOKEN=your-admin-token
```

Restart Claude Code. Memory is now active.
<!-- redoc:end:quick-start -->

---

<!-- redoc:start:installation -->
## Installation

### Plugin Install (recommended)

The plugin registers the MCP server, hooks, and slash commands automatically.

```bash
# Set environment variables first
ENGRAM_URL=http://your-server:37777/mcp
ENGRAM_AUTH_ADMIN_TOKEN=your-admin-token
```

```
/plugin marketplace add thebtf/engram-marketplace
/plugin install engram
```

Restart Claude Code. Everything is configured.

### Docker Compose

```bash
git clone https://github.com/thebtf/engram.git && cd engram
cp .env.example .env   # edit DATABASE_DSN, tokens, embedding config
docker compose up -d
```

**Existing PostgreSQL?** Run only the server container:

```bash
DATABASE_DSN="postgres://user:pass@your-pg:5432/engram?sslmode=disable" \
  docker compose up -d server
```

### Manual MCP Configuration

If not using the plugin, configure MCP directly in `~/.claude/settings.json`:

#### Streamable HTTP (recommended)

```json
{
  "mcpServers": {
    "engram": {
      "type": "url",
      "url": "http://your-server:37777/mcp",
      "headers": {
        "Authorization": "Bearer ${ENGRAM_AUTH_ADMIN_TOKEN}"
      }
    }
  }
}
```

Claude Code expands `${VAR}` from your environment at runtime.

**CLI shortcut:**

```bash
claude mcp add-json engram '{"type":"http","url":"http://your-server:37777/mcp","headers":{"Authorization":"Bearer ${ENGRAM_AUTH_ADMIN_TOKEN}"}}' -s user
```

#### SSE Transport

Use `http://your-server:37777/sse` as the URL (same JSON structure as above).

#### Stdio Proxy (legacy)

For clients that only support stdio:

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

### Build from Source

Requires Go 1.25+ and Node.js (for dashboard).

```bash
git clone https://github.com/thebtf/engram.git && cd engram
make build    # builds dashboard + worker + mcp binaries
make install  # installs plugin + starts worker
```
<!-- redoc:end:installation -->

---

<!-- redoc:start:upgrading -->
## Upgrading from v1.x to v2.x

**Tool consolidation:** 61 legacy tools are consolidated into 7 primary tools. Legacy tool names still work as dispatch aliases but are no longer listed in `tools/list`. Update your workflows to the new API:

| Legacy Tool | v2.x Equivalent |
|-------------|-----------------|
| `search`, `decisions`, `how_it_works`, `find_by_file`, ... | `recall(action="search")`, `recall(action="preset", preset="decisions")`, etc. |
| `edit_observation`, `merge_observations`, ... | `store(action="edit")`, `store(action="merge")`, etc. |
| `get_memory_stats`, `bulk_delete_observations`, ... | `admin(action="stats")`, `admin(action="bulk_delete")`, etc. |

**New environment variables:**
- `ENGRAM_LLM_URL` / `ENGRAM_LLM_API_KEY` / `ENGRAM_LLM_MODEL` — for LLM-driven extraction
- `ENGRAM_ENCRYPTION_KEY` — vault encryption (hex-encoded AES-256)
- `ENGRAM_HYDE_ENABLED` — HyDE query expansion
- `ENGRAM_GRAPH_PROVIDER` — `falkordb` or empty (in-memory)
- `ENGRAM_CONSOLIDATION_ENABLED` / `ENGRAM_SMART_GC_ENABLED` — consolidation features

**Docker image:** Pull the latest from `ghcr.io/thebtf/engram:latest`. Database migrations run automatically on startup.
<!-- redoc:end:upgrading -->

---

<!-- redoc:start:configuration -->
## Configuration

### Server

| Variable | Default | Description |
|----------|---------|-------------|
| `DATABASE_DSN` | — | PostgreSQL connection string **(required)** |
| `DATABASE_MAX_CONNS` | `10` | Maximum database connections |
| `ENGRAM_WORKER_PORT` | `37777` | Server port |
| `ENGRAM_API_TOKEN` | — | Bearer auth token |
| `ENGRAM_AUTH_ADMIN_TOKEN` | — | Admin token |
| `ENGRAM_EMBEDDING_BASE_URL` | — | OpenAI-compatible embedding endpoint |
| `ENGRAM_EMBEDDING_API_KEY` | — | Embedding API key |
| `ENGRAM_EMBEDDING_MODEL_NAME` | — | Embedding model name |
| `ENGRAM_EMBEDDING_DIMENSIONS` | `4096` | Embedding vector dimensions |
| `ENGRAM_LLM_URL` | — | LLM endpoint for extraction/summarization |
| `ENGRAM_LLM_API_KEY` | — | LLM API key |
| `ENGRAM_LLM_MODEL` | `gpt-4o-mini` | LLM model name |
| `ENGRAM_RERANKING_API_URL` | — | Cross-encoder reranker endpoint |
| `ENGRAM_ENCRYPTION_KEY` | — | Vault encryption key (hex-encoded AES-256) |
| `ENGRAM_HYDE_ENABLED` | `false` | Enable HyDE query expansion |
| `ENGRAM_CONTEXT_MAX_TOKENS` | `8000` | Token budget for context injection |
| `ENGRAM_INJECTION_FLOOR` | `0` | Minimum injected count. `0` keeps the silence path active; set `3` for legacy fill behavior |
| `ENGRAM_INJECT_UNIFIED` | `true` | Use the unified retrieval pipeline for inject (disable only for emergency rollback) |
| `ENGRAM_GRAPH_PROVIDER` | — | `falkordb` or empty (in-memory) |
| `ENGRAM_CONSOLIDATION_ENABLED` | `false` | Enable memory consolidation |
| `ENGRAM_SMART_GC_ENABLED` | `false` | Enable smart garbage collection |

### Client (hooks)

| Variable | Default | Description |
|----------|---------|-------------|
| `ENGRAM_URL` | — | Full MCP endpoint URL for plugin |
| `ENGRAM_AUTH_ADMIN_TOKEN` | — | API token for plugin |
| `ENGRAM_WORKSTATION_ID` | auto | Override workstation ID (8-char hex) |
<!-- redoc:end:configuration -->

---

<!-- redoc:start:mcp-tools -->
## MCP Tools

Engram exposes 7 primary tools that consolidate all memory operations. Each tool supports multiple actions.

### `recall` — Search and Retrieve

| Action | Description |
|--------|-------------|
| `search` | Hybrid semantic + full-text search (default) |
| `preset` | Preset queries: `decisions`, `changes`, `how_it_works` |
| `by_file` | Find observations related to specific files |
| `by_concept` | Find by concept tags |
| `by_type` | Find by observation type |
| `similar` | Vector similarity search |
| `timeline` | Browse by time range |
| `related` | Graph-based relation traversal |
| `patterns` | Detected recurring patterns |
| `get` | Get observation by ID |
| `sessions` | Search/list indexed sessions |
| `explain` | Debug search result ranking |
| `reasoning` | Retrieve reasoning traces |

### `store` — Save and Organize

| Action | Description |
|--------|-------------|
| `create` | Store a new observation (default) |
| `edit` | Modify observation fields |
| `merge` | Merge duplicate observations |
| `import` | Bulk import observations |
| `extract` | LLM-driven extraction from raw content |

### `feedback` — Rate and Improve

| Action | Description |
|--------|-------------|
| `rate` | Rate an observation as useful or not |
| `suppress` | Suppress low-quality observations |
| `outcome` | Record outcome for closed-loop learning |

### `vault` — Encrypted Credentials

| Action | Description |
|--------|-------------|
| `store` | Store an encrypted credential |
| `get` | Retrieve a credential |
| `list` | List stored credentials |
| `delete` | Delete a credential |
| `status` | Vault status and health |

### `docs` — Versioned Documents

| Action | Description |
|--------|-------------|
| `create` | Create a document |
| `read` | Read document content |
| `list` | List documents |
| `history` | Version history |
| `comment` | Add comments |
| `collections` | Manage collections |
| `ingest` | Chunk, embed, and store a document |
| `search_docs` | Semantic search across documents |

### `admin` — Bulk Operations and Analytics

21 actions including: `bulk_delete`, `bulk_supersede`, `tag`, `graph`, `stats`, `trends`, `quality`, `export`, `maintenance`, `scoring`, `consolidation`, and more.

### `check_system_health` — System Health

Reports status of all subsystems: database, embeddings, reranker, LLM, vault, graph, consolidation.
<!-- redoc:end:mcp-tools -->

---

<!-- redoc:start:usage -->
## Usage

```python
# Verify connection
check_system_health()

# Search memories
recall(query="authentication architecture")

# Preset queries
recall(action="preset", preset="decisions", query="caching strategy")

# Check file history before editing
recall(action="by_file", files="internal/search/hybrid.go")

# Store an observation
store(content="Switched from Redis to in-memory cache for dev environments", title="Cache strategy change", tags=["architecture", "caching"])

# Extract observations from raw content
store(action="extract", content="<paste raw session notes or code review>")

# Rate a memory
feedback(action="rate", id=123, rating="useful")

# Store a credential
vault(action="store", name="OPENAI_KEY", value="sk-...")

# Retrieve a credential
vault(action="get", name="OPENAI_KEY")
```
<!-- redoc:end:usage -->

---

<!-- redoc:start:troubleshooting -->
## Troubleshooting

| Symptom | Fix |
|---------|-----|
| `check_system_health` shows embeddings unhealthy | Verify `ENGRAM_EMBEDDING_BASE_URL` and API key. The circuit breaker auto-recovers after transient failures. |
| Search returns no results | Check that observations exist: `recall(action="preset", preset="decisions")`. Verify embeddings are healthy. |
| MCP connection refused | Confirm server is running: `curl http://your-server:37777/health`. Check `ENGRAM_URL` in your environment. |
| Vault returns "encryption not configured" | Set `ENGRAM_ENCRYPTION_KEY` (64-char hex string = 32 bytes AES-256). |
| Dashboard not loading | Ensure you built with `make build` (includes dashboard). Check browser console for errors. |
| Plugin not detected after install | Restart Claude Code. Verify `ENGRAM_URL` and `ENGRAM_AUTH_ADMIN_TOKEN` are set as environment variables. |
| High memory usage | Reduce `DATABASE_MAX_CONNS`. Disable consolidation if not needed. Check `ENGRAM_EMBEDDING_DIMENSIONS`. |

Server logs are available at `http://your-server:37777/api/logs`.
<!-- redoc:end:troubleshooting -->

---

<!-- redoc:start:development -->
## Development

```bash
make build            # Build dashboard + all Go binaries
make test             # Run tests with race detector
make test-coverage    # Coverage report
make dev              # Run worker in foreground
make install          # Build + install plugin + start worker
make uninstall        # Remove plugin
make clean            # Clean build artifacts
```

### Project Structure

```
cmd/
  worker/             HTTP API + MCP + dashboard entry point
  mcp/                Standalone MCP server
  mcp-stdio-proxy/    stdio -> SSE bridge
  engram-cli/         CLI client
internal/
  chunking/           AST-aware document chunking
  collections/        YAML collection config
  config/             Configuration with hot-reload
  consolidation/      Decay, associations, forgetting
  crypto/             AES-256-GCM vault encryption
  db/gorm/            PostgreSQL stores + migrations
  embedding/          REST embedding provider + resilience layer
  graph/              In-memory CSR + FalkorDB
  instincts/          Instinct parser and import
  learning/           Self-learning, LLM client
  maintenance/        Background tasks (summarizer, pattern insights)
  mcp/                MCP protocol, 7 primary tool handlers
  privacy/            Secret detection and redaction
  reranking/          Cross-encoder reranker
  scoring/            Importance + relevance scoring
  search/             Hybrid retrieval + RRF fusion
  sessions/           JSONL parser + indexer
  vector/pgvector/    pgvector client
  worker/             HTTP handlers, middleware, service
    sdk/              Observation extraction, reasoning detection
pkg/
  models/             Domain models + relation types
  strutil/            Shared string utilities
plugin/
  engram/             Claude Code plugin (hooks, commands)
ui/                   Vue 3 dashboard SPA
```

### CI Workflows

| Workflow | Description |
|----------|-------------|
| `docker-publish.yml` | Build and publish Docker image to ghcr.io |
| `plugin-publish.yml` | Publish OpenClaw plugin |
| `static.yml` | Deploy website to GitHub Pages |
| `sync-marketplace.yml` | Sync plugin to marketplace |
<!-- redoc:end:development -->

---

<!-- redoc:start:platform-support -->
## Platform Support

| Platform | Server (Docker) | Client Plugin | Build from Source |
|----------|:-:|:-:|:-:|
| macOS Intel | Yes | Yes | Yes |
| macOS Apple Silicon | Yes | Yes | Yes |
| Linux amd64 | Yes | Yes | Yes |
| Linux arm64 | Yes | Yes | Yes |
| Windows amd64 | WSL2 / Docker Desktop | Yes | Yes |
| Unraid | Docker template | N/A | N/A |
<!-- redoc:end:platform-support -->

---

<!-- redoc:start:uninstall -->
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
<!-- redoc:end:uninstall -->

---

<!-- redoc:start:license -->
## License

[MIT](LICENSE)

---

Originally based on [claude-mnemonic](https://github.com/lukaszraczylo/claude-mnemonic) by Lukasz Raczylo.
<!-- redoc:end:license -->
