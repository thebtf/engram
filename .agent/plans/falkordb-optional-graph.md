# Implementation Plan: Optional FalkorDB Graph Backend

**Status:** COMPLETED (FalkorDB graph backend implemented and integrated, 2026-03-11)

## Summary

Add FalkorDB as an optional, supplementary graph backend for engram. When connected, it provides persistent graph storage and efficient multi-hop traversal for relation queries and search expansion. When unavailable, engram continues using PostgreSQL `observation_relations` table for all graph operations — zero degradation.

## Specification

**Spec:** `.agent/specs/falkordb-optional-graph.md`
9 functional requirements, 7 acceptance criteria.

## Analysis Insights

- In-memory `ObservationGraph` (CSR format) is used only for edge detection — NOT for persistent queries. Keep it as-is.
- `RelationStore.GetRelationGraph` does N+1 SQL queries per hop level — graph DB makes this O(1) Cypher query.
- Dead `graph_search.go` references `sqlitevec.QueryResult` — don't revive it. Integrate graph expansion directly into `search/manager.go`.
- `associations.go` O(n²) pairwise comparison stays unchanged — it produces relations that get dual-written.
- FalkorDB Go SDK: `github.com/falkordb/falkordb-go`. API: `FalkorDBNew(&ConnectionOption{Addr})` → `db.SelectGraph(name)` → `graph.Query(cypher, nil, nil)`. [VERIFIED: Context7 docs — needs re-verification against actual SDK source in Phase 0.5]
- FalkorDB shares Redis port 6379 — isolated by graph name (`engram`).
- **Name collision**: `internal/graph/` already has `type RelationType int` (CSR graph). New `GraphStore` interface uses `models.RelationType` (string) — explicitly qualified, no collision.

## Critique Findings (addressed)

| Finding | Status | Resolution |
|---------|--------|------------|
| Cypher parameterized rel types invalid | FIXED | Single edge label `:REL {type: $t}` instead of `:$relType` |
| SDK API unverified | FIXED | Phase 0.5 added: `go get` + read SDK source before coding |
| `RelationType` name collision | FIXED | `RelationEdge` uses `models.RelationType` explicitly |
| Callback inside tx | FIXED | Fire batch callback AFTER tx commit |
| Startup sync blocks readiness | FIXED | Background goroutine, non-blocking |
| SearchManager constructor change | FIXED | Use `SetGraphStore(gs)` setter instead |
| `GetCluster` in spec not in plan | FIXED | Removed from spec (future scope) |
| Dockerfile in modified files | FIXED | Removed (no changes needed) |
| Variable-length path aggregation | NOTED | Will test Cypher against real FalkorDB in Phase 0.5 |
| Search cache staleness | NOTED | Acceptable — 30s TTL, graph expansion is supplementary |

## Phases

### Phase 0.5: SDK Verification (PREREQUISITE)
**Goal:** Verify FalkorDB Go SDK API surface against actual source code.

- Task 0.1: `go get github.com/falkordb/falkordb-go@latest`
- Task 0.2: Read SDK source: verify `FalkorDBNew`, `ConnectionOption` fields, `SelectGraph`, `Query` signatures, `QueryResult` iteration, `Close` method
- Task 0.3: Write minimal integration test against `unleashed.lan:6379`:
  - Create graph, MERGE nodes/edges, MATCH query, verify results
  - Test variable-length path: `MATCH (a)-[*1..2]-(b) RETURN b`
  - If SDK is broken → evaluate raw `go-redis` + `GRAPH.QUERY` as alternative
- Task 0.4: Document verified API in this plan

**Success:** Confirmed SDK API or documented alternative approach.

### Phase 1: Interface + Config (no FalkorDB dependency yet)
**Goal:** Define `GraphStore` interface and wire config. Zero new dependencies.

