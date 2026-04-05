# Deployment Guide

Engram uses a **client-server architecture**:

- **Server** (Docker on remote host): Worker (API + MCP) + PostgreSQL
- **Client** (local workstation): Claude Code plugin (hooks + HTTP MCP)

```
  ┌─── Workstation A ────────────────┐      ┌─── Server (Docker) ──────────────┐
  │                                  │      │                                  │
  │  Claude Code                     │      │  ┌──────────────────────────┐    │
  │    ├── hooks ──POST──────────────────→  │  │  Worker :37777           │    │
  │    └── plugin ──HTTP─/mcp────────────→  │  │  /api/* (hooks+dashboard)│    │
  │                                  │      │  │  /mcp   (Streamable HTTP)│    │
  ├─── Workstation B ────────────────┤      │  │  /sse   (SSE, legacy)    │    │
  │  (same setup, shared brain)      │      │  └────────────┬─────────────┘    │
  └──────────────────────────────────┘      │               │                  │
                                            │  ┌────────────▼─────────────┐    │
                                            │  │  PostgreSQL + pgvector    │    │
                                            │  │  :5432                    │    │
                                            │  └──────────────────────────┘    │
                                            └──────────────────────────────────┘
```

---

## Server Setup

### Option A: Docker Compose (recommended)

```bash
# Clone and configure
git clone https://github.com/thebtf/engram.git
cd engram

# Create .env file
cat > .env << 'EOF'
POSTGRES_PASSWORD=change-me-in-production
API_TOKEN=your-secret-token
EMBEDDING_PROVIDER=openai
EMBEDDING_BASE_URL=http://localhost:4000/v1
EMBEDDING_API_KEY=your-litellm-key
EMBEDDING_MODEL_NAME=openai/Qwen/Qwen3-Embedding-8B
EMBEDDING_DIMENSIONS=4096
EOF

# Start the stack
docker compose up -d
```

Services started:
| Service | Port | Purpose |
|---------|------|---------|
| `postgres` | 5432 | PostgreSQL 17 + pgvector |
| `server` | 37777 | Worker API + MCP SSE (hooks, dashboard, nia tools) |

Verify:
```bash
curl http://localhost:37777/health
# {"status":"ok", ...}
```

### Option B: Unraid

1. **PostgreSQL**: Install `pgvector/pgvector:pg17` from Community Applications (or use existing PostgreSQL instance). Create database `engram` with user `engram`.

2. **Engram**: Create a Docker container manually or use your own template:
   - Image: `ghcr.io/thebtf/engram:main`
   - Configure `DATABASE_DSN` to point to your PostgreSQL instance
   - Set `ENGRAM_API_TOKEN` for security
   - Configure embedding provider (LiteLLM recommended)
   - Map port `37777`

3. **Enable pgvector** on first run:
   ```sql
   -- Connect to your PostgreSQL and run:
   CREATE EXTENSION IF NOT EXISTS vector;
   ```
   The worker runs this automatically on startup, but your PostgreSQL user needs the `CREATE EXTENSION` privilege.

### Option C: Manual Docker

```bash
# 1. Start PostgreSQL with pgvector
docker run -d --name cmplus-postgres \
  -e POSTGRES_DB=engram \
  -e POSTGRES_USER=engram \
  -e POSTGRES_PASSWORD=change-me \
  -p 5432:5432 \
  -v cmplus-pgdata:/var/lib/postgresql/data \
  pgvector/pgvector:pg17

# 2. Build the server image
docker build --target server -t engram-server .

# 3. Start server (worker + MCP SSE on single port)
docker run -d --name engram-server \
  -e DATABASE_DSN="postgres://engram:change-me@host.docker.internal:5432/engram?sslmode=disable" \
  -e ENGRAM_API_TOKEN="your-secret-token" \
  -e ENGRAM_EMBEDDING_PROVIDER=openai \
  -e ENGRAM_EMBEDDING_BASE_URL=http://host.docker.internal:4000/v1 \
  -e ENGRAM_EMBEDDING_DIMENSIONS=4096 \
  -p 37777:37777 \
  engram-server
```

---

## Client Setup

The client runs locally on each workstation. It connects to the remote server via the engram plugin.

### Option A: Plugin Install (recommended)

1. **Set environment variables** (add to shell profile or system environment):

   **macOS / Linux** (`~/.bashrc` or `~/.zshrc`):
   ```bash
   export ENGRAM_URL=http://your-server:37777/mcp
   export ENGRAM_API_TOKEN=your-secret-token
   ```

   **Windows** (PowerShell as admin):
   ```powershell
   [Environment]::SetEnvironmentVariable("ENGRAM_URL", "http://your-server:37777/mcp", "User")
   [Environment]::SetEnvironmentVariable("ENGRAM_API_TOKEN", "your-secret-token", "User")
   ```

