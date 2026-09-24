# Operating Engram

Operational guide for engram server administrators. Covers environment configuration,
redaction rules, startup verification, and restart procedures.

---

## Redaction Rules (EC-F5, EC-F9)

### Overview

Engram supports server-side PII and secret scrubbing before durable memory storage.
Redaction is configured via a server-side rule file and runs before the write-lint
phase. The original content is NOT preserved — redaction is destructive.

### Configuration

Set `ENGRAM_REDACTION_RULES_PATH` to the absolute path of the rule file:

```bash
export ENGRAM_REDACTION_RULES_PATH=/etc/engram/redaction-rules.json
```

If `ENGRAM_REDACTION_RULES_PATH` is **unset**, redaction is a **no-op** and the
system behaves exactly as v6.2.x. No configuration change is required to disable
redaction.

### Rule File Format

Rules are JSON objects with `id` (unique rule identifier), `pattern` (regex), and `replacement` fields:

```json
[
  {
    "id": "aws-access-key",
    "pattern": "AKIA[0-9A-Z]{16}",
    "replacement": "[REDACTED:aws-access-key]"
  },
  {
    "id": "github-pat",
    "pattern": "gh[pousr]_[A-Za-z0-9_]{36,}",
    "replacement": "[REDACTED:github-pat]"
  }
]
```

Rules are applied in order. Each match is replaced; unmatched content is preserved.

### Startup Verification

At startup, engram logs the loaded rule file path and its SHA-256 checksum:

```
INFO  redaction: loaded rule file path=/etc/engram/redaction-rules.json sha256=a3f9... rules=2
```

Operators can use this log line to verify the active rule set matches the expected
configuration. If the expected checksum differs from the logged value, a restart with
the correct rule file is required.

If `ENGRAM_REDACTION_RULES_PATH` is set but the file is **absent at startup**,
the server logs a warning and runs with redaction **disabled** (no-op fallback):

```
WARN  redaction: rule file not found — running with redaction disabled path=/etc/engram/redaction-rules.json
```

### Restart Required for Rule Changes (EC-F9)

**Engram does NOT support hot-reload of redaction rules.**

Modifying the rule file on disk while the server is running has **no effect** on the
active rule set. This is intentional: hot-reload without an explicit signal creates
mid-write rule mismatch ambiguity (a write in flight when the rule set changes may
be partially redacted under one rule set and stored under another).

To apply updated rules:

1. Edit the rule file.
2. Restart the server (or send `SIGHUP` if the SIGHUP handler is enabled in your build).
3. Verify the startup log shows the new SHA-256 checksum.

```bash
# Restart example (systemd)
sudo systemctl restart engram-server

# Verify in logs
journalctl -u engram-server | grep "redaction: loaded"
```

> **Note:** SIGHUP-based reload is optional and must be explicitly enabled at build time.
> Consult the build configuration for your deployment. When in doubt, use a full restart.

### Full Redaction Rejection (EC-F5)

If a redaction rule matches and the resulting content is **empty** (the rule matches
the entire content), the write is **rejected** rather than storing an empty memory.
The error code is `content_fully_redacted`.

Example MCP tool response when full redaction occurs:

```json
{
  "error": "content_fully_redacted",
  "rule_id": "aws-access-key",
  "note": "The memory content was fully redacted by the configured rule. Revise the content or update the redaction rules, then retry."
}
```

The redaction attempt is logged to `audit_log` with:
- `action = 'redacted'`
- `rule_id` of the matched rule

The memory row is **not written** to the database.

### Audit Log

All redaction events (successful scrub or full-content rejection) are written to
`audit_log`:

| `action`    | Meaning                                                                                      |
|-------------|----------------------------------------------------------------------------------------------|
| `redacted`  | Content matched a rule. Write succeeds with scrubbed content (partial match), or is rejected with MCP error code `content_fully_redacted` when the entire content was stripped. |

---

## Snapshot Retention (T049)

Bulk operation snapshots (`bulk_op_snapshots` table) are auto-pruned during the
sleep cycle. Default retention: **30 days**.

Override via environment variable:

```bash
export ENGRAM_SNAPSHOT_RETENTION_DAYS=7   # 7-day retention
```

