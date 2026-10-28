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

Normal single-owner startup uses exactly four Unraid **Variable** rows:
`TZ`, `DATABASE_DSN`, `ENGRAM_VAULT_KEY`, `ENGRAM_AUTH_DISABLED`.
Keep their existing values. `ENGRAM_AUTH_DISABLED=true` is the owner's explicit
trusted single-user LAN choice, not multi-user isolation or a permanent no-auth default.
For auth-on, select `false` and supply the existing `ENGRAM_AUTH_ADMIN_TOKEN` using
the commented conditional row; preserve its old value in the private rollback template.
Code intelligence and telemetry still default to true. Their optional
`ENGRAM_CODE_INTEL_ENABLED=false` and `ENGRAM_TELEMETRY_ENABLED=false` emergency
overrides remain supported but are not normal startup rows.

The provider configuration already belongs to PostgreSQL `model_settings`, not
`~/.engram/settings.json` or `PATCH /api/config`. Its seven keys are:

| Key | Stored value |
|-----|--------------|
| `embedder.url` | Selected embedding URL |
| `embedder.model` | Selected embedding model |
| `embedder.api_key` | Vault-encrypted selected embedding key |
| `embedder.dimensions` | String `0` (omit request parameter) or `1536` (schema-bound size) |
| `llm.url` | Selected chat-completions URL |
| `llm.model` | Selected LLM model |
| `llm.api_key` | Vault-encrypted selected LLM key |

Clients resolve nonempty optional ENV overrides first, then these settings,
then their existing defaults. Configuration is captured at startup; setting
changes take effect after the normal restart, not on each provider request.
An absent URL in both sources leaves that provider disabled.

Before removing provider ENV rows, privately transfer the **current effective**
values through the existing admin MCP `settings` tool: actions `set`, `get`, `list`,
`delete`; `set` takes string fields `key` and `value` plus optional `description`
and `encrypt`. `.api_key` values are always encrypted by the server with the same
Vault; secret `get`/`list` responses are metadata only, not plaintext equality proof.
Use the existing host-local private transport, never raw secrets in chat or shell arguments.
Verify all seven selected values, including decrypting secret equality with the
original Vault key, before admission to the smaller template. The existing
startup backfill is only-missing and fail-soft: it never overwrites existing
DB settings and must not be treated as an equality check. Do not activate a
different provider or replace the Vault key while transferring configuration.

After that verification, the owner's existing template contracts **51 → 4 Variable rows**.
Keep `Repository=ghcr.io/thebtf/engram:latest`, the existing single port mapping,
and all three existing mounts (`/data`, `/unraid`, `/var/lib/engram`) byte-for-byte.
Do not rename `RERANK_URL` to `ENGRAM_RERANK_URL`: the former is ignored;
renaming it would activate a provider that was not selected.

Before the later host update, preserve an access-restricted exact original template
with every old provider value and conditional auth token. Compare the four retained
values, Repository, port and mounts against that original, then use the normal updater.
Do not print secrets or overwrite the Vault key with an example value.
Rollback restores the exact private original template and the prior approved
`latest` promotion through the normal updater; the original ENV overrides take
precedence again. This changes no owner records; data conversion has separate admission.

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
both ENV and stored `embedder.url` are absent, the client is disabled and hybrid
recall degrades gracefully to FTS-only (no vector tier).

| Variable | Default | Description |
|----------|---------|-------------|
| `ENGRAM_EMBEDDING_URL` | (none) | Optional override of stored `embedder.url`; absent in both sources = embedding disabled. |
| `ENGRAM_EMBEDDING_MODEL` | `text-embedding` | Optional override of stored `embedder.model`. |
| `ENGRAM_EMBEDDING_API_KEY` | (none) | Optional override of encrypted `embedder.api_key`. No auth header only when both sources are absent. |
| `ENGRAM_EMBEDDING_DIMENSIONS` | `1536` | Optional override of stored `embedder.dimensions`: `0` omits the request parameter; `1536` requests the schema-bound vector size. |

### LLM

The LLM client (`internal/llm/client.go`) calls an OpenAI-compatible
`/v1/chat/completions` endpoint. It is used for inference-backed crystallization
and query expansion. This is an **external API only** — no local inference is
bundled. When ENV and stored `llm.url` are both absent, the provider is disabled
and crystallization inference remains a no-op without consuming transcripts.

| Variable | Default | Description |
|----------|---------|-------------|
| `ENGRAM_LLM_URL` | (none) | Optional override of stored `llm.url`; absent in both sources = LLM disabled. |
| `ENGRAM_LLM_MODEL` | `chat-default` | Optional override of stored `llm.model`. |
| `ENGRAM_LLM_API_KEY` | (none) | Optional override of encrypted `llm.api_key`. No auth header only when both sources are absent. |

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
| `ENGRAM_TRANSCRIPT_RETENTION_DAYS` | `0` | `0` retains unprocessed transcripts indefinitely; positive days expire old unprocessed rows on the existing four-hour tick even without an LLM. Processed rows are pruned on the same tick. |

The four-input default does not bound unprocessed transcript storage. Selecting
a finite retention value is an explicit owner data policy. Admitted inference
runs before pruning; extraction failures retain pending transcripts for retry.
With a configured consumer, pending transcripts stay in the durable queue across
busy ticks and restarts. Idle ticks retry that queue independently of the new-memory
count threshold; unavailable or failed analysis cannot authorize its expiry.

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