- Task 1.1: Add `GraphStore` interface to `internal/graph/store.go`
  ```go
  // GraphStore provides persistent graph operations for observation relations.
  // NOTE: This uses models.RelationType (string), NOT graph.RelationType (int)
  // which is an unrelated CSR-internal enum.
  type GraphStore interface {
      Ping(ctx context.Context) error
      StoreEdge(ctx context.Context, edge RelationEdge) error
      StoreEdgesBatch(ctx context.Context, edges []RelationEdge) error
      GetNeighbors(ctx context.Context, obsID int64, maxHops int, limit int) ([]Neighbor, error)
      GetPath(ctx context.Context, fromID, toID int64) ([]int64, error)
      SyncFromRelations(ctx context.Context, relations []*models.ObservationRelation) error
      Stats(ctx context.Context) (GraphStoreStats, error)
      Close() error
  }

  type RelationEdge struct {
      SourceID     int64
      TargetID     int64
      RelationType models.RelationType // string: "causes", "fixes", etc.
      Confidence   float64
  }

  type Neighbor struct {
      ObsID int64
      Hops  int
      RelationType models.RelationType
  }

  type GraphStoreStats struct {
      NodeCount int
      EdgeCount int
      Provider  string
      Connected bool
  }
  ```
- Task 1.2: Add `NoopGraphStore` implementation (returns empty results, `Ping` returns error "graph store not configured")
- Task 1.3: Add config fields to `internal/config/config.go`:
  - `GraphProvider string` — `"falkordb"` or `""` (default empty = disabled)
  - `FalkorDBAddr string` — e.g. `unleashed.lan:6379`
  - `FalkorDBPassword string` — env-only
  - `FalkorDBGraphName string` — default `"engram"`
- Task 1.4: Add env var loading: `ENGRAM_GRAPH_PROVIDER`, `ENGRAM_FALKORDB_ADDR`, `ENGRAM_FALKORDB_PASSWORD`, `ENGRAM_FALKORDB_GRAPH_NAME`
- Task 1.5: Tests for NoopGraphStore and config loading

**Files:** `internal/graph/store.go` (new), `internal/graph/noop.go` (new), `internal/config/config.go` (modify), `internal/config/config_test.go` (modify)
**Success:** `go build ./...` passes, config loads FalkorDB env vars, NoopGraphStore returns empty results.

### Phase 2: FalkorDB Implementation
**Goal:** Implement `GraphStore` against FalkorDB with Cypher queries.

- Task 2.1: Create `internal/graph/falkordb/client.go`:
  - `FalkorDBGraphStore` struct implementing `GraphStore`
  - Connect via SDK (verified in Phase 0.5)
  - `SelectGraph(graphName)` on init
  - Create index: `CREATE INDEX ON :Observation(id)` if not exists
- Task 2.2: Implement `StoreEdge` — **single edge label `:REL` with `type` property**:
  ```cypher
  MERGE (a:Observation {id: $src})
  MERGE (b:Observation {id: $tgt})
  MERGE (a)-[r:REL {type: $relType}]->(b)
  SET r.confidence = $conf
  ```
- Task 2.3: Implement `StoreEdgesBatch` — loop of MERGE in single call (FalkorDB supports multi-statement)
- Task 2.4: Implement `GetNeighbors` — tested query from Phase 0.5, likely:
  ```cypher
  MATCH (a:Observation {id: $id})-[r:REL*1..2]-(b:Observation)
  WHERE b.id <> $id
  RETURN DISTINCT b.id, length(r) as hops, head([x IN r | x.type]) as rel_type
  ORDER BY hops LIMIT $limit
  ```
  (Exact syntax verified in Phase 0.5)
- Task 2.5: Implement `GetPath` — `shortestPath` Cypher
- Task 2.6: Implement `SyncFromRelations` — batch MERGE (100 per call), progress logging
- Task 2.7: Implement `Ping` — `RETURN 1`
- Task 2.8: Implement `Stats` — `MATCH (n) RETURN count(n)` + `MATCH ()-[r]->() RETURN count(r)`
- Task 2.9: Implement `Close` — close underlying Redis connection (verify in Phase 0.5)
- Task 2.10: Factory function `NewGraphStore(cfg *config.Config) (graph.GraphStore, error)` — returns FalkorDBGraphStore or NoopGraphStore
- Task 2.11: Unit tests with mock (interface-based)
- Task 2.12: Integration test (`go test -run TestFalkorDB -tags integration` — skipped without env var)

**Files:** `internal/graph/falkordb/client.go` (new), `internal/graph/falkordb/client_test.go` (new), `internal/graph/factory.go` (new), `go.mod` (modify)
**Success:** Integration test passes against real FalkorDB at unleashed.lan:6379.

