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

This guide covers the current single-user `ENGRAM_AUTH_DISABLED=true` installation on its configured HTTP LAN origin. Do not use a hardcoded test address, login, browser read grant, HTTPS, or reverse proxy to make Workspace work. Auth-enabled browser access retains its separate identity and grant checks; that path is deferred here, not removed. A source-built fixture or HTTP selfcheck is not installed acceptance.

### Identify the installation before changing it

1. Record the configured server URL, server version and image digest or source commit, browser bundle, installed plugin and daemon path and version, parser executable and bundle digest, and embedding model and dimensions. Inspect installed components, not a different `engram` binary on `PATH`. Do not record secrets, database DSNs, or private source bodies.
2. At the configured server origin, check `/api/version`, `/api/selfcheck`, `/api/flags`, `/api/auth/me`, and `/api/ready`. Confirm that the effective auth mode is `auth_disabled=true`; do not infer it from a local environment file. A healthy HTTP process does not prove an indexed View or MCP connection.
3. Confirm that the installed server, console, and ordinary plugin actually contain this Workspace implementation. The previous installation's memory service and an older client can remain available separately; neither proves that the new client registered code tools. Source-candidate checks do not certify installation.

### Preserve the installed client and Vault

The no-auth Workspace path does not require an owner grant chooser or its Vault-sealed grant references. If an auth-enabled installation uses the chooser, retain its existing persistent Vault key; replicas must share that key. Replacing it also affects existing encrypted credentials. Do not expose key material in diagnostics.

The current client selects a muxcore daemon namespace from a hash of its persistent `ENGRAM_CLIENT_INSTANCE_ID`. The plugin launcher stores an installation ID in its own plugin-data directory unless an explicit ID is configured. Preserve that directory and keep the ID stable across launches; separate installs must not share an explicit ID. A pre-upgrade v6.49.3 daemon may still run in the old global `engram` namespace while the new installation's daemon runs in its own namespace. The old marker does not identify the installation that owns it. Do not assume the new launcher adopted or stopped that daemon.

For a legacy daemon that needs retirement, first identify its live control status, owner, process image, installed client path, and connected clients against the old marker. If ownership cannot be proven, leave it running and report the coexistence; do not claim exclusive installation. Only with operator authority for that exact daemon, coordinate old client disconnection and use its existing supported graceful control to shut it down. Confirm its status is no longer live and reconnect the intended new installation. Never kill a PID, delete a marker or control socket, or use an unverified process match as a substitute for ownership proof. If that platform or installation has no supported graceful control, stop and escalate retirement to its owner rather than forcing it.

### Connect the ordinary client and register a source

For a new repository, run `engram project init --name "Example Workspace"` **offline from its Git root**, then explicitly track and commit the generated V3 anchor:

```bash
engram project init --name "Example Workspace"
git add -- .engram-project
git commit -m "Initialize Engram project anchor"
```

The command reports whether `.engram-project` is tracked; it does not stage or commit it. If a working copy already has a name-only or invalid marker, do not overwrite it or invent a project ID. Existing project-scoped memories and issues are a separate history: project initialization and UCI registration do not migrate them. Keep the old memory client and its data intact. An owner-approved migration of the existing project row is separate from this new-code journey; no synthetic no-auth identity authorizes a historical backfill.

An existing Git repository with no `.engram-project` can discover the static `project_identity.register_v3` descriptor and any backend-provided unscoped `codebase_*` tools; this is not project-memory access. Registration cannot create the local anchor. For a genuinely new logical project, use the offline initialization and explicit tracking steps above, then call `project_identity.register_v3` with `{}` through the configured ordinary client. Auth-enabled registration requires a server-authenticated master/admin identity; the supported no-auth first-use path does not authorize historical-memory adoption. Do not register a fresh anchor to recover an existing project's memories: retain the old data and client, establish its historical owner and an approved migration first. Malformed, untracked, unreadable, dangling-symlink and tracked-but-deleted anchors still refuse discovery; restore or repair their existing identity through authorized onboarding rather than minting a replacement.

A staged Git deletion of a committed `.engram-project` is not an unonboarded repository. Discovery and `engram project init` refuse a replacement even when the index no longer lists the file; restore the existing accepted anchor through an authorized repair rather than generating a new UUID. An unavailable Git index or HEAD inspection also refuses initialization.

