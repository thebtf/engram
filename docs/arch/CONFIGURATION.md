# Configuration Reference

Configuration is loaded from a JSON settings file with environment variable
overrides. Environment variables always take precedence.

## Config File Location

- **Settings file:** `~/.engram/settings.json`
- **Data directory:** `~/.engram/` (created with `0700` permissions on first run)
- **Collections file:** Override with `COLLECTION_CONFIG` env var

## Loading Precedence

```
compiled defaults  <  ~/.engram/settings.json  <  environment variables
```

The settings file is created on first run with minimal defaults. Parsing errors
are silently ignored (compiled defaults used).

## Environment Variables

### Required

| Variable | Description |
|----------|-------------|
| `DATABASE_DSN` | PostgreSQL connection string. Never stored in config file. Example: `postgres://user:pass@host:5432/engram?sslmode=disable` |

### Authentication (v6)

| Variable | Default | Description |
|----------|---------|-------------|
| `ENGRAM_AUTH_ADMIN_TOKEN` | (none) | Operator token for server admin. Lives ONLY on the server host. |
| `ENGRAM_AUTH_SKIP_LOCAL` | `false` | Skip auth for RFC 1918 (private) IP addresses. Useful for local dev. |
| `ENGRAM_AUTH_TRUSTED_PROXY` | (none) | Trusted reverse proxy address for X-Forwarded-For parsing. |
| `ENGRAM_AUTHENTIK_ENABLED` | `false` | Enable Authentik SSO forward-auth integration. |
| `ENGRAM_AUTHENTIK_AUTO_PROVISION` | `false` | Auto-create users from Authentik headers. |
| `ENGRAM_AUTHENTIK_TRUSTED_PROXIES` | (none) | Comma-separated trusted proxy IPs for Authentik headers. |

### Server

| Variable | Default | Description |
|----------|---------|-------------|
| `ENGRAM_WORKER_HOST` | `127.0.0.1` | Bind address. Set to `0.0.0.0` to expose on network. |
| `ENGRAM_DB_PATH` | `~/.engram` | Data directory path. |
| `DATABASE_MAX_CONNS` | (driver default) | Max PostgreSQL connection pool size. |
| `WORKSTATION_ID` | (auto: hostname + machine ID) | Override workstation identity for consistent cross-session tracking. |

### Memory Retrieval

| Variable | Default | Description |
|----------|---------|-------------|
| `ENGRAM_CONTEXT_MAX_TOKENS` | (compiled) | Max tokens for context injection. |
| `ENGRAM_ALWAYS_INJECT_LIMIT` | (compiled) | Max always-inject memories per session start. |
| `ENGRAM_PROJECT_INJECT_LIMIT` | (compiled) | Max project-scoped memories per injection. |
| `ENGRAM_INJECT_UNIFIED` | `true` | Unified injection mode (same retrieval as search). Set `false` as emergency rollback. |
| `ENGRAM_ENFORCE_SOURCE_PROJECT` | `false` | Strict project isolation for memory retrieval. |

### Embedding

The embedding client (`internal/embedding/client.go`) calls an OpenAI-compatible
`/v1/embeddings` endpoint. It powers the vector tier of hybrid retrieval. When
`ENGRAM_EMBEDDING_URL` is empty the client is disabled and hybrid recall degrades
gracefully to FTS-only (no vector tier).

| Variable | Default | Description |
|----------|---------|-------------|
| `ENGRAM_EMBEDDING_URL` | (none) | Base URL of the embeddings endpoint. Empty = embedding disabled. |
| `ENGRAM_EMBEDDING_MODEL` | `text-embedding` | Model name sent in the request body. |
| `ENGRAM_EMBEDDING_API_KEY` | (none) | When set, sent as `Authorization: Bearer <key>`. Empty = no auth header (key-less LAN endpoint). |

### LLM

The LLM client (`internal/llm/client.go`) calls an OpenAI-compatible
`/v1/chat/completions` endpoint. It is used for inference-backed crystallization
and query expansion. This is an **external API only** — no local inference is
bundled. When `ENGRAM_LLM_URL` is empty the client is disabled and all
crystallization features that require inference become a no-op.

| Variable | Default | Description |
|----------|---------|-------------|
| `ENGRAM_LLM_URL` | (none) | Base URL of the chat completions endpoint. Empty = LLM disabled (crystallization inference is a no-op). |
| `ENGRAM_LLM_MODEL` | `chat-default` | Model name sent in the request body. |
| `ENGRAM_LLM_API_KEY` | (none) | When set, sent as `Authorization: Bearer <key>`. Empty = no auth header (key-less LAN endpoint). |

### Vector Storage

| Variable | Default | Description |
|----------|---------|-------------|
| `ENGRAM_VECTOR_STORAGE_STRATEGY` | `hub` | Storage strategy: `hub` (delayed embedding) or `immediate`. |
| `ENGRAM_HUB_THRESHOLD` | `5` | Access count before embeddings are persisted (hub strategy only). |

### Vault

| Variable | Default | Description |
|----------|---------|-------------|
| `ENGRAM_VAULT_KEY` | (none) | AES-256-GCM master key — **64 hex chars (= 32 bytes)**, e.g. `openssl rand -hex 32`. Decoded via `hex.DecodeString` in `internal/crypto.NewVault`; a base64 value will fail at startup. Primary name. |
| `ENGRAM_ENCRYPTION_KEY` | (none) | Alias for `ENGRAM_VAULT_KEY` (same 64-hex format). |
| `ENGRAM_ENCRYPTION_KEY_FILE` | (none) | Path to a file containing the master key as 32 raw bytes or 64 hex chars. |

