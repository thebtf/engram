# ADR-002: Plugin-First Installation Architecture

**Status:** Accepted
**Date:** 2026-03-06
**Context:** Install scripts and docs are stuck on old local-worker + SQLite model

## Problem

Engram evolved from local SQLite to client-server (remote Docker + local plugin).
All installation artifacts still assume the old architecture:

- Install scripts download server binary and start local worker
- Install scripts check for Python 3.13+/uvx (SQLite-era dependency)
- Plugin `.mcp.json` uses `${ENGRAM_URL}` but no setup flow to configure it
- Goreleaser doesn't build `mcp-stdio-proxy` (needed for non-HTTP clients)
- DEPLOYMENT.md references wrong user (`mnemonic`), wrong dimensions (`1536`), nonexistent Unraid template

## Decision

**Plugin HTTP MCP as primary connection method.**

Claude Code natively supports `type: "http"` in `.mcp.json`. The plugin connects directly
to the remote server via Streamable HTTP (`/mcp` endpoint). No proxy binary needed.

### Architecture

```
Client (workstation)           Server (Docker)
  Claude Code                    Worker :37777
    plugin/.mcp.json  ──HTTP──→   /mcp  (Streamable HTTP)
    hooks/*           ──POST──→   /api/* (REST API)
```

### Installation Flow

1. User deploys server (Docker Compose or manual)
2. User sets `ENGRAM_URL` and `ENGRAM_API_TOKEN` environment variables
3. User installs plugin (marketplace or script)
4. Plugin connects via HTTP MCP — no proxy, no local server

### Fallback: stdio-to-SSE proxy

For MCP clients that don't support HTTP transport:
- `mcp-stdio-proxy` converts stdio → SSE (`/sse` endpoint)
- Built by goreleaser, available in releases
- Not the primary path — documented as Option C

## Consequences

### Positive
- Zero-binary client (plugin config files only for HTTP MCP)
- No local process management
- Simpler install scripts (no worker start/stop)
- Works across all platforms identically

### Negative
- Requires env vars set before Claude Code starts (no runtime prompt)
- `${ENGRAM_URL}` not expanding = most common failure mode
- Need `/engram:setup` command to guide users

### Migration
- Old users with manual `settings.json` MCP config: still works (stdio proxy)
- New users: plugin-first with HTTP MCP
- Install scripts: rewrite to download plugin files + hooks only
