# Configuration Reference

engram configuration is loaded from a JSON settings file with environment variable overrides. Environment variables always take precedence over the JSON file.

## Config File Location

- **Settings file:** `~/.engram/settings.json`
- **Data directory:** `~/.engram/` (created with `0700` permissions on first run)
- **Collections file:** `~/.config/engram/collections.yml` (override with `COLLECTION_CONFIG`)

## Loading Precedence

```
compiled defaults  <  ~/.engram/settings.json  <  environment variables
```

The settings file is created automatically on first run with minimal defaults. Parsing errors in the JSON file are silently ignored and compiled defaults are used.

## Default settings.json (auto-created)

```json
{
  "ENGRAM_WORKER_PORT": 37777,
  "ENGRAM_MODEL": "haiku",
  "ENGRAM_CONTEXT_OBSERVATIONS": 100,
  "ENGRAM_CONTEXT_FULL_COUNT": 25,
  "ENGRAM_CONTEXT_SESSION_COUNT": 10
}
```

## settings.json Keys

All keys use the `ENGRAM_` prefix unless noted otherwise.

### Core

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `ENGRAM_WORKER_PORT` | int | `37777` | Worker HTTP server port |
| `ENGRAM_MODEL` | string | `haiku` | Claude model alias for SDK agent (haiku/sonnet/opus) |
| `ENGRAM_DB_PATH` | string | `~/.engram/engram.db` | Legacy field from SQLite era; parsed but unused in PostgreSQL fork |
| `CLAUDE_CODE_PATH` | string | — | Path to Claude Code CLI (optional) |

### Embedding

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `ENGRAM_EMBEDDING_MODEL` | string | `bge-v1.5` | Legacy field (unused — OpenAI provider only) |
| `EMBEDDING_PROVIDER` | string | `openai` | Embedding provider (`openai` = OpenAI-compatible REST API) |
| `EMBEDDING_BASE_URL` | string | `https://api.openai.com/v1` | OpenAI-compatible API base URL |
| `EMBEDDING_MODEL_NAME` | string | `text-embedding-3-small` | Model name for `openai` provider |
| `EMBEDDING_DIMENSIONS` | int | `1536` | Vector dimensions for `openai` provider |
| `ENGRAM_HUB_THRESHOLD` | int | `5` | Min accesses before storing embedding (hub strategy) |
| `ENGRAM_VECTOR_STORAGE_STRATEGY` | string | `hub` | Vector storage strategy (`hub` = LEANN-inspired, delay until HubThreshold) |

> Note: The ONNX/builtin provider has been removed. Only the `openai` provider is available.

### Reranking (cross-encoder)

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `ENGRAM_RERANKING_ENABLED` | bool | `true` | Enable cross-encoder reranking |
| `ENGRAM_RERANKING_CANDIDATES` | int | `100` | Candidate results fetched before reranking |
| `ENGRAM_RERANKING_RESULTS` | int | `10` | Final results returned after reranking |
| `ENGRAM_RERANKING_ALPHA` | float | `0.7` | Blend weight: 0.0 = all vector score, 1.0 = all cross-encoder score |
| `ENGRAM_RERANKING_MIN_IMPROVEMENT` | float | `0` | Minimum score improvement required to apply reranking |
| `ENGRAM_RERANKING_PURE_MODE` | bool | `false` | Use only cross-encoder scores, discard original vector scores |

### Context Injection

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `ENGRAM_CONTEXT_OBSERVATIONS` | int | `100` | Max observations returned per context injection |
| `ENGRAM_CONTEXT_FULL_COUNT` | int | `25` | First N observations get full detail (narrative + facts); rest are condensed (title + subtitle only) |
| `ENGRAM_CONTEXT_SESSION_COUNT` | int | `10` | Max sessions included in context |
| `ENGRAM_CONTEXT_OBS_TYPES` | string | `bugfix,feature,refactor,change,discovery,decision` | Comma-separated observation types to include |
| `ENGRAM_CONTEXT_OBS_CONCEPTS` | string | `how-it-works,why-it-exists,what-changed,problem-solution,gotcha,pattern,trade-off` | Comma-separated concept tags to include |
| `ENGRAM_CONTEXT_RELEVANCE_THRESHOLD` | float | `0.3` | Minimum similarity score to include an observation |
| `ENGRAM_CONTEXT_MAX_PROMPT_RESULTS` | int | `10` | Max search results per query (0 = threshold-only filtering) |

