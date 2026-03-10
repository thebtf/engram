# Implementation Plan: Engram Claude Code Plugin + Marketplace

**Status:** COMPLETED (marketplace repo live, plugin functional, 2026-03-11)

## Summary

Create a standalone marketplace repository (`thebtf/engram-marketplace`) that distributes Engram as a proper Claude Code plugin. The plugin connects to a remote Engram server via HTTP MCP, Node.js hook scripts, and provides skills/commands. This replaces the broken install scripts and untested onboarding flow.

## Analysis Insights

**Key finding from confidence-check + challenging-plans critique:** Shell scripts are **NOT viable** for hooks. The hooks require:
- SHA256 hashing for `ProjectIDWithName()` (project identification must match server-side)
- Complex JSON parsing (JSONL transcript files, nested structures)
- Multi-call HTTP orchestration (user-prompt makes 3 sequential API calls)
- Cross-platform execution (Windows + macOS + Linux)

**Node.js is the correct choice** because:
- Guaranteed available wherever Claude Code runs (Claude Code IS a Node.js app)
- `crypto.createHash('sha256')` — matches Go's `sha256.Sum256()` exactly
- `JSON.parse()` / `readline` — handles JSONL and nested JSON natively
- `fetch()` built-in (Node 18+) — HTTP calls without `curl` dependency
- Cross-platform without hacks

**Current problems being solved:**
1. `plugin/` directory has stale claude-mnemonic branding (author: lukaszraczylo, SQLite+ChromaDB description)
2. Install scripts reference non-existent repo `thebtf/engram-plus`, no GitHub releases exist
3. Hooks are compiled Go binaries requiring per-platform distribution — overkill for remote server
4. MCP configured manually in `~/.claude.json`, not via plugin system
5. No tested onboarding flow exists

## Phases

### Phase 1: Create Marketplace Repository
Create `thebtf/engram-marketplace` on GitHub with the following structure:

```
thebtf/engram-marketplace/
├── .claude-plugin/
│   └── marketplace.json       # Registry of available plugins
├── plugins/
│   └── engram/
│       ├── .claude-plugin/
│       │   └── plugin.json    # Plugin metadata (Engram branding)
│       ├── .mcp.json          # MCP server config (type: http)
│       ├── hooks/
│       │   ├── hooks.json     # Hook event definitions (6 events)
│       │   ├── lib.js         # Shared utilities (ProjectIDWithName, HTTP, config)
│       │   ├── session-start.js
│       │   ├── user-prompt.js
│       │   ├── post-tool-use.js
│       │   ├── subagent-stop.js
│       │   ├── stop.js
│       │   └── statusline.js
│       ├── commands/
│       │   ├── restart.md     # /restart command
│       │   └── doctor.md      # /doctor diagnostic command
│       └── skills/
│           └── memory/
│               └── SKILL.md   # Memory management skill
└── README.md
```

- Task 1.1: Create GitHub repo `thebtf/engram-marketplace`
- Task 1.2: Write `.claude-plugin/marketplace.json` with plugin entry pointing to `./plugins/engram`
- Task 1.3: Write `plugins/engram/.claude-plugin/plugin.json` with Engram branding

### Phase 2: Plugin MCP Configuration
- Task 2.1: Write `plugins/engram/.mcp.json` with:
  ```json
  {
    "mcpServers": {
      "engram": {
        "type": "http",
        "url": "${ENGRAM_URL}",
        "headers": {
          "Authorization": "Bearer ${ENGRAM_API_TOKEN}"
        }
      }
    }
  }
  ```
  NOTE: Uses `ENGRAM_API_TOKEN` (matching existing codebase env var name from `internal/config/config.go:313`).
  `ENGRAM_URL` is NEW — a full URL (e.g., `http://unleashed.lan:37777/mcp`) replacing the host-only `ENGRAM_WORKER_HOST`.

- Task 2.2: VERIFY that `.mcp.json` `type: "http"` with `${ENV_VAR}` substitution works
  - Check Claude Code plugin loader source or test empirically
  - Fallback: use env var in hook scripts to construct MCP config at install time

### Phase 3: Node.js Hook Scripts
Replace compiled Go binaries with Node.js scripts for remote-server deployment.

#### Task 3.0: Shared library (`lib.js`)
Port essential functions from `pkg/hooks/`:
- `projectIDWithName(cwd)` — `path.resolve()` + `crypto.createHash('sha256')` + truncate to 6 hex chars
  - MUST produce identical output to Go's `ProjectIDWithName()` in `pkg/hooks/response.go:21-32`
