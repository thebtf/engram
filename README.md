# Engram

Engram is persistent shared memory infrastructure for coding-agent workstations.
It keeps memories, behavioral rules, issues, documents, and encrypted credentials
in PostgreSQL while an agent host talks to a local MCP process over stdio.

> **Canonical documentation:** this English README is the current public front
> door. [Русский README](README.ru.md) and [中文 README](README.zh.md) remain in
> the repository, but both are **stale / non-canonical**: they have not yet been
> synchronized with this v5+ architecture. Do not treat them as equivalent setup
> instructions until a later localization batch restores parity.

Engram fixes this by keeping only the memory primitives that proved reliable in production: explicit issues, documents, memories, behavioral rules, credentials, and API tokens. One server, multiple workstations, zero context loss.

In v5.0.0, session-start inject was simplified to a static composite payload: open issues, always-inject behavioral rules, and recent memories. The old dynamic relevance, graph, reranking, and extraction stack left the main product path.

Since then, the v6 line rebuilt governance on top of that stable core: per-workstation keycards, proposal-only rule arbitration, bounded session-start rule routing, and rule-governance telemetry / rollback controls. Engram still keeps hot paths deterministic — no LLM on session-start — while making durable guidance inspectable and reversible.
<!-- redoc:end:intro -->

---

<!-- redoc:start:whats-new -->
## What's New

| Version | Highlight |
|---------|-----------|
| **v6.38.0** | **V7 Meta-memory Discovery (ENG-V7-S2)** — content-free `know_about` MCP tool, S2 `CandidateProposer`, and session-start `meta_summary` behind v7 flags. |
| **v6.37.0** | **V7 State Subsystem (ENG-V7-S1)** — v7 `StateWriter` adapter and bounded native state resume hardening. |
| **v6.32.0** | **Usefulness / Noise Review Loop (CR-008, MPL-3)** — packet-centric bounded review queue with explicit empty/gated/error/sparse states, separate preview/apply, atomic snapshot+audit-backed suppress/preserve, honest metrics. |
| **v6.31.0** | **Native State Plane + Principal Explorer (CR-006 + CR-007, MPL-1/2)** — Engram-native session/goal/task/project state plane with deterministic resume packet; principal/domain/project memory explorer + principal-scoped briefs; CR-005 contract hardening. |
| **v6.30.0** | **Agent Knowledge & Experience Layer foundations (ENG-MPL-1)** — native state plane, principal briefs, packet-centric review loop, first-class experience retrieval with applicability gates, forgetting taxonomy, and selective temporal truth contracts. |
| **v6.29.0** | **Rule Governance Telemetry (RG-3)** — lifecycle health, exception queues, transition controls, rollback-aware snapshots, and usefulness telemetry landed on top of the rule-governance milestones. |
| **v6.0.0** | **BREAKING** — Two-tier token authentication: per-workstation keycards via dashboard `/tokens`, daemon fail-fast on missing token, issuance hardened to browser session. |
| **v5.0.0** | Cleaned Baseline — static-only storage, observations split, session-start gRPC + cache fallback |
| **v4.4.0** | Loom tenant — background task execution and daemon-side project event bridge |
| **v4.0.0** | Daemon architecture — muxcore engine, gRPC transport, local persistent daemon, auto-binary plugin |

