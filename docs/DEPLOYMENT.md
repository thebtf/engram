# Deployment Guide

Engram uses a **runtime stack architecture**:

- **Server** (Docker on remote host): Worker (API + MCP/gRPC authority) + PostgreSQL
- **Operator Web** (Docker on remote host): separate Nuxt control-plane app proxying `/api/*` to the worker
- **Client** (local workstation): Claude Code plugin (hooks + HTTP MCP)

## Token Model (v6)

Two distinct credential tiers, host-pinned:

- **`ENGRAM_AUTH_ADMIN_TOKEN`** — Operator key. Set ONLY in the server-host
  environment (Docker compose `.env` / Unraid template / systemd unit).
  Grants admin-grade access. **MUST NOT be placed on a workstation.**
- **`ENGRAM_TOKEN`** — Per-workstation API token (worker keycard). Issued
  via the dashboard `/tokens` page after admin login. Each workstation
  gets its own. Lives in `~/.claude/settings.json` env or the plugin's
  `user_config.api_token`. **MUST NOT be set on the server host.**

A workstation that starts with `ENGRAM_URL` set but `ENGRAM_TOKEN` empty
exits with a fatal stderr line — replacing the pre-v6 silent
graceful-degrade to `loom_*-only` that masked PR #203's regression.

Keycard issuance, listing, revocation: `/api/auth/tokens` endpoints.
These require an admin browser session cookie. Bearer-token callers
(operator key OR keycard) are rejected with 403.

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
ENGRAM_AUTH_ADMIN_TOKEN=change-me-operator-key
# Optional but recommended when you use the credential vault:
# ENGRAM_VAULT_KEY=$(openssl rand -hex 32)
#
# Optional semantic memory / LLM backends:
# ENGRAM_EMBEDDING_URL=http://localhost:4000/v1/embeddings
# ENGRAM_EMBEDDING_MODEL=text-embedding
# ENGRAM_EMBEDDING_API_KEY=your-litellm-key
# ENGRAM_LLM_URL=http://localhost:4000/v1/chat/completions
# ENGRAM_LLM_MODEL=chat-default
# ENGRAM_LLM_API_KEY=your-litellm-key
EOF

