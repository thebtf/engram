# Engram Dashboard Designer Endpoint Brief

Verified on 2026-06-18 against:

```text
http://unleashed.lan:37777
```

Use **HTTP**, not HTTPS. The HTTPS probe failed TLS negotiation, while HTTP returned live data.

Auth note: the operator says auth is disabled right now, and most dashboard data GETs work without headers. However, `GET /api/auth/me` returned `401`, `GET /api/admin/users` returned `403`, and token admin endpoints still require auth. For visual design, use the data endpoints below and render identity/admin areas as locked or "auth required" states.

## Best Endpoints For Real Dashboard Data

These are the most useful endpoints to open directly in a browser or fetch from a prototype.

```bash
curl -s http://unleashed.lan:37777/api/selfcheck
curl -s http://unleashed.lan:37777/api/config
curl -s http://unleashed.lan:37777/api/projects
curl -s http://unleashed.lan:37777/api/stats/vnext
curl -s http://unleashed.lan:37777/api/stats/retrieval
curl -s http://unleashed.lan:37777/api/search/analytics
curl -s http://unleashed.lan:37777/api/search/recent
curl -s "http://unleashed.lan:37777/api/issues?project=engram&limit=20"
curl -s http://unleashed.lan:37777/api/issues/tracked-projects
curl -s "http://unleashed.lan:37777/api/vault/credentials?project=engram"
curl -s http://unleashed.lan:37777/api/vault/status
curl -s "http://unleashed.lan:37777/api/sessions/list?limit=20"
curl -s http://unleashed.lan:37777/api/backfill/status
```

## Overview / Health

### `GET /api/selfcheck`

Use for the top status strip, service health cards, version chip, uptime, and component list.

Live shape:

```json
{
  "overall": "healthy",
  "version": "v6.25.1-1-g4cae0b9",
  "uptime": "9h59m3s",
  "components": [
    { "name": "Worker Service", "status": "healthy" },
    { "name": "PostgreSQL", "status": "healthy" },
    { "name": "SDK Processor", "status": "healthy" },
    { "name": "SSE Broadcaster", "status": "healthy" }
  ]
}
```

### `GET /api/config`

Use for settings summary cards. This is **not** the full feature-flag endpoint; it is the current partial config surface.

Live top-level keys:

```json
{
  "context": {},
  "features": {},
  "memory": {},
  "storage": {}
}
```

Useful current fields:

```json
{
  "context": {
    "max_tokens": 8000,
    "observations": 100,
    "relevance_threshold": 0.3,
    "session_count": 10
  },
  "features": {
    "enforce_source_project": true,
    "telemetry_enabled": true
  },
  "memory": {
    "always_inject_limit": 20,
    "inject_unified": true,
    "project_inject_limit": 15
  },
  "storage": {
    "database_max_conns": 10,
    "vector_strategy": "hub"
  }
}
```

## Metrics / Analytics

### `GET /api/stats/vnext`

Use for hero metrics: active memories, injection count, citation count, noise ratio, outcome distribution, embedding coverage.

Live shape:

```json
{
  "injection_count": 0,
  "citation_count": 0,
  "uncited_count": 0,
  "noise_ratio": 0,
  "write_gate_stats": { "active": 2825 },
  "project_citation_rates": [],
  "outcomes": {
    "total_sessions": 3625,
    "unrecorded_sessions": 3463,
    "unrecorded_fraction": 0.9553103448275863,
    "by_outcome": {
      "(unrecorded)": 3463,
      "abandoned": 100,
      "partial": 24,
      "success": 38
    }
  },
  "embedding": {
    "chunk_count": 2837,
    "memories_with_chunks": 2837,
    "model": "text-embedding",
    "dimension": 1536,
    "active_memory_count": 2825,
    "embedding_coverage": 1
  }
}
```

### `GET /api/stats/retrieval`

Use for retrieval throughput cards.

```json
{
  "total_requests": 12731,
  "observations_served": 514929,
  "search_requests": 8265,
  "context_injections": 4466,
  "stale_excluded": 0,
  "fresh_count": 502190,
  "duplicates_removed": 5899
}
```

### `GET /api/search/analytics`

Use for search quality/latency widgets.

```json
{
  "total_searches": 8266,
  "searches_today": 0,
  "avg_latency_ms": 1507.1295399515739,
  "zero_result_rate": 0.11722719574159206,
  "filter_searches": 8266,
  "cache_hits": 0,
  "search_errors": 0
}
```

