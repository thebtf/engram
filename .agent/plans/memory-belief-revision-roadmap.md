# Memory Belief Revision Roadmap

## Goal

Roll out an additive v5 belief-revision model that turns memories from append-only records into revisable beliefs, while preserving backward-compatible APIs and non-lossy history. The long-term destination is one unified belief-revision model across `memories` and `behavioral_rules`, but the rollout starts with `memories` first and extends to `behavioral_rules` only after the memory path is proven.

## Guiding Principles

1. **Additive v5 evolution, not redesign.** Extend the current schema and flows instead of replacing them.
2. **Backward-compatible APIs.** Keep `store_memory` and `recall_memory` working for existing clients.
3. **Non-lossy conflict handling.** Supersede or invalidate structurally; do not delete history as part of normal belief revision.
4. **Computed retrieval trust.** Retrieval-time decay is calculated from state, not stored as a denormalized column.
5. **Conservative first rollout.** Prefer bounded heuristics and explicit operator workflows over premature automation.
6. **Memories first, behavioral rules second.** Validate semantics on factual memory before extending them to always-inject guidance.
7. **Phase-gated expansion.** Deferred capabilities should remain deferred until the core model proves stable.

## Phase 0 — Schema + Read Compatibility Scaffolding

### Scope

Add the minimum belief-revision columns to `memories` and wire safe defaults so existing writes and reads continue to function.

Minimum additive schema for P0:
- `status`
- `status_reason`
- `confidence_score`
- `valid_until`
- `last_confirmed_at`
- `source_type`
- `source_ref`
- `supersedes_id`

Implementation intent for this phase:
- add migration(s) for the new columns,
- backfill safe defaults for existing rows where required,
- keep read paths tolerant of null or default values,
- define initial status vocabulary and lifecycle semantics,
- ensure retrieval-time decay remains a computed concept only.

### Exit Criteria

- Schema migration is additive and safe for existing v5 data.
- Existing `store_memory` clients continue to succeed unchanged.
- Existing `recall_memory` clients continue to return results unchanged in shape.
- New rows receive deterministic defaults for belief-revision metadata.
- No decay column or other denormalized retrieval score is introduced.

## Phase 1 — Memory Write-Time Conflict Resolution

### Scope

Introduce bounded write-time candidate lookup and classify new memory writes into non-lossy outcomes.

Target behavior:
- retrieve top relevant existing memories for a new write,
- classify the write as `ADD`, `NOOP`, `UPDATE`, or `INVALIDATE`,
- create lineage through `supersedes_id` when a new memory supersedes an older one,
- transition older memory state using `status`, `status_reason`, and `valid_until` rather than deleting it,
- initialize `confidence_score`, `source_type`, `source_ref`, and `last_confirmed_at` consistently.

This phase should stay conservative:
- prioritize heuristics and bounded similarity windows,
- allow optional model assistance, but do not require heavy NLI,
- preserve a clear audit trail for why a memory was superseded or invalidated.

### Exit Criteria

- The write path can perform at least `ADD`, `NOOP`, and one revision path (`UPDATE` or `INVALIDATE`) reliably.
- Near-duplicates no longer create uncontrolled append-only growth.
- Superseded memories remain stored and structurally linked to the newer belief.
- Invalidated beliefs close their trust window without being deleted.
- Response extensions, if any, remain backward-compatible.

## Phase 2 — Recall Scoring with Temporal Validity + Decay

### Scope

Upgrade recall to rank memories using computed trust-aware scoring based on belief state.

Scoring inputs for this phase:
- `status`
- `confidence_score`
- `valid_until`
- `last_confirmed_at`
- optional source trust weighting from `source_type`

Behavioral goals:
- active and recently confirmed memories rank higher,
- expired or low-confidence memories are down-ranked,
- superseded or invalidated memories are excluded by default or strongly penalized,
- history remains queryable when explicitly needed later,
- computation happens at retrieval time rather than through stored decay fields.

### Exit Criteria

- Recall ranking meaningfully distinguishes active/current memories from stale ones.
- Retrieval-time decay is fully computed and not persisted.
- Expired or superseded memories no longer compete equally with current beliefs.
- Existing recall APIs remain backward-compatible.
- The ranking formula is inspectable enough to support debugging and tuning.

## Phase 3 — Re-Verification and Manual Sweep Hooks

### Scope

Use the new structural fields to support re-verification workflows without requiring a fully automatic garbage collector.

Capabilities in scope:
- identify memories that need re-verification,
- surface manual sweep candidates using confidence, validity, and last-confirmed state,
- add hooks or operator workflows for confirming, refreshing, or invalidating beliefs,
- define what events can update `last_confirmed_at` and confidence safely,
- establish status transition rules for `needs_reverification`.

This phase is intentionally workflow-oriented rather than scheduler-heavy.

### Exit Criteria

- Operators or MCP tooling can list stale or review-needed memories deterministically.
- Manual re-verification can update memory state without schema changes.
- The system has a documented path for refreshing confidence and confirmation timestamps.
- No fully automatic periodic GC is required to make the feature useful.

## Phase 4 — Extend Model to `behavioral_rules`

### Scope

Apply the same belief-revision semantics to `behavioral_rules`, adapting for always-inject guidance behavior.

This phase should answer:
- which fields transfer directly from `memories`,
- whether `behavioral_rules` need the same full lifecycle or a narrower status model,
- how supersession and invalidation should affect injection behavior,
- whether the system should remain two-table with shared semantics or move toward a common underlying belief abstraction.

This phase depends on the earlier memory rollout proving stable first.

### Exit Criteria

- `behavioral_rules` use the same belief-revision vocabulary or a documented compatible subset.
- Always-inject guidance can be superseded or invalidated non-lossily.
- Memory and rule retrieval/injection semantics no longer diverge conceptually.
- The project has a credible path toward one unified belief-revision model across both domains.

## Deferred

The following items are explicitly deferred until after the first rollout proves stable:

- graph/entity/wiki integration,
- NLI-heavy contradiction reasoning,
- fully automatic periodic garbage collection,
- broad autonomous re-verification sweeps,
- deeper unification work beyond additive shared semantics,
- any redesign that replaces the current v5 `store_memory` / `recall_memory` surface.

## Dependency and Order Summary

1. **Phase 0 first** — schema and compatibility scaffolding must exist before any behavior changes.
2. **Phase 1 next** — write-time belief revision depends on the new columns and statuses.
3. **Phase 2 after Phase 1** — recall scoring becomes meaningful once write-time statuses and confidence exist.
4. **Phase 3 after Phase 2** — re-verification hooks depend on stored belief state and useful retrieval surfacing.
5. **Phase 4 last** — extend to `behavioral_rules` only after memory semantics are validated.

Compact dependency chain:

`Phase 0 schema -> Phase 1 write revision -> Phase 2 recall scoring -> Phase 3 re-verification -> Phase 4 behavioral_rules`

## Summary Position

The correct rollout is to make memory beliefs structurally revisable first, score them dynamically at recall time second, operationalize re-verification third, and only then extend the model to behavioral rules. That sequence preserves compatibility, reduces migration risk, and keeps v5 evolution additive rather than disruptive.
