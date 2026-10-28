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

## Single-owner installation

The maintained `.env.example` lists exactly 14 Unraid **Variable** rows:
`TZ`, `DATABASE_DSN`, `ENGRAM_VAULT_KEY`, `ENGRAM_EMBEDDING_URL`,
`ENGRAM_EMBEDDING_API_KEY`, `ENGRAM_EMBEDDING_MODEL`, `ENGRAM_EMBEDDING_DIMENSIONS`,
`ENGRAM_LLM_URL`, `ENGRAM_LLM_API_KEY`, `ENGRAM_LLM_MODEL`, `ENGRAM_AUTH_DISABLED`,
`ENGRAM_AUTH_ADMIN_TOKEN`, `ENGRAM_CODE_INTEL_ENABLED`, `ENGRAM_TELEMETRY_ENABLED`.
Keep the owner's existing values, including the selected embedding dimensions and Vault key.
`ENGRAM_AUTH_DISABLED=true` is a deliberate trusted single-user LAN mode, not multi-user isolation.
Authentication can still be enabled; `ENGRAM_CODE_INTEL_ENABLED=false` and
`ENGRAM_TELEMETRY_ENABLED=false` remain explicit stops.

For the owner's existing Unraid template, the precise cut is **51 → 14 Variable rows**:
remove the 20 empty and 17 redundant/retired nonempty rows, not their backing data.
Keep `Repository=ghcr.io/thebtf/engram:latest`, the existing single port mapping,
and all three existing mounts (`/data`, `/unraid`, `/var/lib/engram`) byte-for-byte.
Do not rename `RERANK_URL` to `ENGRAM_RERANK_URL`: the former is ignored;
renaming it would activate a provider that was not selected.

Before the later host update, preserve an access-restricted exact original template.
Filter only Variable rows outside the allowlist, compare every retained name/value,
Repository, port and mount against the original, then use the normal Unraid updater.
Do not print secrets or overwrite the Vault key with an example value.
If rollback is needed, restore the exact private original template and the prior approved
`latest` promotion through the normal updater. This template-only step changes no database
or owner records; a data conversion requires its separate backup/restore admission.

The Compose examples also require their existing image-manifest, database-password and
reverse-proxy/network inputs. Those are orchestration inputs for the separate three-artifact
stack, not extra owner-template variables. Image-provided HOME/bind/port values are not
Unraid Variable rows. The reference below includes advanced supported controls; it is not
an instruction to populate empty template rows.


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
| `ENGRAM_AUTH_ALLOWED_ORIGINS` | (empty) | Comma-separated exact public HTTP(S) browser origins admitted for logout in split console/backend deployments, e.g. `https://console.example.test`. No wildcard, trailing slash/path, userinfo, query or fragment; malformed entries fail worker configuration. This does not trust forwarded headers or grant authentication. |

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
| `ENGRAM_ENFORCE_SOURCE_PROJECT` | `true` | Enforce source/project isolation; keep its authorization checks. |

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
| `ENGRAM_EMBEDDING_DIMENSIONS` | `1536` | Preserve the selected value. `0` omits a dimensions request; `1536` requests the schema-bound vector size. |

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

### Ordinary v7 runtime

Hybrid recall, audit/retention, privacy scopes, write-lint, candidate review/governance,
continuity reservation, lifecycle, adaptive segmentation/briefs, and crystallization are
built-in behavior. They do not require `ENGRAM_VNEXT_ENABLED`, `ENGRAM_VNEXT_F_ENABLED`,
`ENGRAM_LIFECYCLE_ENABLED`, `ENGRAM_CONTINUITY_SLOT_ENABLED`, `ENGRAM_ADAPTIVE_ENABLED`,
`ENGRAM_CRYSTALLIZATION_ENABLED` or `ENGRAM_GRAPH_ENABLED`; old values are ignored.
Graph readers remain available through their wired stores. Inference still requires its
configured provider, and all authorization, privacy, validation and availability checks remain.

Unified context retrieval has no empty rollback branch or mutable `ENGRAM_INJECT_UNIFIED`
setting. `/api/config` retains `memory.inject_unified=true` as a read-only compatibility
observation for current readers; PATCH rejects that retired control. `/api/flags` retains
the built-in capability names used by the current console, not mutable startup gates.

The dormant V7 plug/S1–S6 activation and Authentik header-login entry are retired.
Their retained historical state, protobufs and lower-level readers are not deleted.
Local login, operator/keycard authentication, the explicit auth-disabled mode and general
trusted-proxy/origin guards remain. Historical V7 flags no longer activate runtime work.

| Variable | Default | Description |
|----------|---------|-------------|
| `ENGRAM_TRANSCRIPT_RETENTION_DAYS` | `0` | Unprocessed transcript retention; `0` means no pruning during provider outages. Processed rows are pruned by the ordinary dream cycle. |

### Removed in v5/v6

v5 removals fall into two categories. **Permanent architectural shifts** (auth model, transport model) are gone for good. **Transitional strip-down items** (graph, retrieval) were removed because the pre-v5 implementations were non-functional; they are being rebuilt from scratch across the vnext milestones (lifecycle, graph, retrieval, crystallization — see `internal/lifecycle`, `internal/graph`, `internal/retrieval`, `internal/crystallization` packages already landing on main). The embedding and LLM clients have been rebuilt and are active — see the Embedding and LLM sections above.

These variables are no longer read by the **server runtime** — do not set them for the server:

- `ENGRAM_API_TOKEN` / `API_TOKEN` → the server reads `ENGRAM_AUTH_ADMIN_TOKEN` (v5 — permanent architectural shift). **Exception:** the `engram-import` CLI tool still reads `ENGRAM_API_TOKEN` for its own Bearer auth (`cmd/engram-import/main.go`); set it only when running that tool, not for the server.
- Historical embedding provider/base/model aliases are ignored. The current client reads `ENGRAM_EMBEDDING_URL`, `ENGRAM_EMBEDDING_MODEL`, `ENGRAM_EMBEDDING_API_KEY`, and the request-dimensions control above.
- `GRAPH_PROVIDER`, `FALKORDB_*` and their compose aliases are ignored; the retained knowledge graph is PostgreSQL-backed and has no startup feature gate.
- `ENGRAM_MODEL`, `ENGRAM_CONTEXT_OBSERVATIONS`, `ENGRAM_CONTEXT_FULL_COUNT`, `ENGRAM_CONTEXT_SESSION_COUNT` → removed or renamed

## settings.json Keys

The settings file accepts the same names as environment variables (without the
`ENGRAM_` prefix for some). See `internal/config/config.go` for the full mapping.
Environment variables always override file values.
