# Developer Quickstart

Get engram running locally and integrated with Claude Code.

---

## Prerequisites

| Dependency | Version | Purpose |
|------------|---------|---------|
| Go | 1.24+ | Build from source |
| PostgreSQL | 15+ | Primary data store |
| pgvector extension | latest | Vector similarity search |
| CGO | enabled | ONNX runtime for local embeddings |
| make | any | Build system |
| jq | any | Used by install scripts |
| curl | any | Health check and install scripts |

**Platform support:** macOS (amd64/arm64), Linux (amd64/arm64), Windows (amd64 via MSYS/Cygwin)

---

## Step 1: PostgreSQL Setup

```sql
-- Create the database and enable pgvector extension
CREATE DATABASE engram;
\c engram
CREATE EXTENSION IF NOT EXISTS vector;
```

> If your PostgreSQL user lacks superuser rights, have a superuser run the `CREATE EXTENSION` command. On managed services (RDS, Supabase, Cloud SQL), enable pgvector through the service control panel.

Verify pgvector is installed:
```sql
SELECT * FROM pg_extension WHERE extname = 'vector';
```

---

## Step 2: Environment Setup

Set the database connection string (required — never stored in config file):
```bash
export DATABASE_DSN="postgres://user:password@localhost:5432/engram?sslmode=disable"
```

Optional: OpenAI-compatible embeddings instead of local ONNX:
```bash
export EMBEDDING_PROVIDER=openai
export EMBEDDING_API_KEY=sk-...
export EMBEDDING_MODEL_NAME=text-embedding-3-small
export EMBEDDING_DIMENSIONS=384  # IMPORTANT: match the vector(384) table schema
```

Optional: authentication token for the worker/SSE endpoints:
```bash
export ENGRAM_API_TOKEN=your-secret-token
```

Persist these in your shell profile or a `.env` file sourced at startup.

---

## Step 3: Build

```bash
git clone https://github.com/YOUR_USER/engram.git
cd engram

# Download ONNX runtime libraries for local embeddings (required even if using OpenAI)
make setup-libs

# Build all binaries (worker, mcp-server) + Vue dashboard
make build
```

Build artifacts are placed in `bin/`:
```
bin/
  worker
  mcp-server
```

Hooks are JavaScript files in `plugin/engram/hooks/` — no compilation needed.

---

## Step 4: Initialize and Start Worker

On first run, the service creates `~/.engram/settings.json` and runs 19 database migrations automatically.

```bash
# Start the worker daemon (background, logs to /tmp/engram-worker.log)
make start-worker

# Verify it's running
curl http://localhost:37777/health
```

Expected response: `{"status":"ok", ...}` (HTTP 200)

Worker binds to `127.0.0.1:37777` by default. To expose on the network:
```bash
export ENGRAM_WORKER_HOST=0.0.0.0
make restart-worker
```

---

## Step 5: Install Plugin Into Claude Code

The `make install` command copies binaries and hooks, registers the plugin, and configures the MCP server in Claude Code:

```bash
make install
```

This does the following:
1. Stops any running worker
2. Copies binaries and JS hooks to `~/.claude/plugins/marketplaces/engram/`
3. Updates `~/.claude/plugins/installed_plugins.json` and `~/.claude/plugins/known_marketplaces.json`
4. Registers the MCP server in `~/.claude/settings.json`
5. Writes hook configuration to `~/.claude/plugins/.../hooks/hooks.json`
6. Starts the worker

### Manual MCP Configuration (alternative)

If you only want the MCP tools for a specific project, add to the project's `.claude/settings.json`:
```json
{
  "mcpServers": {
    "engram": {
      "command": "~/.claude/plugins/marketplaces/engram/mcp-server",
      "args": ["--project", "${CLAUDE_PROJECT}"],
      "env": {
        "DATABASE_DSN": "postgres://user:password@localhost:5432/engram?sslmode=disable"
      }
    }
  }
}
```

For global MCP access, add the same block to `~/.claude/settings.json`.

---

## Step 6: Verify Integration

1. Start a new Claude Code session — you should see in stderr:
   ```
   [engram] Injecting 0 observations from project memory (0 detailed, 0 condensed)
   ```
   (Zero observations on first run is correct.)

2. Check the Vue dashboard at `http://localhost:37777` — it shows memory stats.

3. Test a search via MCP (within Claude Code):
   ```
   Use the nia search tool to find observations about "database"
   ```

4. Check worker logs if anything fails:
   ```bash
   tail -f /tmp/engram-worker.log
   ```

---

## Multi-Workstation Setup

To share memory across multiple machines:

**On the central server:**
```bash
export DATABASE_DSN="postgres://user:password@db-host:5432/engram?sslmode=disable"
export ENGRAM_WORKER_HOST=0.0.0.0
export ENGRAM_API_TOKEN=shared-secret-token
./bin/worker &  # HTTP API + MCP SSE + MCP Streamable HTTP on :37777
```

**On remote workstations:** Configure Claude Code to use `mcp-stdio-proxy`:
```json
{
  "mcpServers": {
    "engram": {
      "command": "/path/to/mcp-stdio-proxy",
      "args": ["--sse-url", "http://central-server:37778"],
      "env": {
        "ENGRAM_API_TOKEN": "shared-secret-token"
      }
    }
  }
}
```

Each workstation automatically gets a unique `workstation_id` (derived from hostname + machine ID). Override with `WORKSTATION_ID` env var for consistent cross-session identity.

---

## Worker Management

```bash
make start-worker    # Start in background
make stop-worker     # Kill running worker
make restart-worker  # Stop + start
make dev             # Run worker in foreground (development)
```

---

## Development Commands

```bash
make test            # Run all tests with race detector
make test-coverage   # Tests with HTML coverage report
make bench           # Run benchmarks
make lint            # golangci-lint
make fmt             # gofmt
make clean           # Remove bin/ and coverage files
```

---

## Uninstall

```bash
make uninstall  # Stops worker, removes binaries, cleans up Claude Code config
```

Data directory `~/.engram/` and PostgreSQL database are NOT removed (to preserve memories). Drop the database manually if desired:
```sql
DROP DATABASE engram;
```

---

## Troubleshooting

**Worker fails to start:**
- Check `DATABASE_DSN` is set: `echo $DATABASE_DSN`
- Check PostgreSQL is accessible: `psql "$DATABASE_DSN" -c "SELECT 1"`
- Check pgvector extension: `psql "$DATABASE_DSN" -c "SELECT * FROM pg_extension WHERE extname='vector'"`
- Review logs: `cat /tmp/engram-worker.log`

**MCP server fails with "data store error":**
- `DATABASE_DSN` must be set in the MCP server's environment (pass via `env` block in settings.json)
- The MCP server connects directly to PostgreSQL — it does not go through the worker

**No observations returned at session start:**
- Normal on first use — observations are created during sessions
- Check worker is running: `curl http://localhost:37777/health`

**Vector search returns no results:**
- Hub storage threshold: new observations need HubThreshold accesses (default=5) before embeddings are stored
- Lower threshold: set `ENGRAM_HUB_THRESHOLD=1` in settings.json
- Check embedding service: `check_system_health` MCP tool

**Embedding dimension error:**
- If switching to OpenAI embeddings, ensure `EMBEDDING_DIMENSIONS` matches the `vector(384)` schema (use a 384-dim model or recreate tables)
- See [GOTCHAS.md](GOTCHAS.md#critical-embedding-dimension-mismatch) for recovery steps
