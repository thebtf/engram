
# Phase 3A: Hybrid Search (RRF + BM25 Integration)

## Goal
Replace the current "vector OR filter" search with a true hybrid pipeline:
FTS (tsvector) + pgvector in parallel → BM25 normalization → RRF fusion.

## Files to Create/Modify

### 1. CREATE `internal/search/rrf.go`

Package `search`. Pure Go, no external dependencies.

```go
package search

import "sort"

// BM25Normalize converts a raw PostgreSQL ts_rank score to [0,1).
// formula: |x| / (1 + |x|)
func BM25Normalize(score float64) float64 {
    if score < 0 {
        score = -score
    }
    return score / (1 + score)
}

// ScoredID pairs a database row ID with a composite search score.
type ScoredID struct {
    DocType string  // "observation", "session", "prompt"
    Score   float64
    ID      int64
}

// RRF fuses multiple ranked lists using Reciprocal Rank Fusion (k=60).
// Each list must be sorted descending by score (best first).
//
// Weighting rules (from qmd spec):
//   - First two lists in the variadic args receive 2× weight multiplier
//   - Top-rank bonuses: rank=0 → +0.05, rank≤2 → +0.02
//
// Returns a deduplicated list sorted by fused score descending.
// Deduplication: same (ID, DocType) pair keeps the highest fused score.
func RRF(lists ...[]ScoredID) []ScoredID {
    const k = 60.0

    // accumulated fused scores keyed by "doctype:id"
    type key struct {
        docType string
        id      int64
    }
    scores := make(map[key]float64)
    order  := make([]key, 0)

    for listIdx, list := range lists {
        weight := 1.0
        if listIdx < 2 {
            weight = 2.0
        }
        for rank, item := range list {
            k := key{docType: item.DocType, id: item.ID}
            rankBonus := 0.0
            if rank == 0 {
                rankBonus = 0.05
            } else if rank <= 2 {
                rankBonus = 0.02
            }
            contrib := weight/(k+float64(rank)+1) + rankBonus
            if _, exists := scores[k]; !exists {
                order = append(order, k)
            }
            scores[k] += contrib
        }
    }

    result := make([]ScoredID, 0, len(scores))
    for _, k := range order {
        result = append(result, ScoredID{
            ID:      k.id,
            DocType: k.docType,
            Score:   scores[k],
        })
    }
    sort.Slice(result, func(i, j int) bool {
        return result[i].Score > result[j].Score
    })
    return result
}
```

### 2. MODIFY `internal/db/gorm/observation_store.go`

Add a new method `SearchObservationsFTSScored` that returns raw ts_rank scores.
Add it AFTER the existing `SearchObservationsFTS` method.

```go
// ScoredObservation pairs an observation with its BM25 relevance score.
type ScoredObservation struct {
    Observation *models.Observation
    Score       float64 // raw ts_rank score (before normalization)
}

// SearchObservationsFTSScored performs full-text search and returns BM25 scores.
// Scores are raw PostgreSQL ts_rank values; callers should normalize with BM25Normalize.
func (s *ObservationStore) SearchObservationsFTSScored(ctx context.Context, query, project string, limit int) ([]ScoredObservation, error) {
    ftsQuery := `
        SELECT o.id, o.sdk_session_id, o.project, o.session_id,
               o.title, o.subtitle, o.narrative, o.scope, o.type, o.status,
               o.is_superseded, o.superseded_by, o.importance_score,
               o.created_at, o.created_at_epoch, o.updated_at,
               o.concepts, o.files_read, o.files_modified, o.facts,
               ts_rank(o.search_vector, websearch_to_tsquery('english', $1)) AS rank_score
        FROM observations o
        WHERE o.search_vector @@ websearch_to_tsquery('english', $1)
          AND (o.project = $2 OR o.scope = 'global')
        ORDER BY rank_score DESC
        LIMIT $3`

    rows, err := s.rawDB.QueryContext(ctx, ftsQuery, query, project, limit)
    if err != nil {
        return nil, fmt.Errorf("fts scored query: %w", err)
    }
    defer rows.Close()

    var results []ScoredObservation
    for rows.Next() {
        var o Observation
        var rankScore float64
        // Scan all columns + rank_score
        if err := rows.Scan(
            &o.ID, &o.SDKSessionID, &o.Project, &o.SessionID,
            &o.Title, &o.Subtitle, &o.Narrative, &o.Scope, &o.Type, &o.Status,
            &o.IsSuperseded, &o.SupersededBy, &o.ImportanceScore,
            &o.CreatedAt, &o.CreatedAtEpoch, &o.UpdatedAt,
            &o.Concepts, &o.FilesRead, &o.FilesModified, &o.Facts,
            &rankScore,
        ); err != nil {
            return nil, fmt.Errorf("scan fts row: %w", err)
        }
        results = append(results, ScoredObservation{
            Observation: toModelObservation(&o),
            Score:       rankScore,
        })
    }
    return results, rows.Err()
}
```

