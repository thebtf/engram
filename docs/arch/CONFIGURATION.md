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

### Vector Storage

| Variable | Default | Description |
|----------|---------|-------------|
| `ENGRAM_VECTOR_STORAGE_STRATEGY` | `hub` | Storage strategy: `hub` (delayed embedding) or `immediate`. |
| `ENGRAM_HUB_THRESHOLD` | `5` | Access count before embeddings are persisted (hub strategy only). |

### Vault

| Variable | Default | Description |
|----------|---------|-------------|
| `ENGRAM_VAULT_KEY` | (none) | AES-256-GCM master key (base64). Primary name. |
| `ENGRAM_ENCRYPTION_KEY` | (none) | Alias for `ENGRAM_VAULT_KEY`. |
| `ENGRAM_ENCRYPTION_KEY_FILE` | (none) | Path to file containing the master key. |

### Operational

| Variable | Default | Description |
|----------|---------|-------------|
| `ENGRAM_TELEMETRY_ENABLED` | `true` | Periodic telemetry snapshots. |
| `ENGRAM_LOG_BUFFER_SIZE` | (compiled) | In-memory log ring buffer size (exposed via `/api/logs`). |
| `ENGRAM_OUTCOME_RECORDER_INTERVAL_MINUTES` | (compiled) | Interval for periodic session outcome recording. |
| `COLLECTION_CONFIG` | (none) | Path to collections YAML config file. |

### Removed in v5/v6

v5 removals fall into two categories. **Permanent architectural shifts** (auth model, transport model) are gone for good. **Transitional strip-down items** (embedding, LLM, graph, retrieval) were removed because the pre-v5 implementations were non-functional; they are being rebuilt from scratch across the vnext milestones (lifecycle, graph, retrieval, crystallization — see `internal/lifecycle`, `internal/graph`, `internal/retrieval`, `internal/crystallization` packages already landing on main). Do not set transitional vars until the rebuilt subsystem ships.

These variables no longer exist — do not set them:

- `ENGRAM_API_TOKEN` → replaced by `ENGRAM_AUTH_ADMIN_TOKEN` (v5 — permanent architectural shift)
- `EMBEDDING_PROVIDER`, `EMBEDDING_API_KEY`, `EMBEDDING_MODEL_NAME`, `EMBEDDING_DIMENSIONS` → removed in the v5 strip-down; server-side embedding is being rebuilt in vnext (`internal/embedding`). Do not set until the rebuilt subsystem ships.
- `ENGRAM_LLM_*` → removed in the v5 strip-down; server-side LLM enhancement is planned to return in a later vnext milestone. Do not set until that ships.
- `ENGRAM_MODEL`, `ENGRAM_CONTEXT_OBSERVATIONS`, `ENGRAM_CONTEXT_FULL_COUNT`, `ENGRAM_CONTEXT_SESSION_COUNT` → removed or renamed

## settings.json Keys

The settings file accepts the same names as environment variables (without the
`ENGRAM_` prefix for some). See `internal/config/config.go` for the full mapping.
Environment variables always override file values.
