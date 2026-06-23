# UI Surface Ledger

**Purpose:** running registry of backend entities/features that need WebUI exposure.
The v5 strip-down also gutted the dashboard (today: health + issues + vault + tokens
only). Instead of a separate "rediscover what needs UI" pass later, every change that
ships a user-visible entity ADDS A ROW HERE at implementation time.

**Maintenance rule (operator directive 2026-06-11):** any task/PR that introduces or
changes an entity an operator would inspect, approve, or tune MUST append/update a row
in this ledger as part of the task. Agent briefs for implementation work must include
this instruction. The ledger is the input for the future dashboard rebuild stage
(combine with §4b U4/U6 upstream-decoupling rewrite — one redesign, not two passes).

**Row format:** entity | source (milestone/PR) | data surface (table/API) | suggested UI
treatment | priority (P1 operator-workflow / P2 observability / P3 nice-to-have).

## Backfill (shipped before ledger existed)

| Entity | Source | Data surface | Suggested UI | Pri |
|--------|--------|--------------|--------------|-----|
| Memories (core) | v6.x | `memories` table; recall/store MCP; REST /api/memories | Memory browser: list+filter by project/tier/epistemic_type/status; detail page | P1 |
| Memory lifecycle (tier, stability, retrievability, decay) | Milestone B, PR #214 | lifecycle fields on memories; promotion_log | Tier badge + decay curve on memory detail; promotion history timeline | P2 |
| Citations / injection effectiveness | Milestone A | citation_log; Thompson Sampling outcomes | Per-project effectiveness view (PRD FR: "Dashboard shows injection effectiveness per project"); memory detail: injection history + score breakdown | P1 (PRD-mandated) |
| Knowledge graph (memory edges) | Milestone C, PR #215 | knowledge_edges; graph MCP | Graph explorer (vis per memory: 1-hop neighbors); edge type/confidence display | P2 |
| Typed nodes (13 types: skill/agent/rule/...) | F-TG2, PR #247 | knowledge_nodes; graph add_node/get_edges | Node browser by type; node detail with edge list; dangling-edge indicator (EC-F7) | P2 |
| Audit trail | W2-TG1, PR #241 | audit_log (action/actor/session/before/after) | Audit viewer: filter by memory/action/actor/date; purge receipts | P1 |
| Crystallization runs | W2-TG3, PR #242 | memories with source_agent=crystallization + fp: tags | Session-end extraction feed: what was extracted from which session | P2 |
| **Crystallization candidates** | F-TG4, PR #249 | crystallization_candidates; list/promote/reject/supersede MCP | **Candidate review queue: pending list + promote/reject buttons + decay countdown — THE operator workflow this feature exists for (US-F1)** | P1 |
| purge_project | W2-TG2, PR #244 | admin MCP (purge_project + confirm); PurgeReceipt | Danger-zone admin page: purge with double-entry confirm + receipt display | P2 |
| Hybrid retrieval explain/rationale | W3 PR #245 + F-TG3 PR #248 | ranking_explanation (FR-C4) + ranking_rationale (v5-surface) in recall responses | Search playground: query box → results with score breakdown (relevance/recency/importance, tier badges, rationale) | P2 |
| Privacy scopes | F-TG1, PR #221 | privacy_scope + source_workstation_id on memories/nodes | Scope badge on memory/node views; workstation filter; "what can workstation X see" preview | P2 |
| Behavioral rules | v6.x | behavioral_rules table | Rules manager: list/edit per project + global; priority ordering; consumer-targeting selector when issue #257 lands (which consumers see this rule) | P1 |
| Documents/collections | v6.x | documents, doc chunks | Collection browser; ingest status | P3 |
| Sleep cycle | W1 PR #239 + B | watermark, idle gate, decay batches | System page: last cycle time, watermark, decayed counts | P3 |
| Session segments / sessions | v6.x | session_segments (migration 120) | Session timeline per project | P3 |
| Embeddings/backfill status | Milestone A | content_chunks; backfill_status admin | System page: embedding coverage %, backfill progress | P3 |

## Pending (in-flight work — add rows as TGs land)

