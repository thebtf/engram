# Deployment Guide

Claude Mnemonic Plus uses a **client-server architecture**:

- **Server** (Docker on remote host): Worker API + MCP SSE + PostgreSQL
- **Client** (local workstation): hooks + MCP stdio proxy

```
  ┌─── Workstation A ────────────────┐      ┌─── Server (Docker) ──────────────┐
  │                                  │      │                                  │
  │  Claude Code                     │      │  ┌──────────┐  ┌─────────────┐  │
  │    ├── hooks ──POST───────────────────→  │  │  Worker   │  │  MCP SSE    │  │
  │    └── mcp-stdio-proxy ──SSE──────────→  │  │  :37777   │  │  :37778     │  │
  │                                  │      │  └────┬─────┘  └──────┬──────┘  │
  │                                  │      │       │               │         │
  ├─── Workstation B ────────────────┤      │  ┌────▼───────────────▼──────┐  │
  │  (same setup, shared brain)      │      │  │  PostgreSQL + pgvector    │  │
  └──────────────────────────────────┘      │  │  :5432                    │  │
                                            │  └───────────────────────────┘  │
                                            └──────────────────────────────────┘
```

---

## Server Setup

### Option A: Docker Compose (recommended)

```bash
# Clone and configure
git clone https://github.com/thebtf/claude-mnemonic-plus.git
cd claude-mnemonic-plus

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
| `worker` | 37777 | Hook receiver + dashboard |
| `mcp-sse` | 37778 | MCP tools via SSE transport |

Verify:
```bash
curl http://localhost:37777/health
# {"status":"ok", ...}
```

### Option B: Unraid

1. **PostgreSQL**: Install `pgvector/pgvector:pg17` from Community Applications (or use existing PostgreSQL instance). Create database `claude_mnemonic` with user `mnemonic`.

2. **Claude Mnemonic Plus**: Add the template from `deploy/unraid/unleashed-CMPlus.xml`:
   - Go to Docker tab → Add Container → Template URL
   - Paste: `https://raw.githubusercontent.com/thebtf/claude-mnemonic-plus/main/deploy/unraid/unleashed-CMPlus.xml`
   - Configure `DATABASE_DSN` to point to your PostgreSQL instance
   - Set `CLAUDE_MNEMONIC_API_TOKEN` for security
   - Configure embedding provider (LiteLLM recommended)

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
  -e POSTGRES_DB=claude_mnemonic \
  -e POSTGRES_USER=mnemonic \
  -e POSTGRES_PASSWORD=change-me \
  -p 5432:5432 \
  -v cmplus-pgdata:/var/lib/postgresql/data \
  pgvector/pgvector:pg17

# 2. Build the server image
docker build --target server -t cmplus-server .

# 3. Start worker
docker run -d --name cmplus-worker \
  -e DATABASE_DSN="postgres://mnemonic:change-me@host.docker.internal:5432/claude_mnemonic?sslmode=disable" \
  -e CLAUDE_MNEMONIC_API_TOKEN="your-secret-token" \
  -e CLAUDE_MNEMONIC_EMBEDDING_PROVIDER=openai \
  -e CLAUDE_MNEMONIC_EMBEDDING_BASE_URL=http://host.docker.internal:4000/v1 \
  -e CLAUDE_MNEMONIC_EMBEDDING_DIMENSIONS=4096 \
  -p 37777:37777 \
  cmplus-server worker

# 4. Start MCP SSE
docker run -d --name cmplus-mcp-sse \
  -e DATABASE_DSN="postgres://mnemonic:change-me@host.docker.internal:5432/claude_mnemonic?sslmode=disable" \
  -e CLAUDE_MNEMONIC_API_TOKEN="your-secret-token" \
  -e CLAUDE_MNEMONIC_EMBEDDING_PROVIDER=openai \
  -e CLAUDE_MNEMONIC_EMBEDDING_BASE_URL=http://host.docker.internal:4000/v1 \
  -e CLAUDE_MNEMONIC_EMBEDDING_DIMENSIONS=4096 \
  -p 37778:37778 \
  cmplus-server mcp-sse