### Operational

| Variable | Default | Description |
|----------|---------|-------------|
| `ENGRAM_TELEMETRY_ENABLED` | `true` | Periodic telemetry snapshots. |
| `ENGRAM_LOG_BUFFER_SIZE` | (compiled) | In-memory log ring buffer size (exposed via `/api/logs`). |
| `ENGRAM_OUTCOME_RECORDER_INTERVAL_MINUTES` | (compiled) | Interval for periodic session outcome recording. |
| `COLLECTION_CONFIG` | (none) | Path to collections YAML config file. |

### vNext Feature Flags

These flags gate optional vNext subsystems. All default to `false` (disabled).
Enable them once the corresponding subsystem is wired and the schema migrations
have been applied.

| Variable | Default | Description |
|----------|---------|-------------|
| `ENGRAM_VNEXT_ENABLED` | `false` | Master vNext gate. Enables retention cron, hybrid retrieval (`recall_memory` / `recall similar` / `recall explain`), `purge_project`, and the audit trail. |
| `ENGRAM_LIFECYCLE_ENABLED` | `false` | Enables the sleep cycle (tier promotion/demotion) and the `tier_filter` recall parameter. |
| `ENGRAM_VNEXT_F_ENABLED` | `false` | Milestone F gate (requires `ENGRAM_VNEXT_ENABLED=true` for the full feature set). Enables privacy-scope enforcement, knowledge node taxonomy, TG3 rationale/filters, crystallization candidates + tools, write-lint two-phase protocol, governance + bulk-op tools, snapshot rollback, and the redaction layer. |
| `ENGRAM_GRAPH_ENABLED` | `false` | Enables the knowledge graph subsystem and `graph` MCP tool (Milestone C). |
| `ENGRAM_ADAPTIVE_ENABLED` | `false` | Enables adaptive memory segmentation and adaptive brief retrieval (`get_memory_brief`). |
| `ENGRAM_CRYSTALLIZATION_ENABLED` | `false` | Enables crystallization (Milestone D, LLM pipeline). On session-end the redacted transcript is persisted to `session_transcripts`; an async dream-cycle (rides the sleep tick) reads unprocessed transcripts, extracts decisions/lessons via the LLM (`ENGRAM_LLM_URL`), and routes them to crystallization candidates. **Language-independent** — decisions are extracted and stored in their original language with a `lang:<code>` tag (the legacy English-only regex extractor was removed). When `ENGRAM_LLM_URL` is unset the dream-cycle is a no-op. Requires `ENGRAM_VNEXT_ENABLED=true` for the audit trail and `ENGRAM_VNEXT_F_ENABLED=true` for the candidate routing path. |
| `ENGRAM_TRANSCRIPT_RETENTION_DAYS` | `0` | Max age (days) before an **unprocessed** `session_transcripts` row is pruned. `0` = no prune of unprocessed rows (unbounded growth accepted during a prolonged LLM outage in exchange for zero silent data loss; operator opts into a safety cap). Processed rows are always pruned on the dream-cycle tick regardless of this value. |

### Removed in v5/v6

v5 removals fall into two categories. **Permanent architectural shifts** (auth model, transport model) are gone for good. **Transitional strip-down items** (graph, retrieval) were removed because the pre-v5 implementations were non-functional; they are being rebuilt from scratch across the vnext milestones (lifecycle, graph, retrieval, crystallization — see `internal/lifecycle`, `internal/graph`, `internal/retrieval`, `internal/crystallization` packages already landing on main). The embedding and LLM clients have been rebuilt and are active — see the Embedding and LLM sections above.

These variables are no longer read by the **server runtime** — do not set them for the server:

- `ENGRAM_API_TOKEN` / `API_TOKEN` → the server reads `ENGRAM_AUTH_ADMIN_TOKEN` (v5 — permanent architectural shift). **Exception:** the `engram-import` CLI tool still reads `ENGRAM_API_TOKEN` for its own Bearer auth (`cmd/engram-import/main.go`); set it only when running that tool, not for the server.
- `EMBEDDING_PROVIDER`, `EMBEDDING_BASE_URL`, `EMBEDDING_MODEL_NAME`, `EMBEDDING_DIMENSIONS`, `EMBEDDING_TRUNCATE` (and the `ENGRAM_EMBEDDING_PROVIDER` / `ENGRAM_EMBEDDING_BASE_URL` / `ENGRAM_EMBEDDING_MODEL_NAME` / `ENGRAM_EMBEDDING_DIMENSIONS` / `ENGRAM_EMBEDDING_TRUNCATE` compose aliases) → the rebuilt embedding client reads `ENGRAM_EMBEDDING_URL`, `ENGRAM_EMBEDDING_MODEL`, and `ENGRAM_EMBEDDING_API_KEY` (see the Embedding section above). The old names are ignored.
- `GRAPH_PROVIDER`, `FALKORDB_*` (and the `ENGRAM_GRAPH_PROVIDER` / `ENGRAM_FALKORDB_*` compose aliases) → no FalkorDB backend exists in the code. The knowledge graph is PostgreSQL-backed (`ENGRAM_GRAPH_ENABLED`). The old names are ignored.
- `ENGRAM_MODEL`, `ENGRAM_CONTEXT_OBSERVATIONS`, `ENGRAM_CONTEXT_FULL_COUNT`, `ENGRAM_CONTEXT_SESSION_COUNT` → removed or renamed

## settings.json Keys

The settings file accepts the same names as environment variables (without the
`ENGRAM_` prefix for some). See `internal/config/config.go` for the full mapping.
Environment variables always override file values.
