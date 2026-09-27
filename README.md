# Engram

Engram is persistent shared memory infrastructure for coding-agent workstations.
It keeps memories, behavioral rules, issues, documents, and encrypted credentials
in PostgreSQL while an agent host talks to a local MCP process over stdio.

> **Canonical documentation:** this English README describes the current source and the chosen no-auth HTTP LAN setup. [Русский README](README.ru.md) covers the same Workspace path; [中文 README](README.zh.md) remains stale and is not equivalent setup guidance.

Engram fixes this by keeping only the memory primitives that proved reliable in production: explicit issues, documents, memories, behavioral rules, credentials, and API tokens. One server, multiple workstations, zero context loss.

In v5.0.0, session-start inject was simplified to a static composite payload: open issues, always-inject behavioral rules, and recent memories. The old dynamic relevance, graph, reranking, and extraction stack left the main product path.

Since then, the v6 line rebuilt governance on top of that stable core: per-workstation keycards, proposal-only rule arbitration, bounded session-start rule routing, and rule-governance telemetry / rollback controls. Engram still keeps hot paths deterministic — no LLM on session-start — while making durable guidance inspectable and reversible.
<!-- redoc:end:intro -->

---

<!-- redoc:start:whats-new -->
## What's New

| Version | Highlight |
|---------|-----------|
| **v6.50.1 candidate** | **Operator Workspace (Feature 011 D-A).** Current candidate source supports Home → Workspace on the configured single-user no-auth HTTP LAN origin. Follow the [operator guide](docs/operating-engram.md); source checks are not installed acceptance. |
| **v6.38.0** | **V7 Meta-memory Discovery (ENG-V7-S2)** — content-free `know_about` MCP tool, S2 `CandidateProposer`, and session-start `meta_summary` behind v7 flags. |
| **v6.37.0** | **V7 State Subsystem (ENG-V7-S1)** — v7 `StateWriter` adapter and bounded native state resume hardening. |
| **v6.32.0** | **Usefulness / Noise Review Loop (CR-008, MPL-3)** — packet-centric bounded review queue with explicit empty/gated/error/sparse states, separate preview/apply, atomic snapshot+audit-backed suppress/preserve, honest metrics. |
| **v6.31.0** | **Native State Plane + Principal Explorer (CR-006 + CR-007, MPL-1/2)** — Engram-native session/goal/task/project state plane with deterministic resume packet; principal/domain/project memory explorer + principal-scoped briefs; CR-005 contract hardening. |
| **v6.30.0** | **Agent Knowledge & Experience Layer foundations (ENG-MPL-1)** — native state plane, principal briefs, packet-centric review loop, first-class experience retrieval with applicability gates, forgetting taxonomy, and selective temporal truth contracts. |
| **v6.29.0** | **Rule Governance Telemetry (RG-3)** — lifecycle health, exception queues, transition controls, rollback-aware snapshots, and usefulness telemetry landed on top of the rule-governance milestones. |
| **v6.0.0** | **BREAKING** — Two-tier token authentication: per-workstation keycards via dashboard `/access`, daemon fail-fast on missing token, issuance hardened to browser session. |
| **v5.0.0** | Cleaned Baseline — static-only storage, observations split, session-start gRPC + cache fallback |
| **v4.4.0** | Loom tenant — background task execution and daemon-side project event bridge |
| **v4.0.0** | Daemon architecture — muxcore engine, gRPC transport, local persistent daemon, auto-binary plugin |

