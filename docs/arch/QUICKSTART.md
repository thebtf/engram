# Developer Quickstart

Get engram running and integrated with Claude Code.

---

## Option A: Docker

For a local development stack and current plugin setup, use the
[README quick start](../../README.md#quick-start-server). It is not a production
deployment or installed-consumer proof. For the owner's Unraid installation,
keep the existing `:latest` Repository and follow the [production latest
channel](../RELEASE-PROTOCOL.md#production-latest-channel); do not replace its
updater with a direct `docker run`. A separately configured three-image Compose
deployment follows the [deployment guide](../DEPLOYMENT.md).

---

## Option B: Build from source

### Prerequisites

| Dependency | Version | Purpose |
|------------|---------|---------|
| Go | Version declared in `go.mod` | Build from source |
| PostgreSQL | 17+ | Primary data store |
| pgvector extension | latest | Vector similarity search |
| pgvectorscale extension | latest | DiskANN indexes for 4096-dim embeddings (optional but recommended) |
| make | any | Build system |

### 1. PostgreSQL setup

```sql
CREATE DATABASE engram;
\c engram
CREATE EXTENSION IF NOT EXISTS vector;
```

### 2. Build

```bash
git clone https://github.com/thebtf/engram.git
cd engram
make build
```

Produces `bin/engram-server` and `bin/engram`.

### 3. Run server

```bash
export DATABASE_DSN="postgres://user:pass@localhost:5432/engram?sslmode=disable"
export ENGRAM_AUTH_ADMIN_TOKEN="your-operator-token"
./bin/engram-server
```

Server starts on `:37777` (HTTP API + gRPC + dashboard, cmux multiplexed).

### 4. Install plugin

Follow the [recommended plugin installation](../../README.md#plugin-install-recommended) in the README.

---

## Multi-Workstation Setup

**Central server** (Docker or bare metal):
```bash
export ENGRAM_WORKER_HOST=0.0.0.0
export ENGRAM_AUTH_ADMIN_TOKEN=operator-secret
```

**Each workstation:**
1. Install the Claude Code plugin (`/plugin install engram`).
2. Run `/engram:setup` — enter the server URL and a worker keycard.
3. Each workstation gets a unique `workstation_id` (auto-derived from hostname + machine ID).

Override with `WORKSTATION_ID` env var for consistent identity across reinstalls.

---

## Verify Integration

1. Start a new Claude Code session — engram hooks fire, memories inject.
2. Dashboard at `http://server:37777` shows memory stats.
3. Test search: ask Claude to use `recall_memory` tool.
4. Check server health: `check_system_health` MCP tool.

---

## Development

```bash
make build           # Build server + client
make test            # go test ./... with race detector
go vet ./...         # Static analysis
```

---

## Troubleshooting

**Server fails to start:**
- Check `DATABASE_DSN` is set and PostgreSQL is accessible.
- Check pgvector extension: `SELECT * FROM pg_extension WHERE extname='vector';`
- Check logs via dashboard or `http://server:37777/api/logs`.

**Plugin not connecting:**
- Run `/mcp` in Claude Code to check engram connection status.
- Verify server URL and token via `/engram:setup`.

**No memories at session start:**
- Normal on first use — memories are created during sessions.
- Check server is running: `curl http://server:37777/api/version`.

**Auth errors (401):**
- Verify worker keycard is valid (not revoked) via dashboard `/tokens`.
- For local dev: set `ENGRAM_AUTH_SKIP_LOCAL=true` on the server.