An unresolvable HEAD is not proof of a new repository. Malformed refs and a missing current-branch ref with prior HEAD/reference history refuse onboarding and offline initialization; repair the existing Git state before retrying. A genuinely unborn repository remains supported.

1. Configure the **new installed plugin** for the same bare server origin as the browser (`ENGRAM_URL` for environment-based setups; no `/mcp` suffix). With `ENGRAM_AUTH_DISABLED=true`, neither a workstation keycard nor a browser login is a prerequisite. Keep any existing authenticated-client settings separate; never copy an admin credential to a workstation. Inspect the actual launcher and daemon configuration rather than assuming a new shell's environment reached an already running daemon.
2. Check the server's effective `ENGRAM_CODE_INTEL_ENABLED` and the daemon's inherited setting. Current source enables code intelligence unless the value is exactly `false`. For JS, TS, and TSX indexing, verify that the installed daemon has a compatible parser executable and parser bundle digest; an executable file hash is not the bundle digest.
3. Start a fresh ordinary agent session using the installed plugin. Inspect its MCP `tools/list` for `codebase_context`, `codebase_index`, `codebase_status`, `codebase_search`, `codebase_graph`, and `codebase_read`. The `codebase_*` tools use UCI; `project` on a public query is compatibility evidence, not a View selector.
4. Register the local Git working copy with `codebase_context` using `{"action":"register","source_label":"my-repository","locator":"file:///absolute/local/worktree"}`. The daemon owns the local path and returns a checkout-bound `context_handle`, Source, Checkout, and profile identities. For a second worktree, use `{"action":"register","source_id":"<returned source_id>","locator":"file:///absolute/local/other-worktree"}`. Keep the returned handles distinct. Do not send local paths to the browser or invent IDs. If an older checkout lacks its durable registration profile, do not pretend a new registration restores its original identity; an eligible new Source has a distinct identity. A new checkout has no published View until indexed.

If a later client session must explicitly reselect a **published** View, use a fresh `codebase_context` list or status response. Pass the complete server-returned typed `context` at the top level of `{"action":"select",...}` (`source_id`, `checkout_id`, `view_id`, `analysis_profile_id`, `generation`, and `space_id` when present). A same-session `context_handle` also works. For the latest View of a checkout, pass its server-returned `checkout` object without `view_id`. Do not combine `checkout` with `view_id`, reuse another session's handle, or invent a UUID; the server rejects the mixed selector with `CONTEXT_MISMATCH`.

The daemon retains at most 1024 transport-session context owners using least-recently-used eviction. An evicted session's opaque handles and selected default no longer authorize requests. Start a fresh session and resolve the server-returned context again instead of reusing an old handle.

### Index and inspect an authorized View

For first indexing, use the no-View `context_handle` returned by `codebase_context` `register` in the **same client session**, then call daemon-side `codebase_status` with that handle to prepare the registered local checkout. `codebase_context` `list` only returns authorized published Views, not fresh checkouts. Do not substitute a repository path, a made-up UUID, or a handle from another client. The following `codebase_index` input uses that server-issued checkout handle:

```json
{"context_handle":"<selected checkout handle>"}
```

Index admission returns `run_id`, not a completed View. On the **daemon-side** `codebase_status`, pass that handle and the returned run ID to wait for a bounded read-your-save barrier:

```json
{"context_handle":"<selected checkout handle>","after_barrier":{"token":"<returned run_id>","wait_ms":5000}}
```

The daemon-side `codebase_status` requires a handle; follow the schema advertised by the installed plugin. Wait for a completed run and a published View, then inspect coverage, omissions, supported languages, and **code-embedding** progress until ready. Configure the shared provider with `ENGRAM_EMBEDDING_URL` as its base URL, optionally ending in `/v1`; the client calls `/v1/embeddings`. Set `ENGRAM_EMBEDDING_MODEL` to a model that returns 1536-dimensional vectors, matching the UCI vector schema. Supply `ENGRAM_EMBEDDING_API_KEY` privately only if the provider requires one. A reachable provider or memory embeddings alone do not prove code embeddings. Preserve the last good View if a job fails.

On the published View, call `codebase_search` with a conceptual query that does **not** contain the known function name, then follow a returned reference. For example, replace the query with one chosen for your corpus:

```json
{"context_handle":"<published View handle>","query":"How does this repository choose a working copy for code search?","limit":10}
```

