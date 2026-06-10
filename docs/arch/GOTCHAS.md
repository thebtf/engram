# Gotchas and Integration Notes

Non-obvious behaviors and operational risks. Read before deploying.

---

## DATABASE_DSN Is Environment-Only

**Severity: OPERATIONAL PITFALL**

`DatabaseDSN` has `json:"-"` — adding it to `settings.json` does nothing. Always
set `DATABASE_DSN` as an environment variable.

---

## pgvector Extension Must Pre-Exist

**Severity: DEPLOYMENT RISK**

Migration runs `CREATE EXTENSION IF NOT EXISTS vector`. This requires SUPERUSER
or the extension to be pre-installed. On managed PostgreSQL (RDS, Cloud SQL,
Supabase), enable pgvector through the control plane before starting the server.

---

## WorkerHost Defaults to Localhost

**Severity: LOW (confusing)**

The effective default bind address is `127.0.0.1` (localhost only), not `0.0.0.0`.
Set `ENGRAM_WORKER_HOST=0.0.0.0` to expose on the network (required for
multi-workstation or Docker setups).

---

## Hub Storage: New Memories Are Not Immediately Vector-Searchable

**Severity: BEHAVIORAL SURPRISE**

With default `ENGRAM_VECTOR_STORAGE_STRATEGY=hub`, embeddings are stored only
after a memory is accessed `ENGRAM_HUB_THRESHOLD` times (default: 5). New
memories are only findable via FTS until the threshold is reached.

**Workaround:** Set `ENGRAM_HUB_THRESHOLD=1` for immediate embedding at the cost
of more storage.

---

## Hooks Are Fire-and-Forget HTTP (Worker Must Be Running)

**Severity: OPERATIONAL**

All JS hooks make HTTP requests to `engram-server` on :37777. If the server is
down:

- `session-start`: returns empty context (no error to Claude Code)
- Other hooks: silently fail (logged to stderr)

No queuing or retry. Memories from sessions where the server was down are lost.

---

## Stop Hook Reads Transcript From Disk

**Severity: INFORMATIONAL**

The `stop` hook reads the Claude Code transcript JSONL directly from
`transcript_path`. In containerized setups where hooks and transcripts live on
different filesystems, summary generation works without transcript context.

---

## Inject Unified Retrieval Is Default

**Severity: BEHAVIORAL CHANGE**

`ENGRAM_INJECT_UNIFIED` defaults to `true` — inject uses the same retrieval path
as search. Score thresholds, freshness filtering, and ranking changes affect both
simultaneously. Set `ENGRAM_INJECT_UNIFIED=false` only as an emergency rollback.

---

## Auth Token Naming (v6 Breaking)

**Severity: MIGRATION**

- `ENGRAM_API_TOKEN` no longer works → use `ENGRAM_AUTH_ADMIN_TOKEN`
- The admin token is the **operator** token (server host only)
- Workstations use **worker keycards** issued via the dashboard `/tokens` page
- Old single-token setups must re-configure after v6 upgrade

---

## Embedding Removed in v5 (rebuild in progress)

**Severity: INFORMATIONAL**

Server-side embedding (OpenAI, ONNX) was removed in v5. `EMBEDDING_*` env vars
are ignored. The `content_chunks` table still exists but is populated externally
(by the client-side daemon or import tools), not by the server. This removal was
the v5 transitional strip-down of the non-functional pre-v5 implementation; vnext
is rebuilding embedding from scratch (Milestones A-C), and `EMBEDDING_*` vars
will be inert until that subsystem ships.

---

## Module Name Unchanged From Upstream

**Severity: LOW**

Go module is `github.com/thebtf/engram` — same as the upstream origin. Not an
issue for normal use; only matters if importing both repos simultaneously.
