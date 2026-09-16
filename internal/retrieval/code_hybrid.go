package retrieval

// code_hybrid.go implements LegacyUnscopedCodeHybridSearch: concurrent FTS +
// vector retrieval over raw code_chunks project IDs, fused with RRF.
//
// This is an explicitly invoked compatibility reader. It has no UCI
// AuthorizedContext, View, or QueryResponse semantics. Current UCI retrieval
// must stay in the codebase intelligence application.
//
// Import-cycle decision: internal/db/gorm does NOT import internal/retrieval
// (verified: no such import exists in any non-test file under internal/db/gorm).
// internal/retrieval imports internal/db/gorm only in _test files, which are
// compile-time-isolated. Therefore the interface below can reference
// gorm.CodeSearchResult directly without forming a cycle. The local hit type
// keeps compatibility callers dependent on retrieval rather than gorm.

import (
	"context"
	"fmt"

	"golang.org/x/sync/errgroup"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

// LegacyUnscopedCodeSearchStore is the minimal raw-project persistence surface
// used by LegacyUnscopedCodeHybridSearch. It is legacy-only and must not back a
// UCI View-scoped query.
//
// Both methods return gorm.CodeSearchResult because internal/db/gorm is a safe
// dependency for internal/retrieval (no import cycle — see file header).
type LegacyUnscopedCodeSearchStore interface {
	// SearchCodeFTS returns FTS-ranked code chunks for projectID matching query.
	// Uses 'simple' text-search config to match the content_tsv generated column.
	SearchCodeFTS(ctx context.Context, projectID, query string, limit int) ([]gormdb.CodeSearchResult, error)
	// FindSimilarCode returns vector-ranked code chunks for projectID whose
	// embedding is within threshold cosine similarity of queryVec.
	// Rows with NULL embeddings are excluded.
	FindSimilarCode(ctx context.Context, projectID string, queryVec []float32, limit int, threshold float64) ([]gormdb.CodeSearchResult, error)
}

// LegacyUnscopedCodeHit is a result element from the raw-project compatibility
// reader. It carries the RRF-fused rank plus the per-hit score from the best
// available source (vector similarity when present, FTS rank otherwise).
type LegacyUnscopedCodeHit struct {
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

// LegacyUnscopedCodeHybridOptions configures one raw-project compatibility read.
type LegacyUnscopedCodeHybridOptions struct {
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

// LegacyUnscopedCodeHybridSearch runs hybrid (FTS + vector) retrieval over raw
// code_chunks project IDs and query strings, fusing results with Reciprocal Rank
// Fusion (RRF, k=60) via the shared retrieval.RRF function.
//
// It is not a UCI query path and must be reached only from the explicitly named
// legacy_unscoped MCP boundary.
//
// Degradation behaviour:
//   - QueryVec empty → FTS-only (vector leg skipped, not an error).
//   - opts.DenseOnly=true → vector-only (FTS leg skipped).
//   - Either leg errors → degrade to the other leg's results.
//   - Both legs fail → empty result, no error.
//
// projectID or query empty, or limit less than one, returns an error.
func LegacyUnscopedCodeHybridSearch(
	ctx context.Context,
	projectID, query string,
	limit int,
	store LegacyUnscopedCodeSearchStore,
	opts LegacyUnscopedCodeHybridOptions,
) ([]LegacyUnscopedCodeHit, error) {
	if err := validateLegacyUnscopedCodeSearch(projectID, query, limit); err != nil {
		return nil, err
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
	hitMap := make(map[int64]LegacyUnscopedCodeHit, len(ftsResults)+len(vecResults))
	for _, r := range ftsResults {
		hitMap[r.ID] = LegacyUnscopedCodeHit{
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
		hitMap[r.ID] = LegacyUnscopedCodeHit{
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
	out := make([]LegacyUnscopedCodeHit, 0, limit)
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

func validateLegacyUnscopedCodeSearch(projectID, query string, limit int) error {
	if projectID == "" {
		return fmt.Errorf("legacy_unscoped_code_hybrid_search: projectID must not be empty")
	}
	if query == "" {
		return fmt.Errorf("legacy_unscoped_code_hybrid_search: query must not be empty")
	}
	if limit < 1 {
		return fmt.Errorf("legacy_unscoped_code_hybrid_search: limit must be at least 1")
	}
	return nil
}
