# Claude Code Adapter

The Claude Code adapter is the reference implementation of the engram lifecycle
integration. It uses Node.js scripts executed by Claude Code's plugin system.

Source: `plugin/engram/hooks/`  
Registration: `plugin/engram/hooks/hooks.json`  
Shared runtime: `plugin/engram/hooks/lib.js`

---

## Automatic Lifecycle Model

`SessionStart` and `UserPromptSubmit` inject relevant context. `Stop` submits
assistant text extracted from the session transcript for citation and session
processing; the text may be truncated. If no transcript path is available and
both session ID and project are present, it attempts to post a minimal event with
empty assistant text; the request can fail. The registered `SessionEnd`
hook can request a retired outcome-propagation endpoint; its handler does not
record or propagate an outcome on the current server. Tool results are not captured
automatically. Use the MCP `store` and `recall` tools for deliberate memory
operations.

---

## Hook → Lifecycle Mapping

| Hook file | Claude Code event | Behavior |
|---|---|---|
| `session-start.js` | `SessionStart` | Context injection |
| `user-prompt.js` | `UserPromptSubmit` | Context injection |
| `stop.js` | `Stop` | Submits extracted assistant text (possibly truncated) for citation and session processing |
| `session-end.js` | `SessionEnd` | Attempts retired outcome callback; its handler returns HTTP 410 |
| `pre-compact.js` | `PreCompact` | Reinject request |

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

The hash-based project fields above describe a historical adapter shape, not
current project authority. The obsolete daemon raw-index/slug producer has been
removed; normal operations use server-resolved canonical UUID identity through
the V3 contract and never create or stage a hash-based `.engram-project` marker.
Use [Context recovery](../operating-engram.md#context-recovery) for the current
supported identity and existing-memory recovery procedure.

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
PreToolUse, UserPromptSubmit, SessionStart
```

For all other hooks (`Stop`, `PreCompact`, `SubagentStop`, `SessionEnd`) the
`hookSpecificOutput` key is omitted entirely; including it would fail Claude Code's
discriminated-union validation. These hooks deliver their output as side effects
(POST requests or file writes) rather than through the context injection path.

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

`agent_output_text` concatenates text extracted from assistant records in the JSONL
transcript. The hook handles both the nested shape
`{"type":"assistant","message":{"role":"assistant","content":[...]}}` used
by current Claude Code and flat shapes from older versions. The hook counts UTF-8
bytes of extracted source text against `MAX_OUTPUT_BYTES` (512 KiB) and may
truncate that text. It does not count the newlines inserted between parts or the
`[truncated]` suffix, so the final field is not strictly capped at 512 KiB. If no
transcript path is available and both session ID and project are present, the hook
attempts a minimal POST with empty `agent_output_text`; the request can fail. A
transcript read failure also results in empty text.

**Output:** Always `""`. `Stop` is not in `HOOKS_WITH_EVENT_NAME`, so
`additionalContext` would be dropped by `lib.writeResponse` anyway. Delivery is
purely via the POST request.

The server's asynchronous citation processing looks up an outcome already recorded
for the session and, when the feedback updater is available, applies
outcome-modulated feedback. Submitting assistant text does not itself record an
outcome. A caller may explicitly record a real observed outcome with the MCP
`feedback` tool using `action="outcome"`, the actual supported `session_id`, and
`outcome` (`success`, `partial`, `failure`, or `abandoned`); `reason` is optional.
This is a separate operation, not an automatic result of `Stop` or `SessionEnd`.

**Crash-safety:** `handleStop` attempts to delete the pending marker created by
`session-start.js` near its start. Successful deletion shows that marker cleanup
was reached, not that the assistant text was delivered or the whole session completed.

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

The registered `SessionEnd` dispatcher invokes `session-end.js`. If hook context
resolution succeeds and a session ID is present, the handler POSTs `{}` to
`/api/sessions/<URL-encoded-sessionID>/propagate-outcome` with a 1200 ms request
timeout. When the request reaches the current server's retirement handler, it
returns HTTP 410 with JSON `contract_version` `engram.outcome-retirement.v1`,
`code` `OUTCOME_CALLBACK_RETIRED`, and `action` `upgrade_outcome_adapter`. The
handler does not propagate or record the outcome. Authentication middleware can
instead return HTTP 401 before the request reaches that handler; context resolution
can also stop the hook before the POST.

When the request helper receives a 410, it treats the response as an error. The handler logs
`[session-end] propagate-outcome failed: ...` to stderr, catches the error, and
returns `""`; `lib.RunHook` then writes `{ "continue": true }` without
`hookSpecificOutput`. If the session ID is missing, it logs that it is skipping
and makes no request. The hook's `propagate-outcome fired` log is emitted only
when the request helper returns successfully; it is not proof of outcome recording.
This compatibility-temporary callback is separate from the `Stop` assistant-text
path and from explicit MCP outcome feedback.

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

| Item | hooks.json | Disk | Notes |
|---|---|---|---|
| `statusline.js` | Not in `hooks.json` | Present | Uses a separate statusline invocation path via `lib.RunStatuslineHook`, not the standard `RunHook` pipeline. |
