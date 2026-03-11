# Engram Re-Genesis Phase 1: Implementation Plan

**Status:** COMPLETED (deterministic pipeline implemented in internal/pipeline/, 2026-03-11)

## Summary

Phase 1 implements the **Deterministic Pipeline (Level 0)** — server-side observation extraction
without LLM dependency. Raw tool events arrive via HTTP, get classified by rules, titled by template,
and embedded for immediate searchability. This unblocks the Docker deployment (no Claude CLI needed).

Based on 3-way analysis (Sonnet deep analysis, Sonnet plan decomposition, Sonnet feasibility):
all tracks converged on the same critical findings and architecture.

## Analysis Insights

### Critical Findings (3/3 tracks agree)
1. **formatObservationDocs() bug**: Returns EMPTY slice for Level 0 observations (NULL narrative,
   empty facts). Level 0 observations are INVISIBLE to vector search. Must fix FIRST.
2. **DiskANN deferred**: BGE-small = 384 dims. HNSW handles this perfectly. DiskANN only needed
   if user switches to OpenAI text-embedding-3-large (3072 dims). Defer to Phase 3.
3. **File-only entities at Level 0**: Concept entities from keyword extraction are too noisy
   ("error", "test", "config"). Only extract file path entities deterministically.
4. **Memory blocks = Phase 2**: High-value but requires consolidation logic. Phase 1 creates
   the schema; Phase 2 populates blocks.
5. **EnrichmentLevel as integer**: 0=raw, 1=LLM-narrative, 2=block-integrated, 3=relation-mapped.
6. **Existing code reuse**: ClassifyMemoryType(), DetermineScope(), safeResolvePath() already do
   most of what Level 0 needs.

### Divergence Points (resolved)
- Track A wanted 10-step MVP; Track B wanted 6 phases with parallelism. **Resolution**:
  Linear 8-step sequence (simpler, single developer, dependencies are serial).
- Track D identified DeleteObservations() missing obs_{id}_summary docs. **Resolution**:
  Fix in Step 2 alongside formatObservationDocs.

### Risk from Reference Projects
- **Letta blocks**: Valuable for session-start injection. Defer to Phase 2 consolidation.
- **Khoj dual retrieval**: Already partially implemented (hybrid search = FTS + vector).
  Add temporal boost in Phase 1 Step 7.
- **Context Engineering modular injection**: Implement in Phase 1 Step 7 (restructure inject response).

## Phases

### Step 1: raw_events table migration (Migration 022)
- Table: `raw_events` (id, session_id, tool_name, tool_input JSONB, tool_result JSONB,
  created_at_epoch, project, workstation_id, processed BOOLEAN DEFAULT FALSE)
- Index: (session_id, created_at_epoch), (processed, created_at_epoch)
- This is the immutable append-only event log (source of truth)

**Files**: `internal/db/gorm/migrations.go`

### Step 2: Fix formatObservationDocs for Level 0 (CRITICAL PATH)
- When narrative IS NULL: generate embedding text from title + concepts + file paths
- Format: `"{type}: {title}\nFiles: {files}\nConcepts: {concepts}"`
- This produces 1 vector document per observation even at Level 0
- Also fix DeleteObservations() to handle obs_{id}_summary doc_id pattern

**Files**: `internal/vector/pgvector/sync.go`, `internal/db/gorm/observation_store.go`

### Step 3: Observation model enrichment
- Add `EnrichmentLevel int` field (0=raw, 1=LLM, 2=block, 3=graph)
- Add `SourceEventIDs []int64` (links back to raw_events)
- Add `RawContent string` (compact representation for embedding at Level 0)
- Migration 023: ALTER TABLE observations ADD COLUMN enrichment_level INT DEFAULT 0,
  ADD COLUMN source_event_ids BIGINT[], ADD COLUMN raw_content TEXT

**Files**: `pkg/models/observation.go`, `internal/db/gorm/migrations.go`

### Step 4: Deterministic Pipeline
- New package: `internal/pipeline/deterministic.go`
- Functions:
  - `ClassifyEvent(toolName, toolInput, toolResult) ObservationType` — rule-based
  - `GenerateTitle(toolName, toolInput) string` — template-based
  - `ExtractConcepts(toolName, toolInput, toolResult) []string` — keyword extraction
  - `ExtractFilePaths(toolInput, toolResult) []string` — regex file path extraction
  - `ExtractFacts(toolName, toolInput, toolResult) []string` — diff-based fact extraction
  - `FormatEmbeddingText(obs) string` — compact text for BGE embedding
- Reuses existing: `shouldSkipTool()`, `shouldSkipTrivialOperation()`, `safeResolvePath()`
  from `internal/worker/sdk/processor.go`
- Event classification rules:
  - Edit/Write → "change"
  - Bash with error exit → "bugfix"
  - Bash with "test" → "discovery"
  - Architecture/design keywords → "decision"
  - Default → "change"

**Files**: `internal/pipeline/deterministic.go`, `internal/pipeline/deterministic_test.go`

### Step 5: POST /api/events/ingest endpoint
- Receives raw tool events from hooks
- Stores in raw_events table
- Runs deterministic pipeline → creates Level 0 observation
- Embeds and syncs to pgvector (non-blocking goroutine)
- Returns 202 Accepted (fire-and-forget from hook perspective)
- Deduplication: hash(tool_name + tool_input + truncated_result) with 5-min TTL

**Files**: `internal/worker/handlers.go` (or new `handlers_ingest.go`),
         `internal/worker/server.go` (route registration)