### `GET /api/vector/metrics` and `GET /api/vectors/health`

Use for vector subsystem status.

```json
{
  "enabled": true,
  "message": "pgvector subsystem active",
  "stats": {
    "chunk_count": 2837,
    "memories_with_chunks": 2837,
    "model": "text-embedding",
    "dimension": 1536
  }
}
```

## Projects

### `GET /api/projects`

Use for project picker, filters, and project count. The live response is a simple string array.

Example:

```json
[
  "_EXTRAS__407a76",
  "16b1f601",
  "3059ec24",
  "4a8aca29",
  "5fb9128e",
  "67e398f8",
  "689ee718",
  "8786eaaa",
  "8dab7620",
  "9c2553be",
  "9e472fe0",
  "a01eaad6"
]
```

`engram` exists in the project list and is a useful filter for issues/vault.

## Issues

### `GET /api/issues?project=engram&limit=20`

Use this as the richest real table/board data source. It returns issue cards plus display-name mapping.

Live shape:

```json
{
  "issues": [
    {
      "id": 184,
      "title": "В светлой теме буквы остаются белыми в диалоговых окнах",
      "body": "",
      "status": "resolved",
      "priority": "critical",
      "type": "bug",
      "source_project": "dashboard",
      "target_project": "engram",
      "source_agent": "human",
      "labels": [],
      "acknowledged_at": null,
      "resolved_at": "2026-05-23T23:05:05.123726+03:00",
      "created_at": "2026-05-03T15:35:35.329936+03:00",
      "updated_at": "2026-05-23T23:05:05.131029+03:00",
      "comment_count": 1
    }
  ],
  "project_names": {
    "dashboard": "dashboard",
    "engram": "engram"
  },
  "total": 64
}
```

### `GET /api/issues/{id}`

Use for issue detail drawer.

```bash
curl -s http://unleashed.lan:37777/api/issues/184
```

Top-level shape:

```json
{
  "issue": {},
  "comments": [],
  "comment_count": 1,
  "source_project_display_name": "dashboard",
  "target_project_display_name": "engram"
}
```

### Other issue endpoints for UI actions

Do **not** call mutation endpoints from a design prototype unless the operator explicitly asks for it.

```text
POST   /api/issues
PATCH  /api/issues/{id}
POST   /api/issues/acknowledge
DELETE /api/issues/{id}
```

## Vault

### `GET /api/vault/status`

Use for vault health, key source, fingerprint, and credential count.

```json
{
  "backup_reminder": "Back up vault.key (or set ENGRAM_VAULT_KEY) ...",
  "credential_count": 24,
  "fingerprint": "aa78e55cf896508c",
  "key_configured": true,
  "key_source": "env"
}
```

### `GET /api/vault/credentials?project=engram`

Use for the credential list. This endpoint lists metadata only; it does not return secret values.

Live shape:

```json
{
  "name": "engram-postgres-dsn",
  "scope": "project",
  "project": "engram",
  "created_at": "2026-06-02T22:04:11+03:00",
  "id": 21
}
```

Secret discipline for design:

- Do not call `GET /api/vault/credentials/{name}` just to populate mock UI.
- If you design a reveal flow, make it a one-shot modal state and never store the revealed value in client state.
- List/status responses must never show secret values.

## Sessions

### `GET /api/sessions/list?limit=20`

Use for session table/list.

Live top-level shape:

```json
{
  "limit": 20,
  "offset": 0,
  "sessions": [
    {
      "id": 91071,
      "claude_session_id": "c01b212d-9453-495c-bdc4-67e27da91465",
      "project": "af2a6d",
      "status": "active",
      "started_at": "2026-06-16T07:01:57+03:00",
      "prompt_counter": 1
    }
  ],
  "total": 3625
}
```

### `GET /api/sessions?claudeSessionId={id}`

Use for session detail drawer.

```bash
curl -s "http://unleashed.lan:37777/api/sessions?claudeSessionId=c01b212d-9453-495c-bdc4-67e27da91465"
```

Detail fields include:

```text
claude_session_id, completed_at, id, injection_strategy, outcome,
outcome_reason, project, prompt_counter, sdk_session_id, started_at,
status, user_prompt, worker_port
```

## Memories