**Pinned snapshots** (rows with `pinned=true`) are exempt from pruning regardless
of age. Pin a snapshot via the `pin_snapshot` MCP tool or the admin dashboard.

---

## Rollback Conflicts (EC-F3)

If a memory or crystallization-candidate row no longer matches the immutable
post-operation state captured by the snapshot, rollback is **refused** atomically.
No rows are restored on conflict.

The conflict is reported as:

```json
{
  "error": "rollback_conflict",
  "conflict_ids": [42, 99],
  "conflict_refs": [{"entity":"memory","id":42}, {"entity":"candidate","id":99}],
  "snapshot_id": "snap-abc123"
}
```

`conflict_ids` preserves the legacy numeric list. `conflict_refs` identifies each
affected `memory` or `candidate` row, including mixed operations with equal IDs. To
resolve:

1. Review the conflicting rows.
2. If safe to proceed, the operator must manually reset the modified rows or create
   a new snapshot capturing the current state.
3. Retry the rollback with the new snapshot.

The audit log records `action='rollback_attempted_with_conflict'` for all refused
rollback attempts.

## Bring up and verify Code Workspace

This is the operator path for the current UCI `codebase_*` tools and the browser Workspace. It is not the [Feature 011 fixture quickstart](../specs/011-operator-code-console/quickstart.md), which prepares a disposable acceptance environment. Do not count a source-built fixture or an HTTP selfcheck as an installed Workspace acceptance.

### Identify the installation before changing it

1. Record the server origin, server version and image digest or source commit, browser bundle identity, installed plugin and daemon path and version, parser executable path and digest, parser bundle digest, supported browser origin and identity realm, and embedding model, dimensions, and preprocessing profile. Get these from the installed image, plugin inventory, and diagnostics; do not infer the active daemon from a shell `engram` on `PATH`. Do not record credential values, database DSNs, or private source bodies.
2. From an authorized workstation, fetch `<server-origin>/api/version`, `/api/selfcheck`, `/api/flags`, and `/api/auth/me` without credentials in the URL. Check PostgreSQL readiness separately. The private intake `check-engram.ps1` can collect these four GET observations for the original deployment but is **not shipped as a public setup command**. A successful service probe does not show that the daemon has code tools or an indexed View.
3. Confirm the installed release actually contains the new Workspace and UCI implementation. A candidate branch or version label is not installation evidence. On the installation inspected on 24 September 2026, server and plugin daemon reported v6.49.3, the new Workspace was not installed, and `/code` failed to initialize on its HTTP LAN origin. These observations describe that installation at that date, not the state of every deployment or a completed rollout.

### Connect the ordinary client and register a source

1. In an authorized admin browser session, open `<server-origin>/access` and issue a **workstation keycard**. Keep `ENGRAM_AUTH_ADMIN_TOKEN` on the server host. Configure the local client with its own keycard using the supported plugin setup. The universal plugin config is `~/.engram/config.json` (`server_url` and `api_token`); some hosts use `ENGRAM_CONFIG_FILE` or launcher overrides. Inspect the active launcher and preserve existing settings instead of replacing the file. `ENGRAM_URL` is the bare server origin for env-based setups, not an HTTP `/mcp` endpoint. Never paste real credentials into diagnostics or this document.
2. Confirm that the **actual local daemon** receives `ENGRAM_CODE_INTEL_ENABLED=true`; a server-side flag does not register local tools. For parser-required JS, TS, and TSX indexing, the daemon needs an installed absolute `ENGRAM_UCI_PARSER_EXECUTABLE` paired with `ENGRAM_UCI_PARSER_BUNDLE_DIGEST`. Read the compatible bundle digest from the parser's own installed output or release metadata, not the executable's file SHA-256. The installer must supply and validate the binary and configuration. The checked plugin-bin installation had no parser; this is a delivery gap, not a request for the user to guess an executable path. Inspect the daemon's own startup configuration without exposing values of secrets; a new shell's environment does not prove what the running daemon inherited. Reconnect or restart only that daemon through the supported plugin procedure.
3. Start a fresh ordinary agent session and inspect its MCP `tools/list`. Expect `codebase_context`, `codebase_index`, `codebase_status`, `codebase_search`, `codebase_graph`, and `codebase_read` with the schemas actually advertised by that host. `codebase_*` are current UCI tools; only the internal raw-project rollback reader is legacy. A `project` argument on public query tools is compatibility evidence and never selects a View.
4. In an installed release containing first-use onboarding, a human source owner with a read-write workstation keycard and the **same persisted principal as the browser user** calls MCP `codebase_context` in that session with `{"action":"register","source_label":"my-repository","locator":"file:///absolute/local/worktree"}`. The daemon must own that local Git working copy; registration returns server-issued `source_id`, `checkout_id`, `analysis_profile_id`, and a no-View `context_handle`, with `binding_kind:"checkout"` and `context:null`. If the response is lost or the client reconnects, the same owner on the same workstation can repeat that registration with the same source label and locator to recover the existing checkout and a new session's context handle; do not create a new Source or guess an ID. For a second working copy under the same Source, use `{"action":"register","source_id":"<returned source_id>","locator":"file:///absolute/local/other-worktree"}`. A retry with that source ID and the original locator also recovers the same checkout. Do not send the local path to the browser. Registration does not grant browser reads: the owner signs in through the supported origin, calls `GET /api/code/grants/choices`, and issues the matching browser read grant with `POST /api/code/grants` using its returned `choice_ref`; the authenticated browser subject then opens Workspace. A newly registered checkout has no published View and will not appear in `codebase_context` `list` until indexed. If the installed daemon does not advertise `register` or the grant flow is unavailable, first indexing and F1 remain blocked; never insert database rows or invent identifiers.