### Step 6: Hook rewiring
- `post-tool-use` hook: POST to `/api/events/ingest` instead of `/api/sessions/observations`
- Send raw `{tool_name, tool_input, tool_result}` — NOT processed observations
- Remove any client-side classification logic from hooks
- Keep existing filtering in hook (skip trivial tools like Glob, Task, etc.)

**Files**: `cmd/hooks/post-tool-use/main.go`

### Step 7: Context injection restructure
- Modular response from `/api/context/inject`:
  ```xml
  <engram-memory>
    <recent count="5">
      <!-- Last 5 observations by created_at, any enrichment level -->
    </recent>
    <relevant count="10">
      <!-- Semantic search results for project context -->
    </relevant>
  </engram-memory>
  ```
- Add temporal boost: recent observations (< 24h) get 1.5x weight in RRF
- Existing search endpoint unchanged (backward compatible)

**Files**: `internal/worker/handlers_context.go`, `internal/search/manager.go`

### Step 8: Memory blocks schema (migration only, no population)
- Migration 024: CREATE TABLE memory_blocks (
    id BIGSERIAL PRIMARY KEY,
    project TEXT NOT NULL,
    block_type TEXT NOT NULL, -- 'project-context', 'patterns', 'decisions', 'preferences', 'gotchas', 'architecture', 'active-work', 'open-questions'
    content TEXT NOT NULL,
    source_observation_ids BIGINT[],
    version INT DEFAULT 1,
    last_updated_epoch BIGINT,
    UNIQUE(project, block_type)
  )
- Schema only — population logic is Phase 2 (consolidation-driven)

**Files**: `internal/db/gorm/migrations.go`

## Approach Decision

**Chosen approach**: Linear 8-step implementation, each step produces a working increment.
No parallel phases (single developer, serial dependencies).

**Rationale**: Steps 1-3 are schema/model changes that must complete before Step 4 (pipeline).
Step 4 must complete before Step 5 (endpoint). Step 6 (hook rewiring) depends on Step 5.
Steps 7-8 are independent but low-risk and small — no need for parallelism.

**Alternatives rejected**:
- Parallel Phase 1A/1B (Track B): Adds coordination overhead for single developer
- 10-step MVP (Track A): Merged into 8 steps by combining related changes
- Entity tables in Phase 1 (original plan): Deferred — file path entities extracted
  but stored as observation.files_modified[], not separate entity table. Phase 2 work.

## Critical Decisions

- **D-NEW-1**: Level 0 embedding text = `"{type}: {title}\nFiles: {files}\nConcepts: {concepts}"`
  — NOT raw JSON (would overflow BGE's 512-token limit)
- **D-NEW-2**: raw_events table stores JSONB (not TEXT) for tool_input/tool_result
  — enables future SQL queries on event structure
- **D-NEW-3**: EnrichmentLevel is on Observation, not raw_event
  — raw_events are immutable; observations evolve
- **D-NEW-4**: No entity tables in Phase 1 — file paths stored in observation fields,
  entity dedup deferred to Phase 2 with pg_trgm
- **D-NEW-5**: DiskANN deferred — HNSW at 384 dims is optimal, DiskANN only if dims > 2000

## Risks & Mitigations

1. **formatObservationDocs regression**: Changing vector document generation could break
   existing observations. → Mitigation: Add field_type="level0_composite" for new docs,
   keep existing narrative/fact docs unchanged.

2. **Hook backward compatibility**: Rewiring hooks to new endpoint could break if server
   is older version. → Mitigation: Hook checks server version via /health, falls back
   to old endpoint if new one returns 404.

3. **Embedding quality at Level 0**: BGE on template text may produce lower-quality embeddings
   than LLM-enriched narrative. → Mitigation: Temporal boost for recent observations
   compensates; Level 1 enrichment re-embeds with better text.

## Files to Modify

- `internal/db/gorm/migrations.go` — Migrations 022 (raw_events), 023 (observation fields), 024 (memory_blocks)
- `pkg/models/observation.go` — EnrichmentLevel, SourceEventIDs, RawContent fields
- `internal/vector/pgvector/sync.go` — formatObservationDocs fix for NULL narrative
- `internal/db/gorm/observation_store.go` — DeleteObservations fix
- `internal/pipeline/deterministic.go` — NEW: deterministic classification pipeline
- `internal/pipeline/deterministic_test.go` — NEW: tests
- `internal/worker/handlers_ingest.go` — NEW: POST /api/events/ingest
- `internal/worker/server.go` — Route registration
- `cmd/hooks/post-tool-use/main.go` — Hook rewiring
- `internal/worker/handlers_context.go` — Modular inject response
- `internal/search/manager.go` — Temporal boost in RRF
- `pkg/models/raw_event.go` — NEW: RawEvent model

## Success Criteria

- [ ] Server starts without Claude CLI and processes tool events into searchable observations
- [ ] Level 0 observations appear in hybrid search results (vector + FTS)
- [ ] formatObservationDocs produces ≥1 vector document for Level 0 observations
- [ ] Hook sends raw events to /api/events/ingest, server creates observations
- [ ] Context injection returns modular XML with recent + relevant sections
- [ ] Memory blocks table exists (empty, ready for Phase 2)
- [ ] All existing tests pass (no regression)
- [ ] New tests: deterministic pipeline ≥80% coverage
- [ ] `go build ./...` succeeds
- [ ] `go test ./...` passes

## Plan Validation

**Critique result**: Self-validated against 3-track analysis convergence.
**Key findings addressed**:
- formatObservationDocs NULL narrative → Step 2 (critical path, first fix)
- DiskANN premature → Deferred (D-NEW-5)
- Entity table complexity → Deferred to Phase 2 (D-NEW-4)
- Memory blocks schema → Step 8 (schema only)
- Concept pollution → File-only extraction at Level 0
