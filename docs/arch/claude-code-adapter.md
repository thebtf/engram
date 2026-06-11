# Claude Code Adapter

The Claude Code adapter is the reference implementation of the engram abstract event
model (see [EVENT\_MODEL.md](EVENT_MODEL.md)). It maps Claude Code's native plugin hook
events to the five canonical engram events using Node.js scripts executed by Claude
Code's plugin system.

Source: `plugin/engram/hooks/`  
Registration: `plugin/engram/hooks/hooks.json`  
Shared runtime: `plugin/engram/hooks/lib.js`

---

## Hook → Abstract Event Mapping

| Hook file | Claude Code event | Abstract event | Direction |
|---|---|---|---|
| `session-start.js` | `SessionStart` | `session_start` | server → agent (context injection) |
| `stop.js` | `Stop` | `session_end` | agent → server (transcript POST) |
| `pre-compact.js` | `PreCompact` | `pre_compact` | agent → server (reinject request) |
| `post-tool-use.js` | `PostToolUse` | `tool_result` | no-op (stub; see note) |

`memory_write` is not a hook; it is served directly by the MCP `store_memory` tool
via the stdio daemon.

---

## Environment and Configuration

Every hook process resolves `ENGRAM_URL` and `ENGRAM_TOKEN` at startup via
`lib.getEngramConfig()`. The resolution chain (highest priority first):

1. `ENGRAM_URL` / `ENGRAM_TOKEN` environment variables
2. Claude Code plugin option env (`CLAUDE_PLUGIN_OPTION_server_url`,
   `CLAUDE_PLUGIN_OPTION_api_token`)
3. Legacy userConfig aliases (`ENGRAM_CLAUDE_USERCONFIG_URL`,
   `ENGRAM_CLAUDE_USERCONFIG_TOKEN`)
4. Config file: `$ENGRAM_CONFIG_FILE` → `<pluginData>/config.json` →
   `~/.engram/config.json`

The resolved values are written back to `process.env` so that any child process
spawned by the hook inherits them. See `lib.getEngramConfig()` and
`lib.resolveConfigFilePath()` for the full implementation.

All HTTP calls use `lib.requestGet` / `lib.requestPost`, which wrap `fetch` with a
configurable `AbortController` timeout. The server origin is extracted from
`ENGRAM_URL` (path stripped) or constructed from `ENGRAM_WORKER_HOST` /
`ENGRAM_WORKER_PORT` (default `127.0.0.1:37777`).

---

## Common Input Shape

Claude Code passes each hook invocation on stdin as a JSON object. `lib.RunHook`
reads stdin, parses it, derives a context object, and calls the handler with
`(ctx, input)`.

**Context fields derived by `lib.RunHook`:**

| Field | Source | Description |
|---|---|---|
| `ctx.SessionID` | `input.session_id` | Claude Code session UUID |
| `ctx.CWD` | `input.cwd` | Working directory of the Claude Code session |
| `ctx.Project` | `lib.ProjectIDWithName(cwd)` | Stable project slug (git-remote SHA-256 or path hash) |
| `ctx.LegacyProject` | `lib.LegacyProjectID(cwd)` | Path-based 6-char hash (migration compatibility) |
| `ctx.PermissionMode` | `input.permission_mode` | Claude Code permission level |
| `ctx.HookEventName` | `input.hook_event_name` or hook name | Discriminated event name |
| `ctx.GitRemote` | `git remote get-url origin` | Remote URL if in a git repo |
| `ctx.RelativePath` | `git rev-parse --show-prefix` | Path within the repo |
| `ctx.RawInput` | stdin string | Original JSON stdin payload (for debugging or custom parsing) |

**Project identity algorithm** (mirrors `internal/proxy/identity.go:ResolveProjectSlug`):

- Git repo with remote: `SHA-256(remoteURL + "/" + relativePath)`, first 8 hex chars.
- Non-git fallback: `SHA-256(absolutePath)`, first 6 hex chars.