| Entity | Source | Data surface | Suggested UI | Pri |
|--------|--------|--------------|--------------|-----|
| Write-lint two-phase protocol | F-TG5 (PR pending) | store_memory Phase1/Phase2: lint_signals + resolution_options + resolution_token (payload-bound: project/actor/content-hash; expired vs not_found error contract); Phase2 action_taken values incl. store_with_contradiction_edge / candidate_pending_created; no-signal path returns legacy-shape {id, scope, privacy_scope, quality_signals}; redaction error content_fully_redacted (EC-F5) | Write-conflict resolution UI: show signals + options when storing via dashboard; lint stats; redaction rules status page (EC-F9 restart-required indicator) | P2 |
| Governance ops: bulk_op_snapshots (rollback/pin) | F-TG6, feat/milestone-f-tg6-governance | `bulk_op_snapshots` table (snapshot_id, op_type, before_state JSONB, status, pinned, affected_memory_ids, created_at); MCP: list_snapshots / rollback_snapshot / pin_snapshot / governance_status | Snapshot list: filter by op_type/status; rollback button (conflict preview); pin toggle; conflict details when rollback blocked (EC-F3); auto-prune indicator (ENGRAM_SNAPSHOT_RETENTION_DAYS) | P1 |
| Governance ops: export/import wizard | F-TG6, feat/milestone-f-tg6-governance | governance MCP tools (export_bundle / import_bundle); governance pkg; ImportReport (detected_format/memories_restored/conflict_count/resolution_tokens) | Export wizard: project selector + format (ZIP/JSONL) + include_embeddings toggle; Import wizard: file upload + auto-detect preview + conflict report display + resolution token listing (EC-F8 no-silent-overwrite) | P1 |
| Governance ops: dry-run preview pane | F-TG6, feat/milestone-f-tg6-governance | dry_run=true param on store_memory / promote_candidate / bulk_promote / bulk_delete / bulk_supersede; returns would_store/would_affect count without DB write | Dry-run toggle on memory store + bulk op forms; preview pane shows would_affect count + what would be stored/promoted/deleted before committing; TG5 nil-safe: preview available even without write-lint orchestrator | P2 |
| Redaction rules status (EC-F9) | F-TG6, feat/milestone-f-tg6-governance + F-TG5 | ENGRAM_REDACTION_RULES_PATH env var; startup log: rule file path + SHA-256 checksum; audit_log action=redacted/content_fully_redacted | Redaction status page: current rule file path + checksum (from startup log); rules list if parseable; restart-required indicator when file modified after startup; link to docs/operating-engram.md | P2 |
| Code intelligence (chunks/graph/search) | absorption CI-A+ | code_chunks, code_graph_edges | Codebase search view; index status per project/worktree | P2 |
| **Books (book-as-context, BOOK-track)** | absorption BOOK-track (synto port, decision #2777) | book sources + compiled per-concept articles + INDEX catalog + confidence lifecycle | **Book upload (PDF/FB2/+) → processing status (ingest/compile/review pipeline) → attachment manager (per-project / global toggle); concept browser with confidence badges; review queue for draft→verified→published (reuse candidate-queue UI pattern)** | P1 (operator-requested workflow) |
| **Server settings management (env → UI migration)** | operator directive 2026-06-12 (re-stated at v6.5.0 release; earlier request existed); server control-plane follow-up | ~30 env vars on Unraid container today (screenshots in session log); target: DB-backed runtime settings + settings API; live seams: `GET /api/config`, `GET /api/flags`, allowlisted `PATCH /api/config` for config-backed fields with hot-reload vs restart-required receipt | **Settings page: all runtime-tunable config editable in UI — embedding/reranking/HyDE/LLM endpoints+models (keys via vault, masked), graph provider+FalkorDB addr, smart-GC thresholds, telemetry, context token budget, vnext feature flags. Bootstrap-only stays env: DATABASE_DSN, ENGRAM_WORKER_HOST/PORT, ENGRAM_VAULT_KEY, ENGRAM_AUTH_ADMIN_TOKEN. Current live state: modal overlay can inspect runtime config/feature flags, save `features.enforce_source_project` + `memory.inject_unified` through `PATCH /api/config`, record config mutation audit entries, and show restart-required receipts; env-controlled fields stay read-only. Still needed: broader env-import migration beyond the two allowlisted settings and writable seams for the remaining settings groups** | P1 (operator-requested workflow) |
| Memory domain registry cleanup | ENG-PMQ-1 follow-up / server control-plane completion; shipped PR #369 | `memory_domain_owners`; REST `GET/PUT/DELETE /api/memory-domains/{domain}` | Settings modal -> Memory domains tab lists explicit ownership rows, edits mode/owner, deletes a row back to implicit legacy policy, shows mutation/error state, and never implies memory rows were deleted. Follow-up only if product later wants a dedicated standalone domain page. | P1 |
| Auth identity/profile/session surface | Operator console hardening after OC-1 | Live: `GET /api/auth/me`, `POST /api/auth/logout`; mustbuild: `PATCH /api/profile`, auth session listing/revoke endpoint | Topbar identity chip opens the design-contract profile menu; profile modal shows current live identity and keeps editable profile + session management controls honest as mustbuild until backend seams exist. | P1 |
| v7 attention (hints/telemetry) | v7 waves | attention_events, HintQueue, telemetry | Hint feed + acceptance metrics (precision/burden per PRD S5) | P2 |
