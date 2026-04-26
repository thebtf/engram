# Engram Setup (v6 — two-tier token model)

Configure the connection to your Engram server.

> **v6 BREAKING CHANGE.** The plugin no longer accepts the operator key
> (`ENGRAM_AUTH_ADMIN_TOKEN`) on workstations. Each workstation now uses a
> per-workstation **API token (worker keycard)** issued via the dashboard
> `/tokens` page. The operator key lives ONLY on the server host.
>
> If you are upgrading from v5.x: you MUST issue a fresh keycard via the
> dashboard before this session can authenticate. See step 2 below.

## Why `settings.json` env vars (not `/config` UI)

Claude Code supports two paths for plugin credentials:

1. **`/config` UI** → stored in `~/.claude/.credentials.json`
   `pluginSecrets["engram@engram"]`. Prone to silent wipes from CC's shared
   credential-store race (anthropics/claude-code#45551 + engram issue #83).
   After `/login`, a concurrent MCP OAuth write, or a CC update, `api_token`
   can disappear and the plugin loses auth without warning.

2. **`settings.json` `env` section** (recommended) → `ENGRAM_URL` +
   `ENGRAM_TOKEN` in `~/.claude/settings.json`. Survives all of the above
   because it's a separate file touched only by your edits.

The plugin accepts either path; this guide uses path 2.

## Instructions

### 1. Determine the server URL

Ask the user:

> What is your Engram server address? (e.g., `http://192.168.1.100:37777`
> or `http://engram.local:37777`)

If the user is unsure, suggest checking their Docker host's IP and port 37777.

Store the answer as `SERVER_URL`.

### 2. Issue a worker keycard via the dashboard

Tell the user:

> 1. Open `{SERVER_URL}/tokens` in your browser.
> 2. Log in (admin email + password).
> 3. Click "Generate token", give it a memorable name (e.g. your workstation
>    hostname), choose scope `read-write`, and click Create.
> 4. **Copy the token shown ONCE.** It will not be shown again.
> 5. Paste it back here.

Store the answer as `API_TOKEN`. The format is `engram_<32-hex-chars>`.

If the user pastes a value that does NOT begin with `engram_`, refuse and
explain that this looks like the operator key — that is forbidden on
workstations as of v6. Ask them to issue a fresh keycard via the dashboard.

### 3. Update settings.json

Read `~/.claude/settings.json`, then add `ENGRAM_URL` and `ENGRAM_TOKEN` to
the `env` section. Use the Edit tool.

**Example result (env section only):**

```json
{
  "env": {
    "ENGRAM_URL": "http://192.168.1.100:37777",
    "ENGRAM_TOKEN": "engram_<32hex-keycard-from-dashboard>"
  }
}
```

If the user has a stale `ENGRAM_AUTH_ADMIN_TOKEN` entry from v5 days,
**remove it** — it is no longer read on the workstation side, and leaving
it there triggers a v6 warning at daemon startup.

If the user has a stale `ENGRAM_API_TOKEN` entry, remove it too (v5-era
name, no longer read).

### 4. Restart Claude Code

> Settings are only read when Claude Code starts. Please **close and reopen
> Claude Code** for the changes to take effect. The engram daemon will exit
> non-zero on startup if `ENGRAM_TOKEN` is missing AND `ENGRAM_URL` is set,
> so you'll see a clear error rather than silent partial-tool degradation.

### 5. Verify connection

After the user restarts and returns:

```
Tool: check_system_health()
```

- **Success**: Report the server version and observation count. Setup complete.
- **Failure**: Run `/engram:doctor` to diagnose.

### Common issues

- **Token format**: Must be `engram_<hex>`. Anything else (especially the
  Docker-host operator token) is rejected at validation time.
- **Token not found / revoked**: Open the dashboard `/tokens` page, generate
  a fresh keycard, repeat step 3.
- **Token mismatch**: Per-workstation. Each workstation needs its own
  keycard; reusing one keycard across machines works but defeats the
  per-machine revocation benefit.
- **Daemon refuses to start**: Check stderr in the CC plugin status panel —
  v6 fail-fast prints the missing-env line directly.
- **Firewall**: Port 37777 must be reachable from this machine to the server.
- **Docker networking**: If the server runs in Docker, use the host
  machine's IP (not `localhost` unless same machine).