```

---

## Client Setup

The client runs locally on each workstation. It connects to the remote server.

### Automated Setup (recommended)

**macOS / Linux:**
```bash
curl -sSL https://raw.githubusercontent.com/thebtf/claude-mnemonic-plus/main/scripts/install.sh | bash
```

**Windows (PowerShell):**
```powershell
irm https://raw.githubusercontent.com/thebtf/claude-mnemonic-plus/main/scripts/install.ps1 | iex
```

After installation, configure the remote server connection:

```bash
# Edit Claude Code settings
# ~/.claude/settings.json (macOS/Linux) or %USERPROFILE%\.claude\settings.json (Windows)
```

Add/update the MCP server entry to use the stdio proxy:

```json
{
  "mcpServers": {
    "claude-mnemonic": {
      "command": "<install-dir>/mcp-stdio-proxy",
      "args": [
        "--url", "http://your-server:37778",
        "--token", "your-secret-token"
      ]
    }
  }
}
```

Configure hooks to point to the remote worker:

```bash
# Set environment variables (add to shell profile)
export CLAUDE_MNEMONIC_WORKER_HOST=your-server
export CLAUDE_MNEMONIC_WORKER_PORT=37777
export CLAUDE_MNEMONIC_API_TOKEN=your-secret-token
```

### Manual Setup

1. **Download binaries** from [GitHub Releases](https://github.com/thebtf/claude-mnemonic-plus/releases):
   - `mcp-stdio-proxy` (or `mcp-stdio-proxy.exe` on Windows)
   - `hooks/` directory (session-start, user-prompt, post-tool-use, subagent-stop, stop, statusline)

2. **Place binaries** in a permanent location:
   ```
   ~/.claude/plugins/marketplaces/claude-mnemonic/       (macOS/Linux)
   %USERPROFILE%\.claude\plugins\marketplaces\claude-mnemonic\  (Windows)
   ```

3. **Configure Claude Code** `~/.claude/settings.json`:
   ```json
   {
     "mcpServers": {
       "claude-mnemonic": {
         "command": "/full/path/to/mcp-stdio-proxy",
         "args": ["--url", "http://your-server:37778", "--token", "your-token"]
       }
     }
   }
   ```

4. **Configure hooks** — create or update `~/.claude/settings.json`:
   ```json
   {
     "hooks": {
       "session-start": "/path/to/hooks/session-start",
       "user-prompt": "/path/to/hooks/user-prompt",
       "post-tool-use": "/path/to/hooks/post-tool-use",
       "subagent-stop": "/path/to/hooks/subagent-stop",
       "stop": "/path/to/hooks/stop"
     }
   }
   ```

5. **Set environment variables** for hooks to find the server:
   ```bash
   # ~/.bashrc, ~/.zshrc, or Windows Environment Variables
   export CLAUDE_MNEMONIC_WORKER_HOST=your-server
   export CLAUDE_MNEMONIC_WORKER_PORT=37777
   export CLAUDE_MNEMONIC_API_TOKEN=your-secret-token
   ```

---

## Embedding Configuration

Claude Mnemonic Plus supports two embedding providers:

### LiteLLM + Qwen3-Embedding-8B (recommended)

High-quality 4096-dimensional embeddings via LiteLLM proxy:

```env
CLAUDE_MNEMONIC_EMBEDDING_PROVIDER=openai
CLAUDE_MNEMONIC_EMBEDDING_BASE_URL=http://your-litellm:4000/v1
CLAUDE_MNEMONIC_EMBEDDING_API_KEY=your-key
CLAUDE_MNEMONIC_EMBEDDING_MODEL_NAME=openai/Qwen/Qwen3-Embedding-8B
CLAUDE_MNEMONIC_EMBEDDING_DIMENSIONS=4096
```

### Built-in ONNX (BGE v1.5, 384 dims)

Runs locally, no external dependencies. Lower quality but zero-config:

```env
CLAUDE_MNEMONIC_EMBEDDING_PROVIDER=builtin
# No other embedding vars needed
```

> **Note:** Changing embedding dimensions on an existing database triggers migration 020, which **truncates all vector data** and re-creates indexes. This is irreversible.

---

## Environment Variables Reference

| Variable | Default | Description |
|----------|---------|-------------|
| `DATABASE_DSN` | (required) | PostgreSQL connection string |
| `CLAUDE_MNEMONIC_WORKER_HOST` | `0.0.0.0` | Worker bind address |
| `CLAUDE_MNEMONIC_WORKER_PORT` | `37777` | Worker HTTP port |
| `CLAUDE_MNEMONIC_MCP_SSE_PORT` | `37778` | MCP SSE port |
| `CLAUDE_MNEMONIC_API_TOKEN` | (empty) | Auth token (shared by worker + MCP SSE) |
| `CLAUDE_MNEMONIC_EMBEDDING_PROVIDER` | `builtin` | `builtin` or `openai` |
| `CLAUDE_MNEMONIC_EMBEDDING_BASE_URL` | `https://api.openai.com/v1` | Embedding API URL |
| `CLAUDE_MNEMONIC_EMBEDDING_API_KEY` | (empty) | Embedding API key |
| `CLAUDE_MNEMONIC_EMBEDDING_MODEL_NAME` | `text-embedding-3-small` | Model identifier |
| `CLAUDE_MNEMONIC_EMBEDDING_DIMENSIONS` | `1536` | Vector dimensions |
| `DATABASE_MAX_CONNS` | `10` | PostgreSQL connection pool size |

---

## Security

- **Always set `CLAUDE_MNEMONIC_API_TOKEN`** in production. Without it, anyone with network access can read/write your observations.
- Token auth uses constant-time comparison (timing-attack safe).
- `DATABASE_DSN` contains credentials — never commit it to source control.
- The worker binds to `0.0.0.0` by default — restrict with firewall rules or set `CLAUDE_MNEMONIC_WORKER_HOST=127.0.0.1` for local-only access.

---

## Health Checks

```bash
# Worker health
curl http://your-server:37777/health

# MCP SSE (with token)
curl -H "Authorization: Bearer your-token" http://your-server:37778/sse
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
curl -sSL https://raw.githubusercontent.com/thebtf/claude-mnemonic-plus/main/scripts/install.sh | bash

# Client (Windows)
irm https://raw.githubusercontent.com/thebtf/claude-mnemonic-plus/main/scripts/install.ps1 | iex
```

Migrations run automatically on startup. No manual database changes needed.
