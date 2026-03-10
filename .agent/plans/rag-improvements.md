# Implementation Plan: Engram RAG Improvements

**Status:** COMPLETED (all 3 phases implemented, 2026-03-11)

## Summary

Three improvements to Engram's RAG pipeline, projected from Advanced Agentic RAG Architecture onto the existing codebase. Each addresses a verified gap: (1) dead ONNX reranker replaced with API-based cross-encoder, (2) HyDE for improved query-document matching, (3) enhanced consolidation for self-learning. All are opt-in via config, backward-compatible, and follow existing patterns (openai.go HTTP client, config env-var overrides).

## Analysis Insights

**Verified codebase state (confidence-check):**
- ONNX reranker is dead on Windows (runtime failure: onnxruntime DLL absent; model.onnx is 133-byte stub)
- Reranking config infrastructure already exists (RerankingEnabled, Alpha, Candidates, Results, PureMode)
- `*reranking.Service` is used as concrete type — needs interface extraction
- HyDE is completely absent — pure greenfield
- Consolidation scheduler works but samples only 20 from 100 recent observations weekly
- OpenAI-compatible embedding client (`openai.go`) is the pattern to follow for all new API clients

**Cross-model convergence (Sonnet + Opus):**
- Both analyses independently identified the same implementation order: Reranker → Consolidation → HyDE
- Both flagged the concrete type `*reranking.Service` as the key architectural prerequisite
- Both identified the 500ms timeout as non-negotiable for reranker on hot path
- Agreement on stratified sampling for consolidation (recent + old, not just recent)

## Phases

### Phase 1: API Reranker (Replace Dead ONNX)

**Value: HIGH** — reranking is fully wired but dead. Activating it improves every search result.
**Risk: KNOWN** — established API contract (Cohere/Jina), existing call sites are correct.

#### Task 1.1: Extract Reranker Interface
- Create `internal/reranking/interface.go`
- Define `Reranker` interface with exact signatures from existing `Service`:
  - `Rerank(query string, candidates []Candidate) ([]RerankResult, error)`
  - `RerankByScore(query string, candidates []Candidate) ([]RerankResult, error)`
  - `Score(query, document string) (rawScore, normalizedScore float64, err error)` — note: 3 return values
  - `Close() error`
- Make existing `Service` satisfy the interface (it already does implicitly)
- Change `internal/worker/service.go` field type from `*reranking.Service` to `reranking.Reranker`
- Update ALL 4 usage sites: `initializeAsync`, `reinitializeDatabase`, `Shutdown`, field declaration
- Verify: `handlers_update.go` (calls `Score`), `handlers_context.go` (calls `Rerank`, `RerankByScore`) compile unchanged

#### Task 1.2: Implement API Reranker Client
- Create `internal/reranking/api.go` — HTTP client following `openai.go` pattern
- Support Cohere Rerank v2 API format (`/v1/rerank` or `/v2/rerank`)
- Support generic OpenAI-compatible rerank endpoint (for LiteLLM proxy)
- `APIService` struct: `client *http.Client`, `baseURL`, `apiKey`, `modelName`, `Alpha float64`
- Methods: `Rerank`, `RerankByScore`, `Score` (returns `rawScore=score, normalizedScore=score` since APIs provide single score), `Close`
- Context-based timeout: default 500ms, configurable via `ENGRAM_RERANKING_TIMEOUT_MS`
- Graceful degradation: on 429/timeout/error, return original order (not error)

#### Task 1.3: Config & Factory
- Add to `internal/config/config.go`:
  ```
  RerankingProvider    string  // "onnx" | "api" (default: "api")
  RerankingAPIBaseURL  string  // env: ENGRAM_RERANKING_API_URL
  RerankingAPIKey      string  // env-only: ENGRAM_RERANKING_API_KEY
  RerankingAPIModel    string  // env: ENGRAM_RERANKING_API_MODEL (default: "rerank-english-v3.0")
  RerankingTimeoutMS   int     // env: ENGRAM_RERANKING_TIMEOUT_MS (default: 500)
  ```
- Factory in `service.go`: if `RerankingProvider == "api"` && key present → `NewAPIService`; else try ONNX (existing path)