### Index and inspect an authorized View

For first indexing, use the no-View `context_handle` returned by `codebase_context` `register` in the **same client session**, then call daemon-side `codebase_status` with that handle to prepare the registered local checkout. `codebase_context` `list` only returns authorized published Views, not fresh checkouts. Do not substitute a repository path, a made-up UUID, or a handle from another client. The following `codebase_index` input uses that server-issued checkout handle:

```json
{"context_handle":"<selected checkout handle>"}
```

Index admission returns `run_id`, not a completed View. On the **daemon-side** `codebase_status`, pass that handle and the returned run ID to wait for a bounded read-your-save barrier:

```json
{"context_handle":"<selected checkout handle>","after_barrier":{"token":"<returned run_id>","wait_ms":5000}}
```

The server-side `codebase_status` allows an omitted handle only for an existing client binding; the daemon-side schema requires it. Follow the discovered schema. Check the run's terminal state, source and checkout identity, published View, expected and indexed files, omissions and languages, and embedding readiness for that View. `ENGRAM_EMBEDDING_URL` and `ENGRAM_EMBEDDING_MODEL`, with `ENGRAM_EMBEDDING_API_KEY` if needed, configure the shared provider; a reachable endpoint or memory vectors do not prove code embeddings are ready. Use the released View's analysis profile, dimension, and preprocessing information. Preserve the last good View on failure; do not erase the index to refresh it.

On the published View, call `codebase_search` with a conceptual query that does **not** contain the known function name, then follow a returned reference. For example, replace the query with one chosen for your corpus:

```json
{"context_handle":"<published View handle>","query":"How does this repository choose a working copy for code search?","limit":10}
```

Check actual vector or hybrid retrieval mode and the correct source citation. Lexical fallback is a degraded result, not proof of semantic search. For `codebase_graph`, copy `source_id`, `view_id`, and `entity_key` from that citation:

```json
{"action":"neighbors","context_handle":"<published View handle>","target":{"source_id":"<returned source_id>","view_id":"<returned view_id>","entity_key":"<returned entity_key>"},"direction":"both"}
```

Follow a direct and a reverse relation, including one neighbor not on the search page. For `codebase_read`, copy the exact `ref`, `span` (byte and line start and end), and bare 64-hex-character `content_digest` from a returned citation, along with the same View handle. Its schema requires all three objects or values. Do not read today's file from disk as a substitute for the stored View span. Evidence labeled ambiguous or heuristic does not prove a resolved call; a missing dynamic edge does not prove no dependency exists.

### Open and accept Workspace