**Important**: Look at the existing `SearchObservationsFTS` implementation to see
the exact column list used in the SELECT. The new method must scan the SAME columns
plus `rank_score` at the end. If the existing method scans fewer columns, match it exactly.

Also look at how `rawDB *sql.DB` is accessed in `ObservationStore` - check the struct definition
in the store to use the correct field name.

### 3. MODIFY `internal/search/manager.go`

Add a new `hybridSearch` method and update `executeSearch`.

**In `executeSearch`**: Change the routing logic to use `hybridSearch` when
both a query AND a vector client are available:

```go
func (m *Manager) executeSearch(ctx context.Context, params SearchParams) (*UnifiedSearchResult, error) {
    if params.Query != "" && m.vectorClient != nil && m.vectorClient.IsConnected() {
        // Use hybrid search: FTS + vector with RRF fusion
        return m.hybridSearch(ctx, params)
    }
    return m.filterSearch(ctx, params)
}
```

**Add new `hybridSearch` method**:

The hybrid search should:

1. Run FTS and vector searches concurrently using goroutines + channel/WaitGroup

2. FTS path (observations only):
   - Call `m.observationStore.SearchObservationsFTSScored(ctx, params.Query, params.Project, params.Limit*2)`
   - Normalize scores with `BM25Normalize(score)`
   - Build `[]ScoredID` list with DocType="observation"

3. Strong-signal short-circuit check:
   - If FTS returned results AND top BM25-normalized score >= 0.85 AND gap to #2 >= 0.15
   - Then skip vector search, use FTS results only

4. Vector path (all doc types):
   - Same as existing `vectorSearch`: call `m.vectorClient.Query(ctx, params.Query, params.Limit*2, where)`
   - Build `[]ScoredID` list using distance-to-similarity as score

5. RRF fusion:
   - Call `RRF(ftsList, vectorList)` → returns sorted fused IDs
   - Take top params.Limit

6. Fetch full records:
   - For fused observation IDs: `m.observationStore.GetObservationsByIDs(ctx, obsIDs, params.OrderBy, 0)`
   - For fused summary IDs: `m.summaryStore.GetSummariesByIDs(ctx, summaryIDs, params.OrderBy, 0)`
   - For fused prompt IDs: `m.promptStore.GetPromptsByIDs(ctx, promptIDs, params.OrderBy, 0)`
   - Apply ExcludeSuperseded filter for observations

7. Return `UnifiedSearchResult`

**Error handling**: If FTS fails, fall back to vector-only (existing vectorSearch).
If both fail, fall back to filterSearch.

## Constraints

- LANGUAGE: All file content MUST be English. No exceptions.
- Go 1.21+ syntax (use `range N` instead of `for i := 0; i < N; i++`)
- No new external dependencies — use only stdlib + already imported packages
- Handle nil/empty gracefully (empty FTS results → skip FTS list in RRF)
- All errors wrapped with fmt.Errorf
- Respect the existing struct/type naming conventions

## Do NOT

- Do NOT add new imports not already in go.mod
- Do NOT modify the test files
- Do NOT change the `SearchObservationsFTS` existing method signature
- Do NOT add collection-related code (that's Phase 3B)
- Do NOT change the filter search path

## Context: ObservationStore rawDB field

Check `internal/db/gorm/observation_store.go` struct definition for the exact field name of `*sql.DB`.
The existing `SearchObservationsFTS` uses `s.rawDB.QueryContext(...)` — use the same field.

## Context: Existing column list in SearchObservationsFTS

Read the existing `SearchObservationsFTS` method to see exactly which columns it scans.
Match them exactly in `SearchObservationsFTSScored`, adding `rank_score` at the end.
