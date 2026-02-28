# Implementation Plan: Storage Architecture v2 — Scale to Millions

## Summary

Fix critical scale bugs in the scheduler (N+1 queries, full-memory loads), benchmark graph query performance at 1M scale, and prepare a conditional path to Apache AGE. The current bottlenecks are application-level anti-patterns, not platform limitations. PostgreSQL 17 + pgvector handles millions with proper query patterns.

## Analysis Insights

**4-way parallel analysis (3 model families) + plan critique:**
- Gemini (thinkdeep): Prove graph queries insufficient before adding graph backend.
- Gemini (planner): Phased approach with scale bug fixes as mandatory Phase 1.
- Sonnet (analyst): Found scale bugs in GORM layer. AGE safest extension path.
- GPT (consensus): Option A (AGE) as target, Option C as interim. Feature-flagged rollout.
- Critique (challenging-plans): 2 of 6 "bugs" already fixed, 1 mischaracterized, 1 overstated. Only 2 genuinely critical.

**Consensus result:**
- 2/2 models AGREE: Fix app-level scale bugs first (Phase 1)
- 2/2 models REJECT: Options B (FalkorDB) and D (Neo4j) — too complex / too costly
- Divergence resolved: C as interim baseline, A (AGE) as conditional target with benchmark gates

**Post-critique corrections:**
- Connection pool already configured (`store.go:55-63`, `config.go:54`)
- B-Tree indexes already exist (migrations 013, 016 — covering composites)
- LIKE search is already a FTS fallback, not primary path
- NOT IN list capped at 100 items — not a real bottleneck
- Current graph queries use Go-level BFS, NOT recursive CTEs
- GetRelationGraph has O(n²) duplicate check (new finding)

## Phases

### Phase 1: Fix Scheduler Scale Bugs (CRITICAL — blocks everything)

1. **Task 1.1: Extend ObservationProvider interface for batch iteration**
   - Add `GetAllObservationsIterator` to `ObservationProvider` interface at `scheduler.go:14-19`
   - Add `GetRelationCountsBatch(ctx, observationIDs) (map[int64]int, error)` to `RelationProvider` at `scheduler.go:22-26`
   - Update mock implementations in `scheduler_test.go`
   - Files: `internal/consolidation/scheduler.go`, `internal/consolidation/scheduler_test.go`

2. **Task 1.2: Wire iterator into RunDecay**
   - Replace `GetAllObservations` call at `scheduler.go:145` with `GetAllObservationsIterator`
   - Batch size: 500 (matches existing iterator default)
   - Replace per-observation `GetRelationCount` (line 172-187) with batch `GetRelationCountsBatch`
   - File: `internal/consolidation/scheduler.go`

3. **Task 1.3: Wire iterator into RunForgetting**
   - Replace `GetAllObservations` at `scheduler.go:272` with `GetAllObservationsIterator`
   - File: `internal/consolidation/scheduler.go`

4. **Task 1.4: Fix GetRelationGraph O(n²) duplicate check**
   - `relation_store.go:253-258` — inner loop scans all existing relations for duplicates
   - Replace with `map[int64]bool` visited set for O(1) lookups
   - File: `internal/db/gorm/relation_store.go`

5. **Task 1.5: Implement GetRelationCountsBatch in relation store**
   - Add batch method to `RelationStore` that fetches counts for multiple observation IDs in one query
   - SQL: `SELECT source_observation_id, COUNT(*) FROM observation_relations WHERE source_observation_id IN (?) GROUP BY source_observation_id`
   - File: `internal/db/gorm/relation_store.go`

6. **Task 1.6 (LOW): Improve LIKE fallback or add trigram index**
   - `searchObservationsLike` at `observation_store.go:740-742` is FTS fallback, not primary path
   - Options: (a) add `pg_trgm` GIN index for LIKE queries, (b) remove LIKE fallback entirely (return empty on FTS miss)
   - Decision: defer to Phase 2 benchmark results
   - File: `internal/db/gorm/observation_store.go`

### Phase 2: Benchmark & Validate (Go/No-Go gate for AGE)

7. **Task 2.1: Create benchmark suite**
   - Synthetic dataset: 1M observations, 5M relations (realistic fan-out)
   - Queries: 1-hop (find direct relations), 2-hop (chain), 3-hop (deep chain)
   - Measure: p50, p95, p99 latency under concurrent vector+write load
   - Pass criteria: p95 < 100ms for 1-3 hop queries
   - File: new `internal/benchmark/`

8. **Task 2.2: Benchmark current Go BFS + SQL CTEs + raw SQL**
   - Current: Go-level BFS in `GetRelationGraph` (iterative queries per depth level)
   - Alternative: recursive CTE in single SQL query (new implementation)
   - Alternative: raw SQL with depth-limited joins
   - Compare all three approaches with EXPLAIN ANALYZE

9. **Task 2.3: Decision gate**
   - If Go BFS or CTEs pass at 3-hop under load: Option C is end-state. Close AGE.
   - If none meet p95 < 100ms at 3-hop: Proceed to Phase 3 (AGE).
   - Document decision in ADR.

### Phase 3: Apache AGE Integration (CONDITIONAL — only if Phase 2 fails)

10. **Task 3.1: AGE spike — Go driver evaluation**
    - Test apache/age Go driver against PG 17
    - Evaluate: Cypher query execution, GORM compatibility, error handling
    - Risk: immature driver may need raw `database/sql` + pgx