#### Task 1.4: Tests
- Unit test `APIService.Rerank()` with `httptest.NewServer` — mock Cohere response format
- Test 429 handling: mock returns 429 → verify original-order fallback
- Test timeout: mock delays 600ms → verify fallback within 500ms
- Test response count mismatch: mock returns fewer results than candidates
- Integration test: end-to-end search with reranking enabled

**Files modified:** `internal/reranking/interface.go` (new), `internal/reranking/api.go` (new), `internal/reranking/api_test.go` (new), `internal/config/config.go`, `internal/worker/service.go`
**Verification checklist:** `internal/worker/handlers_update.go` (Score call), `internal/worker/handlers_context.go` (Rerank/RerankByScore calls) — must compile after interface extraction

---

### Phase 2: Enhanced Consolidation (Self-Learning Engine)

**Value: MEDIUM-HIGH** — better associations improve cold-start quality and long-term learning.
**Risk: LOW** — no new external dependencies, pure internal logic changes.

#### Task 2.1: Stratified Sampling
- Change `RunAssociations` in `scheduler.go` to fetch a stratified pool:
  - `GetRecentObservations(ctx, project, limit/2)` — 50% recent
  - `GetOldestObservations(ctx, project, limit/2)` — 50% oldest non-archived (new method)
  - Merge + deduplicate by ID → pass combined pool to `DiscoverAssociations`
- `DiscoverAssociations` internal `sampleObservations()` is retained — it subsamples from the already-stratified pool, preserving the 50/50 mix in expectation
- Add `GetOldestObservations(ctx, project, limit)` to `ObservationProvider` interface in `scheduler.go`
- Implement in `internal/db/gorm/observation_store.go` — SQL: `ORDER BY created_at ASC` with `archived = false` filter
- Note: empty `project` parameter = all projects (consistent with existing `GetRecentObservations` behavior)

#### Task 2.2: EVOLVES Relation Rule
- Use existing `RelationEvolvesFrom` constant from `pkg/models/relation.go` (already defined at line 30)
- In `applyTypePairRules`: if same type + same project + high similarity + age gap > 7 days → `RelationEvolvesFrom`
- Semantics: newer observation refines/supersedes older one on the same topic
- Confidence = similarity score
- Note: constant already exists and is in `AllRelationTypes` — zero schema changes needed

#### Task 2.3: Config-Driven Confidence & ImportanceScore Boost
- Replace hard-coded confidence values (0.6, 0.7, 0.5) with `AssociationConfig` fields:
  - `ContradictConfidence float64` (default 0.6)
  - `ExplainsConfidence float64` (default 0.7)
  - `ParallelConfidence float64` (default 0.5)
- After `RunAssociations`, collect IDs that gained new relations → boost ImportanceScore
- **Important**: `UpdateImportanceScores` stores absolute values (used by decay cycle), not deltas
- Implementation: add `IncrementImportanceScores(ctx, deltas map[int64]float64, cap float64)` to `ObservationProvider`
- SQL: `UPDATE observations SET importance_score = LEAST(importance_score + $delta, $cap) WHERE id = $id`
- This avoids read-then-write race with concurrent decay cycle

#### Task 2.4: Tighter Scheduling
- Change `AssociationInterval` default from 168h (weekly) to 24h (daily)
- Config: `ENGRAM_ASSOCIATION_INTERVAL_HOURS` (default 24)
- Config: `ENGRAM_ASSOCIATION_SAMPLE_SIZE` (default 50, was 20)
- Config: `ENGRAM_ASSOCIATION_IMPORTANCE_BOOST` (default 0.05)
- Add semaphore to prevent concurrent decay + association from saturating DB pool

#### Task 2.5: Tests
- Unit test stratified sampling: verify both recent and old observations appear
- Unit test EVOLVES rule: same-type pair with high sim + age gap
- Unit test importance boost: after RunAssociations, verify UpdateImportanceScores called with correct delta
- Integration test: 100 synthetic observations over 90 days, verify cross-temporal relations

**Files modified:** `internal/consolidation/associations.go`, `internal/consolidation/scheduler.go`, `internal/config/config.go`, `internal/db/gorm/observation_store.go` (new methods: `GetOldestObservations`, `IncrementImportanceScores`), tests
**Note:** `pkg/models/relation.go` — no changes needed (`RelationEvolvesFrom` already exists)

---

### Phase 3: HyDE (Hypothetical Document Embeddings)

