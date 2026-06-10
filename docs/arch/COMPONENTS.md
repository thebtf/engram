# Component Inventory

## Scope

Runtime binaries, internal Go packages, and JS hooks for `engram` v6.

- Go 1.25+
- PostgreSQL 17 + pgvector + pgvectorscale
- MCP over stdio (gRPC backend), HTTP REST API
- cmux multiplexes HTTP + gRPC on :37777

## Binaries

### `engram-server` (`cmd/engram-server/main.go`)

Long-lived server process. Hosts:
- HTTP REST API (chi router)
- gRPC services (memory, session, health)
- Vue.js dashboard (embedded static assets)
- SSE event bus for real-time dashboard updates
- Background services: outcome recorder, telemetry, reaper

Startup: `internal/worker.NewService(Version)`. Shutdown: graceful with timeout.
Port :37777 via cmux (HTTP and gRPC auto-detected by protocol).

### `engram` (`cmd/engram/main.go`)

Per-session stdio MCP daemon. Started by Claude Code as a plugin command.
Connects to `engram-server` via gRPC. Exposes 39 MCP tools to the agent.
Reports `daemonVersion` at startup. Handles `--help`, `--version` flags.

### `engram-import` (`cmd/engram-import/main.go`)

Bulk JSONL import utility for migrating data into engram.

## Hooks (`plugin/engram/hooks/`)

9 JS hooks executed via node by Claude Code's plugin system. Registration in `hooks.json`.

| Hook | Trigger | Action |
|------|---------|--------|
| `session-start.js` | Session begins | Injects memories as `<engram-context>` |
| `user-prompt.js` | User sends message | Records prompt text and number |
| `post-tool-use.js` | Tool call completes | Records tool invocations and outputs |
| `pre-tool-use.js` | Before tool call | Context enrichment for file-touching tools |
| `pre-compact.js` | Before context compaction | Captures pre-compact state |
| `stop.js` | Session ends | Generates session summary |
| `session-end.js` | Session cleanup | Final session recording |
| `subagent-stop.js` | Subagent completes | Records subagent completion event |
| `statusline.js` | Statusline update | Injects memory count into Claude Code statusline |

Shared utilities in `lib.js`. Tests: `*.test.js`.

## Internal Packages

### Core

| Package | Purpose |
|---------|---------|
| `internal/config` | Config schema, defaults, JSON settings + env override loading |
| `internal/db/gorm` | GORM models, 96 migrations, 25 tables. Substores: MemoryStore, CredentialStore, IssueStore, DocumentStore, SessionStore, BehavioralRulesStore, VersionedDocumentStore, etc. |
| `internal/mcp` | MCP protocol implementation. 39 tool registrations (7 primary + 32 compat). Tool handlers delegate to stores and services. |
| `internal/grpcserver` | gRPC service implementations (memory, session, health, context inject). |
| `internal/worker` | HTTP route layer, middleware (auth, logging), embedded Vue dashboard, SSE events, **retrieval/search logic** (hybrid FTS + vector, moved here after `internal/search` removal in v5). Sub-packages: `sdk`, `session`, `sse`, `reaper`, `projectevents`. |
| `internal/auth` | Two-tier token authentication, Authentik SSO integration, middleware. |

### Domain

| Package | Purpose |
|---------|---------|
| `internal/module` | Modular service framework. Sub-packages: `dispatcher` (event routing), `lifecycle` (startup/shutdown), `obs` (observation metrics), `registry` (service registry). |
| `internal/instincts` | Instinct file import (behavioral guidance from markdown). |
| `internal/collections` | Collection metadata, YAML config, file→collection routing. |
| `internal/sessions` | JSONL session file indexer. Incremental mtime tracking. Workstation/project identity via SHA256. |
| `internal/chunking` | Content chunking by file type. `golang/` (Go AST), `markdown/` (header-based). |
| `internal/privacy` | Secrets redaction before persistence (API keys, passwords, tokens). |
| `internal/crypto` | AES-256-GCM vault for credential encryption. Master key management. |
| `internal/control` | Server control plane (reload, health). |

### Infrastructure

| Package | Purpose |
|---------|---------|
| `internal/proxy` | gRPC proxy for MCP daemon → server communication. |
| `internal/logbuf` | In-memory ring buffer for log lines (exposed via `/api/logs`). |
| `internal/telemetry` | Periodic system health snapshots. |
| `internal/watcher` | fsnotify wrapper for settings.json hot-reload. |
| `internal/update` | Self-update: GitHub releases, checksum verification, binary replacement. |
| `internal/benchmark` | Go benchmark utilities (histogram, seed data). |
| `internal/moduletest` | Test helpers for module framework. |

### Handlers

| Package | Purpose |
|---------|---------|
| `internal/handlers/engramcore` | Core gRPC handler implementations. gRPC connection pool. |
| `internal/handlers/loom` | Loom task engine handlers. |
| `internal/handlers/serverevents` | Server-sent events bridge for real-time updates. |

## `pkg` Packages

| Package | Purpose |
|---------|---------|
| `pkg/models` | Domain types: Memory, SDKSession, Issue, Credential, BehavioralRule, Document, etc. JSON database helpers (JSONStringArray, JSONInt64Array). |
| `pkg/similarity` | Clustering helpers for similarity workflows. |
| `pkg/strutil` | Shared string utilities: Truncate, TruncateTrimmed, ContainsAny. |

## Removed in v5 strip-down

These paths were deleted in the v5 demolition phase (~12,800 lines removed) and must not be
referenced. Some package names have been reused for ground-up vnext rebuilds; those new
implementations share a name but not semantics — do not assume old API or behaviour applies.

**Still absent — do not reference:**
`internal/search`, `internal/vector/`, `internal/consolidation`, `internal/scoring`,
`internal/pattern`, `internal/reranking`, `internal/maintenance`, `pkg/llmclient`,
`internal/synthesis`, `internal/backfill`, `internal/dedup`, `internal/pipeline`,
`internal/palace`.

**Rebuilt in vnext (new implementation, do not assume old semantics):**
`internal/graph`, `internal/embedding` — old implementations were deleted in v5; the packages
now present on main are ground-up vnext rebuilds. The same applies to `internal/lifecycle`,
`internal/retrieval`, and `internal/crystallization`, which are new additions with no v4
equivalent.

The `observations` table was also dropped (replaced by `memories`).
