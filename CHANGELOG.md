# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [6.25.1] - 2026-06-17

### Fixed

- **Server init broken since v6.23.0 — migration 143 used a multi-statement Exec (#307).** Migration
  143 (`model_settings`, shipped v6.23.0) ran `CREATE TABLE` and `CREATE INDEX` in a single
  `tx.Exec`. pgx's extended protocol rejects multiple commands in one prepared statement
  (`SQLSTATE 42601`), so `runMigrations` failed and the server never completed async init on
  v6.23.0–v6.25.0 — `/api/stats` and every DB-backed endpoint returned 500. Split into a
  per-statement slice (the idiom the other multi-statement migrations already use). Forward-only;
  `IF NOT EXISTS` keeps it idempotent. Verified by running the full migration chain on a clean
  PostgreSQL+pgvector database from zero.
- **CI now runs migrations against a clean database (#307).** CI had no PostgreSQL service, so every
  `DATABASE_DSN`-gated test — including the full-chain `TestMigrationsIntegration` — was silently
  skipped, which is how the migration-143 break reached production. A new `migrations` CI job spins
  up a `pgvector/pgvector:pg17` service and runs the clean-DB migration chain on every push, gating
  the entire class of first-run migration failure before merge.

## [6.25.0] - 2026-06-17

### Added

- **One-time env→settings backfill on boot (#259, CR-4 #306).** At startup the server seeds the
  `model_settings` store from the six legacy model-config env vars (`ENGRAM_RERANK_URL` / `_MODEL` /
  `_API_KEY`, `ENGRAM_EMBEDDING_URL` / `_MODEL` / `_API_KEY`) so an operator can migrate off env vars
  without manually re-entering values they already have. For each: if the env var is set AND the
  settings key is absent, the value is written to the store (secrets vault-encrypted) and a one-time
  deprecation is logged. It never overwrites an operator-set value, so the backfill is idempotent and
  safe to run every boot. It is a boot-time data backfill, not a schema migration. Fail-soft per key —
  a store error, or a missing/failed vault for a secret, logs a warning and skips that one key without
  blocking startup (the env var still works at runtime via env-first precedence). After this ships, an
  operator can let the boot backfill seed the store, verify, then remove the env vars when ready.

## [6.24.0] - 2026-06-17

### Added

- **`settings` MCP tool — manage swappable model config, including encrypted secrets (#259, CR-3 #305).**
  A new server-global `settings` tool (actions set / get / list / delete) manages the model_settings
  store from CR-1, completing the settings-store as a usable secret-config surface. An operator can now
  set the reranker/embedder endpoint, model, and API key without a redeploy, and the server picks them up
  on the recall/init path (env still wins when set — env-first precedence). Security properties: set and
  delete require admin (operator) authentication and fail closed when auth is disabled, because settings
  are server-global and change behavior for every consumer; a key whose name ends in `.api_key` (or an
  explicit `encrypt: true`) is stored AES-256-GCM-encrypted via the existing vault; get and list never
  return secret plaintext (a secret reads back as `{encrypted: true, value_set}` only). The reranker and
  embedder now decrypt a stored API key in-process via the vault — decryption is fail-soft, so a missing
  vault, a key-fingerprint mismatch, or a decrypt error falls through to the env var or default and never
  breaks client startup. Storing a secret requires the vault key (`ENGRAM_ENCRYPTION_KEY` /
  `ENGRAM_ENCRYPTION_KEY_FILE`) to be configured on the server.

## [6.23.0] - 2026-06-17

### Added

- **Settings-store foundation for swappable model config (#259, CR-1 #303 + CR-2 #304).** New
  `model_settings` table (migration 143) is a server-global key→value store with optional
  AES-256-GCM encryption for secrets, mirroring the credentials+vault contract: plain config in
  `value`, secret ciphertext in `encrypted_value`, soft-delete clears the payload, writes are
  idempotent upserts that bump a version. The reranking and embedding clients now resolve their
  non-secret config (endpoint URL, model name) with **env-first precedence**: a set environment
  variable wins (so existing deployments are byte-for-byte unchanged and an operator can always
  override), otherwise the value comes from the settings-store, otherwise the built-in default.
  Each low-level client package declares its own minimal resolver interface so it stays
  storage-agnostic (no import cycle); the worker wires a `*SettingsStore`-backed adapter that
  serves non-secret rows only. Secret values (API keys) and the embedding dimension remain
  env-only for now — API-key resolution needs vault decryption (a follow-up CR) and the dimension
  is schema-bound to the `vector` columns. This release ships the table and the read path; nothing
  changes on a deployment until an operator writes a setting, and env always wins when set.

## [6.22.1] - 2026-06-17

### Fixed

- **Issue close wrongly rejected the true owner across slug forms (#302).** `CloseIssue` compared
  the stored `source_project` (raw, as written at creation) against the caller's `source_project`
  (already canonicalized via `ResolveProjectID`). When a project's slug/legacy-id registration
  differed between the creating and closing sessions (e.g. two sessions deriving `aimux` vs
  `aimux_<hash>` for the same git remote), the raw-vs-resolved comparison rejected the true owner
  with `only source project %q can close this issue` — even though the dashboard showed
  `aimux → aimux`. Fix: canonicalize BOTH sides through `ResolveProjectID` before comparison
  (only when authorization actually needs checking — operator-bypass and empty-source paths skip
  the lookup). Adds the first `CloseIssue` regression tests.

## [6.22.0] - 2026-06-17

### Added

- **Opt-in write-time auto-supersede for near-identical memories (rank-9, #301).** write-lint
  Phase1 already detected near-duplicates (Jaccard ≥ `DupThreshold` 0.85) and offered
  `merge_with`/`supersede`, but always suspended for a Phase2 decision — so an ignored signal
  still stored a duplicate. When `ENGRAM_AUTO_SUPERSEDE_THRESHOLD` is set above `DupThreshold`
  (e.g. 0.97), a write matching a prior at/above that threshold now stores the new memory and
  marks the best prior superseded **inline** — no token, no Phase2 round-trip — returning
  `action_taken="auto_superseded"`.
  - **Default 0.0 = DISABLED**; ships completely inert until an operator sets the env var.
    Auto-supersede is destructive (the prior is marked superseded), so it is opt-in by design.
  - Honored only strictly above `DupThreshold`, so the 0.85..threshold band stays signal-only
    (human-in-the-loop). Out-of-range/malformed env clamps to the safe default.
  - A failed `MarkSuperseded` is non-fatal (new memory already stored; leaves the old one active
    = a harmless duplicate, never data loss) and is audit-logged. Jaccard word-set match only;
    no embedding dependency, no migration.

## [6.21.0] - 2026-06-17

### Added

- **Observability: per-project citation rate + outcome-recording health on `/api/stats/vnext`
  (rank-7, #300).** Two query-only additions to the existing live stats endpoint — no migration,
  no flag, no new endpoint.
  - `project_citation_rates`: per-project `sum(citation_count)/sum(injection_count)` over live
    memories (flagged excluded; zero-injection projects omitted). This is the "is recall actually
    improving for this project?" signal — previously computable but never surfaced.
  - `outcomes`: session-outcome recording health (`total_sessions`, `unrecorded_sessions`,
    `unrecorded_fraction`, `by_outcome`). Makes outcome-signal starvation visible as a number —
    the outcome-modulated feedback (ranks 5/6) runs neutral whenever a session's outcome is
    unrecorded, and the automatic outcome path is currently inert (outcome is set only via the
    opt-in `set_session_outcome` tool). A high `unrecorded_fraction` is the at-a-glance signal.
  - Both queries degrade non-fatally; `GROUP BY` uses the raw `outcome` column (index-friendly),
    mapping NULL/'' to "(unrecorded)" in Go.

## [6.20.0] - 2026-06-17

### Changed

- **Outcome-scaled importance_base bump (rank-6 Gap 2, #299).** Session outcome already
  modulated the Thompson priors `ts_alpha`/`ts_beta`, but the `importance_base` citation bump in
  `BatchIncrementCitedN` ran at a fixed magnitude regardless of outcome. `importance_base` drives
  `ListForInjection` ordering (which memories get injected at session start) — a separate surface
  from the ts priors that rank-5 wired into retrieval. So a memory cited in a failed session was
  promoted for future injection exactly as hard as one cited in a successful session.
  - `outcomeMultipliers` gains `importanceFactor` (success 1.5, partial/neutral 1.0, failure 0.25,
    abandoned 0.0), threaded through `UpdateWithOutcome` into `BatchIncrementCitedN`.
  - The SQL scales the growth above base by the factor, monotonic-up clamped; `factor=1.0`
    reproduces the historical `base*ln(2+citation_count)` formula exactly, so default/partial
    behavior is unchanged. Adds outcome sensitivity without a permanent decrement.
  - No migration, no flag. True negative reinforcement and the demolition-dead automatic outcome
    path (outcome is recorded only via the opt-in `set_session_outcome` tool) are tracked
    separately as rank-6 Gap 1 (a product decision).

## [6.19.0] - 2026-06-17

### Changed

- **Thompson reinforcement on the recall path (rank-5, #298).** The session-end feedback loop
  maintains per-memory Thompson priors `ts_alpha`/`ts_beta` (cited → α, uncited-injection /
  violation → β, outcome-weighted), and the injection scorer already used them to prefer
  proven-useful memories at session start — but `retrieval.Score()` (what `recall_memory` ranks
  by) never read them. A memory cited many times had a high `ts_alpha` that was invisible on the
  explicit-recall path.
  - `Score()` now blends the Thompson posterior mean `ts_alpha/(ts_alpha+ts_beta)` into the
    importance signal at the same 0.3 weight the raw `citation_count/injection_count` rate used.
    The posterior is strictly more signal: Bayesian-smoothed (a single citation yields ~0.75, not
    a maxed 1.0), outcome-weighted, and violation-aware (a violated memory ranks below neutral).
  - Gated by an evidence threshold (`ts_alpha+ts_beta > 2.0`): both priors default to 1.0, so a
    no-feedback memory sits at posterior 0.5 and is left untouched rather than dragged to the prior.
  - Raw-rate fallback retained for memories whose `citation_count` predates the ts columns
    (migration 105), so no pre-ts memory loses a boost it previously had.
  - Builds on rank-2 (v6.18.0): ts increments now reflect genuine citations only, so the
    reinforcement amplifies a clean signal rather than self-citation noise. No migration, no flag.

## [6.18.0] - 2026-06-17

### Fixed

- **Anti-poisoning of the citation feedback signal (rank-2, #297).** The stop hook captures
  only assistant-role text, but when the agent echoes an injected engram block verbatim in its
  own turn, citation detection matched the memory against engram's OWN injection and falsely
  marked it cited (or violated) — inflating `citation_count` with self-citation and corrupting
  the rank-1 injection→citation feedback signal (and the rank-5/6 reinforcement built on it).
  - New `feedback.StripInjectedBlocks` removes engram-owned context from agent output before
    both `DetectCitations` and `DetectViolations`. It strips XML wrappers
    (`<engram-*>`, `<user-behavior-rules>`, `<open-issues>` — the `engram-*` arm covers any
    future tag without a code edit) and the live pre-compact markdown reinjection sentinel
    (`# Engram Re-Injection` + Topic/bullet block written to `.engram/reinjection.md`).
  - XML blocks are stripped by pairing each opening tag to its OWN matching closing tag by
    name: an unclosed/truncated echo is kept literal (cannot swallow following genuine prose),
    and a foreign closing tag inside a block is not treated as the boundary. This avoids
    false-negative citations from RE2's lack of backreferences.
  - Only the agent's own references outside the injected wrappers now count toward the feedback
    signal; a genuine reference outside an echoed block is preserved.

## [6.14.0] - 2026-06-16

### Changed

- **Unified embedding dimension on 1536 across memory and code (resolves OQ-5, #293).**
  Both `content_chunks` (memory) and `code_chunks` now use `vector(1536)`, served by a
  single embedding model (Qwen3-Embedding-8B via LiteLLM, server-side MRL `dimensions=1536`).
  Chosen over the model's native 4096 because 1536 ≤ pgvector's 2000-dim HNSW limit (native
  HNSW, no DiskANN/pgvectorscale dependency for the engram vector index), ~2.7× less storage
  and compute, and a 12-triplet EN/RU/code/cross-lingual probe showed 0 ranking inversions
  and 96.1% mean margin retention vs 4096.
  - New `EmbeddingDim` constant is the single source of truth, feeding the embedding client
    request, the backfill dimension guard, and a startup assert.
  - The embedding client now sends the OpenAI `dimensions` param (default `EmbeddingDim`;
    `ENGRAM_EMBEDDING_DIMENSIONS` override accepts only `EmbeddingDim` or `0`=omit; any other
    value is clamped with a warning so it can never request a column-incompatible size).
  - Startup `AssertEmbeddingDimensions` reads the live column type via `format_type` and
    disables the embedding path on drift between the DDL, the GORM tag, and `EmbeddingDim`.
  - `StoreChunks` guards vector length at the single write chokepoint; memory recall queries
    carry `embedding IS NOT NULL` so the partial HNSW index is usable.

### Migration

- **Migration 142** moves `content_chunks.embedding` 4096→1536: DELETEs existing chunk rows,
  ALTERs the column, and replaces the DiskANN index with a native partial HNSW index, wrapped
  in a transaction for atomicity. **Operator action required after deploy:** the memory corpus
  re-embeds automatically via the backfill (recall degrades to FTS-only until it completes);
  verify `dimension=1536` via `get_memory_stats`. Drop the stale `ENGRAM_EMBEDDING_DIMENSIONS=4096`
  from the deploy template (leave 1536 or unset). See the re-embed runbook for details.

## [6.13.1] - 2026-06-16

### Fixed

- **Codex plugin load failure: stray `description` key in `hooks/hooks.json`.** The shared
  hook manifest carried a top-level `description` field, which Claude Code accepts as optional
  but Codex's stricter manifest parser rejects (`unknown field 'description', expected 'hooks'`),
  so Codex failed to load any engram hooks. Removed the cosmetic field — the manifest is now
  `{ "hooks": {...} }`, satisfying both harnesses. The same metadata already lives in both
  `.claude-plugin/plugin.json` and `.codex-plugin/plugin.json`, so nothing is lost.

## [6.13.0] - 2026-06-16

### Added

- **Code intelligence — engram replaces SocratiCode (CI-A absorption track, #286–#291).**
  A complete client-smart / server-thin code-search subsystem, shipped across six CRs and
  gated behind `ENGRAM_CODE_INTEL_ENABLED` (flag-OFF is byte-identical to before — no new
  MCP tools advertised). When enabled, engram exposes three MCP tools that make it a drop-in
  SocratiCode replacement:
  - `codebase_index` (daemon-side) — walks the local project tree, chunks it, and streams the
    delta to the server over gRPC; returns immediately with a `run_id` (async background run,
    per-project single-flight guard).
  - `codebase_search` (server-side) — hybrid FTS + dense-vector search over the indexed code
    with Reciprocal Rank Fusion; FTS-only in V1 until a code-embedding model is configured.
  - `codebase_status` — daemon-side run liveness merged with server-side chunk counts.

  Building blocks landed by the chain:
  - **CR-001 (#286):** `code_chunks` table (migration 139) — `project_id` git-slug scoping,
    `vector(1536)` embedding (native pgvector HNSW), generated `content_tsv` for BM25 — plus
    the GORM model and `CodeChunkStore`.
  - **CR-002a (#287):** pure-Go (CGo-free) line-fallback chunker + manifest builder
    (`internal/codeindex`) with minified-file and binary guards.
  - **CR-003 (#288):** gRPC `CodeIndexNegotiate` (delta) + `CodeIndexUpload` (client stream),
    with a `code_index_sessions` authorization table (migration 140) so the stale-chunk sweep
    is correct across the two-RPC split, including the delete-only re-index case.
  - **CR-004 (#289):** server-side embedding backfill (`internal/embedding/code_backfill.go`)
    that fills `code_chunks.embedding` from an external embed API; disables itself on a
    persistent embedding-dimension mismatch rather than hot-looping.
  - **CR-005 (#290):** hybrid code search (`SearchCodeFTS` + `FindSimilarCode` + RRF reuse in
    `internal/retrieval/code_hybrid.go`) with a benchmark gate and a dense-only fallback;
    the `code_chunks` HNSW index is partial on `embedding IS NOT NULL` (migration 141).
  - **CR-006 (#291):** the three MCP tools above + the `ENGRAM_CODE_INTEL_ENABLED` flag wiring
    across the daemon module (`internal/handlers/codeintel`) and the server.

  Code intelligence stays inert until `ENGRAM_CODE_INTEL_ENABLED=true` AND a 1536-dim embed
  endpoint is configured; `code_chunks` rows are created with a NULL embedding and search
  degrades to BM25/FTS-only until the embeddings exist.

## [6.9.0] - 2026-06-16

### Added

- **`DELETE /api/rules/{id}` — delete a behavioral rule via the API (#257).** No external route
  existed to retire a behavioral rule; the operator had to use SQL or the dashboard. The new
  endpoint soft-deletes a rule by id (reusing the existing `BehavioralRulesStore.Delete`):
  `400` on a non-numeric/≤0 id, `404` when no active rule has that id, `{"deleted": id}` on
  success. It sits behind the same `tokenAuth` middleware as `DELETE /api/memories|issues` —
  no new authorization surface. This is the operator-chosen "legitimate path" for retiring noisy
  always-inject rules (e.g. the global rule id=50 NO-MANUAL-DRIFT noise flagged in #257/#287).

## [6.8.0] - 2026-06-16

### Added

- **Embedding/retrieval telemetry as a first-class server surface (#275).** `/api/stats/vnext`
  now carries an `embedding` sub-object: `chunk_count`, `memories_with_chunks`, `last_chunk_at`,
  `model`, `dimension`, `embedding_coverage` (% of active memories with ≥1 chunk, divide-by-zero
  guarded), `active_memory_count`, `embed_success_count` / `embed_failure_count` since process
  start, and `last_embed_error {at, status_code, message}`. A 404/401/dimension-mismatch is now
  one `GET` away instead of a psql session or a log-dive. Backed by a new mutex-guarded
  `BackfillRecorder` that surfaces the backfill loop's counters. The block is omitted when the
  embedding store is not wired (flag off / `ENGRAM_EMBEDDING_URL` unset).
- **muxcore daemon-registry opt-in (#290).** Bumped `muxcore` v0.25.0 → v0.26.1 and opted the
  engram engine into cross-engine discovery (`engine.Config.Registry`, `ListOwners` only). The
  shared mcp-mux operator point can now discover the engram daemon via `mux_engines` /
  `mux_list(engine_name:"engram")`. Read-only — no cross-engine stop/restart/update is advertised.

### Fixed

- **Security: PEM private-key bodies left in plaintext by `RedactSecrets` (#263).** Patterns were
  applied sequentially against an evolving string, so the header-only PEM pattern rewrote the
  `BEGIN` line first and the full-block pattern then failed to match — leaving the key body + END
  footer unredacted. Redaction is now a two-pass span-collection over the immutable original
  (overlaps resolved so the full-block span subsumes the header-only one; replacements applied
  right-to-left). Non-overlapping secrets redact byte-identically to before.
- **Race between watcher `handleDeletion` / re-watch goroutine and `Stop()` (#262).** Added a
  `w.running` guard under the mutex at `handleDeletion` entry and inside the 500ms re-watch
  goroutine after its sleep, so a debounce timer or delayed goroutine can no longer call `Add`
  on a closed fsnotify watcher. Covered by `-race` tests with concurrent `Stop()`.
- **Stale "vectors permanently removed in v5" health message (#273).** `check_system_health` now
  reports the pgvector subsystem with flag-aware active/dormant status (`Removed:false`) instead
  of the stale v5 strip-down message.

### Changed

- **Provenance-cleanup FR-4 contract-honesty tail — epic NVMD-ENG-1 closed (#281).** Removed the
  `by_file` dead-enum from the `recall` tool (the standalone `find_by_file` alias stays as an
  accurate US3 tombstone); deleted the orphaned `ObservationRelation` / `ObservationConflict`
  structs (0 callers); reworded a stale "in v5" user-facing string; fixed a session-start render
  duplication (#287, `narrative === title` guard); and rewrote the plugin memory `SKILL.md` from
  the pre-v5 ~50-tool taxonomy to the current v5 8-tool surface.

## [6.7.2] - 2026-06-16

### Changed

- **Quiet mode is now "tacit, not mute" — `ENGRAM_QUIET` silences only automatic
  injection, not the whole plugin (#280).** Previously quiet short-circuited every
  hook routed through `RunHook`, including the capture/learning hooks. That made
  the memory write-only while quiet: no session-start noise, but also no
  crystallization (`Stop`), no outcome propagation (`SessionEnd`), and no
  correction/segment signal capture (`UserPromptSubmit`). On a prod with vNext
  active, `get_memory_stats.vnext` showed `injection_count:0 / citation_count:0` —
  the learning loop had nothing to record because quiet had silenced the capture
  side too. The quiet gate now keys on hook role: only the hooks that PUSH context
  into the prompt — `SessionStart`, `PreToolUse`, `PreCompact` — are silenced. The
  capture/learning hooks run under quiet and emit no prompt context (each returns
  an empty result), so the prompt stays exactly as quiet as before while engram
  keeps recording signals and crystallizing lessons. Quiet now stops engram
  talking, not learning. Plugin/setup descriptions of quiet mode updated to match.
  (Note: the citation signal inherently needs injection to measure "did the agent
  use what was injected", so `citation_count` stays 0 under quiet — full
  closed-loop-while-quiet needs prompt-driven recall, tracked as #257.)

## [6.7.1] - 2026-06-16

### Fixed

- **provenance-cleanup migration 137 crash-loop on non-empty prod tables.** v6.7.0
  shipped migration 137 with a per-table row-count guard that aborted the migration
  (and thus the whole startup migration chain) if any of the 8 observation-era
  tables exceeded a hardcoded allowance (0, or 12 for concept_weights). On the
  production database those tables were not empty (observation_conflicts held ~35.7k
  orphaned rows from before the CR-1 rewire), so the guard aborted and the server
  crash-looped on init ("refusing to drop observation_conflicts — it holds 35763
  rows"). The 9 observation-era tables are demolition debt outside the keep-set
  ({issues, memories, vault/credentials, api_tokens}) and are removed regardless of
  row count, so the guard blocked the very operation it guarded. Migration 137 now
  drops unconditionally (matching 138), inside a transaction. No keep-set data is
  touched. (NVMD-ENG-1)

## [6.7.0] - 2026-06-15

### Added

- **ENGRAM_QUIET hook kill-switch.** A new quiet mode suppresses all
  hook-injected context (behavioral rules, reinjection hints, session-start
  output) while keeping the MCP tools working, so a workstation can run
  zero-hint sessions during development. Activate via `ENGRAM_QUIET` /
  `ENGRAM_QUIET_HOOKS` (Claude Code, also exposed as the `engram_quiet` plugin
  option), or via `"quiet": true` in `~/.engram/config.json` for Codex ≥0.139,
  which forwards no environment to hook children. A present (non-empty) env var
  decides outright (truthy mutes, falsey forces active); an absent env var falls
  back to the config-file key. Scope boundary: quiet silences hook *context
  injection* only — it does not disable the MCP daemon (`ensure-binary` still
  bootstraps it). The quiet branch also clears any stale
  `.engram/reinjection.md` so the out-of-band `@`-import hint channel does not
  replay old content. (#276)

### Changed

- **provenance-cleanup epic (NVMD-ENG-1), CR-0 through CR-5.** Demolition of the
  v5 observation-era schema debt behind CI guardrails:
  - CR-0: known-debt baseline guardrails (schema-integrity, provenance-lint,
    tombstone-lint, DATA_MODEL drift) that fail on any *new* drift toward the
    demolished state. (#271)
  - CR-1: citation/injection sink unified on `injection_log`; legacy
    `observation_injections` left with zero live Go readers/writers. (#272)
  - CR-2a: removed the relation/version/reasoning reader + store surface. (#274)
  - CR-4: `Observation` struct marked deprecated; `audit_log.memory_id` gained a
    foreign key to `memories(id)` with `ON DELETE SET NULL` (migration 136), and
    project-level audit events now store `NULL` instead of `memory_id = 0`. (#275)
  - CR-5: reworded stale "removed in vN" tombstone comments that named live
    tables; tombstone-lint baseline reaches empty.

### Fixed

- **FTS recall on over-specified multi-word queries.** `SearchFTS` AND-ed every
  term, so a long multi-word query returned nothing; it now falls back to an OR
  match when the AND pass is empty and there are ≥2 terms, preserving quoted
  phrases and skipping the fallback for negation (`-term`) queries. (#281)

## [6.6.1] - 2026-06-14

### Added

- **Agent-accessible telemetry via MCP.** The `get_memory_stats` MCP tool (and
  `admin` action `stats`) previously returned an empty `{}` stub. It now returns
  real server-side telemetry the agent can read through its per-workstation MCP
  keycard — no admin token, no workstation secret: `memory` (counts by status +
  total), `vnext` (injection/citation/uncited counts + noise_ratio, gated on
  `ENGRAM_VNEXT_ENABLED`), `embedding` (chunk_count, memories_with_chunks,
  last_chunk_at, model, dimension — reusing `embedding.Store.Stats`), and
  `candidates` (pending/total, under `ENGRAM_VNEXT_F_ENABLED`). Each section is
  independently guarded — telemetry never errors when a subsystem is nil or off.
  This puts the closed-loop learning signal (citation rate, noise ratio) and the
  embedding write-verification (chunk_count) directly in the agent's hands. (#269)

## [6.6.0] - 2026-06-14

### Added

- **LLM-based crystallization (FR-7 Milestone D).** Crystallization now extracts
  decisions/lessons from session transcripts via an LLM, **language-independent** —
  decisions stated in Russian, Chinese, or any language are captured and stored in
  their original language with a `lang:<code>` tag. This replaces the previous
  English-only regex extractor (a deliberate Milestone-A stub) that captured nothing
  for non-English work (measured EN=2/RU=0/ZH=0). (#268)
  - **Async dream-cycle.** Session transcripts are persisted (redacted) at session-end
    to a new `session_transcripts` table (migration 135); an async job riding the
    existing sleep-cycle tick reads unprocessed transcripts, builds an adaptive
    per-session/per-batch digest, extracts decisions via the LLM, and routes them to
    crystallization candidates. Runs only when idle (≥4h) with new activity (≥10 memories).
  - **`internal/llm`** — OpenAI-compatible chat client (external API only, no
    server-side inference). `ENGRAM_LLM_URL` / `ENGRAM_LLM_MODEL` / `ENGRAM_LLM_API_KEY`
    (Bearer auth when key set). When `ENGRAM_LLM_URL` is unset the dream-cycle is a no-op.
  - **`ENGRAM_TRANSCRIPT_RETENTION_DAYS`** (default `0` = no prune of unprocessed
    transcripts; processed rows are always pruned on the dream-cycle tick).
  - Structural-loss check guards against lossy candidate supersession.

### Removed

- **Legacy English-only regex decision extractor** (`decisionPatterns` /
  `ExtractDecisions`) and the synchronous session-end `runCrystallization` path —
  replaced entirely by the LLM dream-cycle. (#268)

### Notes

- Feature-gated by `ENGRAM_CRYSTALLIZATION_ENABLED` (flag-OFF is byte-identical to
  pre-feature: no transcript writes, no dream-cycle, no new goroutines). Candidate
  routing additionally requires `ENGRAM_VNEXT_F_ENABLED=true`. To activate, provide
  an external OpenAI-compatible chat endpoint and set the `ENGRAM_LLM_*` vars.

## [6.5.2] - 2026-06-14

### Fixed

- **Embedding base-URL normalization.** Operators who set `ENGRAM_EMBEDDING_URL`
  with a trailing `/v1` (e.g. a LiteLLM proxy URL like `https://host/v1`) caused
  the client to build `https://host/v1/v1/embeddings`, producing repeated
  `HTTP 404 {"detail":"Not Found"}` errors on every embedding batch. The client
  now parses the URL and strips exactly one trailing `/v1` path segment via
  `net/url.Parse` (operating on the path, not a string suffix, so a host that
  ends in `v1` is left intact). Both `https://host` and `https://host/v1` now
  resolve to the same correct endpoint. (#267)
- **Stale "removed in v5" vector health.** `check_system_health`,
  `handleVectorHealth`, `handleVectorMetrics`, and the MCP `vectorHealth` tool
  reported pgvector as permanently removed — a stale message from the v5 HTTP-MCP
  removal. They now report live pgvector status, gated on `ENGRAM_VNEXT_ENABLED`.
  `handleVectorMetrics` additionally distinguishes transient startup state from a
  permanently disabled/failed embedding store after readiness. (#267)

### Added

- **Server-side embedding telemetry.** `EmbeddingStats` (chunk count, memories
  with chunks, last-chunk timestamp, model, dimension) is exposed via
  `Store.Stats(ctx)` and surfaced in `/api/stats/vnext` and the vector-metrics
  endpoint. Operators can verify embeddings are being written by querying the
  server instead of running manual `psql` — health checks belong in the server.
  (#267)

## [6.5.1] - 2026-06-14

### Added

- **Embedding endpoint authentication.** The embedding client now sends
  `Authorization: Bearer <key>` when `ENGRAM_EMBEDDING_API_KEY` is set. When the
  key is empty no header is sent, preserving key-less LAN endpoints byte-identically.
  This unblocks keyed OpenAI-compatible embedding endpoints (e.g. a LiteLLM proxy
  fronting Nebius). Required for the vector tier of hybrid retrieval under
  `ENGRAM_VNEXT_ENABLED=true`. (#266)

### Fixed

- **docker-compose / `.env` environment-name drift.** `docker-compose.yml` set env
  names the server does not read (`ENGRAM_EMBEDDING_BASE_URL`/`MODEL_NAME`,
  `ENGRAM_API_TOKEN`), so embedding and admin-token configuration supplied via
  compose never reached the running server. Corrected to the names the Go code
  actually reads (`ENGRAM_EMBEDDING_URL`/`MODEL`/`API_KEY`,
  `ENGRAM_AUTH_ADMIN_TOKEN`); removed dead `ENGRAM_LLM_*`/`FALKORDB_*` keys.
  Rewrote `.env.example` and `docs/arch/CONFIGURATION.md` to the real contract
  (vault key documented as 64-hex, not base64; `DATABASE_DSN` override now
  interpolated in compose). (#266)

### Changed

- **Upstream decoupling U4–U8 (behavior-preserving).** Contract-driven rewrites
  of the remaining upstream-attributed code: docs site + UI composables (U4/U6),
  install/uninstall/register scripts + goreleaser/golangci/CI config (U5/U7),
  worker handlers, watcher, SDK, config, privacy, chunking, models, static, main,
  clustering, and the LICENSE copyright line (U8). All changes preserve observable
  behavior; existing tests pass unchanged as the contract net. Blame on
  upstream-authored logic reduced to zero. (#256–#265)

## [6.5.0] - 2026-06-12

### Added

- **Milestone F — TG1: Privacy scopes (4-tier).** Memories now carry a
  `privacy_scope` field with four levels: `private`, `project`, `shared`,
  `global`. Retrieval paths enforce scope visibility when
  `ENGRAM_VNEXT_F_ENABLED=true`. Migration: `125_privacy_scope_addition`.

- **Milestone F — TG2: Knowledge node taxonomy (13 types).** The knowledge
  graph extended with typed nodes: `concept`, `entity`, `process`, `decision`,
  `pattern`, `skill`, `tool`, `artifact`, `role`, `constraint`, `goal`,
  `event`, `unknown`. The `graph` MCP tool gains an `add_node` action and
  `node_type` filter on `get_edges` when `ENGRAM_VNEXT_F_ENABLED=true`.
  Migrations: `126_knowledge_nodes_table`, `127_edge_discriminators`.

- **Milestone F — TG3: Explainable rerank + ranking rationale.** `recall_memory`
  (and the legacy `recall` tool under `ENGRAM_VNEXT_ENABLED=true`) now returns
  an optional `rationale` object per result with fields: `recency_days`,
  `confidence`, `citation_count`, `tier`, `substring_match`,
  `filters_applied`. Pass `explain=true` to activate. Store-level
  `ListWithFilters` added (`confidence_min`, `include_superseded`, `limit`).

- **Milestone F — TG4: Crystallization candidates.** Memories that pass the
  crystallization gate are staged as candidates before promotion. New MCP tools
  (active when `ENGRAM_VNEXT_F_ENABLED=true`): `list_candidates`,
  `promote_candidate`, `reject_candidate`, `supersede_candidate`. Migration:
  `132_crystallization_candidates`.

- **Milestone F — TG5: Write-lint two-phase protocol.** `store_memory` with
  `ENGRAM_VNEXT_F_ENABLED=true` runs a two-phase write-lint check before
  committing: Phase 1 returns conflict signals and a resolution token; Phase 2
  accepts the token to complete the write. Includes a redaction middleware layer
  controlled by `ENGRAM_REDACTION_RULES_PATH`. `dry_run=true` support added to
  `store_memory`, `promote_candidate`, and all bulk ops.

- **Milestone F — TG6: Governance operations.** Bulk-op snapshots for
  rollback/audit, export/import bundles (ZIP format with SHA-256 manifest),
  dry-run flag across all write paths, and configurable snapshot retention.
  New MCP tools (active when `ENGRAM_VNEXT_F_ENABLED=true`): `list_snapshots`,
  `rollback_snapshot`, `pin_snapshot`, `redaction_rules_status`. Bulk tools:
  `bulk_promote`, `bulk_delete`, `bulk_supersede`. Migration:
  `133_bulk_op_snapshots`, `134_bulk_op_snapshots_jsonb_checks`.

- **W2: Crystallization pipeline wiring.** Session-end crystallization fires
  when `ENGRAM_CRYSTALLIZATION_ENABLED=true`. Audit trail wired into
  create/delete/supersede/edit paths and sleep cycle promotion/demotion. Audit
  log 90-day retention in `runRetentionCleanup`.

- **W2: `purge_project` admin operation.** `admin(action="purge_project")`
  deletes all memories, candidates, edges, and vectors for a project with
  double-entry confirmation. Active when `ENGRAM_VNEXT_ENABLED=true`. Migration:
  `130_source_workstation_id`.

- **W3: Hybrid retrieval read path.** `recall_memory` and `recall(action="similar")`
  under `ENGRAM_VNEXT_ENABLED=true` use a three-tier hybrid pipeline: Tier 0
  exact text match, Tier 1 vector (FindSimilar), Tier 2 FTS (SearchFTS), fused
  with Reciprocal Rank Fusion (RRF) and FR-C4 recency/confidence scoring.
  Clock-skew fix: all DB timestamp comparisons now use SQL `NOW()` instead of
  Go-side timestamps.

- **FR-E2: Claude Code adapter reference documentation.** `docs/arch/` gains the
  `claude-code-adapter` reference guide. Dead `PostCompact` MCP tool registration
  removed.

- **U1–U3: Upstream decoupling.** Contract-driven test rewrites (9 suites
  across two batches, U1), behavior-preserving rewrites of upstream Go
  infrastructure (U2: update, session manager, gorm store, sdk processor,
  service fragments), and `pkg/models` rewrites (U3). Dead `pkg/models/scoring.go`
  deleted (zero callers).

### Changed

- **`admin` tool schema is flag-aware.** When `ENGRAM_VNEXT_ENABLED=false`
  (default), the `admin` tool description and schema are byte-identical to
  pre-v6.5.0 (no `purge_project`, no `confirm` field). The extended schema
  activates only when `ENGRAM_VNEXT_ENABLED=true`.

- **`recall(action="similar")` forwards to hybrid path under flag.**
  With `ENGRAM_VNEXT_ENABLED=true`, `similar` translates `min_similarity` to
  `vec_threshold` and delegates to `recall_memory` with `tier_filter=tier1_vector`.
  Flag-OFF behavior is byte-identical to v6.4.15.

- **Tier default changed to `episodic`.** New memories (INSERT without explicit
  tier) now default to `tier='episodic'` per spec FR-B2. Existing rows are not
  rewritten. Migration: `131_tier_default_episodic`.

- **`recall_memory` gains `tier_filter` param** (under `ENGRAM_LIFECYCLE_ENABLED`).
  Invalid values return a structured `invalid_tier_filter:` error.

### Fixed

- **SSE broadcast: wedged-client panic and blocking.** A client whose write
  stalled past the 2s write timeout could panic the broadcaster (send on
  closed channel) or block every subsequent broadcast for all clients.
  Dead-client reports now flow through a buffered never-closed channel;
  `Broadcast` returns within the write timeout and stalled clients are
  removed on the next broadcast pass.

- **Clock-skew in `List` / `SearchFTS` / `GetByIDs`.** Queries comparing
  memory timestamps now use `NOW()` (database time) instead of `time.Now()`
  (Go server time), removing false recency ordering when server and DB clocks
  diverge.

- **Synchronous self-update backup rollback.** The self-update path now rolls
  back the backup atomically on error rather than leaving a partial state.

- **`outFile.Close` error on success path.** The file-write helper now checks
  the `Close` error even when the write itself succeeded, preventing silent
  data loss on flush failure.

- **W4: Privacy-scope bypass closure (P1–P3).** Three paths that could return
  memories above the caller's scope ceiling were closed under
  `ENGRAM_VNEXT_F_ENABLED=true`: session-start injection, direct store list,
  and FTS search.

## [6.4.15] - 2026-06-10

### Added

- **Config file credential fallback for Codex ≥ 0.139 and other env-hostile
  harnesses.** Codex 0.139 stopped forwarding `[shell_environment_policy.set]`
  values to plugin MCP server children (openai/codex#24401 — no supported
  replacement for plugin MCP servers). The plugin wrapper (`run-engram.js`)
  and hooks (`lib.js`) now resolve `ENGRAM_URL` and `ENGRAM_TOKEN` from a
  JSON config file as the final fallback in the credential chain:
  `$ENGRAM_CONFIG_FILE` → `<pluginData>/config.json` →
  `~/.engram/config.json`. File format: `{"server_url":"...","api_token":"..."}`.
  On POSIX the file is created/chmod'd 0600; on Windows NTFS user-profile ACLs
  suffice. Token values are never logged. The startup diagnostic line now
  reports `config_file=present(<path>)` or `config_file=missing(<path>)`.

### Changed

- `session-start.js` now calls `lib.getEngramConfig()` (the shared resolver
  with config file fallback) instead of its own `configureRuntimeEnv()`.
- Codex setup docs rewritten: `~/.engram/config.json` is the documented path;
  `shell_environment_policy.set` is noted as legacy/broken for plugin MCP
  servers and retained only for Codex < 0.139.
- FATAL error messages for missing URL/token now include the config file path
  that was checked, so users know the new option without consulting docs.

## [6.4.14] - 2026-06-10

### Fixed

- **Codex plugin MCP server failing with MODULE_NOT_FOUND.** Codex does not
  interpolate `${CLAUDE_PLUGIN_ROOT}` in plugin `.mcp.json` args — the literal
  string reached node and the wrapper never started
  (`Cannot find module '...\${CLAUDE_PLUGIN_ROOT}\scripts\run-engram.js'`).
  The plugin now ships per-consumer MCP configs: the root `.mcp.json` (used by
  Codex) launches the wrapper via a plugin-root-relative path resolved against
  `cwd: "."`, while `.claude-plugin/plugin.json` points Claude Code at
  `claude/.mcp.json`, which keeps the `${CLAUDE_PLUGIN_ROOT}` interpolated
  entrypoint that Claude Code requires (Claude Code does not resolve relative
  args against the plugin root).

## [6.4.13] - 2026-06-10

### Fixed

- **Claude Code plugin MCP server silently not spawning.** The plugin
  `.mcp.json` interpolated `${user_config.*}` inside the `env` block, which
  makes Claude Code skip spawning the MCP server with no error
  (anthropics/claude-code#51573). The env block is removed; the wrapper and
  hooks now read userConfig values through the documented
  `CLAUDE_PLUGIN_OPTION_<KEY>` plugin-subprocess environment variables, with
  explicit `ENGRAM_URL`/`ENGRAM_TOKEN` env still taking precedence.

## [6.4.12] - 2026-06-04

### Fixed

- **Codex remote tool surface behind blocked proxy environments.** The local
  MCP daemon now disables gRPC proxy use for Engram backend connections, so
  Desktop Codex sessions connect directly to `ENGRAM_URL` instead of falling
  through a host/Codex proxy route such as `127.0.0.1:9`. This restores the
  full remote `tools/list` surface after plugin startup.

## [6.4.11] - 2026-06-04

### Fixed

- **Muxcore stale daemon owner detection.** The local MCP daemon marker now
  records the executable path in addition to version and PID, and startup treats
  same-version markers from a different `engram.exe` path as stale. This
  prevents release-smoke or old plugin-data daemons from keeping the global
  muxcore owner slot after a plugin reinstall.

## [6.4.10] - 2026-06-03

### Fixed

- **Codex plugin startup diagnostics.** The MCP wrapper now writes a redacted
  startup environment line to stderr and `pluginData/logs/startup-env.log`, so
  operators can prove whether `ENGRAM_URL` and `ENGRAM_TOKEN` reached the
  plugin-provided MCP child without exposing the token.
- **Codex binary freshness on Windows.** `ensure-binary.js` no longer treats a
  current installed binary as stale when Windows denies Node's piped
  `--version` probe with `EPERM`. It still rejects marker mismatches before the
  fallback, preserving the stale-binary guard added in v6.4.7.

## [6.4.9] - 2026-06-03

### Fixed

- **Repo-local Codex marketplace source.** Added a tracked
  `.agents/plugins/marketplace.json` for local Codex marketplace installs from
  the engram repository root. The local marketplace now uses the same
  `engram-marketplace` marketplace name as the published marketplace so local
  testing installs `engram@engram-marketplace` instead of creating a second
  `engram@engram` identity with stale plugin data.
- **Version bump for marketplace pickup.** Bumped the Claude and Codex plugin
  manifests plus the daemon version source to `6.4.9` so consumer plugin caches
  do not treat the marketplace correction as already installed content.

## [6.4.8] - 2026-06-03

### Fixed

- **Codex plugin MCP launch root.** The Codex MCP config now launches
  `scripts/run-engram.js` through Codex's plugin-root interpolation instead of
  an inline `node -e` bootstrap that fell back to the workspace `cwd` when
  `PLUGIN_ROOT` was not present in the MCP process environment. This fixes
  Desktop Codex startup failures where the plugin cache was updated but MCP
  initialize closed before a response because the wrapper was resolved from the
  project directory.
- **Daemon version source alignment.** The source `internal/version.Daemon`
  default now matches the v6.4.8 plugin manifests, with a regression test to
  prevent future `serverInfo.version` drift from the shipped plugin version.

## [6.4.7] - 2026-06-03

### Fixed

- **Codex plugin binary freshness after reinstall.** The plugin installer now
  verifies the existing `engram` client binary with `engram --version`, not
  only `bin/.version`, before deciding it is current. This forces a refresh
  when Codex has installed the new plugin cache slot but plugin data still
  contains a stale `v6.4.5` binary, preventing repeated `connection closed:
  initialize response` failures after an apparent update.

## [6.4.6] - 2026-06-03

### Fixed

- **Codex MCP entrypoint execution.** Fixed the marketplace `.mcp.json`
  bootstrap so the `node -e` entrypoint actually invokes
  `scripts/run-engram.js` instead of only importing it. In `6.4.5`, Desktop
  Codex could install the correct cache slot but the MCP subprocess exited
  before answering `initialize`, producing `connection closed: initialize
  response`.
- **Stale embedded muxcore daemon after client upgrades.** The local daemon now
  records the muxcore daemon version beside the muxcore control socket, and the
  client shim stops a missing-version or mismatched-version daemon before
  connecting. This prevents a newly installed binary from reusing an older
  persistent muxcore daemon and replaying stale MCP `initialize.serverInfo`
  data such as `v5.0.0` while the visible binary reports `v6.4.5+`.
- **Release binary initialize smoke.** The release-binary workflow now checks
  the Linux release asset's MCP `initialize.serverInfo.version`, not only
  `engram --version`, so version-source drift blocks publication.
- **Entrypoint regression coverage.** Added a Node regression test that runs
  the exact `.mcp.json` eval command against a temporary wrapper and verifies
  the wrapper `main()` function executes.

## [6.4.5] - 2026-06-03

### Fixed

- **Codex plugin launch path from marketplace cache.** Added `cwd: "."` to the
  shared MCP entry so Desktop Codex runs the bootstrap from the installed
  plugin root instead of the agent workspace, allowing the wrapper to resolve
  `scripts/run-engram.js` reliably from `plugins/cache/.../engram/<version>`.
- **Codex plugin data directory inference.** The MCP wrapper now infers Codex's
  plugin data directory from the installed cache layout when `PLUGIN_DATA` is
  not provided, so `ensure-binary.js` can install and refresh the versioned
  `engram` client binary before the MCP handshake.
- **Hook testability.** Plugin hook modules now guard CLI execution with
  `require.main === module`, preventing `node --test` from hanging while
  importing hook files for regression coverage.

## [6.4.4] - 2026-06-03

### Fixed

- **Codex plugin MCP startup configuration.** Added a native Codex plugin
  manifest and a validator-compatible MCP entrypoint so Desktop Codex can
  launch Engram with `ENGRAM_URL` and `ENGRAM_TOKEN` from the Codex shell
  environment policy instead of inheriting Claude-only `userConfig` wiring.
  The shared wrapper now also accepts Claude userConfig values through
  separate compatibility variables, preserving the Claude install path while
  making Codex fail fast with actionable setup errors when required
  workstation credentials are missing.
- **Codex plugin release packaging.** GoReleaser archives now include the
  Codex manifest alongside the Claude manifest, and the release-binary workflow
  verifies the Linux client binary reports the tagged version before uploading
  assets. This keeps plugin installs from pointing at a version whose client
  binary was never published.
- **Marketplace sync after binary publication.** The plugin marketplace sync
  workflow now also runs after a successful `Release Binary` workflow and can
  be dispatched manually, avoiding the GitHub Actions `GITHUB_TOKEN` recursion
  trap where a release created by the release workflow does not trigger the
  downstream `release: published` marketplace workflow.

## [6.4.3] - 2026-06-02

### Fixed

- **Local MCP daemon owner identity with muxcore v0.25.0.** Updated
  `github.com/thebtf/mcp-mux/muxcore` from `v0.24.3` to `v0.25.0`, adopting
  the provider-side `ModeGlobal` default, cross-cwd admission hardening, and
  stale isolated-owner cleanup behavior released for Engram #244
  (`mcp-mux process explosion`). This patch targets the stale-owner/process
  multiplication class observed during Desktop Codex Engram attach failures.
  The Desktop tool-registry snapshot miss remains a separate client-side
  failure mode when direct stdio JSON-RPC to `engram.exe` is healthy.
- **Daemon version reporting.** Centralized the local daemon version used by
  `--version`, MCP `initialize.serverInfo.version`, and the gRPC
  `InitializeRequest.ClientVersion` path so release smoke tests and client
  diagnostics report `v6.4.3` consistently.
- **Fresh PostgreSQL migration startup.** Fixed JSON custom type annotations
  and PostgreSQL boolean predicates so a clean PostgreSQL 17 database can pass
  the fresh GORM AutoMigrate + migration chain used by the release playbook.

## [6.4.2] - 2026-05-23

### Fixed

- **Dialog text color in light theme (#184).** Four portal-based content
  components — `DialogContent`, `AlertDialogContent`, `DialogScrollContent`,
  and the `sheetVariants` cva base in `components/ui/sheet/index.ts` — only
  declared `bg-background` without the matching `text-foreground`. Because
  `DialogPortal` teleports content out of the App tree, the
  `body { @apply text-foreground }` cascade was not always inherited; the
  user-agent default color (commonly white) won the cascade and produced
  white-on-white in light theme. Affected paths included the Delete-Issue
  confirm dialog, Tokens dialog, Vault dialogs, and any other
  `Dialog` / `AlertDialog` / `Sheet` consumer.

  Fix aligns with the shadcn-vue convention already used by
  `DropdownMenuContent`, `DropdownMenuSubContent`, and `SelectContent`
  (paired `bg-popover text-popover-foreground`). No dark-mode regression —
  `--foreground` resolves to the light token there, identical to the
  inherited color.

  Closes engram-internal issue #184. PR #220.

## [6.4.1] - 2026-05-23

### Fixed

- **`scripts/install.sh` archive download (broken since v5.2.5).** The
  `Release` GitHub Actions workflow has been failing on every release
  since v5.2.5 (8 consecutive failures across v5.2.5 / v6.0.0 / v6.0.1 /
  v6.1.0 / v6.2.0 / v6.2.1 / v6.3.0 / v6.4.0) because
  `scripts/generate-plugin-config.sh` tried to copy
  `plugin/.claude-plugin/marketplace.json` — a path deleted in `653fabb`
  (#151) when the marketplace metadata was reshaped. With the GoReleaser
  before-hook failing under `set -e`, the bundled per-platform archives
  (`engram_${VERSION}_${PLATFORM}.tar.gz` / `.zip`) were never published,
  so anyone running
  `curl -sSL https://raw.githubusercontent.com/thebtf/engram/main/scripts/install.sh | bash`
  got a 404. v6.4.1 drops the dead `cp` line and publishes the archives
  the install script expects. See TD-008 in `TECHNICAL_DEBT.md` and
  PR #219 for the full investigation trail.
  (`release-binary.yml` continued to publish bare per-platform binaries
  throughout — only the bundled `install.sh`-consumed archive layer was
  missing.)

## [6.4.0] - 2026-05-23

### Added

- **v7 cognitive platform substrate (CR-001).** New `pkg/cognitive` public
  package and `internal/cognitive/core` substrate package introduce a plug
  architecture for future cognitive subsystems (S1-S6). All v6 behaviour is
  preserved when `ENGRAM_V7_PLUG_ENABLED` is unset. The substrate ships in
  an opt-in / NoOps-only state: enabling the master flag activates 5 NoOp
  subsystems behind per-subsystem flags, but no production behaviour
  changes until a real subsystem (S1+) lands in a future release.
  - `pkg/cognitive` exports 6 cross-subsystem interfaces (`AttentionEventSource`,
    `CandidateProposer`, `HintEmitter`, `StateWriter`, `AttentionEventWriter`,
    `DirectiveDistiller`), 10 payload types, and `NormalizeForDiff` (FR-9
    byte-identity gate helper).
  - `internal/cognitive/core` exports `SubsystemRegistry` (8 methods),
    `AttentionEventBus`, `HintQueue`, `SubsystemMeter`, `ProductMetricsProvider`,
    8 DTOs, `SubsystemDispatcher` with PR-5 panic isolation, and 5 NoOp
    subsystem implementations.
  - `worker.Service` is wired with 4 explicit cognitive fields
    (`cognitiveRegistry`, `cognitiveMeter`, `cognitiveQueue`, `cognitiveBus`)
    plus the lifecycle handle for the hint queue.
- **`/api/stats/v7/*` HTTP endpoints** (subsystems / substrate / product),
  protected by a positive-whitelist auth gate
  (`SourceClient` / `SourceSession` allowed; `SourceMaster` → 403;
  no identity → 401). The endpoints expose registry state, meter snapshots,
  and product-metric provider output when a provider is registered.
- **`make rebaseline-v6` target** for the worktree-based v6.3.0 baseline
  capture pipeline (FR-9 byte-identity gate scaffold).
- **Per-subsystem flag activation.** `ENGRAM_V7_PLUG_ENABLED=true` plus
  per-subsystem flags (`ENGRAM_V7_S1_STATE`, `ENGRAM_V7_S2_METAMEM`,
  `ENGRAM_V7_S3_AMBIENT`, `ENGRAM_V7_S4A_DIRECTIVES_CAPTURE`, ...) gate
  the corresponding NoOp into the `enabled` state. Master flag off keeps
  every NoOp at `registered` and dispatch returns nothing — exactly the
  v6.3.0 behaviour byte-for-byte.

### Fixed

- **Makefile cross-platform targets** built `./cmd/worker` which does not
  exist; corrected to `./cmd/engram-server` across `worker`,
  `build-linux`, `build-darwin`, `build-windows`, plus the swag init
  reference. Cross-platform builds also now apply `$(BUILD_TAGS)`
  consistently with the default `build` target.

### Scope notes

- **FR-9 release validation** ships gate **machinery** (idempotent
  `NormalizeForDiff` + synthetic v6.3.0 baseline fixtures + worktree
  `make rebaseline-v6`). The real v6.3.0 byte-identity proof is tracked
  as **TD-004** and is a release-cutover prerequisite for the eventual
  v7.0.0 major bump. v6.4.0 is intentionally NOT a v7 cutover.
- Additional deferred items: TD-003 (Variant A pre-plug benchmark),
  TD-005 (`ObserveHistogram` O(n) eviction), TD-006 (`foldKey` tag value
  escaping), TD-007 (concurrent Enable/Disable same-name race). All LOW
  severity, none reachable on current code paths.

### Internal

- 30 squashed commits (T001-T021 across SG-1 Foundation, SG-2 Substrate,
  SG-3 Panic-resilient ops, SG-4 FR-9 gate machinery, plus 4 PM/CodeRabbit
  review fix-forward cycles). PR #218.

## [6.0.0] - 2026-04-26

### BREAKING CHANGES

- **Two-tier token model.** The daemon and plugin no longer accept the
  operator key on workstations. Each workstation now reads `ENGRAM_TOKEN`
  — a per-workstation API token (worker keycard) issued via the dashboard
  `/tokens` page. The operator key (`ENGRAM_AUTH_ADMIN_TOKEN`) lives ONLY
  on the server host.
- **Plugin `.mcp.json` env rename.** `ENGRAM_AUTH_ADMIN_TOKEN` →
  `ENGRAM_TOKEN`. No legacy fallback chain — pre-v6 configurations stop
  working until re-issued.
- **Daemon fail-fast on missing token.** When `ENGRAM_URL` is set but
  `ENGRAM_TOKEN` is empty, the daemon writes a single user-actionable
  stderr line and exits 1. Replaces the previous silent graceful-degrade
  to `loom_*-only` (which masked PR #203's regression for days).
- **Issuance hardening.** `POST/GET/DELETE /api/auth/tokens` and
  `GET /api/auth/tokens/{id}/stats` now require an admin browser session
  cookie. Bearer-token authentication (operator key OR worker keycard) is
  rejected with 403. CI scripts that previously minted keycards via the
  master token MUST migrate to a browser session.

### Migration steps for existing users

1. Update the plugin via your marketplace mechanism (`/plugin update
   engram@engram` or equivalent).
2. Open `<your-server-url>/tokens` in a browser, log in as admin.
3. Click "Generate token", name it after this workstation, scope
   `read-write`, copy the value once.
4. Run `/engram:setup`, paste the new keycard. Update
   `~/.claude/settings.json` to set `ENGRAM_TOKEN` and remove any leftover
   `ENGRAM_AUTH_ADMIN_TOKEN` / `ENGRAM_API_TOKEN` entries.
5. Restart Claude Code. The daemon now exits loudly on misconfig.

### Added

- `internal/auth` package — single source of truth for token validation
  shared by HTTP middleware AND gRPC interceptor (FR-2). Two-strategy
  chain (master constant-time compare + prefix-and-bcrypt keycard lookup).
- gRPC `streamAuthInterceptor` covers `ProjectEvents` stream (US5).
- Connection-pool keying by credential hash in `engramcore/grpcpool.go`
  (FR-7). Two workstation sessions with distinct keycards no longer share
  a pooled gRPC connection.
- `internal/config/envnames.go` — single source of truth for env-var names.

### Removed

- Legacy `ENGRAM_API_TOKEN` env-var read paths (none remaining anywhere
  in the codebase). FR-5 / ADR-004 prohibits fallback chains.
- Inline bcrypt + prefix-lookup in `internal/worker/middleware.go` —
  superseded by `auth.Validator` delegate.

## [5.0.0] - 2026-04-23

### Added

- **Static session-start gRPC flow (US13)**: added `GetSessionStartContext` and `NegotiateVersion` to the gRPC API, worker compatibility endpoint `/api/context/session-start`, and hook-side local cache fallback at `${ENGRAM_DATA_DIR}/cache/session-start-{project-slug}.json`.

### Changed

- **Static-only product direction**: Engram now treats explicit writes and deterministic reads as the primary product contract. Session-start inject is simplified to issues + behavioral rules + memories.
- **Version compatibility signaling**: session-start path now performs explicit major-version negotiation instead of silently tolerating client/server skew.
- **Plugin/daemon release alignment**: plugin version `5.0.0` and daemon version `v5.0.0` are released together.

### Removed

- `internal/search` package deleted (search.Manager, RRF, MMR, LLM filter, search metrics)
- `internal/search/expansion` package deleted (HyDE query expansion, Expander)
- `recall` MCP tool reduced to trivial SQL filter (memoryStore.List + in-memory substring)
- Dropped MCP tools: `search`, `timeline`, `decisions`, `changes`, `how_it_works`, `find_by_concept`, `find_by_type`, `get_recent_context`, `get_context_timeline`, `get_timeline_by_query`, `explain_search_ranking`
- patterns subsystem
- graph subsystem
- learning / scoring / maintenance / consolidation loops
- reranking / embeddings-era runtime stack
- `ENGRAM_HYDE_ENABLED` env var removed
- `ENGRAM_LLM_FILTER_ENABLED` env var removed

### Breaking

- observations-era dynamic runtime is no longer the primary storage/retrieval model
- session-start uses static composite payloads rather than the old dynamic inject path
- mixed major client/server versions must fail with an explicit compatibility error on the session-start path

### Notes

- Release notes: `docs/release-notes/v5.0.0.md`

## [3.7.1] - 2026-04-12

Post-MVP stabilization: hotfixes, reconciliation, and feedback import.

### Fixed

- **Session outcome identity handling**: finalize canonical session ID resolution for outcome propagation (`21c7f69`)
- **Context by-file project scoping**: enforce project parameter in `/api/context/by-file` to prevent cross-project observation leakage (`c166179`)
- **Hit-rate markdown reparsing**: remove unnecessary markdown parsing in learning hit-rate analytics (`c5f1e81`)
- **Nil command arrays serialization**: serialize nil command arrays as empty JSON arrays instead of null (`3d9178d`)
- **Missing commands_run migration**: add migration for commands_run column in observations table (`213e562`)

### Added

- **Server-side feedback import**: new `POST /api/import/feedback` endpoint + CLI HTTP client for bulk importing historical feedback data. New `cmd/engram-import/main.go` entry point. (`3ab74d6`, `e5a7e60`)

## [v4.x-in-progress] - 2026-04-11

Learning Memory v4 post-MVP feature wave. This entry tracks the FRs shipped after the v3.7.0 MVP foundation and before the final v4 polish/staging sign-off.

### Shipped FRs

- **FR-4 File-scope prefiltering**: inject/search can now narrow observation retrieval to files currently being edited. Added file-path support to `BuildWhereFilter`, tracked edited files in hook-side session signals, and passed `files_being_edited` through inject/search flows.
- **FR-5 Per-type search lanes**: retrieval can now use type-specific `(min_score, top_k, reranker_weight)` lanes when `ENGRAM_TYPE_LANES_ENABLED=true`, allowing guidance/pitfall, decision, wiki/entity, and default classes to rank differently.
- **FR-6 Project briefing**: per-project synthesized briefing lookup/generation and inject wiring are now present behind `ENGRAM_PROJECT_BRIEFING_ENABLED`.
- **FR-7 Alarm model expansion**: file alarm model now covers semantic Edit/Write trigger matching, Bash command prefix warnings, and repeated-Read path context via `/api/memory/triggers` plus hook-side merged rendering.
- **FR-8 Write-time merge decision**: `DecideMerge` is wired into the observation ingest path with `CREATE_NEW`, `UPDATE`, `SUPERSEDE`, and `SKIP` handling. The supersede path now keeps the old observation active until the replacement insert succeeds.
- **FR-8a Contradiction kill-switch**: `ENGRAM_CONTRADICTION_DETECTION_ENABLED` lets operators disable the old supersede path and fall through to `CREATE_NEW`.
- **FR-8b Wrong-supersede audit artifacts**: `.agent/reports/wrong-supersede-audit.md` and `.agent/reports/restore-candidates.sql` capture the known-bad supersede IDs for operator review.
- **FR-9 Entity-seeded graph traversal**: inject path can derive entity seeds from the current session and fuse graph-neighbor observations with vector results through `search.RRF` when `ENGRAM_INJECT_GRAPH_BFS_ENABLED=true`.

### Memory correction

- The previous memory shorthand "stop hook unreliable" is no longer accurate enough and has been corrected.
- The important distinction is: **Claude Code `Stop` was the wrong lifecycle point for realtime outcome propagation; `SessionEnd` is the correct hook for graceful session exit, and the server periodic recorder remains the backup path.**
- Memory index and memory note were updated to reflect this correction so future sessions do not keep repeating the old oversimplified claim.

## [3.7.0] - 2026-04-11

Learning Memory v4 MVP -- empirically-driven rebuild of the retrieval path after baseline
metrics showed 2164 feedback records with 0 positive / 0 negative citations and 20 noise
candidates with 0 high-value observations. The relevant-memory injection was broadcasting
guidance rules that agents never cited. v4 repairs the foundation before adding features.

Full spec set: `.agent/specs/learning-memory-v4/` (spec.md, roadmap.md, tasks.md, baseline-metrics.md,
challenge-report.md, hook-lifecycle-findings.md).

### Fixed (breaking changes, migration path below)

- **Injection floor anti-pattern removed (FR-1)**: `InjectionFloor` default changed from 3 to 0.
  Previously, when composite scoring eliminated every candidate, the code force-filled the
  response with top-importance observations regardless of query relevance. This made the
  relevance threshold cosmetic. Silence is now a legitimate result.
  Files: `internal/config/config.go`, `internal/worker/handlers_context.go`, new `internal/worker/floor_fill.go` helper.
  Migration: operators who relied on always-non-empty responses can set `ENGRAM_INJECTION_FLOOR=3`.
  Commits: `5e1a56c` (T006), `fbd4da0` (T007).

- **LLM filter silence gate (FR-2)**: when `LLMFilterEnabled=true` and the LLM explicitly
  returns an empty set meaning "nothing is relevant", the code previously overrode this
  with "top-5 composite scoring fallback". The LLM said silence; the code injected noise.
  Now the empty set is honored and an Info log line marks the silence event.
  Error/timeout fallback (return all candidates) is unchanged -- only the intentional
  empty-set path changed.
  Files: `internal/search/llm_filter.go`, `internal/worker/handlers_context.go`.
  Commits: `f52b0d4` + `1a01310` + `77bbd41` (T008), `a02c586` + `14f7921` (T009).

- **Hardcoded inject query replaced (FR-3)**: `handleContextInject` relevant section no longer
  uses `query := project + " code development"`. Injection now routes through
  `RetrieveRelevant`, the same pipeline that user-prompt search uses -- hybrid search,
  composite scoring, LLM filter, adaptive threshold, deduplication. Query is derived from
  the last user prompt for the session, falling back to project name for cold starts.
  New `retrievalHooks` extension point prepared for future F5 typed lanes and F8 BFS phases.
  Files: new `internal/worker/retrieval.go`, new `internal/worker/retrieval_helpers.go`,
  `internal/worker/handlers_context.go`.
  Commits: `d9a3c42` (T010), `4b6c999` (T011).

- **MCP `set_session_outcome` bypass fixed**: `internal/mcp/tools_learning.go` previously
  called only `UpdateSessionOutcome` without triggering `PropagateOutcome`. Utility scores
  were never updated from MCP-initiated outcome signals. Now mirrors the HTTP endpoint's
  goroutine-based propagation. Also wires `SetInjectionStore` on the MCP server.
  Commit: `7342ec3` (T019).

### Added

- **Realtime outcome propagation via SessionEnd hook (FR-5)**: engram's `hooks.json` now
  registers `SessionEnd`, which fires during Claude Code `gracefulShutdown()` with a 1.5s
  budget (SIGINT/SIGTERM/`/exit`/`/clear`). A new `plugin/engram/hooks/session-end.js`
  posts to the new endpoint `POST /api/sessions/{id}/propagate-outcome` fire-and-forget
  with a 1200ms client timeout (300ms headroom under Claude's 1500ms cap).
  `PropagateOutcome` updates `utility_score` for all injected observations in the session
  within seconds of session exit, instead of hours via the maintenance cycle.
  Maintenance `recordPendingOutcomes` remains as crash-proof fallback (catches sessions that
  missed graceful shutdown via SIGKILL, uncaught exceptions, etc.) and skips sessions that
  were already propagated within the last 2 hours.
  The previous CONTINUITY note "stop hook unreliable, never fires" was based on a
  misunderstanding: `Stop` fires per-turn, not at session exit. That is what `SessionEnd`
  is for. engram's `hooks.json` had simply never registered `SessionEnd`.
  Files: new migration `072_sessions_utility_propagated_at`, new handler in
  `internal/worker/handlers_learning.go`, new file `plugin/engram/hooks/session-end.js`,
  `plugin/engram/hooks/hooks.json`, `internal/maintenance/service.go`.
  Commits: `345efcb` (T014), `9fb0b3b` (T015), `6c266de` (T016), `bd46ca6` (T017), `f60a241` (T018).

- **`ENGRAM_INJECT_UNIFIED` rollback flag**: emergency escape hatch to revert to the
  legacy hardcoded-query path (default true; set false for rollback). To be removed
  after two release cycles once the unified path is proven in production.
  Commit: `c69a51b` (T012).

- **Inject latency benchmark script**: `scripts/bench-inject.sh` runs 100 HTTP calls against
  `/api/context/inject` and reports p50/p95/p99 to `.agent/reports/f1-latency-delta.json`.
  Baseline comparison deferred until production p99 is captured (tracked as T005 in the spec).
  Commit: `8856864` (T013).

- **6 new integration tests** covering the unified inject path:
  `TestInjectRelevant_UnifiedPath_UsesLastUserPrompt`,
  `TestInjectRelevant_UnifiedPath_FallsBackToProjectName`,
  `TestInjectRelevant_TwoSessionsDifferentPrompts` (anti-stub proof),
  `TestInjectRelevant_LegacyPath_WhenFlagFalse`,
  plus 2 config tests (`TestInjectUnifiedDefaultTrue`, `TestInjectUnifiedEnvOverride`).
  Plus 3 LLM filter tests (`EmptyResponseSilencesInjection`,
  `ParseFailureFallsBackToAllCandidates`, `TimeoutFallsBackToAllCandidates`).
  Plus 5 floor-fill tests covering silence and backward-compat paths.

### Config

- `ENGRAM_INJECTION_FLOOR` -- default changed **3 -> 0** (breaking if you relied on the floor)
- `ENGRAM_INJECT_UNIFIED` -- new, default **true** (rollback flag)

### Schema

- Migration `072_sessions_utility_propagated_at`: `ALTER TABLE sessions ADD COLUMN IF NOT EXISTS utility_propagated_at TIMESTAMPTZ`. Idempotent.

### Plugin

- Plugin version bumped to **3.7.0** across all three manifests (`plugin/engram/.claude-plugin/plugin.json`, `plugin/openclaw-engram/package.json`, `plugin/openclaw-engram/openclaw.plugin.json`).
- New hook file: `plugin/engram/hooks/session-end.js`.
- `hooks.json` registers `SessionEnd` with 1500ms timeout.

### Empirical baseline (pre-v4)

Captured before the v4 code changes for regression detection. See `.agent/specs/learning-memory-v4/baseline-metrics.md`.
- 2164 feedback records, **100% neutral** -- no user or heuristic ratings ever registered as positive or negative
- **20 noise candidates** with 10+ injections and 0 citations; **0 high-value candidates**
- Top 10 most-retrieved observations: all `guidance` type, 888-1038 retrievals each, 0 citations
- 30-day corpus: 1438 observations (decision 44.5% / discovery 33.6% / guidance 9.0%)
- Near-dedup total merges: 0 (periodic dedup dormant)

### Post-shipment validation

Validation protocol per spec.md sectionValidation Protocol. After deployment:
1. Re-run `admin(action="hit_rate")` and compare noise/value counts to baseline
2. Run a live session with `/exit` and verify `utility_propagated_at` updates within 3s
3. Check inject silence rate: `% of sessions with 0 relevant observations injected` -- target 40% acceptable
4. Watch `learning_llm_calls_total` to ensure LLM filter does not spike cost

## [3.4.1] - 2026-04-10

### Fixed

- **Issues tool not discoverable**: `issues` tool was registered in secondary tools list but not in `primaryTools()`, so `tools/list` never returned it. Agents could not see or use the issues tool. Now included in primary tools (9 consolidated tools).
- **MCP instructions missing issues**: `buildInstructions()` described "7 Tools" without mentioning issues. Updated to "8 Tools" with dedicated Issues section, workflow examples, and anti-pattern guidance ("Do NOT use store or docs for issues").
- **PATCH /api/issues/{id} missing reopen support**: Handler only accepted `status=resolved`. Added `status=reopened` which calls `ReopenIssue` -- needed for openclaw-engram REST-based reopen.

### Added

- **`include_all` parameter for tools/list**: `tools/list` with `include_all: true` or `cursor=all` returns all 50+ expanded tools alongside primary tools. Default remains primary-only for context efficiency.
- **openclaw-engram issues tool**: New `engram_issues` tool with 6 actions (create, list, get, update, comment, reopen) via REST API. Includes client methods, Zod validation, TypeBox schema.
- **Plugin memory skill updated**: Issues section added to `plugin/engram/skills/memory/SKILL.md` with when-to-use guidance.

## [3.0.0] - 2026-04-06

### Added

- **Learning Memory** -- engram now learns from every session which observations are useful
  - **Citation signal wiring**: stop hook detects which injected observations were referenced by the agent (via existing `detectUtilitySignal`), sends citation data to new `POST /api/sessions/{id}/mark-cited` endpoint. `PropagateCitation` updates effectiveness_score per-observation: cited = +0.03, uncited = -0.01.
  - **Observation enrichment**: user prompts stored server-side as context for tool calls. `BuildObservationPrompt` now includes `<user_intent>` tag -- extraction LLM sees WHY the agent acted, not just WHAT it did.
  - **Mid-session extract-learnings**: PreCompact hook sends last 20 messages (4000 token budget) to extract-learnings endpoint. Reliable trigger (replaces unreliable stop hook). Idempotent.
  - **Contradiction detection on write** (Mem0 Algorithm 1 adapted): cosine >= 0.92 = NOOP (near-duplicate), 0.75-0.92 = UPDATE (supersede with EVOLVES_FROM), < 0.75 = ADD. Synchronous, ~3-5ms.
  - **Adaptive per-project threshold**: maintenance Task 20 reads citation rates from injection_log, adjusts relevance threshold +- 0.05 per project. Bounds [0.15, 0.60]. Window: 50 sessions.
  - **Migration 066**: `cited` BOOLEAN column on injection_log with composite index

### Changed

- Store response now includes `action` field (ADD/UPDATE/NOOP) and `superseded_id` when applicable

## [2.5.0] - 2026-04-06

### Added

- **Minimum Viable Learning Loop** -- first production system to close the retrieve -> measure -> adjust -> re-retrieve feedback loop
  - Bayesian effectiveness multiplier in `ApplyCompositeScoring`: `(successes + 1) / (injections + 2)`. No minimum injection gate.
  - Project-only vector search: removed `includeGlobal=true` from 3 context search call sites
  - Project filter on `GetAlwaysInjectObservations`
  - Client min similarity filter > 0.10 in user-prompt.js

## [2.4.1] - 2026-04-06

### Added

- **Stronger MCP instructions**: exclusivity claim ("Your ONLY Persistent Memory"), mandatory AFTER workflow

### Changed

- PostToolUse hook matcher narrowed `*` -> `Write|Edit|Bash|Agent|mcp__aimux` (~50+ fewer node process spawns)
- Behavioral rules de-duplicated (session-start only, removed from user-prompt.js)
- Documentation rewrite (README, CHANGELOG, translations)

## [2.4.0] - 2026-03-29

### Added

- **LLM-Driven Memory Extraction** (ADR-005): `store(action="extract", content="...")` -- agent dumps raw content, LLM extracts structured observations autonomously
- Each extracted observation: type, title, narrative, concepts (from 20 valid concepts)
- Privacy: content redacted via `privacy.RedactSecrets` before LLM call
- Returns: `{extracted, stored, duplicates, titles}`

## [2.3.1] - 2026-03-29

### Added

- **Embedding Resilience Layer** (ADR-004): independent circuit breaker for embeddings
- 4 health states: HEALTHY -> DEGRADED -> DISABLED -> RECOVERING
- Background health check goroutine (30s probe interval)
- Automatic recovery within 60s of API returning
- Selfcheck reports embedding status with failure counts

## [2.3.0] - 2026-03-29

### Added

- **Reasoning Traces -- System 2 Memory** (ADR-003): structured reasoning chains (thought->action->observation->decision->conclusion)
- Quality scoring (0-1) via LLM evaluation -- only traces >= 0.5 stored
- Auto-detection of reasoning patterns in tool events
- `recall(action="reasoning")` retrieves past reasoning by project
- `reasoning_traces` database table with session/project indexes

## [2.2.1] - 2026-03-29

### Fixed

- P1+P2 findings from 13-area investigation report
- Summary observation fallback when assistant message empty
- userPrompt fallback threshold lowered 50->10 chars
- Circuit breaker recovery logging
- BeforeToolCallResult type added to OpenClaw HookResult
- Missing concept keywords backfill migration

## [2.2.0] - 2026-03-28

### Added

- **Server-side periodic summarizer** (maintenance Task 19): sessions summarized automatically, no client hook dependency

### Fixed

- Pre-edit guardrails: guidance rules no longer shown as warnings
- Removed broken client-side summarizer from session-start.js

### Changed

- Summary generation moved from client to server

## [2.1.9] - 2026-03-28

### Added

- Dashboard search miss handling with frequency display
- Session views with date filtering and min_prompts filter

### Fixed

- Search miss API response unwrapping (miss_stats envelope)
- Session list filtering (min_prompts, from, to query params)

## [2.1.8] - 2026-03-28

### Added

- Dashboard UX polish: tooltips on observation cards, cursor-pointer, hover transitions, color coding by type

## [2.1.7] - 2026-03-28

### Added

- Dashboard pattern insights view with LLM-generated descriptions
- Background pattern insight generation (maintenance Task 18, 5 per cycle)
- Session detail view with metadata, observations, injections

### Fixed

- Summary content built from observations when no transcript available

## [2.1.6] - 2026-03-28

### Added

- Knowledge graph local mode (per-observation neighborhood view)
- Graph node search functionality
- Visual styling improvements for graph visualization

## [2.1.5] - 2026-03-28

### Added

- "Sessions Today" counter on dashboard (replaced always-0 "Active Sessions")
- Consistency check endpoint: `GET /api/maintenance/consistency`
- `memory_get` import bridge: read file AND store as observation

## [2.1.4] - 2026-03-28

### Added

- Config hot-reload: atomic config swap via `config.Reload()`, no process restart needed

## [2.1.3] - 2026-03-28

### Added

- Pre-edit guardrails hook (recall by_file before file modifications)
- Session summarization on session start
- Statusline hook: learning effectiveness metric with 60s cache

## [2.1.2] - 2026-03-28

### Added

- 4 user slash commands: `/retro` (session analysis), `/stats` (memory analytics), `/cleanup` (observation curation), `/export` (data export)

## [2.1.1] - 2026-03-28

### Fixed

- Dashboard concept filter (JSONB @> operator replaces LIKE)
- Dashboard type filter
- Hardcoded observation/prompt counts replaced with real API data

## [2.1.0] - 2026-03-28

### Changed

- **MCP Tool API Consolidation**: 61 tools -> 7 primary tools (recall, store, feedback, vault, docs, admin, check_system_health)
- >80% context window reduction (~6100 -> ~900 tokens per session)
- All 61 original tool names work as backward-compatible dispatch aliases
- Updated MCP server instructions for consolidated API
- 6 new router files for action-based dispatch

## [2.0.9] - 2026-03-28

### Added

- OpenClaw plugin expanded from 8 -> 17 tools with lifecycle hooks
- Tool descriptions include trigger conditions
- Stop hook: switched to retrospective injection API
- Statusline: learning effectiveness metric

### Fixed

- `engram_decisions` uses dedicated endpoint
- `memory_forget` defaults to suppress (reversible)
- Suppress action: RowsAffected check + cache invalidation
- Session outcome recording uses Claude session ID string

### Changed

- Removed 7 redundant MCP tool registrations (68 -> 61)

## [2.0.8] - 2026-03-28

### Added

- Session injection retrospective API (`GET /api/sessions/:id/injections`)

### Fixed

- Effectiveness distribution excludes never-injected observations

## [2.0.7] - 2026-03-27

### Note

Releases v0.9.0 through v2.0.7 included incremental improvements to search quality, scoring algorithms, session indexing, knowledge graph, and infrastructure. See [GitHub Releases](https://github.com/thebtf/engram/releases) for detailed per-version notes.

## [0.5.1] - 2026-03-08

### Added

- **MCP instructions** -- `buildInstructions()` returns comprehensive usage guide for all 48+ tools on `initialize` -- any MCP client instantly knows how to use engram
- **Marketplace auto-sync** -- GitHub Actions workflow syncs `plugin/` to `thebtf/engram-marketplace` on push to main

### Fixed

- **Observation extraction in Docker** -- replaced Claude CLI dependency (`claude --print`) with OpenAI-compatible LLM API (`ENGRAM_LLM_URL`). Observation pipeline was completely non-functional in Docker deployments where Claude CLI is not installed.
- **MCP panic recovery** -- added panic recovery with zerolog logging in Streamable HTTP handler
- **FalkorDB int64 panic** -- convert int64 to int for falkordb-go ParameterizedQuery params
- LLM client URL normalization -- handles both `http://host:port` and `http://host:port/v1` formats
- LLM client fallback env var -- now correctly reads `ENGRAM_EMBEDDING_BASE_URL` (was `ENGRAM_EMBEDDING_URL`)
- Configurable LLM concurrency (`ENGRAM_LLM_CONCURRENCY`), timeout, and retry with backoff for transient errors
- Reranking API key optional for TEI/direct backends; batch size configurable via `ENGRAM_RERANKING_BATCH_SIZE`

### Changed

- Plugin version bumped to 0.5.1

## [0.3.0] - 2026-03-07

### Added

- Collection MCP tools: `list_collections`, `list_documents`, `get_document`, `ingest_document`, `search_collection`, `remove_document` -- YAML-configurable knowledge bases with smart chunking
- `import_instincts` MCP tool -- import ECC instinct files as guidance observations with semantic dedup
- Unified document search integration -- `search` tool now includes document results when `type="documents"` or empty
- Per-session utility signal detection for self-learning

### Fixed

- AI review findings for collection tools and instinct import

### Changed

- README complete documentation rewrite

## [0.2.0] - 2026-03-07

### Added

- HTTP logs endpoint (`/api/logs`)
- JavaScript plugin hooks replacing Go binaries -- simpler deployment, no build needed

### Fixed

- Increase embedding timeout for high-dimension models
- Setup command now edits `settings.json` instead of OS environment variables
- Downgrade SDK processor log from Warn to Debug
- Skip session indexing when directory does not exist

## [0.1.0] - 2026-03-07

Initial release with full feature set.

### Added

- **Core Memory System**
  - PostgreSQL 17 + pgvector storage with HNSW cosine vector index
  - Hybrid search: tsvector GIN + vector similarity + BM25, RRF fusion
  - Cross-encoder reranking (ONNX or API-based)
  - BM25 short-circuit optimization for strong text matches
  - HyDE query expansion with template fast path and LLM fallback

- **MCP Server (44 tools)**
  - Search & Discovery (11): hybrid search, timeline, decisions, changes, concept/file/type filters
  - Context Retrieval (4): recent context, timeline views, pattern detection
  - Observation Management (9): CRUD, tagging, merging, bulk operations
  - Analysis & Quality (11): stats, quality scores, trends, scoring breakdowns
  - Sessions (2): full-text session search, listing with filters
  - Graph (2): neighbor traversal, graph statistics
  - Consolidation & Maintenance (3): decay, associations, forgetting

- **Knowledge Graph**
  - 17 relation types: causes, fixes, supersedes, contradicts, explains, shares_theme, etc.
  - In-memory CSR graph traversal
  - Optional FalkorDB integration with async dual-write
  - Graph-augmented search expansion after RRF fusion

- **Memory Consolidation**
  - Relevance decay (daily): exponential time decay with access frequency boost
  - Creative associations (daily): embedding similarity discovery
  - Forgetting (quarterly, opt-in): archives low-relevance observations
  - Stratified sampling and EVOLVES_FROM relation

- **Scoring System**
  - Importance scoring: type-weighted with concept, feedback, retrieval, utility bonuses
  - Relevance scoring: decay x access x relations x importance x confidence
  - Belief revision: telemetry, provenance tracking, smart GC

- **Session Indexing**
  - JSONL parser with workstation isolation
  - Composite key: `workstation_id:project_id:session_id`
  - Incremental indexing

- **Self-Learning**
  - Guidance observation type with context partitioning
  - Utility tracking infrastructure
  - Utility signal detection in hooks
  - LLM-based learning extraction

- **Embeddings**
  - Local ONNX BGE (384D) provider
  - OpenAI-compatible REST API provider
  - Tiered vector indexing (DiskANN for dims > 2000)

- **Infrastructure**
  - Single-port server (37777): HTTP API + MCP SSE + MCP Streamable HTTP
  - MCP stdio proxy for clients that only support stdio
  - Docker deployment with docker-compose
  - GitHub Actions CI: Docker image publishing to ghcr.io
  - Claude Code plugin with marketplace support
  - `/engram:setup` command with doctor diagnostics
  - Token-based authentication for all endpoints
  - Context injection optimization with compact format and token budgeting
  - Install scripts for macOS/Linux

### Attribution

Originally based on [claude-mnemonic](https://github.com/lukaszraczylo/claude-mnemonic) by Lukasz Raczylo.

[Unreleased]: https://github.com/thebtf/engram/compare/v6.5.0...HEAD
[6.5.0]: https://github.com/thebtf/engram/compare/v6.4.15...v6.5.0
[6.4.15]: https://github.com/thebtf/engram/compare/v6.4.14...v6.4.15
[6.4.14]: https://github.com/thebtf/engram/compare/v6.4.13...v6.4.14
[6.4.13]: https://github.com/thebtf/engram/compare/v6.4.12...v6.4.13
[6.4.12]: https://github.com/thebtf/engram/compare/v6.4.11...v6.4.12
[6.4.11]: https://github.com/thebtf/engram/compare/v6.4.10...v6.4.11
[6.4.10]: https://github.com/thebtf/engram/compare/v6.4.9...v6.4.10
[6.4.9]: https://github.com/thebtf/engram/compare/v6.4.8...v6.4.9
[6.4.8]: https://github.com/thebtf/engram/compare/v6.4.7...v6.4.8
[6.4.7]: https://github.com/thebtf/engram/compare/v6.4.6...v6.4.7
[6.4.6]: https://github.com/thebtf/engram/compare/v6.4.5...v6.4.6
[6.4.5]: https://github.com/thebtf/engram/compare/v6.4.4...v6.4.5
[6.4.4]: https://github.com/thebtf/engram/compare/v6.4.3...v6.4.4
[6.4.3]: https://github.com/thebtf/engram/compare/v6.4.2...v6.4.3
[6.4.2]: https://github.com/thebtf/engram/compare/v6.4.1...v6.4.2
[6.4.1]: https://github.com/thebtf/engram/compare/v6.4.0...v6.4.1
[5.0.0]: https://github.com/thebtf/engram/compare/v3.7.1...v5.0.0
[3.7.0]: https://github.com/thebtf/engram/releases/tag/v3.7.0
[2.4.0]: https://github.com/thebtf/engram/compare/v2.3.1...v2.4.0
[2.3.1]: https://github.com/thebtf/engram/compare/v2.3.0...v2.3.1
[2.3.0]: https://github.com/thebtf/engram/compare/v2.2.1...v2.3.0
[2.2.1]: https://github.com/thebtf/engram/compare/v2.2.0...v2.2.1
[2.2.0]: https://github.com/thebtf/engram/compare/v2.1.9...v2.2.0
[2.1.9]: https://github.com/thebtf/engram/compare/v2.1.8...v2.1.9
[2.1.8]: https://github.com/thebtf/engram/compare/v2.1.7...v2.1.8
[2.1.7]: https://github.com/thebtf/engram/compare/v2.1.6...v2.1.7
[2.1.6]: https://github.com/thebtf/engram/compare/v2.1.5...v2.1.6
[2.1.5]: https://github.com/thebtf/engram/compare/v2.1.4...v2.1.5
[2.1.4]: https://github.com/thebtf/engram/compare/v2.1.3...v2.1.4
[2.1.3]: https://github.com/thebtf/engram/compare/v2.1.2...v2.1.3
[2.1.2]: https://github.com/thebtf/engram/compare/v2.1.1...v2.1.2
[2.1.1]: https://github.com/thebtf/engram/compare/v2.1.0...v2.1.1
[2.1.0]: https://github.com/thebtf/engram/compare/v2.0.9...v2.1.0
[2.0.9]: https://github.com/thebtf/engram/compare/v2.0.8...v2.0.9
[2.0.8]: https://github.com/thebtf/engram/compare/v2.0.7...v2.0.8
[2.0.7]: https://github.com/thebtf/engram/compare/v0.5.1...v2.0.7
[0.5.1]: https://github.com/thebtf/engram/compare/v0.3.0...v0.5.1
[0.3.0]: https://github.com/thebtf/engram/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/thebtf/engram/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/thebtf/engram/releases/tag/v0.1.0