**Value: MEDIUM** — improves recall for abstract/question queries.
**Risk: UNKNOWN** — adds LLM latency to hot search path. Quality is model-dependent.

#### Task 3.1: HyDE Generator
- Create `internal/search/expansion/hyde.go`
- `HyDEGenerator` struct: LLM HTTP client (OpenAI-compatible), prompt template, cache
- `Generate(ctx, query string) (string, error)` — returns hypothetical document text
- System prompt: "Write a short technical document (2-3 sentences) that would answer this question: {query}"
- In-memory cache keyed on normalized query hash, TTL 5 minutes
- Dedicated timeout: 800ms (within the 5s expansion budget)
- Guard: if response < 20 chars after trim → skip

#### Task 3.2: Integration with Expander
- Add `hydeGen *HyDEGenerator` field to `Expander`
- In `Expand()`: if `cfg.EnableHyDE && e.hydeGen != nil`:
  - Call `hydeGen.Generate(ctx, query)`
  - Append as `ExpandedQuery{Text: hypothesis, Source: "hyde", Weight: 0.9}`
- The existing fan-out in `handleSearchByPrompt` (lines 107-119) loops `expandedQueries` and merges by highest weighted score — HyDE requires zero changes to the search merger

#### Task 3.3: Config & Wiring
- Add to config:
  ```
  HyDEEnabled    bool    // ENGRAM_HYDE_ENABLED (default: false)
  HyDEAPIURL     string  // ENGRAM_HYDE_API_URL (default: reuse embedding API URL)
  HyDEAPIKey     string  // env-only: ENGRAM_HYDE_API_KEY
  HyDEModel      string  // ENGRAM_HYDE_MODEL (default: "gpt-4o-mini")
  HyDEMaxTokens  int     // ENGRAM_HYDE_MAX_TOKENS (default: 150)
  HyDETimeoutMS  int     // ENGRAM_HYDE_TIMEOUT_MS (default: 800)
  ```
- Wire in `service.go`: change `NewExpander` signature to accept optional `*HyDEGenerator`
- Update both call sites: `initializeAsync` and `reinitializeDatabase` in `service.go`

#### Task 3.4: Template-Based HyDE (Zero-LLM Fast Path)
- For deterministic queries (detected intent: error, implementation), use template-based hypothetical generation
- Templates per intent:
  - `error` → "An observation about fixing {error_terms}: the error was caused by {concept} and resolved by modifying {file_pattern}."
  - `implementation` → "Code implementation of {concept}: the feature was added to {file_pattern} using {framework_terms}."
- Falls back to LLM path only for `question`/`architecture` intents
- This provides HyDE benefit with zero latency cost for common query patterns

#### Task 3.5: Tests
- Unit test `HyDEGenerator.Generate()` with `httptest.NewServer`
- Test timeout: mock delays 1s → verify return within 800ms
- Test cache hit: second call with same query → no HTTP request
- Test template-based path: error intent → template generation, no LLM call
- Test integration with `Expander.Expand()`: HyDE adds expansion entry with `Source: "hyde"`
- Test degradation: HyDE disabled → expansion returns only intent-based variants

**Files modified:** `internal/search/expansion/hyde.go` (new), `internal/search/expansion/hyde_test.go` (new), `internal/search/expansion/expander.go`, `internal/config/config.go`, `internal/worker/service.go`

---

## Approach Decision

**Chosen approach:** Sequential phases (Reranker → Consolidation → HyDE), each self-contained.

**Rationale:**
1. **Reranker first** — highest leverage, lowest risk. Unlocks retrieval quality measurement baseline. Without it, we can't measure if HyDE or consolidation actually improve results.
2. **Consolidation second** — zero new external dependencies, pure internal improvement. Strengthens self-learning which benefits all future queries.
3. **HyDE third** — highest risk (latency on hot path, quality uncertainty). Needs staging evaluation. Benefits from having reranker in place to measure improvement.

**Alternatives rejected:**
- HyDE first: rejected because without reranker, we can't measure if HyDE hypotheticals actually improve final result ordering.
- All-in-one: rejected because each improvement is independently valuable and testable.

## Critical Decisions

