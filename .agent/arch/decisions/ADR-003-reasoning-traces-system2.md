# ADR-003: Add Reasoning Traces (System 2 Memory)

## Status

Proposed

## Context

Competitive analysis of Cipher (campfirein/cipher) revealed a key gap: engram stores
observation RESULTS (title + narrative) but not the REASONING CHAIN that produced them.

Cipher implements "Dual Memory":
- System 1: Regular memories (concepts, logic, interactions) — equivalent to engram observations
- System 2: Reasoning traces — extracted thought→action→observation→decision→conclusion steps,
  evaluated for quality (0-1), stored in separate "reflection vector store"

Currently engram's learning extractor (`internal/learning/extractor.go`) extracts learnings
from transcripts, but only final observations — not the reasoning steps that led to them.
The agent's actual thought process is lost.

## Decision Drivers

* Agent effectiveness improves when it can recall HOW past reasoning worked, not just WHAT was decided
* Reasoning quality evaluation enables closed-loop improvement (low-quality reasoning → learn to avoid)
* System 2 memories complement existing System 1 (observations) without replacing them
* Existing LLM extraction infrastructure can be extended

## Decision

Add reasoning trace extraction and storage to engram as a new observation type or separate table.

### Design

1. **New observation type `reasoning`** or separate `reasoning_traces` table with fields:
   - `session_id`, `steps[]` (type: thought|action|observation|decision|conclusion|reflection + content),
   - `quality_score` (0-1), `task_context` (goal, domain, complexity), `metadata`

2. **Extraction**: Extend `ProcessObservation` to detect reasoning patterns in tool events.
   When agent shows multi-step reasoning (if→then→because→therefore), extract the chain.

3. **Storage**: Store in pgvector alongside observations for semantic search.
   Separate from regular observations to avoid polluting context injection.

4. **Retrieval**: New `recall(action="reasoning", query="...")` returns relevant past reasoning chains.
   Injected when similar tasks detected.

## Consequences

### Positive
- Agents learn HOW to reason, not just WHAT to remember
- Quality scoring enables reasoning improvement over time
- Compatible with existing vector search infrastructure
- No breaking changes — additive feature

### Negative
- LLM extraction cost increases (reasoning analysis more expensive than observation extraction)
- Storage growth — reasoning traces are verbose
- New table/type adds schema complexity

## Implementation Notes

- Phase 1: New `reasoning_traces` table + extraction prompt
- Phase 2: Quality evaluation via LLM (0-1 score)
- Phase 3: Reasoning injection into context when similar task detected
- Inspiration: Cipher's `def_reflective_memory_tools.ts`, `store_reasoning_memory.ts`

## References

- Cipher source: `src/core/brain/tools/def_reflective_memory_tools.ts`
- Investigation: `.agent/reports/investigate-cipher-*.md`