### Phase 3: Dual-Write Wiring
**Goal:** Every relation written to PostgreSQL is also written to FalkorDB (async, non-blocking).

- Task 3.1: Create `internal/graph/writer.go` — `AsyncGraphWriter`:
  - Buffered channel (capacity 1000)
  - Background goroutine draining channel, batching edges (flush every 100 edges or 500ms)
  - On channel full: drop + log warning (never block PG write path)
  - Graceful shutdown via context cancellation + channel drain
- Task 3.2: Add `OnRelationsStored` callback to `RelationStore`:
  - `type RelationCallback func(relations []*models.ObservationRelation)`
  - Set via `RelationStore.SetCallback(cb)`
  - Fire AFTER transaction commits in `StoreRelations`, not inside tx
  - For single `StoreRelation`: wrap in slice, fire callback
- Task 3.3: Wire `AsyncGraphWriter` into `internal/worker/service.go`:
  - Create GraphStore via factory during init
  - Create AsyncGraphWriter wrapping GraphStore
  - Set RelationStore callback → AsyncGraphWriter.Enqueue
- Task 3.4: Initial sync on startup — **background goroutine** (non-blocking):
  - If FalkorDB connected, spawn goroutine
  - Load all relations from PG in batches (1000 per query)
  - Call `SyncFromRelations` per batch
  - Log progress, don't block worker readiness
- Task 3.5: Tests for AsyncGraphWriter (channel full, batch flush, shutdown)

**Files:** `internal/graph/writer.go` (new), `internal/graph/writer_test.go` (new), `internal/worker/service.go` (modify), `internal/db/gorm/relation_store.go` (modify — add callback)
**Success:** Relations appear in FalkorDB within 500ms of PG write. Channel-full logs warning, doesn't block.

### Phase 4: Graph-Augmented Search
**Goal:** Search results expanded via graph neighbors when FalkorDB is available.

- Task 4.1: Add `SetGraphStore(gs GraphStore)` method to `SearchManager` (setter, not constructor change)
- Task 4.2: After RRF fusion in `hybridSearch()`, if GraphStore is not noop:
  - Take top-5 result IDs
  - `GetNeighbors(id, 2, 10)` for each (parallel, bounded)
  - Collect unique neighbor IDs not already in results
  - Fetch those observations from PG (batch query)
  - Apply score decay: parent_score × 0.7^hops
  - Merge into results, re-sort by score
  - Cap at original limit
- Task 4.3: Add `GraphExpansions int64` metric to `SearchMetrics`
- Task 4.4: Config: `GraphSearchExpansion bool` (default true when graph provider is set)
- Task 4.5: Tests with mock GraphStore (interface)

**Files:** `internal/search/manager.go` (modify), `internal/search/manager_test.go` (modify)
**Success:** Search with FalkorDB returns graph-expanded results. Without FalkorDB, identical behavior.

### Phase 5: MCP Tools + Deploy Config
**Goal:** Expose graph capabilities via MCP and update deployment configs.

- Task 5.1: Add `get_graph_neighbors` MCP tool:
  - Input: `observation_id` (required), `max_hops` (default 2), `limit` (default 20)
  - Output: list of `{id, title, type, project, relation_type, hops}`
  - If graph not configured: return `{error: "graph backend not configured"}`
- Task 5.2: Add `get_graph_stats` MCP tool:
  - Returns: `{provider, connected, node_count, edge_count}`
- Task 5.3: Update `docker-compose.yml` — add commented-out FalkorDB service:
  ```yaml
  # falkordb:
  #   image: falkordb/falkordb:latest
  #   ports:
  #     - "${FALKORDB_PORT:-6379}:6379"
  #   volumes:
  #     - falkordb_data:/data
  ```
- Task 5.4: Update Unraid template with optional FalkorDB env vars
- Task 5.5: Add `/api/graph/sync` POST endpoint for manual re-sync
- Task 5.6: Add graph stats to existing `/api/stats` response

**Files:** `internal/mcp/server.go` (modify), `docker-compose.yml` (modify), Unraid template (modify), `internal/worker/service.go` (modify), `internal/worker/handlers.go` (modify)
**Success:** MCP tools work. Docker compose can optionally include FalkorDB.

## Approach Decision