# Start the stack
docker compose up -d
```

For a source-free host that should only pull runtime images, use:

```bash
docker compose -f deploy/docker-compose.runtime.yml up -d
```

If the host already runs `engram-server` and PostgreSQL separately and you only
need to add the new control plane, use the standalone operator-web rollout:

```bash
export OPERATOR_WEB_PORT=3000
export OPERATOR_WEB_API_TARGET=http://host.docker.internal:37777
docker compose -f deploy/docker-compose.operator-web-standalone.yml up -d
```

The standalone file uses `host.docker.internal:host-gateway` so the new
`operator-web` container can proxy `/api/*` to an already-running server on the
same host without re-wiring the existing backend deployment first.

Services started:
| Service | Port | Purpose |
|---------|------|---------|
| `postgres` | 5432 | PostgreSQL 17 + pgvector |
| `server` | 37777 | Worker API + MCP / gRPC authority |
| `operator-web` | 3000 | New Nuxt operator control plane app |

Recommended operator-facing entrypoint:
- browser UI -> `http://host:3000`
- workstation/plugin traffic -> `http://host:37777`

If the host uses a reverse proxy or different public route, keep the same-origin
rule intact:
- browser route -> `operator-web`
- `/api/*` on that same origin -> proxied to `engram-server`

Verify:
```bash
curl http://localhost:37777/health
# {"status":"ok", ...}

curl http://localhost:3000/login
# HTML login shell from operator-web

curl http://localhost:3000/api/auth/setup-needed
# proxied auth/bootstrap check through operator-web

# optional workstation-side smoke against the deployed origin
pwsh -NoProfile -File scripts/smoke-operator-web-remote.ps1 -BaseUrl http://host:3000 -WorkerBaseUrl http://host:37777
```

### Option B: Unraid

1. **PostgreSQL**: Install `pgvector/pgvector:pg17` from Community Applications (or use existing PostgreSQL instance). Create database `engram` with user `engram`.

2. **Engram Server**: Create a Docker container manually or use your own template:
   - Image: `ghcr.io/thebtf/engram:main`
   - Configure `DATABASE_DSN` to point to your PostgreSQL instance
   - Set `ENGRAM_AUTH_ADMIN_TOKEN` on the server host
   - Optionally set `ENGRAM_VAULT_KEY`, embedding, and LLM env vars
   - Map port `37777`

3. **Operator Web**: Create a second Docker container for the control plane:
   - Image: `ghcr.io/thebtf/engram-operator-web:main`
   - Set `NUXT_PUBLIC_API_BASE=/api`
   - Set `NUXT_ENGRAM_API_TARGET=http://<engram-server-host>:37777`
   - Map port `3000`
   - If `operator-web` runs on the same host as the existing server container,
     the repo now ships a ready-made standalone compose file:
     - `deploy/docker-compose.operator-web-standalone.yml`
     - default upstream target: `http://host.docker.internal:37777`

4. **Enable pgvector** on first run:
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

# 2. Build the runtime images
docker build --target server -t engram-server .
docker build --target operator-web -t engram-operator-web .

# 3. Start server (API + gRPC authority)
docker run -d --name engram-server \
  -e DATABASE_DSN="postgres://engram:change-me@host.docker.internal:5432/engram?sslmode=disable" \
  -e ENGRAM_AUTH_ADMIN_TOKEN="your-operator-key" \
  -e ENGRAM_EMBEDDING_URL=http://host.docker.internal:4000/v1/embeddings \
  -e ENGRAM_EMBEDDING_MODEL=text-embedding \
  -e ENGRAM_EMBEDDING_API_KEY="your-embedding-key" \
  -p 37777:37777 \
  engram-server

# 4. Start operator web
docker run -d --name engram-operator-web \
  -e NITRO_HOST=0.0.0.0 \
  -e NITRO_PORT=3000 \
  -e NUXT_PUBLIC_API_BASE=/api \
  -e NUXT_ENGRAM_API_TARGET=http://host.docker.internal:37777 \
  -p 3000:3000 \
  engram-operator-web
```

---

## Client Setup

The client runs locally on each workstation. It connects to the remote server
through the local `engram` stdio daemon launched by the plugin.

### Option A: Plugin Install (recommended)

1. **Set environment variables** (add to shell profile or system environment):

   **macOS / Linux** (`~/.bashrc` or `~/.zshrc`):
   ```bash
   export ENGRAM_URL=http://your-server:37777
   export ENGRAM_TOKEN=your-workstation-keycard
   ```

   **Windows** (PowerShell as admin):
   ```powershell
   [Environment]::SetEnvironmentVariable("ENGRAM_URL", "http://your-server:37777", "User")
   [Environment]::SetEnvironmentVariable("ENGRAM_TOKEN", "your-workstation-keycard", "User")
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

3. **Restart Claude Code** — the plugin launches the local `engram` stdio daemon,
   which then talks to the remote server using `ENGRAM_URL` + `ENGRAM_TOKEN`.

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

If you need to run the local daemon manually, the same workstation variables
apply:

```json
{
  "mcpServers": {
    "engram": {
      "command": "/path/to/engram",
      "env": {
        "ENGRAM_URL": "http://your-server:37777",
        "ENGRAM_TOKEN": "your-workstation-keycard"
      }
    }
  }
}
```

> **Note:** The admin/operator token (`ENGRAM_AUTH_ADMIN_TOKEN`) belongs only on
> the server host. Workstations use keycards via `ENGRAM_TOKEN`.

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
| `ENGRAM_AUTH_ADMIN_TOKEN` | (empty) | Operator key for browser/admin flows; server-host only |
| `ENGRAM_EMBEDDING_URL` | (empty) | OpenAI-compatible `/v1/embeddings` endpoint |
| `ENGRAM_EMBEDDING_API_KEY` | (empty) | Embedding API key |
| `ENGRAM_EMBEDDING_MODEL` | `text-embedding` | Embedding model identifier |
| `ENGRAM_VAULT_KEY` | (empty) | Vault master key (required only for credential vault) |
| `ENGRAM_LLM_URL` | (empty) | OpenAI-compatible `/v1/chat/completions` endpoint |
| `ENGRAM_LLM_MODEL` | `chat-default` | LLM model identifier |
| `ENGRAM_LLM_API_KEY` | (empty) | LLM API key |
| `DATABASE_MAX_CONNS` | `10` | PostgreSQL connection pool size |

### Client Variables (set on each workstation)

| Variable | Default | Description |
|----------|---------|-------------|
| `ENGRAM_URL` | (required) | Server base URL (e.g. `http://server:37777`) |
| `ENGRAM_TOKEN` | (empty) | Per-workstation keycard issued through the operator UI |

---

## Security

- **Always set `ENGRAM_AUTH_ADMIN_TOKEN`** on the server host in production.
- **Never put `ENGRAM_AUTH_ADMIN_TOKEN` on a workstation.** Workstations use `ENGRAM_TOKEN`.
- Browser/operator traffic should go to `operator-web` on `:3000`; workstation daemon/plugin traffic should go to the server on `:37777`.
- `DATABASE_DSN` contains credentials — never commit it to source control.
- The worker binds to `0.0.0.0` by default — restrict with firewall rules or set `ENGRAM_WORKER_HOST=127.0.0.1` for local-only access.

---

## Health Checks

```bash
# Server health
curl http://your-server:37777/health

# Operator web login shell
curl http://your-server:3000/login

# Proxied operator-web bootstrap check
curl http://your-server:3000/api/auth/setup-needed

# Optional remote workstation smoke
pwsh -NoProfile -File scripts/smoke-operator-web-remote.ps1 -BaseUrl http://your-server:3000 -WorkerBaseUrl http://your-server:37777
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