Confirm that the conceptual `codebase_search` result reports vector or hybrid retrieval in the selected published View, with its source citation. Lexical fallback is degraded, not semantic proof. The indexer derives graph edges automatically; do not create them manually. `codebase_graph` resolves a function by name, not by the citation's chunk `entity_key`. Use the citation's `source_id` and `view_id`, and copy the function name from the cited source excerpt; if the excerpt does not show the function name, first read the cited span from the same View with `codebase_read`.

```json
{"action":"neighbors","context_handle":"<published View handle>","target":{"source_id":"<returned source_id>","view_id":"<returned view_id>","name":"<function name from the source excerpt>"},"direction":"both"}
```

Follow a direct and a reverse relation, including one neighbor not on the search page. For `codebase_read`, copy the exact `ref`, `span` (byte and line start and end), and bare 64-hex-character `content_digest` from a returned citation, along with the same View handle. Its schema requires all three objects or values. Do not read today's file from disk as a substitute for the stored View span. Evidence labeled ambiguous or heuristic does not prove a resolved call; a missing dynamic edge does not prove no dependency exists.

The JS, TS, and TSX facts/v6 parser keeps resolver-only lexical metadata separate from published references. Source references retain their 8192-site bound, and lexical facts have an independent 16384-site bound. Exhaustion reports partial coverage instead of displacing published references. Legal accessors, overloads, and merged declarations retain separate navigable occurrences without turning an ambiguous name into a resolved graph target.

Direct `eval` is classified from the parsed callee, not text inside its arguments. Parentheses such as `(eval)(code)` preserve direct eval; optional calls such as `eval?.(code)` and sequence calls such as `(0, eval)(code)` are indirect. A parameter or alias may hold intrinsic eval, so a shadowed name alone does not prove safety. In sloppy-script JavaScript, passing intrinsic eval to a parameter named `eval` still permits direct eval; strict-mode modules reject parameter bindings named `eval`. Only a proven unchanged non-intrinsic function binding avoids that conservative direct-eval treatment.

### Open and accept Workspace

Open the configured HTTP LAN console from **Home → Workspace** without logging in. Select **Repository → Working copy → Indexed snapshot**, then **Pin selected View**. If several registrations share a human-readable repository or working-copy name, use the displayed number of indexed working copies and snapshots to distinguish the published one; labels alone do not establish identity. A checkout without a published View may offer **Index checkout** only when a single fresh daemon target is available. Request indexing, refresh authorized choices, and explicitly pin the published snapshot; request acknowledgement is not completion. The server checks the no-auth Source, Checkout, and View. Keep another working copy and browser tab on their own selected Views.

A published snapshot is an immutable View. When its daemon goes offline, the authorized catalog entry, historical pin, and released structure, search, graph, and source remain readable. The server still checks durable Source-and-Checkout registration, ownership, and any required browser grant. A fresh daemon-target observation is required for bootstrap and index actions, not for published-View reads. Successful daemon-side polling of a published View renews that two-minute observation; expiry or daemon restart requires a fresh target before those actions can proceed.

Check freshness, embedding coverage and count, embedding job state, and any error reason in the selected Workspace status. **Index ready** with complete code embeddings means semantic search can be checked, but the search result's `vector` or `hybrid` retrieval mode is the proof for that query. **Semantic search not ready** means lexical search or source reading may still work; check the provider, model, and job reason before requesting reindex or reconciliation. **Updating** keeps the pinned snapshot visible; **Newer snapshot available** requires an explicit switch; a failed job does not replace the last good snapshot. Refresh status after action. Search conceptually, follow an automatically derived direct or reverse relation, inspect its evidence precision, and read the source in the **same selected View**. A relation marked ambiguous or entity-level does not prove an exact call site, and a missing unsupported relation does not prove absence. In a second tab, pin the other worktree and recheck each tab's displayed copy and snapshot after reload. If the old document lease is still active, close the old document or wait for expiry before using **Retry tab binding**; an immediate retry can still report a conflict. Never transfer a tab's binding, UUID, or handle between tabs.