---

## Output Contract

All hooks write a JSON object to stdout via `lib.writeResponse`:

```json
{ "continue": true }
```

When the hook produces context text, it is embedded in `hookSpecificOutput`:

```json
{
  "continue": true,
  "hookSpecificOutput": {
    "hookEventName": "<HookName>",
    "additionalContext": "<text>"
  }
}
```

Claude Code only accepts `hookSpecificOutput` on a defined set of hooks. The
constant `HOOKS_WITH_EVENT_NAME` in `lib.js` tracks which hooks support it:

```
PreToolUse, UserPromptSubmit, PostToolUse, SessionStart
```

For all other hooks (`Stop`, `PreCompact`, `SubagentStop`,
`SessionEnd`) the `hookSpecificOutput` key is omitted entirely; including it
would fail Claude Code's discriminated-union validation. These hooks deliver
their output as side effects (POST requests, file writes) rather than through
the context injection path.

**Exit codes:** Non-zero exit is treated by Claude Code as a hook failure. All
engram hooks catch errors and call `lib.writeResponse` before exiting, so the
exit code is always 0 in practice.

---

## Primary Hooks

### `session-start.js` — `session_start`

**Trigger:** Fires once when Claude Code initialises a session, before the first
user prompt is processed. The `ensure-binary.js` preflight script runs first in
the same `SessionStart` hook group (60 s timeout); `session-start.js` runs second
(30 s timeout).

**Stdin input schema:**

```json
{
  "session_id": "string",
  "cwd": "string",
  "permission_mode": "string",
  "hook_event_name": "SessionStart"
}
```

**Server interaction:**

1. `POST /api/store` — fire-and-forget timeline event `"Session started on <project>"`.
2. `GET /api/context/session-start?project=<slug>` (5 s timeout) — fetches the
   session-start payload containing issues, behavioral rules, and recent memories.
3. `POST /api/issues/acknowledge` — acknowledges open issue IDs (fire-and-forget).

**Response output shape:**

On success the handler returns a plain text string which `lib.writeResponse`
embeds as `additionalContext` in `hookSpecificOutput` (SessionStart is in
`HOOKS_WITH_EVENT_NAME`). The string is composed of up to three XML blocks in
order:

```
<open-issues count="N" project="..." action-required="true">
  ...
</open-issues>

<user-behavior-rules>
  # Behavioral Rules (Always Active)
  ...
</user-behavior-rules>

<engram-static-memories>
  # Recent Memory
  ...
</engram-static-memories>
```

Blocks are omitted when their array is empty.

**Cache / degraded mode:**

On live-fetch failure the hook falls back to a project-scoped cache file at
`<pluginData>/cache/session-start-<slug>.json`. If a cache exists the context is
injected with a stale warning banner:

```
<engram-session-start-stale>
WARNING: Engram session-start context is stale because live fetch failed. ...
</engram-session-start-stale>
```

If neither live fetch nor cache is available, the hook injects a no-cache banner
and continues:

```
<engram-session-start-unavailable>
WARNING: Engram session-start context is unavailable and no cache is present. ...
</engram-session-start-unavailable>
```

**Crash-safety:** On startup the hook creates a pending marker in the OS temp
directory (`$TMPDIR/.engram-pending-<sessionID>`). At each subsequent session
start any markers older than 2 hours are treated as evidence of a crashed
session and a `timeline` observation is POSTed to `/api/store` (fire-and-forget).

**Not-configured path:** When `ENGRAM_URL` or `ENGRAM_TOKEN` are absent after the
full resolution chain, the hook returns a setup guidance block instead of
attempting any server call:

```
<engram-setup>
Engram plugin is installed but not configured.
...
</engram-setup>
```

---

### `stop.js` — `session_end`

**Trigger:** Fires when Claude Code's agent turn ends (the `Stop` event). Timeout
in `hooks.json`: 90 s.

