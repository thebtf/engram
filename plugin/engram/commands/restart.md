# Restart Engram Worker

Restart the Engram worker process. Use when experiencing issues with the memory system.

## Instructions

1. Call `check_system_health` to verify current connection and get server info.

2. If connected, report current status and ask the user if they want to proceed with restart.

3. The restart endpoint is on the same server as MCP. If `ENGRAM_URL` is set, derive the base URL from it (strip `/mcp` path). Otherwise, ask the user for the server address.

4. Call the restart endpoint:
   ```bash
   curl -X POST <base-url>/api/restart -H "Authorization: Bearer ${ENGRAM_AUTH_ADMIN_TOKEN}"
   ```

5. Wait 2 seconds, then call `check_system_health` again to verify the worker restarted.

6. Report the result, including the version number.