- `getServerURL()` — reads `ENGRAM_URL` env var, falls back to `http://${ENGRAM_WORKER_HOST || '127.0.0.1'}:${ENGRAM_WORKER_PORT || 37777}`
- `httpGET(path)` / `httpPOST(path, body)` — fetch() wrappers with `ENGRAM_API_TOKEN` auth header
- `readInput()` — reads stdin, parses JSON, returns parsed object
- `writeResponse(additionalContext)` — outputs Claude Code hook response format:
  ```json
  {"continue": true, "hookSpecificOutput": {"hookEventName": "...", "additionalContext": "..."}}
  ```
- `isInternal()` — checks `ENGRAM_INTERNAL=1` (skip processing for internal calls)

#### Task 3.1: `session-start.js`
- Reads stdin JSON (source: "startup"/"resume"/"clear"/"compact")
- GET `/api/context/inject?project={projectID}&cwd={cwd}`
- Parses observations array from response
- Builds `<engram-context>` block with full detail (first N) + condensed (rest)
- Returns additionalContext via writeResponse()

#### Task 3.2: `user-prompt.js` (COMPLEX — 3 sequential API calls)
- Reads stdin JSON (prompt field)
- Step 1: GET `/api/context/search?project={projectID}&query={prompt}&cwd={cwd}` — find relevant memories
- Step 2: POST `/api/sessions/init` — initialize/resume session
- Step 3: POST `/sessions/{sessionId}/init` — start SDK agent
- Builds `<relevant-memory>` block from search results
- Returns additionalContext via writeResponse()

#### Task 3.3: `post-tool-use.js`
- Reads stdin JSON (tool_name, tool_input, tool_response)
- Checks skipTools list (Read, Glob, Task, AskUserQuestion, etc.)
- POST `/api/sessions/observations` with tool data
- Returns empty response (no context injection)

#### Task 3.4: `subagent-stop.js` (MISSING FROM ORIGINAL PLAN — added per critique)
- Reads stdin JSON
- POST to server with subagent completion data
- Returns empty response

#### Task 3.5: `stop.js` (COMPLEX — local JSONL parsing + 2 API calls)
- Reads stdin JSON (transcript_path)
- Step 1: Parse transcript JSONL file locally:
  - `fs.createReadStream()` + `readline.createInterface()`
  - Handle both string and array content formats
  - Extract last user and assistant messages
- Step 2: GET `/api/sessions?claudeSessionId={sessionId}` — find session
- Step 3: POST `/sessions/{sessionDbId}/summarize` with extracted messages
- Returns empty response

#### Task 3.6: `statusline.js`
- Reads stdin JSON (workspace, cost, context_window data)
- GET `/api/stats?project={projectID}`
- Formats ANSI-colored status line (supports default/compact/minimal formats)
- NOTE: statusline is registered via `settings.json`, NOT hooks.json
  The plugin install should handle this, or /doctor should configure it

#### Task 3.7: Write `hooks/hooks.json` with 6 hook events
```json
{
  "hooks": {
    "SessionStart": [{"hooks": [{"type": "command", "command": "node ${CLAUDE_PLUGIN_ROOT}/hooks/session-start.js", "timeout": 30}]}],
    "UserPromptSubmit": [{"hooks": [{"type": "command", "command": "node ${CLAUDE_PLUGIN_ROOT}/hooks/user-prompt.js", "timeout": 10}]}],
    "PostToolUse": [{"matcher": "*", "hooks": [{"type": "command", "command": "node ${CLAUDE_PLUGIN_ROOT}/hooks/post-tool-use.js", "timeout": 10}]}],
    "SubagentStop": [{"hooks": [{"type": "command", "command": "node ${CLAUDE_PLUGIN_ROOT}/hooks/subagent-stop.js", "timeout": 10}]}],
    "Stop": [{"hooks": [{"type": "command", "command": "node ${CLAUDE_PLUGIN_ROOT}/hooks/stop.js", "timeout": 30}]}],
    "Notification": [{"hooks": [{"type": "command", "command": "node ${CLAUDE_PLUGIN_ROOT}/hooks/statusline.js", "timeout": 5}]}]
  }
}
```

### Phase 4: Commands and Skills
- Task 4.1: Update `/restart` command for remote server
  - POST to `${ENGRAM_URL}/api/restart` (or GET `/api/health` to just verify)
- Task 4.2: Create `/doctor` command — diagnostics:
  - Check `ENGRAM_URL` and `ENGRAM_API_TOKEN` env vars are set
  - Check server reachability (GET `/health`)
  - Check MCP connection (POST `/mcp` with initialize)
  - Verify ProjectIDWithName produces matching output between Node.js and server
  - Report version info
