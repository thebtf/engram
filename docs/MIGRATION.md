# Migration Guide

## v4.0.0 — MCP Transport Change (HTTP → gRPC)

### What changed

engram v4.0.0 replaces the MCP HTTP transports (SSE and Streamable HTTP) with gRPC.
MCP tool calls now go through a local daemon that communicates with the server via gRPC.

The REST API (`/api/*`), dashboard, and dashboard SSE remain unchanged on HTTP.

### Last version with HTTP MCP transport

**Commit:** `c890d62` (2026-04-13)
**Tag:** `v3.8.0`

To check out the last version with SSE and Streamable HTTP MCP support:
```bash
git checkout v3.8.0
```

### Plugin migration

**Before (v3.x):**
```json
{
  "mcpServers": {
    "engram": {
      "type": "http",
      "url": "${ENGRAM_URL}",
      "headers": { "Authorization": "Bearer ${ENGRAM_API_TOKEN}" }
    }
  }
}
```

**After (v4.x):**
```json
{
  "mcpServers": {
    "engram": {
      "type": "stdio",
      "command": "engram"
    }
  }
}
```

### Prerequisites for v4.x

1. Install the `engram` binary (download from GitHub Releases)
2. Ensure `engram` is on your PATH
3. Set environment variables:
   - `ENGRAM_URL` — server address (e.g., `http://your-server:37777`)
   - `ENGRAM_API_TOKEN` — authentication token

The daemon starts automatically on first use. No manual setup required.

### Removed endpoints

| Endpoint | Transport | Replacement |
|----------|-----------|-------------|
| `GET /sse` | MCP SSE | Local daemon via gRPC |
| `POST /message` | MCP SSE message | Local daemon via gRPC |
| `POST /mcp` | MCP Streamable HTTP | Local daemon via gRPC |

### Kept endpoints

All `/api/*` REST endpoints continue working unchanged.
Dashboard SSE (`/api/events`) continues working unchanged.
