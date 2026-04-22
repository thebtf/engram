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

Engram fixes this by keeping only the memory primitives that proved reliable in production: explicit issues, documents, memories, behavioral rules, credentials, and API tokens. One server, multiple workstations, zero context loss.

In v5.0.0, session-start inject is simplified to a static composite payload: open issues, always-inject behavioral rules, and recent memories. The old dynamic relevance, graph, reranking, and extraction stack is gone from the main product path.

A reduced static-first MCP surface remains for the surviving entity model, keeping context usage lean while preserving the workflows that actually worked.
<!-- redoc:end:intro -->

---

<!-- redoc:start:whats-new -->
## What's New in v5.0.0

| Version | Highlight |
|---------|-----------|
| **v5.0.0** | Cleaned Baseline — static-only storage, observations split, session-start gRPC + cache fallback |
| **v4.4.0** | Loom tenant — background task execution and daemon-side project event bridge |
| **v4.0.0** | Daemon architecture — muxcore engine, gRPC transport, local persistent daemon, auto-binary plugin |

See [Releases](https://github.com/thebtf/engram/releases) for full changelog.
<!-- redoc:end:whats-new -->

---

<!-- redoc:start:architecture -->
## Architecture

Single server on port `37777` serves the HTTP REST API, gRPC service (via cmux), Vue 3 dashboard, and the static storage/query surface. Each workstation runs a local daemon that connects to the server via gRPC. Multiple Claude Code sessions share one daemon.

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

    H_A -- "stdio / gRPC" --> Server
    H_B -- "stdio / gRPC" --> Server

    subgraph "Engram Server :37777"
        Server[Worker]
        Server --> |HTTP API| API[REST Endpoints]
        Server --> |gRPC| GRPC[Static session-start + tool bridge]
        Server --> |Web| Dash["Vue 3 Dashboard"]
    end

    Server --> PG[(PostgreSQL 17)]
```

**Server** (Docker on remote host / Unraid / NAS):
- PostgreSQL 17
- Worker — HTTP API, gRPC, Vue 3 dashboard, static entity stores

**Client** (each workstation):
- Hooks — session-start, session-end, and related Claude Code lifecycle integrations
- MCP Plugin — connects Claude Code to the local daemon / server bridge
- Slash commands — `/setup`, `/doctor`, `/restart` and memory-related workflows
<!-- redoc:end:architecture -->

---

<!-- redoc:start:features -->
## Features

### Search and Retrieve
- **Static session-start payload** — issues + behavioral rules + memories via gRPC `GetSessionStartContext`
- **Project-scoped memory recall** — simple SQL-backed retrieval for static memories
- **Document search** — versioned documents and collection-backed search remain available

### Store and Organize
- **Memories** — explicit project-scoped notes in the `memories` table
- **Behavioral rules** — always-inject guidance in the `behavioral_rules` table
- **Versioned documents** — collections with history and comments
- **Encrypted vault** — AES-256-GCM credential storage with scoped access
- **Cross-project issues** — explicit operational coordination between agents/projects

### Resilience and Operations
- **Session-start cache fallback** — `${ENGRAM_DATA_DIR}/cache/session-start-{project-slug}.json` is used when the server is temporarily unavailable
- **Version negotiation** — explicit major-version compatibility checks on the session-start path
- **Config hot-reload** — change settings without restart
- **Graceful daemon restart** — binary swap and control socket flow remain in place

### Dashboard and UX
- **Vue 3 dashboard** — focused on the surviving static entity surface
- **Lifecycle hooks** — session-start / session-end and related integrations remain installed
- **Multi-workstation support** — one server, multiple local daemons, shared static memory surface
<!-- redoc:end:features -->

---

<!-- redoc:start:use-cases -->
## Use Cases

- **Context continuity** — start a new session and receive a static session-start block with active issues, behavioral rules, and recent memories
- **Offline fallback** — if the server is temporarily unavailable, the client can reuse the last successful session-start payload with a visible staleness banner
- **Architectural memory** — query explicit memories and documents before making design changes
- **Operational coordination** — agents file and resolve cross-project issues explicitly
- **Credential management** — store and retrieve API keys and secrets without `.env` files
- **Multi-workstation sharing** — multiple local daemons connect to one shared Engram server
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

### Binary Installation (v4+)

Download the engram daemon binary from [GitHub Releases](https://github.com/thebtf/engram/releases):

```bash
# Linux (amd64)
curl -L https://github.com/thebtf/engram/releases/latest/download/engram-linux-amd64 -o engram
chmod +x engram && sudo mv engram /usr/local/bin/

# macOS (Apple Silicon)
curl -L https://github.com/thebtf/engram/releases/latest/download/engram-darwin-arm64 -o engram
chmod +x engram && sudo mv engram /usr/local/bin/

# Windows (amd64) — download engram-windows-amd64.exe, add to PATH
```

Set environment variables:
```bash
export ENGRAM_URL=http://your-server:37777
export ENGRAM_API_TOKEN=your-token
```

Verify: `echo '{"jsonrpc":"2.0","id":1,"method":"ping"}' | engram`

The daemon starts automatically on first use. Multiple Claude Code sessions share one daemon.

### Manual MCP Configuration

If not using the plugin, configure MCP directly in `~/.claude/settings.json`:

#### Stdio (v5 recommended)

```json
{
  "mcpServers": {
    "engram": {
      "command": "engram"
    }
  }
}
```

### Build from Source

Requires Go 1.25+ and Node.js (for dashboard).

```bash
git clone https://github.com/thebtf/engram.git && cd engram
make build    # builds dashboard + daemon + release assets
make install  # installs plugin + starts daemon
```
<!-- redoc:end:installation -->

---

<!-- redoc:start:upgrading -->
## Upgrading to v5.0.0

v5.0.0 is a **breaking cleanup release**.

What changed:
- Engram now uses a static-only storage model for its primary runtime path
- session-start inject is based on static issues + behavioral rules + memories
- the old dynamic learning / graph / reranking / extraction stack is no longer part of the main product path
- client and server now negotiate major-version compatibility for the session-start path

Upgrade steps:
1. upgrade the plugin to `5.0.0`
2. upgrade the daemon to `v5.0.0`
3. restart Claude Code and the daemon
4. verify plugin update detection and session-start cache fallback

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
| `ENGRAM_VAULT_KEY` | — | Canonical vault key for credential encryption |
| `ENGRAM_ENCRYPTION_KEY` | — | Legacy fallback vault key env var |
| `ENGRAM_DATA_DIR` | auto | Daemon data directory (also used for session-start cache path) |

### Client (hooks)

| Variable | Default | Description |
|----------|---------|-------------|
| `ENGRAM_URL` | — | Full MCP/server URL for plugin and hooks |
| `ENGRAM_AUTH_ADMIN_TOKEN` | — | API token for plugin |
| `ENGRAM_API_TOKEN` | — | Legacy fallback token env var for hooks / plugin runtime |
| `ENGRAM_DATA_DIR` | auto | Cache and daemon state directory |
| `ENGRAM_WORKSTATION_ID` | auto | Override workstation ID (8-char hex) |
<!-- redoc:end:configuration -->

---

<!-- redoc:start:mcp-tools -->
## MCP Tools

Engram exposes a reduced static-first MCP surface for the surviving entity model.

Primary categories in v5:
- issues / issue comments
- memories / behavioral rules
- documents
- credentials / vault
- loom background tasks

The old dynamic search / graph / learning-oriented tool surface is no longer the primary v5 path.

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
