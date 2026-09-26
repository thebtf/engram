---
description: Configure Engram for Claude Code, Oh My Pi, or Codex
---

# Engram Setup (v6 — two-tier token model)

Configure the connection to your Engram server.

> **v6 BREAKING CHANGE.** Do not put the server-host operator key
> (`ENGRAM_AUTH_ADMIN_TOKEN`) on a workstation. Each workstation needs a
> browser-admin-issued client keycard from `{SERVER_URL}/access`.
> Upgrading from v5.x requires a new keycard before the client can authenticate.

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
  "api_token": "engram_<32hex-keycard-from-access>"
}
```

On POSIX systems, restrict the file to your user account with
`chmod 600 ~/.engram/config.json` and keep its parent directory private. On Windows,
check the file's NTFS ACLs rather than assuming inherited permissions are
private. Never commit, paste into chat, or log the keycard.

The wrapper uses a non-empty `ENGRAM_URL` / `ENGRAM_TOKEN` (or forwarded plugin
options) before config values. For config files, `ENGRAM_CONFIG_FILE` selects
an explicit path; otherwise an existing plugin-data `config.json` takes
precedence over `~/.engram/config.json`. Check which file the launcher reports
if an older value keeps winning. The home config is the fallback for hosts
that do not forward environment variables to plugin children.

### Updating OMP

An OMP marketplace update refreshes only the catalog. Upgrade the installed
plugin with `omp plugin upgrade engram@engram`, then run `/reload-plugins` or
restart OMP / start a new session before expecting updated MCP discovery.

### Legacy path (Codex < 0.139 only)

For Codex versions that still forward `shell_environment_policy.set`:

```toml
[shell_environment_policy.set]
ENGRAM_URL = "http://your-server:37777"
ENGRAM_TOKEN = "engram_<32hex-keycard-from-access>"
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

### 2. Issue a client keycard through Access

Ask the user to do this in their own browser; do not request the keycard in
chat or call the issuance API on their behalf:

> 1. Open `{SERVER_URL}/access` and log in with a **real browser admin
>    session**. A server running `ENGRAM_AUTH_DISABLED=true` exposes a
>    synthetic admin identity, which cannot issue keycards; ask the server
>    operator to enable real authentication and provision an admin first.
> 2. In **Keycards**, enter a workstation name, choose `read-write` for a
>    client that stores memories (`read-only` only for a read-only client),
>    and enter the intended principal and principal kind (`human`, `agent`,
>    or `service`). Use the **same principal and kind** as the owner of
>    private memories the client must read; a different keycard cannot read
>    another principal's private memories merely by having `read-write` scope.
>    Set an expiry if required, then issue the keycard.
> 3. Copy the one-time `engram_` keycard directly into your local client
>    configuration in step 3. Do not send it to the assistant or store it
>    in the repository. Dismiss the one-time display after saving it.

If Access says forbidden or issuance is disabled, stop and resolve the admin
session/authentication setup with the server operator. A bearer operator key
or existing client keycard is not a substitute for a browser admin session.

### 3. Update local agent config

Have the user edit their local config privately; examples below contain only
placeholders. Do not read back or echo a populated credential file.

For Claude Code, put `ENGRAM_URL` and `ENGRAM_TOKEN` in the `env` section of
`~/.claude/settings.json`:

```json
{
  "env": {
    "ENGRAM_URL": "http://192.168.1.100:37777",
    "ENGRAM_TOKEN": "engram_<32hex-keycard-from-access>"
  }
}
```

Replace the placeholder locally with the one-time keycard. Remove stale
`ENGRAM_AUTH_ADMIN_TOKEN` and `ENGRAM_API_TOKEN` entries from the workstation
config; neither is the workstation credential.

For OMP and Codex, put the server URL and keycard in the config file shown in
"OMP and Codex setup" above (or select it with `ENGRAM_CONFIG_FILE`).

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

- **Token format**: The client keycard is `engram_` followed by 32 hex
  characters. Never use the server-host operator key on a workstation.
- **Token not found / revoked**: A real browser admin can issue a replacement
  in `/access`; repeat step 3 without sharing the keycard in chat.
- **Private memory denied**: Check the keycard's principal **and kind** match
  the private memory owner; `read-write` scope alone does not grant access.
- **Token mismatch**: Issue a separate keycard for each workstation so it
  can be revoked independently.
- **Daemon refuses to start**: Check stderr in the CC plugin status panel —
  v6 fail-fast prints the missing-env line directly.
- **Firewall**: Port 37777 must be reachable from this machine to the server.
- **Docker networking**: If the server runs in Docker, use the host
  machine's IP (not `localhost` unless same machine).

### Quiet mode (mute automatic context injection)

Quiet mode stops Engram PUSHING context into the prompt. In Claude Code this
means no hook-injected session-start behavioral rules / memories / issues and
no pre-tool-use or pre-compact context; capture/learning hooks still run. In
OMP it suppresses the native `session_start` and `before_agent_start` injection
paths. Use it when injected context is more noise than signal: a stale or
mis-scoped server-side rule set, focused development, or any session where
"zero hints" beats "wrong hints".

**Scope — what quiet mode does and does NOT silence.** Quiet mode silences
context injection only. It deliberately does NOT disable the MCP daemon: the
`store`/`recall`/`vault`/`issues`/... tools keep working, and SessionStart binary
bootstrap (`ensure-binary.js`, which downloads/updates the daemon binary only
when it is missing or version-stale) still runs. That is by design — muting
injection must not break the tools. The bootstrap is rare (only on first install
or a version bump), best-effort, and non-fatal; it makes no context injection.
If you want zero MCP activity too, disable the engram plugin rather than using
quiet mode.

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

OMP 17.x loads the MCP server, skills, slash commands, and the native Engram
extension from the marketplace, but does not execute Claude `hooks.json`.
Quiet mode therefore suppresses native OMP injection rather than Claude hook
execution; it never disables MCP tools.