### Graph Search

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `ENGRAM_GRAPH_ENABLED` | bool | `true` | Enable graph-aware relation traversal in search |
| `ENGRAM_GRAPH_MAX_HOPS` | int | `2` | Maximum relation hops for graph expansion |
| `ENGRAM_GRAPH_BRANCH_FACTOR` | int | `5` | Expand top N neighbors per node |
| `ENGRAM_GRAPH_EDGE_WEIGHT` | float | `0.3` | Minimum edge confidence to follow |
| `ENGRAM_GRAPH_REBUILD_INTERVAL_MIN` | int | `60` | Graph rebuild interval (minutes) |

### Database

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `database_max_conns` | int | `10` | PostgreSQL connection pool size |

> Note: This key uses lowercase without the `ENGRAM_` prefix.

---

## Environment-Only Variables

These variables are **never loaded from settings.json** — they are env-only for security reasons.

| Variable | Default | Description |
|----------|---------|-------------|
| `DATABASE_DSN` | — | PostgreSQL connection string (contains password; `json:"-"` in struct) |
| `ENGRAM_WORKER_HOST` | `127.0.0.1` | Worker bind address. Set to `0.0.0.0` for network access. |
| `ENGRAM_API_TOKEN` | — | Bearer token for worker and SSE endpoint authentication |
| `EMBEDDING_API_KEY` | — | API key for OpenAI-compatible embedding provider |
| `COLLECTION_CONFIG` | `~/.config/engram/collections.yml` | Path to collections YAML config |
| `SESSIONS_DIR` | `~/.claude/projects/` | Directory for Claude Code JSONL session files (session indexer) |
| `WORKSTATION_ID` | auto (sha256(hostname+machine_id)[:8]) | Override the auto-generated workstation identifier |

---

## Notable Behaviors

### DATABASE_DSN is strictly env-only
The `DatabaseDSN` field has `json:"-"` in the struct tag — it is never serialized or deserialized from the JSON file. Even if you add `database_dsn` to `settings.json`, it will not be loaded. Always use `DATABASE_DSN` environment variable.

### WorkerHost default inconsistency
The `Config` struct's `WorkerHost` field has default `0.0.0.0`, but `GetWorkerHost()` returns `127.0.0.1` when the field is empty. The net result is that the worker binds to `127.0.0.1` by default (localhost only). To expose the worker on the network, set `ENGRAM_WORKER_HOST=0.0.0.0`.

### Hub storage strategy
With `ENGRAM_VECTOR_STORAGE_STRATEGY=hub` (default), embeddings are only stored in the `vectors` table after an observation has been accessed `ENGRAM_HUB_THRESHOLD` times (default: 5). New observations are **not** immediately searchable via vector similarity — only via FTS. This reduces storage but means semantic search misses fresh observations.

### Config file watcher
The MCP server process watches `~/.engram/settings.json` for changes. On any change, it calls `os.Exit(0)`. Claude Code is expected to restart the process. This is a deliberate crude-but-simple restart mechanism.

---

## Collections YAML Schema

Path: `~/.config/engram/collections.yml` (or `COLLECTION_CONFIG` env)

```yaml
collections:
  - name: docs                       # required: collection identifier (alphanumeric)
    description: "Project documentation"  # optional: human-readable label
    path_context:                    # optional: path prefix -> context hint
      docs/api: "REST API reference documentation"
      docs/arch: "Architecture documentation"

  - name: source
    description: "Go source code"
    path_context:
      internal/: "Internal packages (not exported)"
      cmd/: "Binary entrypoints"
      pkg/: "Exported packages"
```

**Behavior:**
- If the file does not exist, an empty registry is used — not an error.
- Collections are loaded at MCP server and worker startup.
- `path_context` maps path prefixes to descriptive strings used for context-aware document routing.
- Collection names must be unique. Order is preserved from the YAML file.
