# Continuity State

**Last Updated:** 2026-03-02
**Session:** Re-Genesis Phase 2 — Architecture finalization

## Current Goal
Finalize re-genesis architecture and begin Phase 1 implementation (Level 0 deterministic pipeline).

## Genesis Progress
- Phase 1 (DECOMPOSE): DONE — problem analysis, current implementation audit
- Phase 2 (DECIDE): DONE — Progressive Refinement architecture chosen
- Phase 3 (FEATURE MAP): Not started
- Phase 4 (SPECS): Not started
- Phase 5 (VALIDATE): Not started
- Phase 6 (CODIFY): Not started

## Key Decisions (this session)

### Architecture: Progressive Refinement (replaces engram-processor daemon)
- **Level 0**: Deterministic + embedding, server-side, no LLM. Solves Docker blocker.
- **Level 1**: Batch LLM enrichment, client-side, optional. Upgrades quality.
- **Level 2**: Consolidation. Already implemented.

### Key Insights
1. **Batch > real-time**: ECC continuous-learning-v2 validates this (hooks → JSONL, observer batches every 5min)
2. **LLM optional**: ~80% classification is rule-based, embedding works on raw text, consumer is Claude
3. **Content rot**: 4 types (code/decision/approach/discovery), batch handles all, pre-filter first+last Edit
4. **No new binary**: Level 0 inside existing engram-server. No daemon, no WAL, no second port.
5. **Embedding sufficient**: BGE-small (384d) or OpenAI REST, both already implemented
6. **SQLite-vec is dead code**: `//go:build ignore` on 5/6 files, delete entirely

### Challenge Report Applied (5 corrections)
1. `formatObservationDocs` must handle Level 0 (NULL narrative → zero vectors without fix)
2. `Observation` model needs `EnrichmentLevel` + `SourceEventIDs` fields + migration
3. Stop hook must rewire from broken `/summarize` to `/finalize`
4. Need `FormatEmbeddingText()` for BGE 512-token limit (raw JSON overflows)
5. Early validation: test 80% classification accuracy claim against real data

## Plan Document
`.agent/plans/re-genesis-architecture.md` — fully updated, challenge-reviewed, corrections applied.

## Uncommitted Changes
- `.agent/plans/re-genesis-architecture.md` — rewritten sections 1, 5, 6
- `.agent/CONTINUITY.md` — updated
- Memory files updated

## Key Files
- Architecture: `.agent/plans/re-genesis-architecture.md`
- Embedding: `internal/embedding/{model,service,openai}.go`
- Vector sync: `internal/vector/pgvector/{client,sync}.go` (formatObservationDocs needs modification)
- Observation model: `pkg/models/observation.go` (needs enrichment_level field)
- Processing pipeline: `internal/worker/sdk/processor.go` (to be replaced)
- Hooks: `cmd/hooks/{post-tool-use,stop,user-prompt,session-start}/main.go`

## Next Steps
1. Continue genesis: Phase 3 (Feature Map) — list exact features for Phase 1 implementation
2. Or: proceed directly to Phase 1 implementation (deterministic pipeline)
3. Pending: Task #32 benchmark suite (code complete, not committed)