An unavailable or malformed status response does not replace an already confirmed View or its released search and source results; use **Refresh status** to retry. Failed chooser refreshes release their loading state and leave **Refresh authorized choices** available. Only the latest request for the current tab binding may update choices or status, and an obsolete response cannot clear a newer request's pending state. Returning to Workspace from another console section resumes the same server tab binding and restores its historical View only after a successful status check, even if the chooser now lists only a newer snapshot. If this restoration is unavailable, contextual controls stay hidden: use **Refresh authorized choices** to retry rather than pinning the newer snapshot as a recovery step. Access denial or a context mismatch clears the pin and its saved recovery state; it cannot be restored by navigating away and back.

No-auth HTTP LAN access assumes a trusted single-user deployment; it is not an authenticated multi-user access policy. Login, read-grant issuance, HTTPS, and a proxy are not prerequisites for this configured path. An auth-enabled deployment retains its principal, grant, and tab-bound authorization requirements; do not use the no-auth path to bypass them.

### Verify F1–F7 on installed components

Use an agreed disposable repository. Set expected answers before querying, and record installed component identities without secrets or private source in the record. A second operator should be able to follow this guide without private hints.

1. **F1: first use.** From an offline Git root, initialize and explicitly commit its V3 anchor. In the ordinary new plugin client, discover six code tools and register a working copy without SQL or manual UUIDs. Enter Workspace from Home on the configured HTTP LAN origin without login or grants.
2. **F2: two working copies.** Give A and B different saved versions of one mechanism. Search and read each in its own context. Change a disposable file in A; after watcher publication, verify that A changes while B and the browser's previously selected View remain unchanged until switched. Reconnect the new client and resolve its context again. Check old memory and its older client separately; neither is the code View.
3. **F3: semantic search.** Index more than 50 eligible candidates. Wait for code embeddings to become ready. Run exact-symbol, non-lexical conceptual, and negative queries. Inspect vector or hybrid retrieval, profile, source span, totals, and continuation; lexical fallback is not semantic success.
4. **F4: graph and evidence.** Follow a derived direct and reverse relation to an off-page neighbor. Read its released evidence and exact span through the same View. If evidence is only partial or entity-level, report that limit. Do not write graph edges manually.
5. **F5: recovery.** In an owned test environment, observe queued or unavailable indexing when the daemon is offline, then restore it and verify publication. Interrupt the test embedding provider and confirm an honest error or degraded state with the last good View intact. Authentication-enabled grant revocation is a separate deferred scenario.
6. **F6: retired writers.** Check the [retired-writer matrix](#f6-retired-writer-matrix) against installed server and plugin components. A missing menu item does not prove writer retirement; historical records and permitted readers must remain available.
7. **F7: instructions.** Have another operator repeat the path from this guide on the installed components. Record dead ends, F1–F6 outcomes, and component identities before claiming installed acceptance.

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
| Workspace cannot open on HTTP LAN | Check the configured browser origin, installed console bundle, `/api/auth/me` auth mode, and browser errors. Do not substitute localhost, HTTPS, login, or a proxy. |
| No working copy appears | Check that the installed new client registered and indexed the correct Source and Checkout in the no-auth realm; inspect catalog and index state before assuming a browser grant is missing. |
| `LOCAL_CODE_CATALOG_FULL` while registering a no-auth checkout | The shared Workspace catalog supports 128 active checkouts. Existing checkout registration replays still work; take a no-longer-needed checkout offline before registering another. Do not hide catalog entries or use another Source label to bypass the limit. |
| API healthy but no code tools | Compare the server's `/api/flags` with the actual daemon path, plugin version, inherited `ENGRAM_CODE_INTEL_ENABLED`, and fresh `tools/list`. Current source is on unless exactly `false`; an older installed daemon may differ. |
| No JS, TS, or TSX facts | Inspect the installed parser executable, bundle digest, and extraction diagnostics. Do not use a source path as a binary. |
| Search is lexical only | Inspect the chosen View's code-embedding jobs, model profile, and provider. |
| Saved changes do not appear | Inspect the owning checkout watcher, ignore rules, index job, and View publication; keep older Views intact. |
| A result contains another working copy's code | Stop relying on the result and investigate authorization and context isolation before retrying. |

Observe API and PostgreSQL readiness, daemon liveness, selected checkout and View, file coverage, embedding progress, job state, retrieval mode, and evidence limitations. `codebase_status` and the selected Workspace view are the relevant sources; memory aggregates are not code-index health. Apply updates through the supported installer or deployment path, preserve the prior components for rollback, reconnect the selected daemon, and repeat F1–F7 on the actual installed artifacts before claiming a release complete.
