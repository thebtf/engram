# Migration Guide

## v4.2.0 → v4.3.0: Modular Daemon Refactor

### ZERO user-visible change — no migration steps required

Engram v4.3.0 introduces an internal modular daemon framework that wraps the
existing MCP→gRPC routing as its first tenant (`engramcore` module). There are
**no breaking changes** to any external interface:

- Plugin `.mcp.json` — **unchanged**, no config edits required
- MCP tool names, descriptions, and input schemas — **unchanged** (all 68
  tools work identically per NFR-5)
- Hook scripts (`session-start`, `user-prompt`, `post-tool-use`, `stop`) —
  **unchanged**
- engram-server gRPC and HTTP APIs — **unchanged**
- PostgreSQL schema — **unchanged**
- Dashboard URL, project identity hash, SessionStart hook output — **unchanged**

### What changed internally

| Improvement | Details |
|---|---|
| Graceful restart | Issue #71 closed. Plugin auto-upgrade via `ensure-binary.js` now triggers a clean drain → snapshot → shutdown → binary swap → re-exec cycle. Active CC sessions reconnect transparently. |
| Snapshot persistence | Each module persists opaque state across restarts via versioned `SnapshotEnvelope` files. Forward-compat: unknown future versions degrade to empty-state restore with a WARN log rather than aborting startup. |
| OpenTelemetry metrics | `engram_handletool_duration_ms` histogram, `engram_handletool_errors_total` counter, and `engram_module_init_duration_ms` histogram are exported when `OTEL_EXPORTER_OTLP_ENDPOINT` is set. No-op by default. |
| Structured logging | JSON by default; `ENGRAM_LOG_FORMAT=text` for development. Canonical fields: `module`, `tool`, `session_id`, `project_id`, `duration_ms`, `error_code`. |

### Reference links

- Full spec: `.agent/specs/modular-daemon/spec.md`
- Architecture decisions (D1–D22): `.agent/specs/modular-daemon/design.md`
- Module author guide: `docs/modules/README.md`
- Closed issue: [#71 — graceful restart on plugin auto-upgrade](../../issues/71)

---

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
