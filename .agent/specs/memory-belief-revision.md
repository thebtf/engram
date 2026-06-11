# Memory Belief Revision for v5

**Status:** Proposed

## Context

Engram v5 already has a minimal memory system with backward-compatible `store_memory` and `recall_memory` APIs, but the current model treats stored memories as durable stickers rather than revisable beliefs. The existing schema stores basic identity, content, tags, authorship, timestamps, soft-delete state, and versioning. The current write path persists new memories directly and optionally writes always-inject content as `behavioral_rules`. The current recall path performs project-scoped listing with in-memory substring and tag filtering.

This proposal is an additive v5 evolution, not a redesign. It introduces a minimal belief-revision layer for memories first, keeps the existing API surface stable, and creates a clear path to later unify `memories` and `behavioral_rules` under one belief model.

## Problem

The current system has three structural gaps:

1. **No write-time conflict resolution.** New memories are appended even when they refine, supersede, or invalidate older ones.
2. **No temporal validity model.** TTL is derived from tags for response purposes only and is not represented structurally in storage.
3. **No retrieval-time trust adaptation.** Recall does not account for confidence, expiry, stale-but-useful history, or the need for re-verification.

This creates a write-only memory accumulation pattern:
- outdated facts remain indistinguishable from current facts,
- contradictory entries require manual interpretation,
- retrieval cannot down-rank stale memories or promote recently confirmed ones,
- there is no structural hook for re-verification workflows.

## Goals

1. Add a minimal, additive belief-revision model to `memories` without breaking current clients.
2. Represent supersession, confidence, provenance, and temporal validity explicitly.
3. Resolve obvious write-time conflicts through a bounded additive workflow inspired by Mem0-style action selection, but adapted for Engram's non-lossy storage model as `ADD / SUPERSEDE / INVALIDATE / NOOP`.
4. Compute retrieval-time decay dynamically rather than persisting a denormalized decay column.
5. Support re-verification and manual sweep workflows without requiring a full autonomous garbage collector in the first rollout.
6. Preserve historical traceability: invalidated or superseded memories remain queryable for audit and learning.
7. Establish the long-term direction: one unified belief-revision model for both `memories` and `behavioral_rules`, rolled out in phases with `memories` first.

## Non-Goals

The first rollout does **not** include:

- a full redesign of the v5 memory subsystem,
- graph/entity/wiki integration,
- NLI-heavy contradiction reasoning,
- fully automatic periodic garbage collection,
- breaking changes to `store_memory` or `recall_memory`,
- immediate unification of `memories` and `behavioral_rules` into one table,
- a requirement that every write must call an LLM.

## Current Verified State

The following state is already verified for v5:

### Schema

- `pkg/models/memory.go` and `internal/db/gorm/models.go`: `memories` currently store `project`, `content`, `tags`, `source_agent`, `edited_by`, `created_at`, `updated_at`, `deleted_at`, `id`, and `version`.
- `pkg/models/behavioral_rule.go` and `internal/db/gorm/models.go`: `behavioral_rules` currently store `project?`, `content`, `priority`, `edited_by`, `created_at`, `updated_at`, `deleted_at`, `id`, and `version`.

### Write path

- `internal/mcp/tools_memory.go` routes `store_memory` to `memoryStore.Create`.
- `always_inject=true` writes through `behavioralRulesStore.Create`.
- There is no write-time conflict resolution.
- TTL is currently derived from tags and returned in the response only; it is not stored structurally in either table.

### Recall path

- `internal/mcp/tools_memory.go::handleRecallMemory` and `internal/mcp/tools_recall.go::handleRecallSearch` perform project-scoped listing plus in-memory substring and tag filtering.
- Retrieval does not currently score by confidence, temporal validity, or decay.

### Prior art

- This PR contains the reproducible planning artifacts for this rollout:
  - `.agent/specs/memory-belief-revision.md`
  - `.agent/plans/memory-belief-revision-roadmap.md`
- External patterns informing this proposal:
  - Mem0-style conflict handling retrieves semantically similar memories, then classifies whether to add, revise, drop, or ignore a candidate; for Engram, this proposal narrows that into the explicitly non-lossy vocabulary `ADD / SUPERSEDE / INVALIDATE / NOOP`.
  - Graphiti/Zep-style temporal validity is non-lossy: facts are invalidated by closing their validity window rather than deleting them.

## Proposed Architecture

### 1. Add a belief state to memories, not a replacement data model

The proposed model extends the existing `memories` table with a small set of fields that capture belief state, provenance, and temporal validity. Existing content, tags, and timestamps remain the primary payload. New columns describe how much the system trusts a memory, whether it is still current, and how it relates to later revisions.

This keeps the rollout additive and migration-friendly.

### 2. Use non-lossy write-time conflict resolution

