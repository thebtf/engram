# Engram Doctor

Diagnose Engram connectivity and health. Tests the actual MCP connection, not just environment variables.

## Instructions

Run these checks in order. Stop at the first failure and report.

### 1. MCP Connection Test

Call the `check_system_health` MCP tool. This is the definitive test — if it works, Engram is connected regardless of how it's configured.

```
Tool: check_system_health()
```

- **Success**: Report server version, observation count, and health status. Skip to step 3.
- **Failure**: Proceed to step 2 for diagnostics.

### 2. Connection Diagnostics (only if step 1 failed)

Check what's configured:

a. Check if `engram` MCP server appears in `/mcp` listing (it may be under `plugin:engram:engram` or `engram`).

b. If the server shows as failed, report the error shown in `/mcp`.

c. Common issues:
   - `${ENGRAM_URL}` not expanded → env var not set. User needs to set `ENGRAM_URL` in system environment.
   - Connection refused → server not running or wrong address.
   - 401/403 → wrong token in `ENGRAM_API_TOKEN` or `Authorization` header.
   - DNS resolution failed → hostname not reachable from this machine.

d. Report the specific failure and suggest the fix. Always include:

> Run `/engram:setup` to configure or reconfigure your connection.

e. If the URL appears to be a bare host without `/mcp` (e.g., `http://host:37777` instead of `http://host:37777/mcp`), suggest adding the `/mcp` path suffix.

### 3. Memory Health (only if step 1 succeeded)

Call `get_memory_stats` to report:
- Total observations
- Storage size
- Last consolidation time
- Any warnings

### 4. Report Summary

```
Engram Doctor Results:
- MCP Connection: [connected / failed — reason]
- Server Version: [version or N/A]
- Observations: [count]
- Health: [healthy / warnings]
```
