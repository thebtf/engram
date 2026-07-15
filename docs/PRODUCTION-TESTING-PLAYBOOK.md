# Production Testing Playbook — engram

**Purpose:** Customer-mode walkthrough of the engram product. Run before every
release. The agent (or human reviewer) walks through the scenarios pretending
to be a user with no internal knowledge — the public docs and this playbook
are the only allowed inputs.

**Bootstrap version:** v6.29.0 — refreshed for the split runtime stack
(`server` + `operator-web`). Future releases extend the scenario list.

## Scope

The playbook covers the following surfaces:

| # | Surface | Binary / Component |
|---|---------|--------------------|
| 1 | Server | `cmd/engram-server` — HTTP API + gRPC authority on :37777 |
| 2 | CLI client | `cmd/engram` — stdio MCP proxy invoked by Claude Code |
| 3 | Operator Web | `apps/operator-web` — Nuxt control plane on :3000 proxying `/api/*` to the server |
| 4 | Claude Code plugin | `plugin/engram` — installed via `/plugin marketplace add thebtf/engram-marketplace` |

Out of scope for this bootstrap: `cmd/engram-import`, full Unraid deployment
flow (covered separately by `docs/DEPLOYMENT.md`), backup/restore, and full
legacy-dashboard removal. If a scenario still requires the legacy dashboard,
record that as a cutover gap rather than silently treating it as canonical.

## Prerequisites

- Go 1.25+ (`go version`)
- Docker (for postgres dependency in scenario S2)
- Node 22+ (for building `apps/operator-web` in scenario S1)
- A running engram runtime stack for scenarios S2/S3/S4:
  - server at `http://<host>:37777`
  - operator web at `http://<host>:3000`
- Claude Code CLI installed (for scenario S4)

## Canonical scenarios

### S1 — Build the current runtime artifacts from source

**As a user, I clone the repo and build the current server, client, and operator UI artifacts.**

Steps:
1. From repo root run `go build -o /tmp/engram-server ./cmd/engram-server`
2. From repo root run `go build -o /tmp/engram ./cmd/engram`
3. From `apps/operator-web/` run `npm ci` and `npm run build`
4. Stronger runtime proof: run `pwsh -NoProfile -File scripts/smoke-operator-web.ps1`
5. Run the built binaries with `--help` (or no args) and observe usage output

Expected:
- Both `go build` invocations exit 0 with no compiler errors
- `apps/operator-web` production build exits 0
- If the smoke script is run, it exits 0 and proves operator-web login +
  proxied issue mutations through the runtime stack
- Both binaries print usage / startup banner without crashing

Failure signals:
- `undefined: <symbol>` errors — auth refactor incomplete
- `package not found` — module path drift
- `apps/operator-web` build fails — operator-facing UI is not release-ready
- `scripts/smoke-operator-web.ps1` fails — the split runtime stack is not
  release-ready even if isolated builds pass
- Binary panics on `--help`

### S2 — Server starts cleanly with required env vars

**As an operator, I start the server with the current admin-token env var.**

Steps:
1. Set `ENGRAM_AUTH_ADMIN_TOKEN=test-operator-key`
2. Set `DATABASE_DSN=...` (the canonical name read by `internal/config/config.go`;
   the production-candidate path requires PostgreSQL)
3. Run `engram-server.exe`
4. Observe startup logs

Expected:
- Server listens on `:37777` (or the configured worker port)
- No `panic`, no `FATAL` lines
- HTTP `GET /health` returns 200

Failure signals:
- Server exits during startup citing missing env var -> FR-4 violated
- Server exits because `DATABASE_DSN` is omitted -> the release smoke is not
  configured for the current PostgreSQL-backed runtime path
- Logs or docs still steer workstations toward the server-host-only
  `ENGRAM_AUTH_ADMIN_TOKEN` instead of `ENGRAM_TOKEN`
- gRPC bind fails

### S3 — Operator web loads, authenticates, and reaches the MVP surfaces

**As an operator, I open the new control plane, complete first-run setup if needed, log in, and verify the real MVP surfaces.**

Steps:
1. With the runtime stack running, open `http://localhost:3000/login`
2. If this is a fresh database, call the first-run setup flow and create the
   first admin account through the operator web app
3. Log in with that admin account; setup does not create an authenticated
   browser session by itself
4. Navigate through the MVP operator surfaces:
   - `/projects`
   - `/rules`
   - `/issues`
   - `/vault`
   - `/system`
   - `/settings`
   - `/memories`
5. If the release also claims full UI cutover for workstation onboarding,
   verify where keycard issuance lives. If that still requires the legacy
   dashboard, record it as a cutover gap instead of silently accepting it.

Expected:
- Login/setup shell loads at `:3000`
- Authenticated operator routes render without severe console errors
- Proxied API calls succeed from the operator web app
- MVP mutation surfaces remain honest about what is backed today
- Unauthenticated `GET /api/auth/me` 401 responses before login are expected

Failure signals:
- 404 on `/login` or a named MVP route
- Post-login operator routes fall back to the legacy dashboard to complete a
  claimed MVP flow
- Browser must use `:37777` dashboard pages for ordinary operator work while
  docs claim `:3000` is canonical
- Proxied rules/issues/vault flows fail after successful login

### S4 — Plugin installs in Claude Code and exposes MCP tools

**As a Claude Code user, I install the engram plugin and use it.**

Real Claude Code plugin installation mutates the operator's consumer home. In
automated release emulation, prefer an isolated disposable home with the plugin
wrapper pointed at the release-candidate `cmd/engram` binary. Run the real
consumer-home path only when the operator explicitly authorizes that mutation.

Steps:
1. Start the runtime stack with PostgreSQL and create a workstation keycard
   through the currently supported operator flow. If keycard issuance still
   depends on the legacy dashboard, record that explicitly as a cutover gap.
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
  e.g., `mcp__engram__store`, `mcp__engram__issues`,
  `mcp__engram__vault`, etc.
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
| Operator web `/login` or MVP pages 404 | `operator-web` image/build or route wiring broken | `apps/operator-web`, `docker-compose.yml`, `deploy/docker-compose.runtime.yml` |
| Plugin smoke still requires the legacy dashboard for keycard issuance | UI cutover incomplete | `docs/DEPLOYMENT.md`, operator-web route inventory, legacy dashboard compatibility path |
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
