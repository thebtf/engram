# ADR-001: Belief Revision and Knowledge Quality Assurance

**Status:** Proposed (preliminary)
**Date:** 2026-03-06
**Context:** Self-learning system accumulates observations without correction mechanism

## Problem

Engram stores observations as facts without confidence scoring, contradiction detection, or decay.
If a false fact enters the knowledge base, it propagates through retrieval loops:

1. False fact stored → agent retrieves it as "verified"
2. Downstream decisions build on false premise → more "confirming" observations
3. Positive feedback loop amplifies the error
4. No mechanism distinguishes false facts from true ones

This is the "poisoned knowledge" problem — a self-learning loop without correction.

## Decision Drivers

- Observations are write-once with no revision mechanism
- No confidence scoring on stored facts
- No contradiction detection between observations
- No provenance tracking (was this from tool output? training memory? prior session?)
- No periodic re-verification or garbage collection
- Self-learning Phase 4 (belief revision) was deferred to v1.1

## Proposed Architecture

### 1. Confidence Score + Decay

Each observation gets `confidence_score float` (0.0-1.0):
- Initial: 0.9 (from tool verification), 0.7 (from session context), 0.5 (from training memory)
- Decay: if fact NOT confirmed on re-use (test fails, API returns error) → confidence -= 0.2
- Threshold: `< 0.3` → marked `disputed`, excluded from retrieval unless explicitly requested

### 2. Contradiction Detection

On write, compare new observation embedding against existing:
- If semantic similarity > 0.85 AND content contradicts → conflict resolution
- Newer verification wins; old observation marked `superseded_by: <new_id>`
- Requires lightweight NLI (natural language inference) or heuristic comparison

### 3. Provenance Chain

Source hierarchy for confidence initialization:
```
tool_output_this_session (0.95) > docs_context7 (0.90) > indexed_nia (0.85) >
web_tavily (0.75) > prior_session_kg (0.70) > training_memory (0.50)
```

Each observation stores: `source_type`, `source_ref`, `created_by`, `created_at`

### 4. Periodic Audit (GC)

Cron-like: every N sessions, re-verify observations with `confidence < 0.7` or `age > 30 days`.
Options: re-verify with source, archive, or delete.

### 5. Negative Learning (Anti-patterns)

Store NOT ONLY "X works" but also "X does NOT work because Y".
`observation_type` enum: `fact`, `anti_pattern`, `deprecated`, `superseded`
Anti-patterns are first-class citizens in retrieval.

## Schema Changes (preliminary)

```sql
ALTER TABLE observations ADD COLUMN confidence_score FLOAT DEFAULT 0.9;
ALTER TABLE observations ADD COLUMN superseded_by UUID REFERENCES observations(id);
ALTER TABLE observations ADD COLUMN observation_type VARCHAR(20) DEFAULT 'fact';
ALTER TABLE observations ADD COLUMN source_type VARCHAR(30);
ALTER TABLE observations ADD COLUMN source_ref TEXT;
```

## Consequences

### Positive
- Self-correcting knowledge base
- False facts decay naturally
- Contradictions surfaced explicitly
- Provenance enables trust hierarchy

### Negative
- Schema migration required
- NLI for contradiction detection adds complexity
- Confidence decay needs tuning (too aggressive = data loss, too passive = no effect)
- Periodic audit adds background processing load

### Risks
- Over-aggressive decay could remove valid but rarely-used facts
- Contradiction detection false positives (similar topics != contradictions)
- Migration of existing observations (what confidence to assign retroactively?)

## Alternatives Considered

1. **Manual curation only** — rejected: doesn't scale, defeats purpose of self-learning
2. **Version-all observations** — rejected: storage bloat without clear benefit
3. **External fact-checking service** — rejected: latency, cost, dependency

## Open Questions

- What confidence threshold for retrieval inclusion? (0.3? 0.5?)
- Should decay be time-based or usage-based?
- How to handle retroactive confidence assignment for existing data?
- Is lightweight NLI feasible in Go, or should contradiction detection be embedding-only?