On memory creation, Engram should perform a bounded similarity-based candidate lookup against recent and relevant existing memories. For each candidate set, the write path should classify the new memory into one of four actions:

- **ADD** — create a new independent memory.
- **SUPERSEDE** — create a new memory revision and mark the older belief as `superseded` while keeping both rows.
- **NOOP** — reject near-duplicate creation while preserving the existing belief.
- **INVALIDATE** — transition a previous belief to `status=invalidated` and close its validity window without creating a replacement memory or deleting history.

For the first rollout, Engram should prefer deterministic heuristics plus bounded model assistance only where needed. The key architectural rule is that conflict resolution is **non-lossy**: older memories are not deleted, and belief-state transitions do not imply soft-delete. `status` captures belief semantics (`active`, `superseded`, `invalidated`, `needs_reverification`), while existing `deleted_at` remains the separate storage-layer tombstone used only for actual record retirement.

### 3. Represent temporal validity directly

A memory should be allowed to be:
- active and current,
- active but expiring,
- superseded by a newer belief,
- invalidated because it is no longer true,
- pending re-verification when trust has degraded.

The first rollout does not require full interval algebra. A single `valid_until` field is enough to represent the end of a belief's currently trusted window. If a belief is invalidated or replaced, its end state is represented structurally rather than by deletion.

### 4. Compute retrieval-time decay, do not persist it

Decay should be a computed scoring factor at retrieval time, based on:
- confidence score,
- status,
- whether `valid_until` has passed,
- recency of confirmation (`last_confirmed_at`),
- optional source trust weighting.

This keeps the schema minimal, avoids stale denormalized columns, and allows scoring policy to evolve without repeated backfills.

### 5. Re-verification is a workflow, not a cron requirement

The first rollout should expose enough state to support:
- manual sweeps,
- operator dashboards or MCP tools that surface stale memories,
- future hooks that ask the user or system to confirm or refresh beliefs.

A fully automatic periodic garbage collector is deferred. The system should first become structurally capable of re-verification before automating it.

### 6. Long-term unification target

The long-term target is one unified belief-revision model for both `memories` and `behavioral_rules`. However, rollout should be phased:

1. apply the model to `memories`,
2. validate write and recall behavior,
3. extend the same semantics to `behavioral_rules`.

This avoids mixing factual memory evolution with always-inject guidance semantics in the first migration.

## Schema Changes

This proposal recommends the following minimum additive schema for **Phase 0 / P0** on `memories`:

| Column | Type intent | Purpose |
|---|---|---|
| `status` | enum/text | Current belief state of the memory; separate from `deleted_at` soft-delete |
| `status_reason` | text | Human/audit explanation for the current state |
| `confidence_score` | float | Current trust score for retrieval and re-verification, normalized to `[0.0, 1.0]` |
| `valid_until` | timestamp nullable | End of the currently trusted validity window |
| `last_confirmed_at` | timestamp nullable | Last time the memory was explicitly or implicitly reconfirmed |
| `source_type` | enum/text | Provenance category describing what kind of source produced the memory |
| `source_ref` | text nullable | Sanitized opaque pointer to the originating source instance (tool call, document, issue, session, etc.) |
| `supersedes_id` | int64 nullable self-reference | FK-like link from the newer memory row to the prior memory row it supersedes |

### Recommended initial status vocabulary

The exact enum implementation can remain open, but the initial vocabulary should support at least:

- `active`
- `superseded`
- `invalidated`
- `needs_reverification`

This is intentionally narrower than a final long-term lifecycle. The action vocabulary should map into these stored states non-lossily: `ADD` -> new row with `status=active`; `SUPERSEDE` -> new row with `status=active` plus prior row transitioned to `status=superseded`; `INVALIDATE` -> prior row transitioned to `status=invalidated` with its validity window closed; `NOOP` -> no new row.

### Important schema notes

- These columns are **additive** and should default safely so existing reads keep working.
- `status` and `deleted_at` are different layers: `status` expresses belief state, while `deleted_at` remains the storage-layer soft-delete tombstone. A memory can be `superseded` or `invalidated` with `deleted_at IS NULL`.
- `status` should default to `active`; `status_reason` should default to empty/nullable audit text.
- `confidence_score` should be initialized deterministically from source class and rollout defaults, with a DB-level default and check constraint enforcing `0.0 <= confidence_score <= 1.0`.
- `valid_until` is nullable because not every memory has a known expiry horizon.
- `source_agent`, `source_type`, and `source_ref` serve different roles: `source_agent` identifies who emitted the memory, `source_type` classifies what kind of source produced it, and `source_ref` points to the specific source instance.
- `source_ref` must not store raw secrets or raw PII. Prefer opaque IDs or redacted handles, redact before persistence/logging, and cap the stored value to a reasonable bounded length (for example 255-512 chars) to avoid unbounded payload leakage.
- `supersedes_id` should be nullable `int64` and point from the newer belief row to the prior belief row it supersedes. This is a self-reference on `memories.id`, making lineage direction explicit: child revision -> superseded parent.
- The `memories` table should enforce lineage integrity for `supersedes_id`: add a foreign key to `memories(id)`, add `CHECK (supersedes_id IS NULL OR supersedes_id <> id)`, and reject physical deletion or soft-deletion (`deleted_at`) of a parent row while active revisions still reference it. Belief-state transitions on the parent (such as `active -> superseded` during normal supersession, or `active -> invalidated`) are part of the expected lifecycle and must remain allowed; the integrity rule guards row removal, not status changes.
- Add DB indexes that support the first retrieval/write-path queries rather than indexing every new column blindly. At minimum, document indexes for active-belief recall and supersession traversal.
- Retrieval-time decay must **not** be stored as a column.