### `GET /api/memories?project={project}&limit=50`

Use for memory grid when a selected project has rows.

```bash
curl -i "http://unleashed.lan:37777/api/memories?project=engram&limit=50"
```

Current live caveat: `project=engram` returns an empty array. Some other projects returned `200 OK` with `Content-Length: 0` instead of `[]`; design the empty state robustly and do not rely on memory rows as the first visual data source.

### `GET /api/memories/{id}`

Available on the live server build `v6.25.1-1-g4cae0b9` / commit `4cae0b9`. A missing or invisible memory returns `404`:

```bash
curl -i http://unleashed.lan:37777/api/memories/1
```

Observed response for a missing row:

```text
HTTP/1.1 404 Not Found
memory not found
```

### Mutation endpoints

Render buttons/states if useful, but do not call these from a design prototype without explicit operator consent:

```text
POST   /api/memories
DELETE /api/memories/{id}
```

## Rules

REST currently exposes only row-level mutations:

```text
PATCH  /api/rules/{id}
DELETE /api/rules/{id}
```

`PATCH /api/rules/{id}` is available on live server build `v6.25.1-1-g4cae0b9` / commit `4cae0b9`. It accepts:

```json
{
  "content": "new rule text",
  "priority": 10,
  "edited_by": "operator-console"
}
```

`GET /api/rules` returned `404`. Rule listing/creation is still MCP-only (`list_rules`, `store_rule`) unless a small REST bridge is added. For design, draw rule list/edit states from mock data or existing prototype fixtures, and label the live REST gap.

## Backfill

### `GET /api/backfill/status`

Use for migration/backfill status widgets.

```json
{
  "total_runs": 0,
  "active_runs": {},
  "total_observations": 0
}
```

Mutation endpoint:

```text
POST /api/backfill/session
```

Do not call it from design-only work.

## Auth / Admin

These are **not** open in the current unauthenticated probe:

```text
GET /api/auth/me          -> 401
GET /api/admin/users      -> 403
GET /api/auth/tokens      -> 401
```

Design these as locked/admin-only states unless the operator provides a token or a session cookie.

Other admin/token endpoints exist for the real app, but they are not useful for unauthenticated visual design:

```text
PUT    /api/admin/users/{id}
GET    /api/admin/invitations
POST   /api/admin/invitations
POST   /api/auth/tokens
DELETE /api/auth/tokens/{id}
GET    /api/auth/tokens/{id}/stats
```

## Missing / Gated / Placeholder Areas

Render these honestly as gated, placeholder, or mock-backed states. Do not fake them as live REST data.

```text
GET /api/flags             -> 404
GET /api/migrations        -> 404
GET /api/settings/model    -> 404
GET /api/rules             -> 404
Candidate queue            -> MCP-only / VNEXT_F-gated
Lifecycle operations       -> MCP-only / LIFECYCLE-gated
Graph                       -> MCP-only / GRAPH-gated
Code Intel                  -> MCP-only / CODE_INTEL-gated
Noise trend time series     -> must-build; only snapshot exists in /api/stats/vnext
Rule enable/disable         -> must-build; no enabled column yet
```

## Four UI Honesty Guardrails

1. Never call tombstones or deprecated shims just to fill UI: `rate_memory`, `feedback(rate)`, `POST /api/import/feedback`, instinct import, old session index/check shims.
2. Treat `search_collection` as removed even if it returns HTTP 200 with a string body. A string deprecation response is not "zero results".
3. Restart-required settings are not applied just because a save request returned 200. Render `saved - restart required`, not green `applied`.
4. Secrets are write-only. Credential list/status screens show metadata only. Reveal is a one-shot flow and the value must be dropped when the panel closes.

## Recommended Data Sources For Missing Design Pieces

For a high-fidelity dashboard mock right now, prioritize:

1. `/api/issues?project=engram&limit=20` for rich rows, priorities, statuses, detail drawers, comments.
2. `/api/stats/vnext`, `/api/stats/retrieval`, `/api/search/analytics`, `/api/vector/metrics` for KPI cards and charts.
3. `/api/sessions/list?limit=20` for activity/history tables.
4. `/api/vault/status` and `/api/vault/credentials?project=engram` for vault cards and credential table.
5. `/api/projects` for project picker and filters.

Use memory/rules/candidates/graph/settings as designed placeholders unless the server team adds the small REST bridges.