11. **Task 3.2: Graph query interface extraction**
    - Create `GraphQuerier` interface for relation traversal
    - Implement Go BFS backend (extract from `GetRelationGraph`)
    - NOTE: existing `internal/graph/observation_graph.go` has CSR in-memory graph — evaluate reuse
    - File: new `internal/graph/interface.go`, `internal/graph/bfs/`, `internal/graph/age/`

12. **Task 3.3: AGE implementation behind feature flag**
    - Implement `GraphQuerier` backed by AGE Cypher queries
    - Feature flag: `ENGRAM_GRAPH_BACKEND=bfs|age` (default: bfs)
    - SQL fallback per-query on AGE failure

13. **Task 3.4: Benchmark AGE vs BFS vs CTEs**
    - Run same suite from Phase 2 against AGE implementation
    - Compare p50/p95/p99 directly
    - Go/no-go: AGE must beat BFS at 3-hop p95 to justify complexity

### Phase 4: Production Hardening

14. **Task 4.1: Extract vectorSync to interface**
    - `worker/service.go:127` has concrete `*pgvector.Sync`
    - ~12 methods to abstract (`SyncObservation`, `SyncSummary`, `BatchSync*`, `Delete*`, etc.)
    - Used in 35+ locations — significant refactoring
    - File: `internal/worker/service.go`, new `internal/vector/syncer.go`

15. **Task 4.2: Integration tests with real PostgreSQL**
    - Testcontainers or Docker-based PG instance for CI
    - Cover: observation CRUD, vector sync, hybrid search, relation queries, decay cycle

## Approach Decision

**Chosen approach:** Phased C→A with benchmark gates
**Rationale:** Both model families agree current bottlenecks are app-level. Fix those first (Phase 1), benchmark at scale (Phase 2), add AGE only if graph queries demonstrably fail under load (Phase 3). This avoids premature complexity while keeping a clear upgrade path.

**Alternatives rejected:**
- **Option B (FalkorDB):** Distributed state, Saga pattern, dual writes. Operational complexity kills latency budget.
- **Option D (Neo4j):** Full platform replacement. Enormous migration cost for 1-3 hop queries.
- **Option C as permanent end-state:** Risky — if relation volume grows faster than node count, graph query performance may degrade. AGE path must remain open.

## Critical Decisions

- **Decision 1:** Fix scheduler bugs before architecture change. Both models 9/10 and 8/10 confident.
- **Decision 2:** Benchmark at 1M scale before committing to AGE. Prevents premature complexity.
- **Decision 3:** Feature-flagged AGE rollout (if needed). SQL fallback per-query de-risks immaturity.
- **Decision 4:** Reject polyglot persistence (FalkorDB/Neo4j). Single-DB constraint is a feature, not limitation.

## Risks & Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| AGE Go driver immature | Medium | Feature flag + SQL fallback; raw pgx if driver fails |
| AGE slower than BFS (reported benchmarks) | High | Phase 2 benchmark gate — don't adopt if slower |
| ObservationProvider interface change breaks tests | Low | Update mocks in scheduler_test.go as part of Task 1.1 |
| Relation fan-out explosion (millions of edges) | High | Pagination on relation queries; configurable max depth |
| vectorSync refactoring scope (~35 call sites) | Medium | Phase 4 — do after core performance is validated |

## Files to Modify

### Phase 1 (Scale Bug Fixes)
- `internal/consolidation/scheduler.go` — extend interfaces, wire iterator, batch relations
- `internal/consolidation/scheduler_test.go` — update mocks for new interface methods
- `internal/db/gorm/relation_store.go` — fix O(n²) duplicate check, add batch count method

### Phase 2 (Benchmarks)
- New: `internal/benchmark/` — benchmark suite (synthetic data generator + query runner)
- New: `internal/db/gorm/relation_store_cte.go` — CTE-based graph traversal alternative

### Phase 3 (AGE — conditional)
- New: `internal/graph/interface.go` — GraphQuerier interface
- New: `internal/graph/bfs/` — BFS implementation (extract from GetRelationGraph)
- New: `internal/graph/age/` — AGE implementation
- Evaluate reuse: `internal/graph/observation_graph.go` (existing CSR in-memory graph)

### Phase 4 (Hardening)
- `internal/worker/service.go` — extract vectorSync to interface (~35 call sites)
- New: `internal/vector/syncer.go` — VectorSyncer interface definition
- New: CI integration test setup

## Success Criteria

- [ ] ObservationProvider interface extended with iterator method
- [ ] RelationProvider interface extended with batch count method
- [ ] RunDecay uses iterator (no full-memory load)
- [ ] RunForgetting uses iterator
- [ ] GetRelationGraph uses O(1) duplicate check (map, not loop)
- [ ] Benchmark suite runs against 1M synthetic observations
- [ ] Graph query p95 < 100ms for 1-3 hop documented
- [ ] Go/No-Go decision on AGE recorded in ADR
- [ ] All tests passing (80%+ coverage on changed code)

## Plan Validation

**Consensus result:** Phased C→A approach
**Gemini confidence:** 9/10 (Option C sufficient)
**GPT confidence:** 8/10 (Option A as target with C as interim)
**Critique result:** REVISE — accepted. 2 zombie tasks removed, 1 recharacterized, 1 new bug added, interface dependencies documented, Go BFS (not CTEs) correctly identified as current implementation.
