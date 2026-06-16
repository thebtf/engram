package retrieval

// code_hybrid.go implements CodeHybridSearch: concurrent FTS + vector retrieval
// over the code_chunks table, fused with RRF (Reciprocal Rank Fusion).
//
// Import-cycle decision: internal/db/gorm does NOT import internal/retrieval
// (verified: no such import exists in any non-test file under internal/db/gorm).
// internal/retrieval imports internal/db/gorm only in _test files, which are
// compile-time-isolated. Therefore the interface below can reference
// gorm.CodeSearchResult directly without forming a cycle. The CodeHit local
// type is still defined here as the external result shape so that callers
// (CR-006 worker) depend on the retrieval package, not the gorm package, for
// search results — same pattern hybrid.go uses with models.Memory vs gorm rows.

import (
	"context"
	"fmt"

	"golang.org/x/sync/errgroup"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

// CodeSearchStoreInterface is the minimal interface CodeHybridSearch requires
// from the code chunk persistence layer. Keeping it here (rather than in the
// gorm package) preserves the retrieval package as the public API surface for
// search callers and enables fake implementations in tests without importing gorm.
//
// Both methods return gorm.CodeSearchResult because internal/db/gorm is a safe
// dependency for internal/retrieval (no import cycle — see file header).
type CodeSearchStoreInterface interface {
	// SearchCodeFTS returns FTS-ranked code chunks for projectID matching query.
	// Uses 'simple' text-search config to match the content_tsv generated column.
	SearchCodeFTS(ctx context.Context, projectID, query string, limit int) ([]gormdb.CodeSearchResult, error)
	// FindSimilarCode returns vector-ranked code chunks for projectID whose
	// embedding is within threshold cosine similarity of queryVec.
	// Rows with NULL embeddings are excluded.
	FindSimilarCode(ctx context.Context, projectID string, queryVec []float32, limit int, threshold float64) ([]gormdb.CodeSearchResult, error)
}

// CodeHit is the result element returned by CodeHybridSearch.
// It carries the RRF-fused rank plus the per-hit score from the best
// available source (vector similarity when present, FTS rank otherwise).
type CodeHit struct {
	ID        int64
	FilePath  string
	ByteStart int
	ByteEnd   int
	Language  string
	Content   string
	// Score is the raw retrieval score carried from the winning leg:
	// - vector leg present: cosine similarity (0..1)
	// - FTS leg only: ts_rank_cd value
	// Score is informational; RRF ordering governs the final slice order.
	Score float64
}

// CodeHybridOptions configures an individual CodeHybridSearch call.
type CodeHybridOptions struct {
	// QueryVec is the dense embedding of the query string. When nil or empty
	// the vector leg is skipped and retrieval degrades gracefully to FTS-only.
	QueryVec []float32
	// VecThreshold is the minimum cosine similarity for vector candidates [0,1].
	// When 0 the store default (0.7) applies.
	VecThreshold float64
	// DenseOnly skips the FTS leg entirely and uses only the vector leg.
	// This is the risk-4 fallback path documented in ADR-001 §8: if the
	// BM25-in-Postgres SQL planner is too slow under production load (see
	// benchmark gate in code_chunk_store_bench_test.go), callers can set
	// DenseOnly=true to avoid the tsvector GIN scan and merge overhead.
	// DenseOnly requires QueryVec to be non-empty; an empty QueryVec with
	// DenseOnly=true returns an empty result (not an error).
	DenseOnly bool
}

// CodeHybridSearch runs hybrid (FTS + vector) retrieval over the code_chunks
// table for a project and query string, fusing results with Reciprocal Rank
// Fusion (RRF, k=60) via the shared retrieval.RRF function.
//
// Concurrency model: both legs run concurrently via errgroup, mirroring
// HybridSearch in hybrid.go. A failure in either leg is non-fatal: the
// goroutine logs nothing (degrade silently, matching the FTS-degrade pattern in
// hybrid.go) and the other leg's results are returned alone.
//
// Degradation behaviour:
//   - QueryVec empty → FTS-only (vector leg skipped, not an error).
//   - opts.DenseOnly=true → vector-only (FTS leg skipped).
//   - Either leg errors → degrade to the other leg's results.
//   - Both legs fail → empty result, no error.
//
// limit <= 0 defaults to 10. projectID or query empty returns an error.
func CodeHybridSearch(
	ctx context.Context,
	projectID, query string,
	limit int,
	store CodeSearchStoreInterface,
	opts CodeHybridOptions,
) ([]CodeHit, error) {
	if projectID == "" {
		return nil, fmt.Errorf("code_hybrid_search: projectID must not be empty")
	}
	if query == "" {
		return nil, fmt.Errorf("code_hybrid_search: query must not be empty")
	}
	if limit <= 0 {
		limit = 10
	}

	const (
		ftsLimit = 50
		vecLimit = 50
		rrfK     = 60
	)

	var (
		ftsResults []gormdb.CodeSearchResult
		vecResults []gormdb.CodeSearchResult
	)

	eg, egCtx := errgroup.WithContext(ctx)

	// FTS leg — skipped when DenseOnly is set.
	if !opts.DenseOnly {
		eg.Go(func() error {
			rows, err := store.SearchCodeFTS(egCtx, projectID, query, ftsLimit)
			if err != nil {
				// FTS failure is non-fatal; degrade to vector-only result.
				return nil
			}
			ftsResults = rows
			return nil
		})
	}

	// Vector leg — skipped when QueryVec is empty.
	useVector := len(opts.QueryVec) > 0
	if useVector {
		eg.Go(func() error {
			rows, err := store.FindSimilarCode(egCtx, projectID, opts.QueryVec, vecLimit, opts.VecThreshold)
			if err != nil {
				// Vector failure is non-fatal; degrade to FTS-only result.
				return nil
			}
			vecResults = rows
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		// errgroup.Wait returns the first non-nil error returned by a goroutine.
		// Our goroutines return nil on failure (degrade pattern), so this path
		// is reached only for context cancellation or similar harness errors.
		return nil, err
	}

	// Build ID-ordered slices for RRF (rank = position in each result list).
	ftsIDs := make([]int64, len(ftsResults))
	for i, r := range ftsResults {
		ftsIDs[i] = r.ID
	}
	vecIDs := make([]int64, len(vecResults))
	for i, r := range vecResults {
		vecIDs[i] = r.ID
	}

	// Fuse with shared RRF (k=60, deterministic tie-break: score desc → best
	// rank asc → id asc). RRF is defined in hybrid.go and reused verbatim.
	fusedIDs := RRF(ftsIDs, vecIDs, rrfK)
	if len(fusedIDs) == 0 {
		return nil, nil
	}

	// Build a lookup map from both legs. Prefer the vector score when a chunk
	// appears in both legs (cosine similarity is better calibrated than ts_rank_cd).
	hitMap := make(map[int64]CodeHit, len(ftsResults)+len(vecResults))
	for _, r := range ftsResults {
		hitMap[r.ID] = CodeHit{
			ID:        r.ID,
			FilePath:  r.FilePath,
			ByteStart: r.ByteStart,
			ByteEnd:   r.ByteEnd,
			Language:  r.Language,
			Content:   r.Content,
			Score:     r.Score,
		}
	}
	for _, r := range vecResults {
		// Overwrite any FTS entry: vector similarity is preferred for the Score field.
		hitMap[r.ID] = CodeHit{
			ID:        r.ID,
			FilePath:  r.FilePath,
			ByteStart: r.ByteStart,
			ByteEnd:   r.ByteEnd,
			Language:  r.Language,
			Content:   r.Content,
			Score:     r.Score,
		}
	}

	// Materialise results in RRF order, capped at limit.
	out := make([]CodeHit, 0, limit)
	for _, id := range fusedIDs {
		if len(out) >= limit {
			break
		}
		if hit, ok := hitMap[id]; ok {
			out = append(out, hit)
		}
	}
	return out, nil
}