- Task 4.3: Create `skills/memory/SKILL.md` — memory management skill:
  - How to search observations
  - How to manage collections
  - How to view session history

### Phase 5: Update Engram Main Repo
- Task 5.1: Update `plugin/` directory templates — fix branding to Engram, update description
- Task 5.2: Fix `scripts/install.sh` — change `thebtf/engram-plus` to `thebtf/engram`
  (keep script for local deployment, just fix the broken repo reference)
- Task 5.3: Fix `scripts/install.ps1` — same repo name fix
- Task 5.4: Update `README.md` with plugin installation option:
  ```
  ## Quick Install (Plugin)
  /plugin marketplace add thebtf/engram-marketplace
  /plugin install engram

  ## Manual Install (Local Deployment)
  See scripts/install.sh
  ```
- Task 5.5: Update `.goreleaser.yaml` release header to reference plugin installation
- Task 5.6: Reconcile `scripts/generate-plugin-config.sh` with marketplace
  - GoReleaser generates `.claude-plugin/` for release archives (local deployment)
  - Marketplace repo has its own `.claude-plugin/` (plugin deployment)
  - These serve different audiences — document the distinction
- Task 5.7: Update `docs/arch/QUICKSTART.md` and `docs/DEPLOYMENT.md`

### Phase 6: End-to-End Testing
- Task 6.1: FIRST — verify `/plugin marketplace add` and `/plugin install` CLI commands exist
  - Test on current Claude Code version
  - If commands don't exist, fall back to manual registration (jq scripts)
- Task 6.2: Verify ProjectIDWithName parity
  - Run Go version and Node.js version on same paths
  - Ensure SHA256 output matches
- Task 6.3: Test onboarding flow on Windows (current machine):
  1. Set ENGRAM_URL and ENGRAM_API_TOKEN env vars
  2. `/plugin marketplace add thebtf/engram-marketplace` (or manual registration)
  3. `/plugin install engram`
  4. Restart Claude Code session
  5. Verify MCP tools appear (engram search, timeline, etc.)
  6. Verify hooks fire (session-start loads context, stop saves summary)
  7. Run `/doctor` to validate installation
- Task 6.4: Verify hooks work with remote server (unleashed.lan:37777)
- Task 6.5: Document any issues found and fix

## Approach Decision

**Chosen approach:** Node.js hook scripts + separate marketplace repo

**Rationale:**
- Node.js guaranteed available wherever Claude Code runs (zero additional dependencies)
- Handles all hook complexity: SHA256 hashing, JSONL parsing, multi-call orchestration, structured JSON output
- Cross-platform without hacks (Windows, macOS, Linux)
- Separate marketplace repo keeps the engram source repo clean

**Alternatives considered and rejected:**
1. **Shell scripts (bash)** — REJECTED after confidence-check:
   - Cannot compute SHA256 project IDs portably
   - JSONL parsing with grep/sed is fragile (stop hook parses megabytes of nested JSON)
   - Not truly cross-platform (Windows needs Git Bash)
   - Needs curl + jq dependencies
2. **Compiled Go binaries (current approach)** — REJECTED for plugin distribution:
   - Plugin system works via git clone (no platform-specific binaries)
   - `EnsureWorkerRunning()` is wrong for remote server
   - Kept for local deployment via GoReleaser
3. **Bun bundle** — REJECTED: not guaranteed installed, marginal benefit over Node.js
4. **Monorepo (engram repo = marketplace)** — REJECTED: engram repo is heavy (ONNX models, CGO deps)

## Critical Decisions

- **Decision 1**: Node.js for hooks, not shell scripts or compiled binaries
  - Rationale: Cross-platform, zero deps, handles SHA256/JSONL/multi-call
  - Tradeoff: ~100ms startup per hook (acceptable with 10-30s timeouts)

- **Decision 2**: Marketplace in separate repo (`thebtf/engram-marketplace`)
  - Rationale: Lightweight clone, can host multiple plugins, no ONNX models
  - Tradeoff: Two repos to maintain, but plugin files change rarely

- **Decision 3**: `ENGRAM_URL` (full URL) replaces `ENGRAM_WORKER_HOST` (hostname only)
  - Rationale: MCP config needs full URL, hostname-only is insufficient
  - Tradeoff: New env var alongside existing ones — backward compatible
  - `ENGRAM_API_TOKEN` stays (matches existing codebase)

- **Decision 4**: Compiled Go binaries remain in GoReleaser for LOCAL deployment
  - Rationale: Users with local worker need `EnsureWorkerRunning()` + compiled hooks
  - Both paths coexist: plugin (Node.js, remote) vs local (Go binaries)

