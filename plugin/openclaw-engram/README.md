# openclaw-engram

OpenClaw plugin that connects agents to an [engram](https://github.com/thebtf/engram) persistent memory server for long-term context retention.

## Prerequisites

- OpenClaw gateway installed and running
- Engram server running and reachable (default port `37777`)
- An API token configured on the engram server
- The matching release Go client (`engram`) on PATH, or its absolute executable
  selected with `ENGRAM_EXECUTABLE`. It must support
  `project resolve --root <actual-workspace> --json`; no JS/TS identity fallback exists.

### Tested compatibility boundary

The compatibility suite uses exact official npm packages `openclaw@2026.9.2`
and `openclaw@2026.10.7`, with their declared runtime dependencies and no lifecycle
scripts. The 2026.9.2 fixture exercises the exported loader with explicit synthetic
discovery/configuration, then the real hook runner and tool-result adapter; the
2026.10.7 fixture exercises its real manifest parser, registrars, hook runner and
tool-result adapter. Neither starts a gateway, provider, model or plugin service.
This does not prove automatic discovery/trust, installation, live authenticated
store/recall, memory-slot migration or rollback on the reported host
`2026.9.2 (3928bad)`. That build's image/package/SDK parity is not available.
No enforced OpenClaw minimum or complete installed-consumer PASS is claimed.

The existing server response contains `observations` in both 7.0.5-source and
7.0.6-source contracts; absence-of-observations and agent-based backend selection
allegations from the old b5 checkout do not describe those sources. Search remains
project-scoped under the server's existing authorization/visibility policy.
Optional `session_id` on search in this repaired server supplies audit provenance
only; it is not authentication or session-private retrieval. An older server may
ignore that field. Static injection already accepts genuine session attribution.

Installation commands below change the host. Preserve its current configuration,
memory slot and useful memory/history before an owner-authorized install or slot
change. Fixture success does not authorize changing another consumer's grants,
configuration, history, host version or model settings.

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
| `contextLimit` | number | `10` | Maximum ordinary query-matched memories per prompt build, after ID deduplication |
| `sessionContextLimit` | number | `20` | Maximum ordinary static memories per prompt build, after ID deduplication |
| `tokenBudget` | number | `2000` | Combined prompt-enrichment character bound (`4 × tokenBudget`), including instructions, rules, escaping and wrappers; not an exact model-token measurement |
| `timeoutMs` | number | `5000` | Per-request HTTP timeout in milliseconds |
| `autoExtract` | boolean | `true` | Capture bounded visible conversation at `agent_end`/compaction and tool events; requires explicit conversation access |
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

### OpenClaw conversation access

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
history and enrich the model prompt. With `autoExtract`, bounded visible text and
tool events are also sent to the configured Engram server. Without the grant,
the host blocks conversation hooks; this plugin also refuses compaction/tool
capture instead of using those hooks to bypass the grant. Explicit memory tools
remain separate. `hooks.allowPromptInjection: false` blocks prompt enrichment.
The plugin does not change either host policy.

## What it provides

### Hooks

Hooks register automatically; prompt enrichment additionally needs the host opt-in above:

| Hook | Effect |
|---|---|
| `session_start` | Initializes tracking with the actual event session ID and canonical project when available; it has no initial prompt |
| `before_prompt_build` | Resolves the current project and composes static plus query-matched context under one budget |
| `after_tool_call` | Forwards actual SDK `params`/`result`, only with a genuine session ID and conversation grant |
| `before_compaction` | Captures bounded visible user/assistant text when the host supplies messages and the grant permits access |
| `agent_end` | Captures the final run transcript, including text blocks, through the same bounded serializer |

Prompt enrichment is supported through `before_prompt_build`, not before-tool
injection. This adapter does not register `before_tool_call`: the official 9.2
and 10.7 tool-hook result supports parameter/block/approval decisions, not prompt
context. No file-context bridge or tool-parameter rewriting is provided.

`session_end` declares metadata, not messages, so it is not a final-transcript
reader. The plugin never reads its `sessionFile` or fabricates an abandoned/task
outcome from missing messages. Agent-run `success` is not proof of user-task
acceptance; automatic phrase-based task outcomes are no longer emitted. Without
transcript evidence, extraction is skipped, not reported as successful.

Each prompt build resolves the current workspace afresh. Failed current identity
resolution delivers no previous project's context. Neither agent ID nor session
key substitutes for a missing conversation ID; retrieval can still use the
canonical project without conversation attribution. History is read-only and
turn tiers derive from that run's messages, not a shared cross-session counter.

`observations` is the selected memory pool, not a pool to append to `recent` and
`relevant` section views. Distinct `guidance`/`always_inject` rules are preserved
with ID deduplication; enabled router packets retain their routing semantics and
replace legacy always-active wording. Credentials remain excluded. Required
guidance has priority over ordinary memory counts and the dynamic tier budget.
Rule IDs and ordinary memory IDs are deduplicated independently. Any usable
enrichment, including dynamic-only results, includes its budgeted project/tool
instructions; genuinely empty responses remain empty.
All returned prompt contributions, including static instructions and escaped
wrappers, share `tokenBudget`. If complete required guidance cannot fit, the plugin
logs the budget refusal and returns no enrichment rather than truncating quoted
records, security wrappers or required rules. Server-side injection selection
audit is not an exact receipt of the smaller client-rendered subset.

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
5. Check the host-owned `hooks.allowConversationAccess` grant and ensure
   `hooks.allowPromptInjection` is not false

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