## API Compatibility

The existing `store_memory` and `recall_memory` APIs should remain backward-compatible.

### `store_memory`

Backward-compatibility requirements:
- existing callers can continue sending the current payload,
- the server fills new belief-revision fields with defaults,
- response shape may be extended, but existing fields must not change semantics,
- conflict resolution should not require clients to understand new schema fields.

Optional future-compatible response additions may include:
- action taken (`ADD`, `SUPERSEDE`, `NOOP`, `INVALIDATE`),
- affected prior memory ID,
- current status/confidence summary.

### `recall_memory`

Backward-compatibility requirements:
- existing query behavior remains valid,
- if no belief-aware scoring inputs are available, the system falls back gracefully,
- new ranking logic should improve ordering without requiring new client parameters.

Optional future-compatible filters may later expose:
- include superseded,
- only active beliefs,
- needs re-verification,
- source type filtering.

Those filters are not required for the initial rollout.

## Acceptance Criteria

- [ ] Migration adds the new belief-revision columns to `memories` without breaking existing clients or reads.
- [ ] Existing `store_memory` callers continue to succeed without payload changes.
- [ ] Existing `recall_memory` callers continue to return results without API breakage.
- [ ] New memories receive default `status`, `confidence_score`, `source_type`, and lineage-safe null defaults where appropriate.
- [ ] The write path can classify at least `ADD`, `NOOP`, and one non-lossy revision path (`SUPERSEDE` or `INVALIDATE`) for memories.
- [ ] Superseded or invalidated memories remain stored and queryable for audit/history.
- [ ] Retrieval can down-rank expired, stale, or low-confidence memories using computed decay rather than a stored decay column.
- [ ] `valid_until` and `last_confirmed_at` influence retrieval or re-verification surfacing once populated.
- [ ] Re-verification candidates can be identified structurally from stored fields without requiring a separate redesign.
- [ ] The design clearly leaves graph/entity/wiki integration, NLI-heavy contradiction reasoning, and fully automatic periodic GC for later phases.

## Risks

### 1. Status semantics drift

If status definitions are vague, different write paths may assign inconsistent states. The rollout should keep the initial vocabulary intentionally small and document transitions explicitly.

### 2. Over-eager invalidation

Near-duplicate or related memories can be mistaken for contradictions. The first rollout should bias toward conservative non-lossy supersession, not aggressive invalidation.

### 3. Confidence inflation without evidence

If confidence defaults are too high and reconfirmation is not wired, new fields may look precise without being meaningful. Initial scoring should stay simple and auditable.

### 4. Retrieval complexity without observable value

Belief-aware scoring can become hard to reason about. Decay and trust logic should be computed from a small, inspectable set of factors in the first iteration.

### 5. Premature table unification

Trying to force `behavioral_rules` into the first rollout would increase migration risk and muddy semantics. Guidance rules should follow only after the memory model proves stable.

## Open Questions

1. What initial `confidence_score` mapping should be used for each `source_type`?
2. Should `valid_until` represent only hard expiry, or also soft review deadlines in the first rollout?
3. Which write path should be allowed to set `last_confirmed_at`: explicit confirmation only, or also successful downstream reuse?
4. What candidate search window is sufficient for write-time conflict resolution without making writes too expensive?
5. Should `INVALIDATE` remain only an action vocabulary term that maps to a `status` transition, or should the system also persist explicit action/audit history separately?
6. When Phase 4 reaches `behavioral_rules`, should the model remain two-table with shared semantics, or converge toward a common underlying belief record abstraction?
7. Which operator or MCP affordance should own manual re-verification sweeps first: admin tooling, dashboard workflow, or both?

## Summary Position

This proposal treats memory belief revision as an additive v5 evolution rather than a redesign. The minimum viable change is to make memories structurally revisable through status, confidence, provenance, temporal validity, and supersession lineage, then compute trust-aware retrieval dynamically. Once that works for `memories`, the same model can be extended to `behavioral_rules` as the second rollout phase.
