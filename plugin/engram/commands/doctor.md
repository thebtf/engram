---
description: Diagnose Engram MCP connectivity and subsystem health
---

# Engram Doctor

Diagnose Engram connectivity and health. Tests the actual MCP connection, not just environment variables.

## Instructions

Run the applicable checks in order. If a check fails, preserve its diagnostic and continue safe diagnosis and repair through the existing owner; do not stop at the first failure.

### 1. MCP Connection Test

Call the `check_system_health` MCP tool. A successful call demonstrates a useful tool call through the current local MCP connection and backend; a failure alone does not identify which layer failed.

```
Tool: check_system_health()
```

- **Success**: Report the returned health status. Report server version and observation count only if separately observed from a supported source; otherwise use N/A. Continue to step 3.
- **Failure or unavailable tool**: Proceed to step 2 for diagnostics.

### 2. Connection Diagnostics (only if step 1 failed)

Check what's configured:

a. Where the host supports `/mcp`, check whether the `engram` MCP server appears in its listing (it may be under `plugin:engram:engram` or `engram`). Report its connection state and any error without exposing credentials.

b. Distinguish local MCP initialization and discovery from backend gRPC connectivity and readiness. The local client uses stdio and its daemon calls the server over gRPC. HTTP readiness alone does not prove the local MCP connection or a useful tool call.

c. Investigate the specific error rather than assuming a cause:
   - `${ENGRAM_URL}` not expanded may mean the variable was not set or forwarded. Check the launcher configuration and its precedence. For Codex ≥ 0.139, the Engram config file is the supported path because `config.toml` `[shell_environment_policy.set]` is not forwarded to plugin MCP children.
   - Connection refused may indicate a stopped server or wrong address.
   - 401/403 may indicate a credential or permission problem; check the server's authentication mode and authorized workstation keycard without exposing secrets or bypassing authorization.
   - DNS resolution failure may indicate that the hostname cannot be resolved from this machine.

d. `ENGRAM_URL` is a server origin such as `http://host:37777`, not an HTTP MCP endpoint. Do not append `/mcp` or `/sse`. Use `/engram:setup` only when configuration actually needs setup or correction, not as a blanket recovery step.

e. Continue supported, safe recovery for the observed failure. Preserve identity and data; do not fabricate credentials or identity, or blindly replay a mutation with an unknown outcome. If no safe action remains, report the exact unresolved boundary and the evidence needed to proceed. Do not require a manual session restart as a blanket remedy.

### 3. Memory Health (only if step 1 succeeded)

Call `admin(action="stats")` to report:
- Total observations
- Storage size
- Last consolidation time
- Any warnings

### 4. Report Summary

```
Engram Doctor Results:
- Local MCP connection and discovery: [observed state / unknown — reason]
- Backend gRPC/readiness: [observed state / unknown — reason]
- Useful MCP tool call: [succeeded / failed / unavailable — reason]
- Server Version: [version or N/A]
- Observations: [count or N/A]
- Health: [healthy / warnings / unknown]
```