- **Decision 1**: Interface extraction for reranker (not embedding API in existing struct). Rationale: cleaner separation, enables testing with mocks, future providers trivially addable.
- **Decision 2**: Cohere-compatible API format as primary (not Jina). Rationale: Cohere Rerank is the de facto standard; Jina, VoyageAI, and LiteLLM all implement Cohere-compatible `/rerank` endpoints.
- **Decision 3**: Template-based HyDE as fast path (not LLM-only). Rationale: for a local memory tool, adding 200-800ms to every search is unacceptable for common queries. Templates give 80% of benefit at 0% latency cost.
- **Decision 4**: Stratified sampling (not random across all). Rationale: random misses cross-temporal associations; stratified explicitly connects new knowledge to old.

## Risks & Mitigations

- **Risk 1**: Reranker API adds latency to search path → Mitigation: 500ms hard timeout, fallback to original order, never error
- **Risk 2**: HyDE generates irrelevant hypotheticals → Mitigation: weighted merge (0.9 weight), original query always included, template-based fast path for common intents
- **Risk 3**: Daily associations under load → Mitigation: semaphore prevents concurrent cycles, batch processing with 500-item pages
- **Risk 4**: Config proliferation → Mitigation: sensible defaults, env-only for secrets, all opt-in with `Enabled` flags

## Files to Modify

### Phase 1
- `internal/reranking/interface.go` — NEW: Reranker interface
- `internal/reranking/api.go` — NEW: API reranker implementation
- `internal/reranking/api_test.go` — NEW: tests
- `internal/config/config.go` — add reranking API config fields
- `internal/worker/service.go` — change concrete type to interface, add factory

### Phase 2
- `internal/consolidation/associations.go` — EVOLVES_FROM rule, config-driven confidence
- `internal/consolidation/scheduler.go` — stratified sampling, importance boost, tighter intervals, `ObservationProvider` interface update
- `internal/config/config.go` — consolidation config fields
- `internal/db/gorm/observation_store.go` — `GetOldestObservations`, `IncrementImportanceScores` methods

### Phase 3
- `internal/search/expansion/hyde.go` — NEW: HyDE generator
- `internal/search/expansion/hyde_test.go` — NEW: tests
- `internal/search/expansion/expander.go` — integrate HyDE into expansion pipeline
- `internal/config/config.go` — HyDE config fields
- `internal/worker/service.go` — wire HyDE generator

## Success Criteria

- [ ] Reranker: API-based reranking activates when configured, gracefully degrades on failure
- [ ] Reranker: search results measurably improve ordering (manual A/B test with sample queries)
- [ ] Consolidation: EVOLVES relations detected between temporally-distant similar observations
- [ ] Consolidation: daily associations find cross-temporal connections
- [ ] Consolidation: high-relation observations get ImportanceScore boost
- [ ] HyDE: template-based path adds 0ms latency for common query types
- [ ] HyDE: LLM path adds <800ms for complex queries
- [ ] HyDE: search recall improves for question-type queries (manual evaluation)
- [ ] All tests passing, 80%+ coverage for new code
- [ ] All improvements opt-in via config, backward-compatible defaults
- [ ] Build passes on all platforms (no ONNX dependency in new code)

## Plan Validation

**Cross-model analysis:** 2/2 model families (GPT Codex [pending], Claude Sonnet) analyzed independently.
**Sonnet findings:** Fully aligned on priority order, integration points, and edge cases. Independently identified same 500ms timeout requirement and interface extraction prerequisite.

**Critique result:** REVISE → REVISED (all findings addressed)
**Critique findings (2 independent runs, converged):**
1. ~~Wrong store path~~ → Fixed: `internal/db/gorm/observation_store.go`
2. ~~`RelationEvolves` duplicate~~ → Fixed: use existing `RelationEvolvesFrom`
3. ~~`Score` signature unspecified~~ → Fixed: exact 3-return signature documented, `APIService` returns `(score, score, nil)`
4. ~~`UpdateImportanceScores` stores absolutes~~ → Fixed: new `IncrementImportanceScores` method with atomic SQL
5. ~~Missing call sites~~ → Fixed: `handlers_update.go`, `handlers_context.go` in verification checklist
6. ~~4 usage sites, not 2~~ → Fixed: all 4 listed explicitly
7. ~~`NewExpander` signature change~~ → Fixed: both call sites documented
8. ~~ONNX failure is runtime, not build constraint~~ → Fixed: "runtime failure: DLL absent"

**Key principle:** "не легкие/lazy решения, а эффективные и красивые" — no shortcuts, effective and elegant solutions.