2. **Install the plugin** from [GitHub Releases](https://github.com/thebtf/engram/releases):

   **macOS / Linux:**
   ```bash
   curl -sSL https://raw.githubusercontent.com/thebtf/engram/main/scripts/install.sh | bash
   ```

   **Windows (PowerShell):**
   ```powershell
   irm https://raw.githubusercontent.com/thebtf/engram/main/scripts/install.ps1 | iex
   ```

3. **Restart Claude Code** — the plugin uses Streamable HTTP MCP to connect directly to the server. No proxy binary needed.

4. **Verify** — in Claude Code, run `/engram:doctor` to check connectivity.

### Option B: Manual Setup

1. **Set environment variables** as described in Option A.

2. **Clone or download** the `plugin/` directory from the repo.

3. **Register the plugin** — add to `~/.claude/settings.json`:
   ```json
   {
     "projects": {
       "*": {
         "plugins": ["path/to/engram/plugin"]
       }
     }
   }
   ```

4. **Restart Claude Code.**

### Option C: stdio Proxy (for non-HTTP MCP clients)

If your MCP client does not support HTTP transport, use the stdio-to-SSE proxy:

```json
{
  "mcpServers": {
    "engram": {
      "command": "/path/to/engram-mcp-stdio-proxy",
      "args": ["--url", "http://your-server:37777", "--token", "your-token"]
    }
  }
}
```

> **Note:** Claude Code natively supports HTTP MCP — prefer Option A.

---

## Embedding Configuration

Engram supports two embedding providers:

### LiteLLM + Qwen3-Embedding-8B (recommended)

High-quality 4096-dimensional embeddings via LiteLLM proxy:

```env
ENGRAM_EMBEDDING_PROVIDER=openai
ENGRAM_EMBEDDING_BASE_URL=http://your-litellm:4000/v1
ENGRAM_EMBEDDING_API_KEY=your-key
ENGRAM_EMBEDDING_MODEL_NAME=openai/Qwen/Qwen3-Embedding-8B
ENGRAM_EMBEDDING_DIMENSIONS=4096
```

### Note on Legacy ONNX Provider

The built-in ONNX BGE provider has been removed. Only the OpenAI-compatible REST API provider is available. Set `ENGRAM_EMBEDDING_PROVIDER=openai` and configure `ENGRAM_EMBEDDING_BASE_URL`, `ENGRAM_EMBEDDING_API_KEY`, and `ENGRAM_EMBEDDING_MODEL_NAME`.

> **Note:** Changing embedding dimensions on an existing database triggers migration 020, which **truncates all vector data** and re-creates indexes. This is irreversible.

---

## Environment Variables Reference

### Server Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `DATABASE_DSN` | (required) | PostgreSQL connection string |
| `ENGRAM_WORKER_HOST` | `0.0.0.0` | Worker bind address |
| `ENGRAM_WORKER_PORT` | `37777` | Worker HTTP port (API + MCP) |
| `ENGRAM_API_TOKEN` | (empty) | Auth token for all endpoints |
| `ENGRAM_EMBEDDING_PROVIDER` | `openai` | Embedding provider (`openai`) |
| `ENGRAM_EMBEDDING_BASE_URL` | `https://api.openai.com/v1` | Embedding API URL |
| `ENGRAM_EMBEDDING_API_KEY` | (empty) | Embedding API key |
| `ENGRAM_EMBEDDING_MODEL_NAME` | `text-embedding-3-small` | Model identifier |
| `ENGRAM_EMBEDDING_DIMENSIONS` | `4096` | Vector dimensions |
| `ENGRAM_EMBEDDING_TRUNCATE` | `true` | Truncate embeddings to fit dimensions |
| `ENGRAM_GRAPH_PROVIDER` | (empty) | `falkordb` to enable graph backend |
| `ENGRAM_FALKORDB_ADDR` | (empty) | FalkorDB address (e.g. `falkordb:6379`) |
| `ENGRAM_FALKORDB_PASSWORD` | (empty) | FalkorDB password |
| `ENGRAM_FALKORDB_GRAPH_NAME` | `engram` | FalkorDB graph name |
| `DATABASE_MAX_CONNS` | `10` | PostgreSQL connection pool size |

### Client Variables (set on each workstation)

| Variable | Default | Description |
|----------|---------|-------------|
| `ENGRAM_URL` | (required) | Server MCP endpoint (e.g. `http://server:37777/mcp`) |
| `ENGRAM_API_TOKEN` | (empty) | Auth token (same as server's `ENGRAM_API_TOKEN`) |

---

## Security

- **Always set `ENGRAM_API_TOKEN`** in production. Without it, anyone with network access can read/write your observations.
- Token auth uses constant-time comparison (timing-attack safe).
- `DATABASE_DSN` contains credentials — never commit it to source control.
- The worker binds to `0.0.0.0` by default — restrict with firewall rules or set `ENGRAM_WORKER_HOST=127.0.0.1` for local-only access.

---

## Health Checks

```bash
# Server health
curl http://your-server:37777/health

# MCP Streamable HTTP (with token)
curl -X POST -H "Authorization: Bearer your-token" \
  -H "Content-Type: application/json" \
  http://your-server:37777/mcp

# MCP SSE (legacy, with token)
curl -H "Authorization: Bearer your-token" http://your-server:37777/sse
```

---

## Upgrading

```bash
# Docker Compose
docker compose pull
docker compose up -d

# Unraid
# Update the container from the Docker tab (check for updates)

# Client (macOS/Linux)
curl -sSL https://raw.githubusercontent.com/thebtf/engram/main/scripts/install.sh | bash

# Client (Windows)
irm https://raw.githubusercontent.com/thebtf/engram/main/scripts/install.ps1 | iex
```

Migrations run automatically on startup. No manual database changes needed.