See [Releases](https://github.com/thebtf/engram/releases) for full changelog.

### Two-Tier Token Model (v6)

Engram v6 separates two credential tiers, each pinned to a single host class:

| Tier | Name | Lives in | Purpose | Issuance |
|---|---|---|---|---|
| **1 — Operator key** | `ENGRAM_AUTH_ADMIN_TOKEN` | Server-host environment ONLY (Docker, compose) | Admin-grade access for migrations, server-internal RPCs, dashboard bootstrap | Operator-managed (Docker env) |
| **2 — Worker keycard** | `ENGRAM_TOKEN` | Workstation `~/.claude/settings.json` env | Daemon ↔ server gRPC, regular MCP tool calls | Generated via dashboard `/tokens` page (admin-only browser session) |

Operator keys NEVER appear on workstations. Worker keycards NEVER appear on the server host. There is no `OR`-fallback between the two names — the daemon ignores the admin name; the server ignores the workstation name. Workstation startup with `ENGRAM_URL` set but `ENGRAM_TOKEN` empty exits non-zero with an actionable error. Keycard issuance requires a browser admin session — bearer callers get 403 on `/api/auth/tokens`.

Migration from v5.x: open `<server-url>/tokens`, log in as admin, generate a keycard, paste it via `/engram:setup`. See [CHANGELOG.md](CHANGELOG.md) for full migration steps.
<!-- redoc:end:whats-new -->

---

<!-- redoc:start:architecture -->
## Architecture

```mermaid
flowchart LR
  Host[Agent host] -->|stdio MCP| Daemon[local engram daemon]
  Daemon -->|gRPC| Server[engram-server :37777]
  Hooks[Lifecycle hooks] -->|REST| Server
  Browser[Operator browser] -->|HTTP| Console[Nuxt operator console]
  Console -->|REST API| Server
  Server --> DB[(PostgreSQL 17 + pgvector)]
```

`engram-server` multiplexes REST and gRPC on its worker listener with cmux.
The agent-facing MCP protocol stays local: the `engram` executable is a stdio
daemon and forwards tool calls over gRPC. Do **not** append `/mcp`, `/sse`, or
another MCP transport suffix to `ENGRAM_URL`; the server does not offer those as
current setup paths.

The server image embeds a generated Nuxt console by default. The provided Compose
stack also starts a dedicated Nuxt console on port 3000. Setting
`ENGRAM_OPERATOR_CONSOLE_URL` makes the server proxy browser traffic to an
explicit external console; see [the architecture overview](docs/arch/OVERVIEW.md)
for the boundary details.

## Quick start: server

Prerequisites: Docker with Compose, plus a secure password and an operator token
for a real deployment. Docker Compose uses a Compose file to define and start a
multi-container application; see the official [Docker Compose documentation](https://docs.docker.com/compose/).

```bash
git clone https://github.com/thebtf/engram.git
cd engram
cp .env.example .env
commit=$(git rev-parse HEAD)
cat >> .env <<EOF
ENGRAM_SERVER_IMAGE=engram-local-server
ENGRAM_OPERATOR_IMAGE=engram-local-operator-console
ENGRAM_POSTGRES_IMAGE=engram-local-postgres
ENGRAM_BUILD_VERSION=sha-$commit
EOF
# Also set POSTGRES_PASSWORD and ENGRAM_AUTH_ADMIN_TOKEN in .env before production use.
docker compose up -d --build
docker compose ps
```

The pull-only production flow uses digest identities from a release manifest
instead; see [the deployment guide](docs/DEPLOYMENT.md).

The supplied stack starts PostgreSQL 17 with pgvector, `engram-server` on
`WORKER_PORT` (default `37777`), and the standalone Nuxt console on
`OPERATOR_CONSOLE_PORT` (default `3000`). The server image also serves its
embedded console at the server origin when no external console proxy is set.

Verify the process and database readiness separately:

```bash
curl -fsS http://localhost:37777/health
curl -fsS http://localhost:37777/api/ready
docker compose logs --tail=100 server
```

`/health` proves that the HTTP process is answering. `/api/ready` is the
readiness check for the initialized service. If you changed `WORKER_PORT`, use
that port in these commands.

## Configure a workstation

Keep credentials separated:

| Location | Variable | Purpose |
| --- | --- | --- |
| Server host only | `ENGRAM_AUTH_ADMIN_TOKEN` | Operator credential for administrative API access. |
| Workstation only | `ENGRAM_URL` | Bare server origin, for example `http://server.example:37777`. |
| Workstation only | `ENGRAM_TOKEN` | Revocable per-workstation keycard used by the local daemon. |

Never copy `ENGRAM_AUTH_ADMIN_TOKEN` into a plugin, agent-host configuration, or
workstation environment. The daemon fails fast when a server URL is configured
but `ENGRAM_TOKEN` is absent.

Install or build the local daemon, then register it with your agent host as an
MCP process that runs `engram` on stdio and receives `ENGRAM_URL` and
`ENGRAM_TOKEN` in its environment. A host-agnostic process definition is:

```text
command: engram
transport: stdio
environment:
  ENGRAM_URL: http://server.example:37777
  ENGRAM_TOKEN: <per-workstation-keycard>
```

For a source checkout, build the binaries with:

```bash
make build
```

The exact agent-host configuration file is host-specific; the invariant is the
command, stdio transport, and the two workstation variables above. Confirm the
host lists Engram's tools before relying on it for session continuity.

### Keycard issuance limitation

The server implements authenticated admin token issuance at `/api/auth/tokens`,
but the currently promoted Nuxt `/access` page is an access-administration
surface, not an accepted browser keycard-issuance workflow. The local daemon and
some older text still refer to `/tokens`; that browser route is not present in
the promoted console. Obtain a per-workstation keycard only through your
operator's verified administrative procedure; this README deliberately does not
invent an Access/Settings click path.

## Use Engram

After the host discovers the local daemon's tools, use the memory tools from the
host rather than an HTTP MCP endpoint. A minimal persistence check is:

1. Store a unique, non-secret marker through the discovered Engram memory tool.
2. Start a fresh agent session or restart the local `engram` daemon.
3. Recall the marker through the same host.
4. Inspect it in the operator console's Memory route if the console is enabled.

Successful recall after a new connection proves the stdio, gRPC, persistence,
and read paths. A successful process check alone does not.

## Operator console

The console source is a Nuxt application. Nuxt uses file-based routing: current
page files map to the route inventory in
[`docs/arch/current-surface.json`](docs/arch/current-surface.json). The ledger
distinguishes, per route, whether a page file exists, whether each deployment
form (standalone Nuxt, the server's embedded bundle, a proxied external
console) can actually direct-load it, the backing capability's flag/readiness
state, and whether the end-to-end operator workflow is accepted. **A page file
existing does not mean the workflow behind it is accepted** — read the ledger's
`journey_status` field, not just route presence, before relying on a page.

Known corrections from that ledger:

- **`/health` (embedded server form only):** the Go server registers its
  machine health handler at this same path before the SPA fallback, so a
  direct browser load of the embedded console's `/health` returns machine
  health JSON, not the `pages/health.vue` dashboard. This is a P0 defect, not
  a working route; use `/api/selfcheck` for the same data today. The
  standalone Nuxt console (port 3000) has no such route collision and is
  expected to serve `pages/health.vue`, but that direct load was not replayed
  in this batch (`apps/operator-console/node_modules` is not installed here) —
  treat it as the likely remedy, not a confirmed one, until replayed.
- **`/access`:** this is a live access-administration page (providers,
  invitations, users, roles, sessions, audit log). It is *not* a keycard
  issuance wizard. See "Keycard issuance limitation" above for what remains
  unaccepted.
- **`/settings`:** on mount it opens the general-settings modal and, if you
  land on `/settings` directly, immediately redirects to `/`. There is no
  separate settings screen; refreshing or deep-linking to `/settings` reopens
  the modal over the overview rather than showing a stable settings page.
- **Graph, candidate queue, code intelligence:** these routes/tools exist in
  source and load at direct request, but their data operations are rejected
  until `ENGRAM_GRAPH_ENABLED`, `ENGRAM_VNEXT_F_ENABLED`, or
  `ENGRAM_CODE_INTEL_ENABLED` are set. Flag presence is not the same as an
  accepted end-to-end workflow; the ledger records both separately.

Use the console for operational overview, search, memory, rules, issues,
documents, credentials, and access administration; treat health, graph, queue,
and settings per the corrections above.

## Configuration and deployment notes

Copy `.env.example` to `.env`; it is the deployment template for the supplied
Compose stack. Important defaults and boundaries:

- PostgreSQL uses the bundled `pgvector/pgvector:pg17` service unless
  `DATABASE_DSN` overrides it.
- `ENGRAM_EMBEDDING_URL` is optional. With no embedding endpoint, recall remains
  available through full-text search rather than a vector tier.
- `ENGRAM_RERANK_URL` is an optional source-wired recall path, not a default or
  advertised core capability. See the source classification in the ledger.
- `ENGRAM_GRAPH_ENABLED`, `ENGRAM_VNEXT_F_ENABLED`, and
  `ENGRAM_CODE_INTEL_ENABLED` enable distinct non-default surfaces.
- `ENGRAM_AUTH_DISABLED=true` is a local smoke/debug choice, not a production
  security setting.

For a separate console host, set `ENGRAM_OPERATOR_CONSOLE_URL` on the server to
an absolute URL. The server validates that this value includes a scheme and host
before proxying browser routes.

---

<!-- redoc:start:installation -->
## Installation

### Plugin install (recommended)

The marketplace plugin registers the MCP server, skills, and slash commands in
Claude Code and Oh My Pi. Claude Code also activates the bundled lifecycle hooks;
OMP 17.x does not execute Claude `hooks.json`, so automatic capture and context
injection remain Claude-only.

**Prerequisite:** Node.js 18+ must be available on `PATH`. It runs the bundled
lifecycle hooks and lets the standalone installers validate the release
bootstrap policy before installation. Windows-compatible Bash installs (Git
Bash, MSYS2, or Cygwin) also require `unzip` on `PATH`.

Install the marketplace plugin first, then run `/engram:setup` to create the
universal `~/.engram/config.json` configuration and restart the host.

Claude Code:

```
/plugin marketplace add thebtf/engram-marketplace
/plugin install engram
```

Oh My Pi:

```bash
omp plugin marketplace add thebtf/engram-marketplace
omp plugin install engram@engram
```

For an OMP update, refreshing the marketplace updates only its catalog. Upgrade
the installed plugin, then run `/reload-plugins` (or restart OMP or start a new
session) before expecting updated MCP discovery:

```bash
omp plugin upgrade engram@engram
```

Restart the agent host after configuration.


### Docker Compose

```bash
git clone https://github.com/thebtf/engram.git && cd engram
cp .env.example .env   # edit DATABASE_DSN, tokens, embedding config
docker compose up -d
```

**Existing PostgreSQL?** Run only the server container:

```bash
DATABASE_DSN="postgres://user:pass@your-pg:5432/engram?sslmode=disable" \
  docker compose up -d server
```

### Binary Installation (v4+)

Download the engram daemon binary from [GitHub Releases](https://github.com/thebtf/engram/releases):

```bash
# Linux (amd64)
curl -L https://github.com/thebtf/engram/releases/latest/download/engram-linux-amd64 -o engram
chmod +x engram && sudo mv engram /usr/local/bin/

# macOS (Apple Silicon)
curl -L https://github.com/thebtf/engram/releases/latest/download/engram-darwin-arm64 -o engram
chmod +x engram && sudo mv engram /usr/local/bin/

# Windows (amd64) — download engram-windows-amd64.exe, add to PATH
```

Set environment variables:
```bash
export ENGRAM_URL=http://your-server:37777
export ENGRAM_TOKEN=engram_your_workstation_keycard
```

Verify: `echo '{"jsonrpc":"2.0","id":1,"method":"ping"}' | engram`

The daemon starts automatically on first use. Multiple Claude Code sessions share one daemon.

### Manual MCP Configuration

If not using the plugin, configure MCP directly in `~/.claude/settings.json`:

#### Stdio (recommended)

```json
{
  "mcpServers": {
    "engram": {
      "command": "engram",
      "env": {
        "ENGRAM_URL": "http://your-server:37777",
        "ENGRAM_TOKEN": "${ENGRAM_TOKEN}"
      }
    }
  }
}
```

**CLI shortcut:**

```bash
claude mcp add-json engram '{"type":"stdio","command":"engram","env":{"ENGRAM_URL":"http://your-server:37777","ENGRAM_TOKEN":"${ENGRAM_TOKEN}"}}' -s user
```

`ENGRAM_URL` must be the bare server origin (`http://host:37777`); do not append `/mcp`, `/sse`, or another MCP transport suffix — the server does not offer those, and the client uses the origin directly for daemon gRPC and hook REST calls. `ENGRAM_TOKEN` must always be the workstation keycard, never the operator key.

### Build from Source

Requires Go 1.25+ and Node.js (for dashboard).

```bash
git clone https://github.com/thebtf/engram.git && cd engram
make build    # builds dashboard + daemon + release assets
make install  # installs plugin + starts daemon
```
<!-- redoc:end:installation -->

---

<!-- redoc:start:upgrading -->
## Upgrading to v6.x

The important v6 upgrade contract is the workstation token split plus the rule-governance milestones layered onto the static core.

What changed across v6:
- workstation auth moved from shared admin tokens to per-workstation keycards (`ENGRAM_TOKEN`)
- session-start stayed deterministic, but rule delivery now flows through candidate -> arbiter -> router -> telemetry milestones
- rule-governance snapshots, rollback conflict handling, and usefulness telemetry are now part of the backend surface
- client and server still negotiate major-version compatibility on the session-start path

Upgrade steps:
1. upgrade the plugin and daemon to the target `v6.x` release
2. open `<server-url>/tokens`, issue a workstation keycard, and configure `ENGRAM_TOKEN`
3. restart Claude Code and the daemon
4. verify plugin update detection, session-start cache fallback, and the current server version

**Docker image:** Pull the latest from `ghcr.io/thebtf/engram:latest`. Database migrations run automatically on startup.
<!-- redoc:end:upgrading -->

---

<!-- redoc:start:configuration -->
## Configuration

### Server

| Variable | Default | Description |
|----------|---------|-------------|
| `DATABASE_DSN` | — | PostgreSQL connection string **(required)** |
| `DATABASE_MAX_CONNS` | `10` | Maximum database connections |
| `ENGRAM_WORKER_PORT` | `37777` | Server port |
| `ENGRAM_AUTH_ADMIN_TOKEN` | — | Operator/admin token. Server-host only. |
| `ENGRAM_VAULT_KEY` | — | Canonical vault key for credential encryption |
| `ENGRAM_ENCRYPTION_KEY` | — | Legacy fallback vault key env var |
| `ENGRAM_DATA_DIR` | auto | Daemon data directory (also used for session-start cache path) |

### Client (hooks)

| Variable | Default | Description |
|----------|---------|-------------|
| `ENGRAM_URL` | — | Full MCP/server URL for plugin and hooks |
| `ENGRAM_TOKEN` | — | Workstation keycard for plugin, daemon, and hooks |
| `ENGRAM_SERVER_URL` | — | Optional alias for `ENGRAM_URL` in some launchers |
| `ENGRAM_DATA_DIR` | auto | Cache and daemon state directory |
| `ENGRAM_WORKSTATION_ID` | auto | Override workstation ID (8-char hex) |
<!-- redoc:end:configuration -->

---

<!-- redoc:start:mcp-tools -->
## MCP Tools

Engram exposes a static-first MCP surface for the surviving entity model, extended across the v6 line as the Memory Product Layer milestones landed.

Static core (v5 baseline):
- issues / issue comments
- memories / behavioral rules
- documents
- credentials / vault
- loom background tasks

Memory Product Layer additions (v6.30–v6.38):
- native state plane — session/goal/task/project resume packet (CR-006)
- principal explorer + briefs — principal/domain-scoped memory inspection and bounded briefs (CR-007)
- review loop — packet-centric candidate/suppress/preserve governance over the candidate/snapshot/audit seams (CR-008)
- v7 state subsystem — feature-flagged `StateWriter` adapter and stricter resume-packet validation (ENG-V7-S1)
- v7 meta-memory discovery — feature-flagged `know_about`, S2 `CandidateProposer`, and session-start `meta_summary` (ENG-V7-S2)

The old dynamic search / graph / learning-oriented tool surface was stripped in the v5 demolition phase; the v6 Memory Product Layer rebuilds durable agent knowledge deliberately rather than resurrecting that stack.

### `store` — Save and Organize

| Action | Description |
|--------|-------------|
| `create` | Store a new observation (default) |
| `edit` | Modify observation fields |
| `import` | Bulk import observations |

### `feedback` — Suppression and Outcomes

| Action | Description |
|--------|-------------|
| `suppress` | Suppress low-quality memories |
| `outcome` | Record a session outcome |

### `vault` — Encrypted Credentials

| Action | Description |
|--------|-------------|
| `store` | Store an encrypted credential |
| `get` | Retrieve a credential |
| `list` | List stored credentials |
| `delete` | Delete a credential |
| `status` | Vault status and health |

### `docs` — Versioned Documents and Collections

| Action | Description |
|--------|-------------|
| `create` | Create a versioned document |
| `read` | Read document content |
| `list` | List versioned documents |
| `history` | Read version history |
| `comment` | Add a document comment |
| `collections` | List configured collections |
| `documents` | List documents in a collection |
| `get_doc` | Read a collection document |
| `remove` | Soft-delete a collection document |
| `ingest` | Upsert collection document metadata and content |

### `admin` — Administrative Telemetry

`stats` returns memory-system telemetry. `purge_project` is additionally available when the vNext gate is enabled and requires admin authorization plus project-name confirmation.

### `check_system_health` — System Health

Reports status of all subsystems: database, embeddings, reranker, LLM, vault, graph, consolidation.

### V7 conditional tools

When `ENGRAM_V7_PLUG_ENABLED=true` and the slice flag is enabled:

| Tool / surface | Flag | Description |
|----------------|------|-------------|
| `get_state` / `set_state` v7 adapter | `ENGRAM_V7_S1_STATE=true` | Routes native state writes through the v7 S1 subsystem while preserving the stable state-plane tools. |
| `know_about` | `ENGRAM_V7_S2_METAMEM=true` | Returns a content-free discovery packet for a topic: `topic`, `project`, `count`, `total_candidates`, `top_tags`, `date_range`, and `memories`. Empty matches return an empty packet, not memory body text or a tool error. |
| session-start `meta_summary` | `ENGRAM_V7_S2_METAMEM=true` | Adds aggregate project/count/tag/timestamp landscape data so agents can decide whether detail fetches are needed. |
<!-- redoc:end:mcp-tools -->

---

<!-- redoc:start:usage -->
## Usage

```python
# Verify connection
check_system_health()

# Search memories
recall(action="search", project="engram", query="authentication architecture")

# Store an observation
store(action="create", project="engram", content="Switched from Redis to in-memory cache for dev environments", title="Cache strategy change", tags=["architecture", "caching"])

# Suppress a low-quality memory
feedback(action="suppress", id=123)

# Store a global credential
vault(action="store", name="OPENAI_KEY", value="sk-...", scope="global")

# Retrieve a global credential
vault(action="get", name="OPENAI_KEY")
```
<!-- redoc:end:usage -->

---

<!-- redoc:start:troubleshooting -->
## Troubleshooting

| Symptom | Check | Smallest safe action |
| --- | --- | --- |
| Server does not answer | `docker compose ps` and `docker compose logs --tail=100 server` | Fix the failing service or port mapping; do not change client URLs until `/health` answers. |
| Process is live but not ready | `curl -i http://host:37777/api/ready` and PostgreSQL logs | Wait for initialization or correct the database configuration. |
| Agent host cannot discover tools | Verify the host launches `engram` as a stdio command | Correct the local host configuration; `/mcp` and `/sse` are not substitutes. |
| Daemon rejects startup | Check that `ENGRAM_URL` is a bare origin and `ENGRAM_TOKEN` is set | Supply a valid per-workstation keycard, never the operator token. |
| Browser console is unavailable | Check the server origin and `docker compose logs --tail=100 operator-console` | Use the embedded server console or repair the standalone console service/proxy configuration. |
| Recall has no vector results | Check `ENGRAM_EMBEDDING_URL` and server logs | Configure a compatible embedding endpoint only if vector recall is required; FTS-only recall is a supported fallback. |
<!-- redoc:end:troubleshooting -->

## Development

The project requires Go 1.25+ for its current build. Use the focused Go checks
for the server and MCP layers:

```bash
go test ./internal/worker ./internal/mcp
```

`make build` builds the server and local stdio daemon. The promoted operator
console has its own source in `apps/operator-console/`; its routes are validated
against the ledger rather than duplicated in prose.

## Security and support

- Treat `.env`, database backups, and vault keys as secrets; do not commit them.
- Use a unique production `POSTGRES_PASSWORD` and a non-empty
  `ENGRAM_AUTH_ADMIN_TOKEN`.
- Restrict server and database network exposure to the intended operator and
  workstations.
- Report security vulnerabilities privately through the repository's security
  contact/process rather than opening a public issue with credentials or exploit
  details.

## Documentation map

- [Architecture index](docs/arch/INDEX.md)
- [System overview](docs/arch/OVERVIEW.md)
- [Runtime components](docs/arch/COMPONENTS.md)
- [Architecture rationale](docs/arch/architecture.md)
- [Current architecture and route ledger](docs/arch/current-surface.json)
- [License](LICENSE)

## License

[MIT](LICENSE)
