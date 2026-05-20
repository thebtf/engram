# Technical Debt

Items deferred from active work with documented impact and fix path.

## Active

### TD-001: Ingestion chunks bypass write-gate
**Found:** 2026-05-21, code review of Milestone C
**Impact:** Duplicate document chunks stored without Jaccard dedup detection. Wastes storage, may produce duplicate injection candidates.
**Fix:** Wire `writegate.Check()` in `internal/mcp/tools_ingest.go` before `memoryStore.Create`. Requires loading existing project memories for comparison (same pattern as `tools_memory.go` store_memory handler).
**Severity:** MEDIUM

### TD-002: Graph traverse returns duplicate edges at different depths
**Found:** 2026-05-21, code review of Milestone C
**Impact:** Cosmetic — same edge appears in traverse results at depth=1 and depth=2 when traversed from both endpoints. No correctness impact; callers see redundant entries.
**Fix:** Add `visitedEdges map[int64]bool` in `internal/graph/traverse.go` Traverse function, skip edges already in results.
**Severity:** LOW
