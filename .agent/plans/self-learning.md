# Implementation Plan: Engram Self-Learning

## Status: Phases 1-3 IMPLEMENTED, Phase 4 DEFERRED

Last updated: 2026-03-07

## Summary

Feedback-driven self-learning system that tracks retrieval utility, extracts behavioral patterns via LLM at session end, and adapts observation confidence. Transforms Engram from a static memory store into an adaptive system that improves with use.

**Spec:** `.agent/specs/self-learning.md` (10 FRs, 7 NFRs, 8 ACs)

## Architecture

Hooks are JS scripts in `plugin/hooks/` (NOT Go binaries — migrated from `cmd/hooks/` in commit b370db5).

- `plugin/hooks/user-prompt.js` — search context, inject observations, mark-injected
- `plugin/hooks/stop.js` — parse transcript, summarize, extract learnings, detect utility signals
- `plugin/hooks/lib.js` — shared HTTP helpers

## Implementation Status

### Phase 1: Guidance Observations — DONE
- `MemTypeGuidance` exists in `pkg/models/observation.go`
- `ObsTypeGuidance` added with TypeBaseScores = 1.4
- Guidance observations injected via `<relevant-memory>` block in user-prompt.js
- Context search returns guidance observations alongside regular ones

### Phase 2: Utility Tracking — DONE
- `UtilityScore` + `InjectionCount` fields on Observation model
- `POST /api/observations/mark-injected` — global injection counter (handlers_scoring.go)
- `POST /api/observations/{id}/utility` — EMA update with signal ("used"/"corrected"/"ignored"), alpha=0.1, max delta=0.05
- `parseTranscript()` in stop.js collects last 50 messages in ring buffer
- user-prompt.js extracts observation IDs and calls mark-injected fire-and-forget

### Phase 3: LLM Extraction — DONE
- `POST /api/sessions/{id}/extract-learnings` — LLM-based extraction at session end
- stop.js calls extract-learnings with last 50 messages after summarize
- Sanitization, deduplication, and feature flag all in server-side handler

### Phase 2.5: Per-Session Utility Signal Detection — IN PROGRESS
Added per-session injection tracking and verbatim citation / correction pattern matching:

**Server-side:**
- `session_observation_injections` table (session_id, observation_id, injected_at)
- `POST /api/sessions/{sessionId}/mark-injected` — dual-write (per-session + global counter)
- `GET /api/sessions/{sessionId}/injected-observations` — returns obs with title/type/facts

**Client-side (stop.js):**
- `detectUtilitySignal(obs, assistantText)` — verbatim citation + correction pattern proximity
- After transcript parse, fetches injected obs for session, matches against assistant text
- Calls `POST /api/observations/{id}/utility` with "used" or "corrected" signal

### Phase 4: Shadow Scoring & Adaptive Thresholds — DEFERRED
Requires empirical data from Phases 1-3. See roadmap item #8.

## Critical Decisions

1. **MemoryType="guidance"** — uses existing enum infrastructure, no parallel bool
2. **No adaptive parameters in v1** — fixed scoring weights, shadow scoring deferred
3. **Unambiguous signals only** — verbatim citation (positive) + explicit correction (negative)
4. **LLM on learning path only** — hot path (user-prompt) stays LLM-free (<500ms)

## Files

| Component | File | Status |
|-----------|------|--------|
| Observation types | `pkg/models/observation.go` | Done |
| Scoring config | `pkg/models/scoring.go` | Done |
| Migrations | `internal/db/gorm/migrations.go` | Done |
| Scoring store | `internal/db/gorm/scoring_store.go` | Done |
| Scoring handlers | `internal/worker/handlers_scoring.go` | Done |
| Route registration | `internal/worker/service.go` | Done |
| User-prompt hook | `plugin/hooks/user-prompt.js` | Done |
| Stop hook | `plugin/hooks/stop.js` | Done |
| Shared lib | `plugin/hooks/lib.js` | No changes needed |