**Chosen approach:** Separate `GraphStore` interface (Approach A)
**Rationale:** Clean separation — in-memory CSR graph (`ObservationGraph`) stays for edge detection in consolidation. `GraphStore` is for persistent queries. No coupling between them.
**Alternatives rejected:**
- Approach B (extend ObservationGraph): Would mix in-memory compute with I/O
- Approach C (wrap RelationStore): Too tight coupling, hard to test independently

## Critical Decisions

- **Single edge label `:REL` with `type` property** (not parameterized rel types): OpenCypher doesn't support `$relType` after colon. `WHERE r.type = $t` is queryable and avoids 17 distinct patterns.
- **Async dual-write via buffered channel**: Non-blocking, drop-on-full policy. FalkorDB is supplementary — losing an edge write is acceptable, blocking PG is not.
- **Don't revive `graph_search.go`**: Coupled to dead sqlitevec types. Graph expansion in search/manager.go is simpler.
- **MERGE (not CREATE) for Cypher**: Idempotent — safe for re-sync, crash recovery, duplicates.
- **Setter pattern for SearchManager**: `SetGraphStore()` instead of constructor change — minimizes blast radius.
- **Background initial sync**: Non-blocking, doesn't delay worker readiness.
- **SDK-first, raw-Redis fallback**: Try `falkordb-go` SDK first. If broken, use `go-redis` + `GRAPH.QUERY` commands.

## Risks & Mitigations

- **Risk 1**: FalkorDB Go SDK immature (v0.x) → Mitigation: Phase 0.5 verification. Fallback to raw `go-redis` + `GRAPH.QUERY`.
- **Risk 2**: FalkorDB down → Mitigation: Timeout + error handling on all ops. NoopGraphStore fallback. Log warning, no crash.
- **Risk 3**: Large initial sync → Mitigation: Background goroutine, batch MERGE (100 per call), progress logging.
- **Risk 4**: Async write buffer pressure → Mitigation: Bounded channel (1000), drop policy, metrics.
- **Risk 5**: Variable-length path Cypher compatibility → Mitigation: Verified in Phase 0.5 against real FalkorDB.

## Files to Modify

**New files:**
- `internal/graph/store.go` — GraphStore interface + types
- `internal/graph/noop.go` — NoopGraphStore
- `internal/graph/factory.go` — NewGraphStore factory
- `internal/graph/writer.go` — AsyncGraphWriter
- `internal/graph/writer_test.go` — tests
- `internal/graph/falkordb/client.go` — FalkorDB implementation
- `internal/graph/falkordb/client_test.go` — tests

**Modified files:**
- `internal/config/config.go` — new fields + env vars
- `internal/config/config_test.go` — test new config
- `internal/db/gorm/relation_store.go` — OnRelationsStored callback (after tx commit)
- `internal/search/manager.go` — graph expansion post-RRF via setter
- `internal/worker/service.go` — wire GraphStore, AsyncGraphWriter, background sync
- `internal/mcp/server.go` — new MCP tools
- `internal/worker/handlers.go` — graph sync + stats endpoints
- `docker-compose.yml` — optional FalkorDB service (commented)
- `go.mod` — falkordb-go dependency

## Success Criteria

- [x] `go build ./...` succeeds with zero FalkorDB env vars (fallback mode)
- [x] With FalkorDB connected, edges dual-written within 500ms
- [x] Search returns graph-expanded results when FalkorDB available
- [x] FalkorDB failure → graceful degradation (logged warning, no crash)
- [x] `get_graph_neighbors` MCP tool returns multi-hop results
- [x] Bulk sync populates FalkorDB from PostgreSQL relations
- [ ] All tests pass, 80%+ coverage on new code (worker tests pre-broken on Windows)

## Plan Validation

**Critique result:** REVISE (3 critical + 6 warnings) → all addressed in revision
**Analysis:** Full codebase read of graph/, consolidation/, search/, relation_store, config, worker/service.
**SDK:** Context7 docs verified basic API. Phase 0.5 will verify against actual SDK source.
**Key findings addressed:**
- Cypher parameterized rel types → single `:REL` label with `type` property
- SDK API surface → Phase 0.5 mandatory verification step
- RelationType name collision → explicit `models.RelationType` usage with comment
- Constructor blast radius → setter pattern
- Sync blocking readiness → background goroutine
- Callback inside tx → fire after commit
