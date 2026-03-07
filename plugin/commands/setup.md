# Engram Setup

Configure the connection to your Engram server by editing `~/.claude/settings.json`.

## Instructions

### 1. Determine the server URL

Ask the user:

> What is your Engram server address? (e.g., `http://192.168.1.100:37777` or `http://engram.local:37777`)

If the user is unsure, suggest checking their Docker host's IP and port 37777.

Store the answer as `SERVER_URL`. The MCP endpoint is `{SERVER_URL}/mcp`.

### 2. Determine the API token

Ask the user:

> What is your Engram API token? (Leave empty if you haven't set one on the server.)

If empty, note that the server must also have no `ENGRAM_API_TOKEN` set (or it will reject requests).

Store the answer as `API_TOKEN`.

### 3. Update settings.json

Read `~/.claude/settings.json`, then add `ENGRAM_URL` and `ENGRAM_API_TOKEN` to the `env` section. Use the Edit tool to modify the file directly.

If the `env` section already exists, add the two keys to it. If it doesn't exist, create it.

**Example result (env section only):**

```json
{
  "env": {
    "ENGRAM_URL": "http://192.168.1.100:37777/mcp",
    "ENGRAM_API_TOKEN": "your-token-here"
  }
}
```

Replace values with the actual answers from steps 1 and 2. If the token is empty, set it to `""`.

### 4. Restart Claude Code

Tell the user:

> Settings are only read when Claude Code starts. Please **close and reopen Claude Code** for the changes to take effect.

### 5. Verify connection

After the user restarts and returns, run the health check:

```
Tool: check_system_health()
```

- **Success**: Report the server version and observation count. Setup is complete.
- **Failure**: Run `/engram:doctor` to diagnose the issue.

### Common issues

- **URL must include `/mcp`**: The correct format is `http://host:37777/mcp`, not just `http://host:37777`.
- **Token mismatch**: The token here must match `ENGRAM_API_TOKEN` (or `API_TOKEN` in `.env`) on the server.
- **Firewall**: Ensure port 37777 is open between this machine and the server.
- **Docker networking**: If the server runs in Docker, use the host machine's IP, not `localhost` (unless running on the same machine).