**Stdin input schema:**

```json
{
  "session_id": "string",
  "cwd": "string",
  "transcript_path": "string",
  "hook_event_name": "Stop"
}
```

`transcript_path` is the primary path to the JSONL session transcript. If absent
the hook falls back to `CLAUDE_CONVERSATION_PATH` or `CLAUDE_TRANSCRIPT_PATH` env
vars.

**Server interaction:**

`POST /api/hooks/session-end` (60 s timeout):

```json
{
  "session_id": "string",
  "project": "string",
  "agent_output_text": "string"
}
```

`agent_output_text` is extracted from the JSONL transcript by collecting lines
where `role === "assistant"` (or `type === "assistant"`). The hook handles both
the nested shape `{"type":"assistant","message":{"role":"assistant","content":[...]}}` used
by current Claude Code and flat shapes from older versions. Content is capped at
500 KB (`MAX_OUTPUT_BYTES`); truncated payloads include a `[truncated]` suffix.

**Output:** Always `""`. `Stop` is not in `HOOKS_WITH_EVENT_NAME`, so
`additionalContext` would be dropped by `lib.writeResponse` anyway. Delivery is
purely via the POST request.

**Crash-safety:** The pending marker created by `session-start.js` is deleted at
the start of `handleStop`. Successful deletion confirms a clean session end.

**Error handling:** All failures (transcript read, POST) are logged to stderr and
swallowed. The hook always exits 0.

---

### `pre-compact.js` — `pre_compact`

**Trigger:** Fires immediately before Claude Code compacts the context window
(`PreCompact` event). Timeout in `hooks.json`: 10 s.

**Stdin input schema:**

```json
{
  "session_id": "string",
  "cwd": "string",
  "trigger": "manual|auto",
  "custom_instructions": "string",
  "hook_event_name": "PreCompact"
}
```

`trigger` is `"manual"` when the user ran `/compact` and `"auto"` when
Claude Code compacted automatically. `custom_instructions` carries any
instructions the user passed to `/compact` (empty string when none).

`extractTopic` in `pre-compact.js` reads `input.summary`,
`input.last_human_message`, and `input.conversation_title` as fallback
sources that earlier Claude Code versions may have populated; current
Claude Code sends neither, so `extractTopic` returns `""` and the
reinject request uses a project-wide query instead of a topic-scoped one.

**Server interaction:**

1. `POST /api/context/reinject` (8 s timeout):

   ```json
   {
     "project": "string",
     "topic": "string",
     "session_id": "string",
     "limit": 10
   }
   ```

   On success, if the response contains memories, the hook writes them to
   `.engram/reinjection.md` in the project CWD. If no memories are returned and
   the file exists it is deleted (prevents stale guidance on subsequent cycles).

2. `GET /api/context/inject?project=<slug>[&query=<topic>]` (8 s timeout,
   fire-and-forget) — primes the server-side inject cache.

**Output:** Always `""`. `PreCompact` is not in `HOOKS_WITH_EVENT_NAME`; Claude
Code drops any `additionalContext` from this hook. Re-injection is delivered via
the `.engram/reinjection.md` file, which the agent reads on the next turn.

**Error handling:** POST failure is logged to stderr; the fire-and-forget GET
failure is silently ignored. The hook always exits 0.

---

### `post-tool-use.js` — `tool_result`

**Trigger:** Fires after any tool call whose name matches the matcher
`Write|Edit|Bash|Agent|mcp__aimux` (`PostToolUse` event). Timeout in
`hooks.json`: 10 s.

**Stdin input schema (passed by Claude Code):**

```json
{
  "session_id": "string",
  "cwd": "string",
  "tool_name": "string",
  "tool_input": {},
  "tool_response": {},
  "hook_event_name": "PostToolUse"
}
```

**Handler behaviour:** The current implementation is a stub that returns `""`
immediately without reading the input or making any server calls:

