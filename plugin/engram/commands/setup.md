---
description: Configure Engram for Claude Code, Oh My Pi, or Codex
---

# Engram Setup (v6 — two-tier token model)

Configure the connection to your Engram server.

> **v6 BREAKING CHANGE.** The plugin no longer accepts the operator key
> (`ENGRAM_AUTH_ADMIN_TOKEN`) on workstations. Each workstation now uses a
> per-workstation **API token (worker keycard)** issued via the dashboard
> `/tokens` page. The operator key lives ONLY on the server host.
>
> If you are upgrading from v5.x: you MUST issue a fresh keycard via the
> dashboard before this session can authenticate. See step 2 below.

## OMP and Codex setup

> **Note (Codex ≥ 0.139):** Codex stopped forwarding
> `[shell_environment_policy.set]` values to plugin MCP server children in
> 0.139 (see openai/codex#24401 — no documented replacement for plugin MCP
> servers). `ENGRAM_URL` / `ENGRAM_TOKEN` set in `config.toml` are no longer
> seen by the engram wrapper. The **engram config file** is the supported path
> from v6.4.15 onward.

### Supported path: engram config file

Create `~/.engram/config.json` (or a path of your choice pointed to by
`ENGRAM_CONFIG_FILE`):

```json
{
  "server_url": "http://your-server:37777",
  "api_token": "engram_<32hex-keycard-from-dashboard>"
}
```

On POSIX systems the file should be readable only by your user account:

```sh
chmod 600 ~/.engram/config.json
```

On Windows the file lives in your user profile; NTFS ACLs inherited from the
parent directory already restrict access to your account.

The engram plugin reads this file as the final fallback, so it works in OMP,
Codex, Claude Code, and any other harness that does not forward environment
variables to plugin children.

### Legacy path (Codex < 0.139 only)

For Codex versions that still forward `shell_environment_policy.set`:

```toml
[shell_environment_policy.set]
ENGRAM_URL = "http://your-server:37777"
ENGRAM_TOKEN = "engram_<32hex-keycard-from-dashboard>"
```

This path was never a documented contract for plugin MCP servers and stopped
working with Codex 0.139. Prefer the config file for new setups.

Then restart Codex or open a new Codex thread so MCP startup sees the new
environment. If Codex offers plugin authentication during install, provide the
same server URL and worker keycard there.

## Claude Code setup

Claude Code supports two paths for plugin credentials:

1. **`/config` UI** → stored in `~/.claude/.credentials.json`
   `pluginSecrets["engram@engram"]`. Prone to silent wipes from CC's shared
   credential-store race (anthropics/claude-code#45551 + engram issue #83).
   After `/login`, a concurrent MCP OAuth write, or a CC update, `api_token`
   can disappear and the plugin loses auth without warning.

2. **`settings.json` `env` section** (recommended) → `ENGRAM_URL` +
   `ENGRAM_TOKEN` in `~/.claude/settings.json`. Survives all of the above
   because it's a separate file touched only by your edits.

The Claude plugin accepts either path; this guide uses path 2.

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

### 3. Update local agent config

For Claude Code, read `~/.claude/settings.json`, then add `ENGRAM_URL` and
`ENGRAM_TOKEN` to the `env` section. Use the Edit tool.

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

For OMP and Codex, create `~/.engram/config.json` as shown in "OMP and Codex
setup" above. The config file also works as a universal fallback for any harness
that does not forward environment variables to plugin children.

### 4. Restart the agent host

> Settings are only read when the agent host starts. Please **close and reopen
> Claude Code or OMP, or start a new Codex thread** for the changes to take
> effect. The plugin wrapper exits non-zero when `ENGRAM_URL` or
> `ENGRAM_TOKEN` is missing, so you'll see a clear error rather than silent
> partial-tool degradation.

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

### Quiet mode (mute automatic hook injection)

Quiet mode stops engram PUSHING context into the prompt: no session-start
behavioral rules / memories / issues, no pre-tool-use or pre-compact context.
The capture/learning hooks still run — engram keeps crystallizing lessons and
recording session outcomes — so quiet stops engram *talking*, not *learning*.
Use it when injected context is more noise than signal: a stale or mis-scoped
server-side rule set, focused development, or any session where "zero hints"
beats "wrong hints".

**Scope — what quiet mode does and does NOT silence.** Quiet mode silences hook
*context injection* (the prompt noise). It deliberately does NOT disable the MCP
daemon: the `store`/`recall`/`vault`/`issues`/... tools keep working, so the
SessionStart binary bootstrap (`ensure-binary.js`, which downloads/updates the
daemon binary only when it is missing or version-stale) still runs. That is by
design — muting injection must not break the tools. The bootstrap is rare (only
on first install or a version bump), best-effort, and non-fatal; it makes no
context injection. If you want zero MCP activity too, disable the engram plugin
rather than using quiet mode.

Set it the same way you set credentials for your harness:

- **Claude Code** — env var `ENGRAM_QUIET=1` (alias `ENGRAM_QUIET_HOOKS=1`) in
  `~/.claude/settings.json` `env`, next to `ENGRAM_URL`/`ENGRAM_TOKEN`. The
  plugin-config option `engram_quiet` also works (`CLAUDE_PLUGIN_OPTION_*`).
- **Codex ≥0.139** — env vars are NOT forwarded to plugin hook children
  (openai/codex#24401), so the env var will NOT work. Add `"quiet": true` to
  `~/.engram/config.json` instead, alongside `server_url`/`api_token`:

  ```json
  {
    "server_url": "http://your-server:37777",
    "api_token": "engram_<keycard>",
    "quiet": true
  }
  ```

Truthy values: `true` (boolean) or the strings `1`/`true`/`yes`/`on`
(case-insensitive); unset or anything else leaves hooks fully active. Reversible
— remove the var/key to restore. Explicit env always wins over the config file.

OMP 17.x loads the MCP server, skills, and slash commands from the marketplace,
but does not execute Claude `hooks.json`; quiet mode therefore has no hook effect
under OMP.
