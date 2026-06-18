# Production Testing Playbook — engram

**Purpose:** Customer-mode walkthrough of the engram product. Run before every
release. The agent (or human reviewer) walks through the scenarios pretending
to be a user with no internal knowledge — the public docs and this playbook
are the only allowed inputs.

**Bootstrap version:** v6.0.0 — first playbook for this project, scoped to
the surfaces touched by the v6 auth release (server, plugin install, CLI
proxy, dashboard token issuance). Future releases extend the scenario list.

## Scope

The playbook covers the following surfaces:

| # | Surface | Binary / Component |
|---|---------|--------------------|
| 1 | Server | `cmd/engram-server` — HTTP API + gRPC + dashboard on :37777 |
| 2 | CLI client | `cmd/engram` — stdio MCP proxy invoked by Claude Code |
| 3 | Dashboard | `ui/` Vue 3 SPA served by the server |
| 4 | Claude Code plugin | `plugin/engram` — installed via `/plugin marketplace add thebtf/engram-marketplace` |

Out of scope for this bootstrap: `cmd/engram-import`, full Unraid deployment
flow (covered separately by `docs/DEPLOYMENT.md`), backup/restore.

## Prerequisites

- Go 1.25+ (`go version`)
- Docker (for postgres dependency in scenario S2)
- Node 20+ (for building embedded dashboard assets in scenarios S1/S3)
- A running engram server (local or `unleashed.lan:37777`) for scenarios S2/S3/S4
- Claude Code CLI installed (for scenario S4)

## Canonical scenarios

### S1 — Build all binaries from source

**As a user, I clone the repo and run `go build` to produce binaries.**

Steps:
1. From repo root run `npm ci` and `npm run build` in `ui/`
2. Copy the fresh `ui/dist/*` bundle into `internal/worker/static/`
3. From repo root run `go build -o /tmp/engram-server.exe ./cmd/engram-server`
4. From repo root run `go build -o /tmp/engram.exe ./cmd/engram`
5. Run each binary with `--help` (or no args) and observe usage output

Expected:
- Dashboard assets are embedded in the server binary; a plain server build
  without a fresh copied bundle is not a release-candidate smoke.
- The copied dashboard bundle is a build input in the working tree, not a
  tracked source artifact; `internal/worker/static/placeholder.html` remains
  the tracked placeholder for empty-source builds.
- Both `go build` invocations exit 0 with no compiler errors
- Both binaries print usage / startup banner without crashing

Failure signals:
- `undefined: <symbol>` errors — auth refactor incomplete
- `package not found` — module path drift
- Binary panics on `--help`

### S2 — Server starts cleanly with required env vars

**As an operator, I start the server with the v6 admin token env var.**

Steps:
1. Set `ENGRAM_AUTH_ADMIN_TOKEN=test-operator-key`
2. Set `DATABASE_DSN=...` (the canonical name read by `internal/config/config.go`;
   the production-candidate path requires PostgreSQL)
3. Run `engram-server.exe`
4. Observe startup logs

Expected:
- Server logs `cmux listening on :37777` (or equivalent)
- No `panic`, no `FATAL` lines
- HTTP `GET /api/health` returns 200

Failure signals:
- Server exits during startup citing missing env var -> FR-4 violated
- Server exits because `DATABASE_DSN` is omitted -> the release smoke is not
  configured for the current PostgreSQL-backed runtime path
- Logs warn about a deprecated env var rename (the v6 surface is
  `ENGRAM_AUTH_ADMIN_TOKEN` server-host-only; on workstations the
  pre-v6 `ENGRAM_AUTH_ADMIN_TOKEN` was renamed to `ENGRAM_TOKEN`)
- gRPC bind fails

### S3 — Dashboard loads and `/#/tokens` page is reachable

**As an operator, I open the dashboard, complete first-run setup, log in, and visit the token page.**

Steps:
1. With server running, open `http://localhost:37777/`
2. If this is a fresh database, call the first-run setup flow and create the
   first admin account
3. Log in with that admin account; setup does not create an authenticated
   browser session by itself
4. Navigate to `http://localhost:37777/#/tokens`
5. Observe the token issuance UI

Expected:
- Dashboard SPA loads
- `/#/tokens` page renders without severe console errors
- "Create Token" button is visible to session-admin only
- `GET /api/auth/tokens` returns 200 for the logged-in session-admin caller
- Unauthenticated `GET /api/auth/me` 401 responses before login are expected

Failure signals:
- 404 on `/#/tokens` route
- 403 on `/api/auth/tokens` from a session-admin caller (regression of FR-6)
- Bearer-only callers receive 200 on `/api/auth/tokens` (privilege escalation)

### S4 — Plugin installs in Claude Code and exposes MCP tools

**As a Claude Code user, I install the engram plugin and use it.**

Real Claude Code plugin installation mutates the operator's consumer home. In
automated release emulation, prefer an isolated disposable home with the plugin
wrapper pointed at the release-candidate `cmd/engram` binary. Run the real
consumer-home path only when the operator explicitly authorizes that mutation.

Steps:
1. Start an engram server with PostgreSQL and create a keycard via S3
2. In an isolated disposable consumer home, install or symlink the release
   plugin wrapper
3. Configure plugin/user settings:
   - `server_url=http://unleashed.lan:37777` (or local)
   - `api_token=<keycard issued via S3>`
4. Start the MCP client/proxy through the same wrapper path a consumer uses
5. Verify `tools/list` and run at least one harmless health/read tool
6. If explicitly authorized for a real Claude Code smoke, install via
   `/plugin marketplace add thebtf/engram-marketplace`, restart Claude Code,
   and ask "what engram tools do I have?"

Expected:
- Isolated plugin smoke starts without errors
- The assistant or `tools/list` surface lists tools beyond `loom_*` —
  e.g., `mcp__engram__store_memory`, `mcp__engram__list_issues`,
  `mcp__engram__credential_*`, etc.
- The token field maps to the `ENGRAM_TOKEN` env var (FR-3)

Failure signals (the bug this release fixes):
- Only `loom_*` tools visible → plugin auth wiring broken
- `engram MCP server failed to initialize` in logs
- Daemon exits with `ENGRAM_TOKEN required` despite token being configured

## Failure-mode catalog

| Signal | Likely cause | Where to look |
|---|---|---|
| Only `loom_*` tools in Claude Code | Plugin env var name mismatch | `plugin/engram/.mcp.json` `env` block |
| Server starts but `/api/auth/tokens` 500 | Validator wiring drift | `internal/grpcserver/server.go` SetValidator |
| gRPC accepts master, rejects keycard | FR-2 regression (PR #203 class) | This is exactly what `tests/critical/auth_two_tier_test.go` catches |
| Dashboard `/tokens` 404 | UI route not built | `ui/dist` fresh build needed |
| Daemon exits silently on first launch | `ENGRAM_URL` set, `ENGRAM_TOKEN` empty (FR-4 startup gate) | check exit code, stderr |

## Verdict template

After running each scenario, the agent fills in the per-scenario row and
overall verdict per `references/customer-mode-protocol.md`. Verdict report
is written to `.agent/reports/emulation-playbook-run-<date>.md`.

## Maintenance

- Add a new scenario whenever a user-visible feature ships (`/emulation-playbook --add <slug>`)
- Re-run the playbook before every release — see `/release --push` Step 5d
- Promote stable scenarios into `tests/critical/` over time
- Keep the playbook under 500 LOC; if it grows, split by surface