## Risks & Mitigations

- **Risk 1**: `ProjectIDWithName` SHA256 output mismatch between Node.js and Go
  - Mitigation: Write explicit test comparing outputs for same inputs
  - Key detail: Go uses `filepath.Abs(cwd)` — Node.js must use `path.resolve(cwd)`
  - Windows path separators may differ — normalize before hashing

- **Risk 2**: Claude Code plugin system may not support `/plugin marketplace add` or `.mcp.json` `type: "http"` with env vars
  - Mitigation: Test empirically in Phase 6.1 BEFORE building full structure
  - Fallback: Manual registration via jq scripts (like current install.sh)

- **Risk 3**: `node` command in hooks.json may not work on all platforms
  - Mitigation: Claude Code depends on Node.js — `node` should be in PATH
  - Fallback: Use full path to node if needed

- **Risk 4**: Marketplace repo versioning disconnect from server releases
  - Mitigation: Plugin files (hooks) change rarely since all logic is server-side
  - Process: Update marketplace repo only when API contract changes

## Files to Create/Modify

### New Files (in `thebtf/engram-marketplace` repo)
- `.claude-plugin/marketplace.json` — marketplace registry
- `plugins/engram/.claude-plugin/plugin.json` — plugin metadata
- `plugins/engram/.mcp.json` — MCP HTTP config
- `plugins/engram/hooks/hooks.json` — hook definitions (6 events)
- `plugins/engram/hooks/lib.js` — shared utilities (ProjectIDWithName, HTTP, config)
- `plugins/engram/hooks/session-start.js` — context injection on session start
- `plugins/engram/hooks/user-prompt.js` — memory search + session init (3 API calls)
- `plugins/engram/hooks/post-tool-use.js` — tool result capture
- `plugins/engram/hooks/subagent-stop.js` — subagent completion tracking
- `plugins/engram/hooks/stop.js` — transcript parsing + session summary
- `plugins/engram/hooks/statusline.js` — status bar formatting
- `plugins/engram/commands/restart.md` — restart command
- `plugins/engram/commands/doctor.md` — diagnostics command
- `plugins/engram/skills/memory/SKILL.md` — memory skill
- `README.md` — marketplace documentation

### Modified Files (in `thebtf/engram` repo)
- `plugin/.claude-plugin/plugin.json.tpl` — fix branding
- `plugin/.claude-plugin/marketplace.json.tpl` — fix branding
- `scripts/install.sh` — fix repo name `thebtf/engram-plus` → `thebtf/engram`
- `scripts/install.ps1` — fix repo name
- `README.md` — add plugin installation option
- `docs/arch/QUICKSTART.md` — update onboarding
- `docs/DEPLOYMENT.md` — update client setup
- `.goreleaser.yaml` — update release header

## Success Criteria

- [ ] `thebtf/engram-marketplace` repo exists on GitHub with correct structure
- [ ] `/plugin marketplace add thebtf/engram-marketplace` succeeds (or manual registration works)
- [ ] `/plugin install engram` installs plugin with hooks, MCP, commands
- [ ] MCP tools (engram search, timeline, etc.) appear after session restart
- [ ] Hooks fire correctly:
  - session-start loads context from server
  - user-prompt searches memories and returns relevant context
  - post-tool-use captures significant tool results
  - subagent-stop tracks subagent completion
  - stop parses transcript and sends summary
- [ ] ProjectIDWithName produces identical output in Node.js and Go for same paths
- [ ] `/doctor` command reports healthy status
- [ ] `/restart` command works with remote server
- [ ] Works on Windows (current machine) with remote server at unleashed.lan:37777
- [ ] No stale claude-mnemonic branding anywhere

## Plan Validation

**challenging-plans critique:** REVISE
**Key findings addressed:**
1. ✅ Hooks complexity acknowledged — Node.js replaces shell scripts
2. ✅ `subagent-stop` hook added (was missing)
3. ✅ API endpoints corrected to match actual server routes
4. ✅ ProjectIDWithName SHA256 parity explicitly planned and tested
5. ✅ Env var naming: `ENGRAM_API_TOKEN` kept (matches codebase), `ENGRAM_URL` is new (full URL)
6. ✅ `user-prompt` correctly documented as 3-call sequence, not fire-and-forget
7. ✅ `stop` JSONL parsing explicitly handled via Node.js readline
8. ✅ GoReleaser reconciliation planned (Task 5.6)
9. ✅ Plugin system verification added as Phase 6.1 prerequisite

**Codex Plan Review:** [pending]
