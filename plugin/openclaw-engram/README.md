# openclaw-engram

OpenClaw plugin that connects agents to an [engram](https://github.com/thebtf/engram) persistent memory server for long-term context retention.

## Prerequisites

- OpenClaw gateway installed and running
- Engram server running and reachable (default port `37777`)
- An API token configured on the engram server
- The matching release Go client (`engram`) on PATH, or its absolute executable
  selected with `ENGRAM_EXECUTABLE`. It must support
  `project resolve`, `project choices` and `project select` with the current JSON
  caller-context contract; no JS/TS identity fallback exists.

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
`project` UUID override. Missing or invalid project context returns a scoped error.
Ambiguous existing-project recovery uses `engram_project_context` as described below;
the adapter does not replace host configuration or infer another caller workspace.
Explicit global Vault calls do not require project context. Channel `/migrate`
uses the configured `workspaceDir` or the documented `~/.openclaw/workspace/`
default. Before importing, it resolves that workspace through the Go client unless
a valid `project` UUID override is configured.

### Project context recovery

When an operation is held by `PROJECT_MIGRATION_REQUIRED`, conflicting identity
or a stale selection, use the ordinary registered `engram_project_context` tool:

1. Call it with `action: "choices"`. This reads fresh authorized existing projects
   without selecting one. Review each name, full `project_id`, available
   `repository_qualifier` and opaque `selection_ref` as catalog data.
2. Identify the intended existing project from the original task. If that remains
   ambiguous, present the meaningful choices. Do not select first/latest, by name
   alone or merely because one result exists; an empty list does not authorize
   creation of a replacement project.
3. Call `action: "select"` with that exact fresh `selection_ref` in the same caller
   workspace/server. The native Go client rechecks authorization and owns the
   caller-scoped binding. No marker/cache/data migration, permission change or
   fabricated session is performed by the adapter. Do not reuse a previously
   visible catalog after denial.
4. After confirmed selection, retry the original operation unchanged, including
   its target and actual actor/session/client. Neither recovery action replays
   the operation automatically. A lost selection response is an unknown outcome:
   the binding may already have changed. Recovery also does not prove an earlier
   issue/memory write failed; inspect unknown mutation outcomes before retrying.

If `choices` or `select` is unavailable, hold recovery until the relevant
connectivity, authorization or local-storage problem is resolved. Reconcile an
unconfirmed selection using the bounded owner-inspection guidance below; if the
binding or outcome cannot be established safely, it remains unknown and the
operation stays held. Refresh stale choices only after access is restored and any
unknown selection outcome is reconciled. Do not replay an unknown write; preserve
the original operation, target, actor, session and client.

For an existing selected caller binding, a changed marker digest is accepted only
when the valid pinned canonical ID and caller scope still match. The client
rechecks the fresh authorized catalog, rejects a different ID or a race, and does
not rewrite the marker or binding during revalidation.

Caller-binding publication does not depend on the old persistent `.lock` file and
does not alter it. Atomic no-overwrite publication preserves existing identity
files, including the marker and binding. Ordinary `project resolve` can bootstrap
and persist when no binding is established; `project resolve --check` does not
prove a caller selection. Neither is universal read-only confirmation of an
unknown selection.

Both actions use only the actual tool `ctx.workspaceDir`, never terminal cwd,
`config.workspaceDir`, another project or a caller-supplied path. Missing host
workspace returns `PROJECT_CONTEXT_REQUIRED` before a child is launched. The
installed native executable receives fixed argument arrays without a shell;
configured URL/token are passed only in its child environment. The public tool
accepts no root, project UUID, credential or session override. An explicit
configured `project` override remains unchanged and continues to govern the
operations that use it.

The actual `ctx.workspaceDir` is passed as the project root, not as the child
executable lookup cwd. Relative `PATH` entries can therefore depend on the
inherited parent cwd; use an absolute `ENGRAM_EXECUTABLE` when appropriate.

Output contains only validated public choice/selection fields and static recovery
guidance, with quoted prompt-safe values. Native output is bounded to 2 MiB and
4096 choices; rendered choice output is also bounded to 2 MiB. Oversized, malformed
or denied results do not expose a partial catalog, raw stderr, credentials or
private caller paths. `PROJECT_IDENTITY_INVALID` and unknown refusals retain the
preserve-first owner boundary: bounded regular-file inspection (4096 bytes maximum)
and authorized repair, not a repeated check loop, shell/SQL workaround, symlink
traversal, deletion or initialization of a replacement project.

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
| `engram_remember` | Store a memory with title, type tag and project/global/agent-private scope |
| `memory_store` | Store a project-scoped memory (memory-core compatible parameters) |
| `engram_decisions` | Retrieve past architectural decisions by query |
| `memory_get` | Read a local workspace Markdown file, optionally store it, or search by query |
| `engram_suppress` | Suppress a retained memory by ID; `action: "unsuppress"` restores a flagged memory |
| `memory_forget` | Suppress by default; `permanent: true` archives without erasing the memory |
| `memory_migrate` | Import local `MEMORY.md` / `memory/**/*.md` files into engram |
| `engram_issues` | Create, list, read, comment, resolve, close or reopen cross-project issues |
| `engram_project_context` | Read fresh authorized project choices and explicitly select an existing caller context |

All explicit memory writers, file migration and file synchronization use the canonical
`POST /api/memories`. The retired observations import endpoint is not retried or
used as a fallback. A successful result requires a returned memory ID and matching
project/content. A distinct title is retained before the body in `content`; a title
identical to the body is not duplicated. Observation type
uses the existing `type:<type>` tag convention, not `epistemic_type`. Supplied tags
are retained. This does not restore the retired observation table or its native
title/type fields, near-duplicate detection or transaction-wide bulk import.

`memory_store` is project-scoped. `engram_remember` with `scope: "agent"` requests
principal-private visibility within the project; the server derives the owner
from the configured bearer keycard and refuses a write without a principal.
Host `agentId` is source attribution, not authentication. Use an agent-bound
keycard for that agent; sharing one principal across agents shares its private
memory. Workstation-private scope is not substituted for agent privacy.
Memory writer tools preserve the host `sessionId` and fall back to its `sessionKey` when
the ID is absent. These are source-session identifiers, not authentication; a
project UUID is never substituted. Commands, CLI and the watcher have source
labels but do not invent host session metadata.

Lifecycle tools use authenticated `POST /api/memories/{id}/transition` with
`action: "suppress"`, `"unsuppress"` or `"archive"`. Suppress changes active to
flagged; unsuppress changes flagged to active; archive accepts active or flagged.
Each successful transition increments the version once and preserves the row,
content and `deleted_at`. Flagged/archived memories leave active lists and context
search; nondeleted owner detail reads remain available. Archive is not data erasure
and cannot be undone through these tools.

These by-ID calls do not require workspace resolution. A configured canonical
`project` UUID is sent as an optional narrowing selector, never as an access grant.
When absent, the server resolves the memory's unique live canonical membership
and applies current caller visibility and domain-manage policy. An explicitly
empty, invalid, global/self or different-project selector does not fall back to
another project. Revoked keys are rejected by the normal bearer middleware.
The legacy `/{id}/suppress` delete path and browser selection/version protocol
are unchanged.

Success requires a matching action, memory ID, resulting status, version and
canonical project in the server receipt. A lost or invalid acknowledgement is
reported as an unknown outcome, not success or proof of failure. Inspect the
memory before deciding whether another action is appropriate; no automatic retry
or retired observation endpoint fallback is used.

File migration and synchronization retain `.engram-migrated.json` file hashes and
confirmed per-chunk IDs. Before each POST, an atomic checkpoint records a `pending`
attempt. A valid response changes it to a confirmed ID; a definitive refusal is
recorded as `rejected`, an unsent request as `not_sent`, and an ambiguous response
as `unknown`. A process interrupted between the POST and confirmation leaves
`pending`, so a later watcher event cannot automatically resend it. Incomplete
files are not marked done. Existing in-process marker operations are serialized;
there is no persistent lock, PID/TTL reclaim or new background retry. As before,
independent processes sharing a marker are not a supported coordinated writer.

To recover an unresolved file through the existing user surface:

1. Inspect the reported project, source file and server outcome before deciding
   whether another write is appropriate. A lost response does not prove absence.
2. If you deliberately want to reimport all chunks despite possible duplicates,
   invoke `memory_migrate` with `force: true`, `/migrate --force [path]`, or
   `openclaw memory migrate --force`. This is an explicit new write, not a safe
   retry or proof that the earlier attempt failed.
3. Read the result: confirmed replacement IDs and earlier unknown attempts are
   reported separately. Only after every chunk in that forced file has a matching
   confirmed response does an atomic `force-reimport-with-duplicate-risk`
   acknowledgement permit normal future file updates. It covers only earlier
   attempts whose exact chunk fingerprint received a confirmed replacement in
   that force. Forcing different content B never acknowledges unknown content A;
   returning to A remains blocked. The original unknown history stays unchanged;
   no prior write is declared absent or confirmed. A failed or partial force does
   not acknowledge the risk. Exactly-once delivery is not claimed.

An unchanged legacy file checkpoint without project attribution is preserved but
not accepted as proof of completion for a selected project. Inspect it, or use the
same explicit force reimport with its duplicate risk. Dry-run stays offline and
does not invoke native project resolution or write progress. It can compare a
checkpoint with a locally validated configured project UUID; without that UUID it
shows candidates and marks checkpoint project attribution unverified, rather than
treating any file as completed.

#### Issue lifecycle and attribution

`engram_issues` retains `create`, `list`, `get`, `update`, `comment` and `reopen`,
and exposes `close` through the registered action schema. Use
`update` with `status: "resolved"` for a reported solution awaiting verification;
use `close` after checking completion, or `reopen` when verification fails.
`acknowledged` and `reopened` remain active backlog. A status label or lifecycle
entry alone does not prove historical verification. Authorized participants may
resolve, close and reopen under backend policy; the adapter adds no authorization.

Mutations resolve the canonical caller project and preserve the host `agentId`
as agent attribution. They separately report `client: "openclaw"`. Creation sends
the exact host `ctx.sessionId` as `created_by_session`; comments and lifecycle
PATCH requests send it as `session_id`. When the host supplies no session ID,
session attribution stays absent: no session key, project, installation or
authentication fallback is used. Caller-supplied attribution cannot override the
host context. Existing issue project routing is unchanged by these calls.
These HTTP fields are reported provenance, not authentication or proof of session
ownership. Close reports success only after the existing PATCH call succeeds;
an unavailable server or failed response is not reported as successful closure.

Optional `target_session` on `create` records an explicit recipient reference,
separate from the creator's actual host session. Empty string or `null` means no
recipient; omission never infers one. `update` can set/change that recipient, or
clear it with `target_session: ""` or `target_session: null`; omission preserves
the stored recipient. Recipient-only updates do not change status. If supplied,
`status` remains restricted to `"resolved"`; an update without either status or
recipient is invalid, including a comment-only update. Recipient edits, including
mixed status/recipient updates, remain subject to backend creator/operator
source-field authorization. Being named as recipient grants no permission and
does not prove delivery or that a session is live.

Explicit `list` selectors are independent: `source_session` matches only issue
creation attribution, `target_session` matches the stored recipient, and
`participant_session` matches creator OR authored-comment OR lifecycle-event
attribution. Multiple supplied filters compose with AND before count/pagination.
Session-only filters retain the canonical default project inbox; existing
explicit project filters retain their scope. No selector defaults to the caller's
session or establishes identity/ownership. Nonempty references may be UUIDs or
opaque host conversation IDs, up to 256 UTF-8 bytes, without surrounding
whitespace, control/formatting characters or a nil UUID. Empty, null or malformed
explicit list selectors are rejected rather than broadening the read.
List/detail output includes an available nonempty `target_session`; missing or
empty recipient data is not invented from actor attribution.

Detail output renders available `source_client`, `created_by_session` and
`created_by_session_source`, plus comment `author_client`, `author_session` and
`author_session_source`. Available `history_events` are rendered separately from
authored comments, including status, actor, session, provenance source, client and
timestamp. All values use the existing escaped, quoted prompt-safe formatter.
Older responses without these optional fields or history remain readable;
missing provenance is not invented.

`get` accepts `history_limit` (integer 1..100, default 50) and `history_after_id`
(exclusive nonnegative event-ID cursor, default 0). Cursors must be exactly
representable JavaScript safe integers. The client requests one page only;
comments are unchanged. When the server reports `history_has_more: true` and a
valid advancing `history_next_after_id`, output includes the exact next
`engram_issues` get call with that cursor and the same page size. Only an explicit
consumer call fetches another page. A missing/invalid cursor reports continuation
unavailable; an older response without pagination metadata reports unknown
completeness, never an invented terminal page.

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