```js
async function handlePostToolUse() {
  return '';
}
```

The `tool_result` abstract event is not yet implemented at the hook level. The
hook file exists and is registered so the matcher-based filtering mechanism is in
place for future use.

**Output:** Always `""`. `PostToolUse` is in `HOOKS_WITH_EVENT_NAME` so the
empty string produces `{ "continue": true }` with no `hookSpecificOutput`.

> **Code vs. spec note:** `EVENT_MODEL.md` describes `tool_result` as mapping to
> `post-tool-use.js`. The hook is registered and runs, but the handler body is a
> no-op. No server endpoint is called. This is accurate as of the current
> codebase.

---

## Auxiliary Hooks

These hooks fire on additional Claude Code events. They do not map to abstract
engram events but extend the adapter with operational and observability features.

### `user-prompt.js` — `UserPromptSubmit`

Fires before each user prompt is submitted (10 s timeout). Reads `input.user_message`
or `input.message`. Makes two fire-and-forget POSTs: `POST /api/hooks/segment-check`
(topic shift detection, up to 2000 chars) and, when correction patterns are detected in
the prompt text (multilingual regex list), `POST /api/hooks/correction` (up to 5000
chars). Returns `""`. `UserPromptSubmit` is in `HOOKS_WITH_EVENT_NAME`; the empty return
produces `{ "continue": true }` with no `hookSpecificOutput`.

### `pre-tool-use.js` — `PreToolUse`

Fires before tool calls matching `Edit|Write` (1 s timeout). For `Edit` and
`Write`, fetches file-level context from `GET /api/context/by-file` (200 ms) and
trigger-based context from `POST /api/memory/triggers` (200 ms) in parallel.
Returns a `systemMessage` JSON string containing a `<file-context>` block with
warnings and context observations classified by type. Returns `""` on cache miss,
skip path (temp dirs, `node_modules`), or fetch error. `PreToolUse` is in
`HOOKS_WITH_EVENT_NAME` so the `systemMessage` payload is valid.

> **Matcher limitation:** `hooks.json` registers `PreToolUse` with matcher
> `Edit|Write`. The handler in `pre-tool-use.js` also contains code branches for
> `Bash` and `Read` (trigger-context-only path), but Claude Code never invokes
> the hook for those tools with the current registration. Those branches are
> inactive unless the matcher is extended.

### `session-end.js` — `SessionEnd`

Fires at session cleanup after `Stop` (`SessionEnd` event, 1500 ms timeout — the
smallest timeout of all registered hooks). Calls
`POST /api/sessions/<sessionID>/propagate-outcome` (1200 ms timeout) to trigger
server-side propagation of the session outcome to dependent records. Returns `""`.
`SessionEnd` is not in `HOOKS_WITH_EVENT_NAME`. Exists as a lightweight finalizer
distinct from the full transcript processing in `stop.js`.

### `subagent-stop.js` — `SubagentStop`

Fires when a subagent (spawned via the `Agent` tool) completes (10 s timeout).
Calls `POST /api/sessions/subagent-complete` with `{ claudeSessionId, project }`.
Returns `""`.

### `statusline.js` — statusline

Not registered in `hooks.json`. A standalone statusline renderer invoked via a
separate Claude Code statusline plugin mechanism using `lib.RunStatuslineHook`.
Currently returns a static string `[engram] ○ v5 cleanup in progress`. Offline
fallback renderer is the same function.

---

## Known Discrepancies

| Item | hooks.json | Disk | Notes |
|---|---|---|---|
| `post-tool-use.js` | Registered with matcher `Write\|Edit\|Bash\|Agent\|mcp__aimux` | Present, handler is a stub | `tool_result` abstract event has no server-side implementation yet. |
| `statusline.js` | Not in `hooks.json` | Present | Uses a separate statusline invocation path via `lib.RunStatuslineHook`, not the standard `RunHook` pipeline. |
