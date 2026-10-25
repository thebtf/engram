# openclaw-engram

OpenClaw plugin that connects agents to an [engram](https://github.com/thebtf/engram) persistent memory server for long-term context retention.

## Prerequisites

- OpenClaw gateway installed and running
- Engram server running and reachable (default port `37777`)
- An API token configured on the engram server
- The matching release Go client (`engram`) on PATH, or its absolute executable
  selected with `ENGRAM_EXECUTABLE`. It must support
  `project resolve --root <actual-workspace> --json`; no JS/TS identity fallback exists.

## Installation

### Fresh install

1. **Temporarily remove the memory slot if configured** (avoids circular validation during install):

   ```bash
   # If plugins.slots.memory is currently set to "engram", clear it first:
   openclaw config set plugins.slots.memory ""
   ```

2. **Install the plugin:**

   ```bash
   openclaw plugins install openclaw-engram
   ```

   This runs `npm pack` and extracts the plugin into `~/.openclaw/extensions/engram/`.

3. **Configure the engram server connection:**

   ```bash
   openclaw config set plugins.entries.engram.config.url http://your-engram-server:37777
   openclaw config set plugins.entries.engram.config.token your-api-token
   ```

4. **Assign as the memory backend:**

   ```bash
   openclaw config set plugins.slots.memory engram
   ```

5. **Restart the gateway:**

   ```bash
   openclaw gateway restart
   ```

### Updating

```bash
openclaw plugins install openclaw-engram
openclaw gateway restart
```

### Note on package naming

The npm package is `openclaw-engram` but the plugin ID is `engram`.
OpenClaw may show a cosmetic warning about ID mismatch — this is harmless.
The plugin registers and operates correctly under the `engram` ID.

## Configuration

All fields are set under `plugins.entries.engram.config`:

| Field | Type | Default | Description |
|---|---|---|---|
| `url` | string | `http://localhost:37777` | Engram server base URL |
| `token` | string | *(required)* | Bearer token for API authentication |
| `project` | string | *(current workspace)* | Explicit logical project UUID override, never an agent/name/path selector |
| `contextLimit` | number | `10` | Maximum observations injected per prompt turn |
| `sessionContextLimit` | number | `20` | Maximum observations injected at session start |
| `tokenBudget` | number | `2000` | Token budget for context injection (~4 chars/token) |
| `timeoutMs` | number | `5000` | Per-request HTTP timeout in milliseconds |
| `autoExtract` | boolean | `true` | Auto-extract observations on compaction and session end |
| `workspaceDir` | string | `~/.openclaw/workspace/` | Workspace path for `/migrate` when called from channel context (Telegram, Discord) |
| `logLevel` | string | `warn` | Log verbosity: `debug`, `info`, `warn`, `error` |

Project resolution reads the portable `.engram-project` UUID or the real Git
common-dir binding through the Go client. Initialize only an explicitly selected
new project with `engram project init`; carry the marker into independent clones.
Project-scoped hooks and tools use their current `ctx.workspaceDir` or a configured
`project` UUID override. Missing or invalid project context returns a scoped error
without registration/capability roundtrips or changes to the OpenClaw host.
Explicit global Vault calls do not require project context. Channel `/migrate`
uses the configured `workspaceDir` or the documented `~/.openclaw/workspace/`
default. Before importing, it resolves that workspace through the Go client unless
a valid `project` UUID override is configured.

### OpenClaw 2026.10.7 prompt-hook access

For this non-bundled plugin, automatic prompt enrichment requires an explicit
host-owned opt-in at `plugins.entries.engram.hooks.allowConversationAccess: true`.
It is **not** a field under `plugins.entries.engram.config`. For example, merge
this entry into your existing OpenClaw configuration only if you want to grant it:

```json
{
  "plugins": {
    "entries": {
      "engram": {
        "hooks": { "allowConversationAccess": true }
      }
    }
  }
}
```

The grant lets prompt hooks access the current request and prepared conversation
history and enrich the model prompt. Engram sends its context query to your
configured Engram server. Without the grant, OpenClaw blocks these prompt hooks;
explicit memory tools remain separate. `hooks.allowPromptInjection: false` also
blocks prompt enrichment. The plugin does not change either host policy.

The compatibility regression exercises the official `openclaw@2026.10.7` manifest
parser, registrars and hook runner. It is not proof of plugin discovery/trust,
full configuration validation, or installed store/recall/context delivery.

## What it provides

### Hooks

Hooks register automatically; prompt enrichment additionally needs the host opt-in above:

| Hook | Effect |
|---|---|
| `session_start` | Initializes session tracking; it does not modify prompt context |
| `before_prompt_build` | Adds bounded session context to system context and query-matched context to the current turn |
| `after_tool_call` | Forwards tool events to engram for self-learning |
| `before_compaction` | Backfills conversation transcript into engram before context is compacted |
| `session_end` | Final transcript backfill when the session closes |

Session context is resolved and fetched for the current workspace on each prompt
build. No previous project's session context is reused if the workspace binding
changes or current identity resolution fails. Query-matched context keeps its
per-turn path; both use bounded, quoted context formatting.

### Agent tools

Available to the agent during a session:

| Tool | Description |
|---|---|
| `engram_search` | Semantic search across all stored observations |
| `memory_search` | Alias for `engram_search` (memory-core compatible name) |
| `engram_remember` | Store a new observation with structured metadata |
| `memory_store` | Alias for `engram_remember` |
| `engram_decisions` | Retrieve past architectural decisions by query |
| `memory_get` | Retrieve a specific observation by ID |
| `memory_forget` | Delete an observation by ID |
| `memory_migrate` | Import local `MEMORY.md` / `memory/**/*.md` files into engram |

### Slash commands

| Command | Description |
|---|---|
| `/memory` | Show memory status and search |
| `/remember <text>` | Store a memory immediately |
| `/migrate [--dry-run] [--force] [path]` | Import local memory files into engram |

### CLI

```bash
openclaw memory status              # Show server health and version
openclaw memory search <query>      # Search engram memory
openclaw memory store <text>        # Store a memory
openclaw memory migrate             # Import local memory files
openclaw memory migrate --dry-run   # Preview without importing
openclaw memory migrate --force     # Re-import already migrated files
```

## Troubleshooting

**"plugin not found: engram" during install**

Circular dependency: OpenClaw tries to load the memory plugin before it is installed.
Fix: clear `plugins.slots.memory` first (step 1 of Installation), install, then re-set the slot.

**"plugin id mismatch" warning**

The npm package name (`openclaw-engram`) differs from the plugin ID (`engram`).
This is cosmetic — the plugin loads and works correctly. No action needed.

**Plugin loads but no memory is injected into prompts**

1. Verify the server is reachable: `openclaw memory status`
2. Check `url` and `token` are set correctly
3. Increase `logLevel` to `debug` and restart the gateway to see injection details
4. Confirm `plugins.slots.memory` is set to `engram`
5. On OpenClaw 2026.10.7, check the host-owned `hooks.allowConversationAccess`
   grant and ensure `hooks.allowPromptInjection` is not false

**Context injection is slow or times out**

Increase `timeoutMs` (default 5000 ms). Engram failures are non-blocking — if the server is unreachable the agent continues without memory context.

## Development

Requirements: Node.js >=18, TypeScript >=5.

```bash
cd plugin/openclaw-engram
npm install
npm run build       # compile TypeScript → dist/
npm run typecheck   # type-check without emitting
```

The compiled output in `dist/` is what OpenClaw loads. Re-run `npm run build` after any source change and restart the gateway.