See [Releases](https://github.com/thebtf/engram/releases) for full changelog.

### Authentication mode

The current single-user path uses `ENGRAM_AUTH_DISABLED=true`. Set `ENGRAM_URL` to the configured server origin and use the installed plugin without workstation keycard issuance, browser sign-in, HTTPS, or a reverse proxy. Keep this HTTP LAN deployment on a trusted network. Auth-enabled deployments retain the v6 operator-token and workstation-keycard separation, browser identities, and explicit code read grants; that path is deferred from this no-auth Workspace guide, not removed. Never place an admin credential on a workstation.
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

Prerequisites: Docker with Compose and a database password. For this trusted single-user HTTP LAN journey, configure `ENGRAM_AUTH_DISABLED=true` in `.env`. Production image selection and secrets have separate requirements in the [deployment guide](docs/DEPLOYMENT.md).

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
# Set POSTGRES_PASSWORD and ENGRAM_AUTH_DISABLED=true in .env for the trusted no-auth LAN path.
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

For a no-auth installation, set `ENGRAM_AUTH_DISABLED=true` on the server. Configure the **new installed plugin** with `ENGRAM_URL` as the bare configured server origin; no `ENGRAM_TOKEN` is required in this mode. The agent host launches the local `engram` MCP process over stdio. Do not add `/mcp` or `/sse` to the URL, and do not paste an admin credential into client configuration. An auth-enabled installation instead requires its own workstation keycard and browser authorization. A host-agnostic process definition is:

```text
command: engram
transport: stdio
environment:
  ENGRAM_URL: http://your-server:37777
```

Direct launches create a stable, non-secret `client-instance-id` in
`ENGRAM_DATA_DIR` (default `~/.engram`) on first use. Keep this installation
state across restarts; the plugin launcher uses the same atomic identity format.
An explicit `ENGRAM_CLIENT_INSTANCE_ID` overrides the generated ID.

For a source checkout, build the binaries with:

```bash
make build
```

The exact agent-host configuration file is host-specific. Confirm the installed plugin's fresh MCP `tools/list` before relying on its code tools; a successful daemon process check alone does not prove registration.

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

### Workspace

In a new Git repository, run `engram project init --name "Example Workspace"` offline from its Git root. Explicitly `git add -- .engram-project` and commit the V3 anchor; initialization does not stage or commit it. Do not replace a legacy marker or treat a new anchor as migration of old project memories and issues.

With the current `ENGRAM_AUTH_DISABLED=true` installation, open the configured HTTP LAN console and choose **Home → Workspace → Repository → Working copy → Indexed snapshot → Pin selected View**. No login, grants, HTTPS, or proxy are needed for this single-user path. Register A and B from the ordinary installed plugin's `codebase_context` tool, then index and check daemon-side `codebase_status` until each published View and its code embeddings are ready. If labels repeat, distinguish entries by their indexed working-copy and snapshot counts; never guess an internal ID. Set the embedding provider's `ENGRAM_EMBEDDING_URL` to its base URL (a trailing `/v1` is accepted) and use an `ENGRAM_EMBEDDING_MODEL` returning 1536-dimensional vectors.

Search with a conceptual query and confirm `vector` or `hybrid` retrieval rather than lexical fallback. Follow an automatically derived direct or reverse graph relation, inspect its evidence precision, and read the source in the same selected View. Workspace reports freshness, coverage, code-embedding job state and error reason; a newer View requires an explicit switch, including after a tab reload. Keep A and B pinned separately across tabs. An indexed Go example does not prove JS, TS, or TSX graph support; check the installed parser and a meaningful example for each language. Keep the older memory service and client separate from the new code client: a new V3 project or UCI Source does not migrate historical memory. Auth-enabled browser sign-in and Source/Checkout grants remain separate deferred behavior. The [operator guide](docs/operating-engram.md) gives the full F1–F7 installed verification path.

Manual knowledge-graph writers and plaintext Book intake are retired. Historical graph and Book readers, records, documents, and provenance remain available. Historical graph records are not UCI code-graph facts.

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
- **`/access`:** this is the live access-administration page for providers,
  invitations, users, roles, sessions, audit log, and workstation keycards.
  From an authenticated admin browser session, use its **Workstation keycards**
  panel to issue, copy once, and revoke `ENGRAM_TOKEN` credentials. `/tokens`
  is not a promoted console route.
- **`/settings`:** on mount it opens the general-settings modal and, if you
  land on `/settings` directly, immediately redirects to `/`. There is no
  separate settings screen; refreshing or deep-linking to `/settings` reopens
  the modal over the overview rather than showing a stable settings page.
- **Graph, candidate queue, legacy MCP tools:** these routes and tools have their own feature settings. Current source enables code intelligence unless `ENGRAM_CODE_INTEL_ENABLED` is exactly `false`. They are separate from Workspace, which uses the configured auth realm and selected View. Route presence alone does not prove an installed journey.

Use Workspace for browser investigation of authorized code. Use the console for operational overview, search, memory, rules, issues, documents, credentials, and access administration. Treat health, candidate queue, and settings per the corrections above.

## Configuration and deployment notes

Copy `.env.example` to `.env`; it is the deployment template for the supplied
Compose stack. Important defaults and boundaries:

- PostgreSQL uses the bundled `pgvector/pgvector:pg17` service unless
  `DATABASE_DSN` overrides it.
- `ENGRAM_EMBEDDING_URL` is optional. With no embedding endpoint, recall remains
  available through full-text search rather than a vector tier.
- `ENGRAM_RERANK_URL` is an optional source-wired recall path, not a default or
  advertised core capability. See the source classification in the ledger.
- `ENGRAM_GRAPH_ENABLED` and `ENGRAM_VNEXT_F_ENABLED` enable distinct non-default surfaces. Current source enables code intelligence by default; `ENGRAM_CODE_INTEL_ENABLED=false` is its explicit stop.
- `ENGRAM_AUTH_DISABLED=true` selects the deliberate trusted single-user HTTP LAN path. It is not a multi-user security boundary; auth-enabled installations retain their own credentials and grants.

For a separate console host, set `ENGRAM_OPERATOR_CONSOLE_URL` on the server to
an absolute URL. The server validates that this value includes a scheme and host
before proxying browser routes.

---

<!-- redoc:start:installation -->
## Installation

### Plugin install (recommended)

The marketplace plugin registers the MCP server, skills, and slash commands in
Claude Code and Oh My Pi. Claude Code also activates the bundled lifecycle hooks.
OMP 17.x does not execute Claude `hooks.json`, so Claude capture paths do not run
there; the bundled native Engram extension instead injects Engram context on `session_start` and
`before_agent_start`.

**Prerequisite:** Node.js 18+ must be available on `PATH`. It runs the bundled
lifecycle hooks and lets the standalone installers validate the release
bootstrap policy before installation. Windows-compatible Bash installs (Git
Bash, MSYS2, or Cygwin) also require `unzip` on `PATH`.

Install the marketplace plugin, configure its server origin for the chosen auth mode, and restart the host. The no-auth path needs only `ENGRAM_URL`; auth-enabled setup uses its separate workstation keycard. Do not replace an existing client installation to test the new one.

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

For a local source build, use the [server quick start](#quick-start-server), including required image names and build version. For a digest-pinned deployment, use the [deployment guide](docs/DEPLOYMENT.md). Do not run `docker compose up` without its image variables.

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

Set the no-auth client server origin:
```bash
export ENGRAM_URL=http://your-server:37777
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
        "ENGRAM_URL": "http://your-server:37777"
      }
    }
  }
}
```

**CLI shortcut:**

```bash
claude mcp add-json engram '{"type":"stdio","command":"engram","env":{"ENGRAM_URL":"http://your-server:37777"}}' -s user
```

`ENGRAM_URL` is the bare configured server origin; do not append `/mcp` or `/sse`. The auth-enabled path requires a separate workstation `ENGRAM_TOKEN`, never the operator credential.

### Build from Source

Requires Go 1.26.6+ and Node.js (for dashboard).

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
2. open `<server-url>/access`, issue a workstation keycard from **Workstation keycards**, and configure `ENGRAM_TOKEN`
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

The project requires Go 1.26.6+ for its current build. Use the focused Go checks
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