From the supported browser origin, sign in as the granted subject, then use **Home → Workspace → Repository → Working copy → Indexed snapshot**. The owner issues the separate read grant from `GET /api/code/grants/choices` and `POST /api/code/grants` with its `choice_ref`; the Workspace tab performs its own handshake, `POST /api/code/contexts`, and selects the returned server-issued choice. For a first View, the selected no-View checkout yields `index_intent_selection_ref` for the UI's `POST /api/code/index-intents`; poll the returned intent until the released View appears, then select it. Confirm search, off-page graph neighbor, and source evidence without typing a binding, UUID, or direct hidden URL. Browser grants belong to the authenticated subject and source or checkout owner; the workstation keycard alone cannot grant browser access. HTTPS or a secure browser origin does not grant read authority by itself. Do not use insecure-browser flags or a synthetic admin as a substitute for an origin and identity decision. On the inspected HTTP LAN installation, `crypto.randomUUID` was unavailable before the handshake; recheck the actual chosen supported origin after rollout.

### Verify F1–F7 on the installed components

Use an authorized disposable repository or agreed test corpus. Set expected answers before querying, and record the installed component identities above with observations; keep secrets and private source out of the record. An independent operator should follow these steps without private chat guidance.

1. **F1: first use.** In a new client, discover the six code tools, get an authorized handle without SQL or manual UUIDs, and enter Workspace from Home under a supported browser identity. A missing tool, context, grant, or entry is a failed path, not a passed service check.
2. **F2: two working copies.** Give A and B different saved, uncommitted versions of one mechanism. Search and read each from its own context. Change, rename, then remove a disposable file in A. After watcher publication, verify A changes while B and the browser's previously selected View remain unchanged until switched. Reconnect a client and resolve its context again. Record measured publication and embedding latency; the watcher debounce is not an end-to-end latency promise.
3. **F3: semantic search.** Index more than 50 eligible candidates. Run exact-symbol, non-lexical conceptual (including a Russian-language query where applicable), and negative searches. Inspect the actual vector or hybrid mode, profile, source span, total and continuation; do not infer success from a nonempty first page or from lexical fallback.
4. **F4: graph and evidence.** Follow a direct and a reverse relation to an off-page neighbor, then read its stored source span and evidence type. Continue from that neighbor. Report partial, ambiguous, or unsupported relations rather than claiming the graph is complete. Do not create nodes or edges manually.
5. **F5: recovery and authorization.** In a controlled, owned environment only, stop the test daemon, submit an authorized index intent, observe queued or unavailable state, restore its owner, and verify one correct execution. Interrupt the test embedding provider and confirm a useful status with the last good View intact. Revoke a test subject's read grant and confirm that graph, source, and search no longer disclose content. Do not stop shared processes or revoke production grants for this test.
6. **F6: retired writers.** Check the [exact retired-writer matrix](#f6-retired-writer-matrix) against the installed version before and after restart with the old flags enabled. A missing menu item does not prove writer retirement; historical records and authorized readers must remain available.
7. **F7: instructions.** Ask a second operator to repeat this path from only the supported origin, approved credentials, readable repository names, and this guide. Record all hints required, dead ends, F1–F6 outcomes, limits, and exact component identities. Fix missing instructions or onboarding before claiming PASS.

### F6 retired-writer matrix

The source-candidate denominator is **5 HTTP methods + 3 MCP actions + 2 old bookmarks = 10 negative checks per start**. Compare the matrix with the actual installed server, daemon, and console before testing; source-only fixture evidence does not certify an installed release.

| Former writer or bookmark | Expected refusal |
| --- | --- |
| `POST /api/graph/nodes` | HTTP 405; no node created. |
| `POST /api/graph/edges` | HTTP 405; no edge created. |
| `DELETE /api/graph/nodes/{id}` | HTTP 405; no node deleted. |
| `DELETE /api/graph/edges/{id}` | HTTP 405; no edge deleted. |
| `POST /api/books` | HTTP 405; no Book job admitted. |
| MCP `graph` action `add_edge` | Absent from the advertised `graph.action` enum; call returns `IsError`, `unknown graph action: add_edge`. |
| MCP `graph` action `remove_edge` | Absent from the action enum; call returns `IsError`, `unknown graph action: remove_edge`. |
| MCP `graph` action `add_node` | Absent from the action enum; call returns `IsError`, `unknown graph action: add_node`. |
| Old `/graph` bookmark | No manual graph editor. |
| Old `/books` bookmark | No plaintext Book uploader. |

Repeat all 10 checks with `ENGRAM_GRAPH_ENABLED=true` and `ENGRAM_BOOKS_ENABLED=true` after restart. Record the component identities and each refusal. Retained reads include `GET /api/graph/nodes`, `/api/graph/edges`, `/api/graph/traverse`, `/api/graph/find-path`, `GET /api/books/{id}/status`, authorized `/api/documents`, `/api/documents/history`, `/api/documents/comments`, `/api/rules`, `/api/issues`, `/api/context/search`, MCP `graph` actions `get_edges`, `traverse`, `find_path`, `synonyms`, Tier2 `Traverse` in `internal/retrieval/hybrid.go`, and the separate UCI `codebase_graph`. Verify their existing ACLs, not just availability. Before transitioning residual Book jobs to `failed: interrupted by retirement`, ensure no old Book writer is live; preserve document versions, `source_book_job_id`, and partial documents. The source matrix follows `internal/worker/service.go`, `internal/mcp/tools_graph.go`, `internal/worker/handlers_graph_test.go`, `internal/worker/handlers_books_retirement_test.go`, `internal/mcp/tools_graph_t014_test.go`, and the [DA03 source receipt](../specs/011-operator-code-console/acceptance/da03-retirement-receipt.json). It is not installed acceptance evidence.

The linked DA03 source receipt records **NOT_PROVEN** for full source acceptance because its bookmark browser test selectors failed. Repeat the two bookmark checks with scoped selectors on the exact installed UI; do not convert its HTTP/MCP fixture results into a release PASS.

The Windows amd64 (`win32-x64`) parser has been built and smoke-tested as a source candidate, but its release asset is not published and the ordinary installed Windows parser is not yet certified. Linux amd64 and macOS arm64 have no parser targets in the generated package policy, even though launcher clients exist for those platforms; do not infer JS, TS, or TSX facts or graph coverage from a source build. On Windows, Go and structured text remain in the first scope; JS, TS, and TSX require a published parser bundle installed by the supported plugin path. At release, check the published asset against the `win32-x64` `asset`, `size`, and executable `sha256` in the matching package's generated `plugin/engram/parser-targets.json`, then check the installed parser's `--bundle-digest` output and daemon diagnostics for the separate compatible bundle digest. A candidate policy entry alone is not installation evidence. Full C#, Python, Vue SFC, communities, and the old manual graph or Book workflows are not promised. The source evidence manifest `tools/uci-parser/manifest.json` still marks Windows and Linux amd64 `not_claimed` and `not_reverified` for the v2 facts contract, with macOS targets blocked; do not turn candidate smoke evidence into a manifest or installed-release PASS.

### Diagnose by symptom

| Symptom | Check first |
| --- | --- |
| No Workspace entry | Inspect the installed browser bundle and candidate commit, not only the server version. |
| Binding fails before a request | Inspect browser origin, crypto capability, selected identity, and UI startup. HTTPS does not replace read grants. |
| API healthy but no code tools | Inspect the actual daemon path, plugin version, inherited `ENGRAM_CODE_INTEL_ENABLED`, and fresh `tools/list`. |
| No JS, TS, or TSX facts | Inspect the installed parser executable, bundle digest, and extraction diagnostics. Do not use a source path as a binary. |
| Search is lexical only | Inspect the chosen View's code-embedding jobs, model profile, and provider. |
| Saved changes do not appear | Inspect the owning checkout watcher, ignore rules, index job, and View publication; keep older Views intact. |
| A result contains another working copy's code | Stop relying on the result and investigate authorization and context isolation before retrying. |

Observe API and PostgreSQL readiness, daemon liveness, selected checkout and View, file coverage, embedding progress, job state, retrieval mode, and evidence limitations. `codebase_status` and the selected Workspace view are the relevant sources; memory aggregates are not code-index health. Apply updates through the supported installer or deployment path, preserve the prior components for rollback, reconnect the selected daemon, and repeat F1–F7 on the actual installed artifacts before claiming a release complete.
