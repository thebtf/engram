# Phase 2: Synthetic Benchmark Specification

## Goal
Prove PostgreSQL + pgvector handles 1M observations with sub-100ms graph query latency. This is the Go/No-Go gate for Apache AGE.

## Infrastructure
- **Target:** unleashed.lan:5432 (production PostgreSQL)
- **Database:** `engram_bench` (create before, drop after)
- **User:** `mnemonic_user` (same as production)
- **pgvector:** Required (HNSW cosine index)

## Synthetic Dataset

### Observations (1,000,000 rows)
- Distributed across 50 projects (20,000 per project)
- Types: feature (40%), bugfix (20%), discovery (15%), decision (10%), refactor (10%), change (5%)
- Importance scores: normal distribution, mean=0.5, stddev=0.2
- Content: random text 100-500 chars (realistic for observation content)
- CreatedAt: spread over 365 days
- Tags/concepts: 3-5 per observation from a pool of 200 concepts

### Relations (5,000,000 rows)
- Average 5 relations per observation (some have 0, some have 20+)
- Types: similar_to (40%), leads_to (25%), contradicts (10%), supports (15%), references (10%)
- Confidence: uniform 0.3-1.0
- Power-law distribution: most observations have 2-5 relations, ~1% have 15+

### Vectors (1,000,000 rows)
- 384-dimensional float32 (matches BGE model)
- Random unit vectors (sufficient for index performance testing)
- HNSW index with m=16, ef_construction=128

## Benchmark Queries

### Q1: 1-hop relation lookup (most common)
```sql
-- Given observation ID, find all directly related observations
-- Current: Go-level GetRelationsByObservationID
-- Target: p95 < 10ms
```

### Q2: 2-hop chain (common)
```sql
-- Given observation ID, find observations 2 hops away
-- Approach A: Go BFS (current GetRelationGraph with maxDepth=2)
-- Approach B: SQL CTE
-- Target: p95 < 50ms
```

### Q3: 3-hop deep chain (rare but important)
```sql
-- Given observation ID, find observations 3 hops away
-- Approach A: Go BFS (GetRelationGraph with maxDepth=3)
-- Approach B: SQL recursive CTE
-- Target: p95 < 100ms
```

### Q4: Hybrid search (vector + FTS + relations)
```sql
-- Semantic search + expand by 1-hop relations
-- Current: hybrid search → GetRelatedObservationIDs
-- Target: p95 < 100ms total
```

### Q5: Decay cycle (batch operation)
```sql
-- Process all observations in batches of 500
-- Fetch relation counts + avg confidence per batch
-- Target: full cycle for 1M observations < 5 minutes
```

## Concurrency Profile
- 4 concurrent readers (simulating 4 workstations)
- 1 writer (simulating observation ingest at 10 obs/sec)
- 1 decay worker (batch processing)

## Measurement
- Warm up: 100 queries before measuring
- Sample: 1000 queries per query type
- Metrics: p50, p95, p99, max, mean, stddev
- Tool: Go `testing.B` benchmarks with custom histogram

## Success Criteria
| Query | p50 | p95 | p99 |
|-------|-----|-----|-----|
| Q1 (1-hop) | < 5ms | < 10ms | < 25ms |
| Q2 (2-hop) | < 20ms | < 50ms | < 100ms |
| Q3 (3-hop) | < 50ms | < 100ms | < 250ms |
| Q4 (hybrid) | < 50ms | < 100ms | < 250ms |
| Q5 (decay/1M) | — | — | < 5 min total |

## Decision Gate
- ALL p95 targets met → Option C is end-state. Close AGE.
- Q3 p95 > 100ms under concurrent load → Evaluate AGE (Phase 3).
- Q1-Q2 fail → Investigate query plans, indexing issues (not an AGE trigger).

## Implementation Notes
- Use `internal/db/gorm/` store implementations directly (test real GORM overhead)
- Seed using raw SQL `COPY` for speed (1M row insert via GORM would be too slow)
- Run migrations first to create proper schema + indexes
- After benchmarks: `DROP DATABASE engram_bench`
