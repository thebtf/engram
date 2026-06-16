package retrieval

// Pure-logic tests for CodeHybridSearch using a fake CodeSearchStoreInterface.
// No database required — these tests always run in CI.
//
// Coverage:
//   (a) RRF fusion order: a chunk in BOTH legs ranks above one in a single leg.
//   (b) FTS-only degradation when QueryVec is empty.
//   (c) DenseOnly=true skips the FTS leg.
//   (d) One leg erroring degrades to the other leg's results rather than failing.

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

// fakeCodeStore is a controllable fake implementation of CodeSearchStoreInterface.
// Each method returns a canned result or error set by the test.
type fakeCodeStore struct {
	ftsResults []gormdb.CodeSearchResult
	ftsErr     error
	vecResults []gormdb.CodeSearchResult
	vecErr     error

	// Call tracking for the DenseOnly / FTS-skip assertions.
	ftsCalled bool
	vecCalled bool
}

func (f *fakeCodeStore) SearchCodeFTS(_ context.Context, _, _ string, _ int) ([]gormdb.CodeSearchResult, error) {
	f.ftsCalled = true
	return f.ftsResults, f.ftsErr
}

func (f *fakeCodeStore) FindSimilarCode(_ context.Context, _ string, _ []float32, _ int, _ float64) ([]gormdb.CodeSearchResult, error) {
	f.vecCalled = true
	return f.vecResults, f.vecErr
}

// codeResult is a convenience constructor for test fixtures.
func codeResult(id int64, path string, score float64) gormdb.CodeSearchResult {
	return gormdb.CodeSearchResult{
		ID:        id,
		FilePath:  path,
		ByteStart: 0,
		ByteEnd:   100,
		Language:  "go",
		Content:   "func stub() {}",
		Score:     score,
	}
}

// TestCodeHybridSearch_RRFFusionOrder asserts that a chunk appearing in BOTH the
// FTS and vector legs ranks above chunks that appear in only one leg.
//
// RRF score for a chunk in both lists = 1/(1+k+1) + 1/(1+k+1) = 2/(k+2).
// RRF score for a chunk in one list  = 1/(1+k+1) = 1/(k+2).
// Therefore the shared chunk must always rank first.
func TestCodeHybridSearch_RRFFusionOrder(t *testing.T) {
	// ID 10: appears in BOTH legs (rank 0 in FTS, rank 0 in vector).
	// ID 20: appears in FTS only (rank 1 in FTS).
	// ID 30: appears in vector only (rank 1 in vector).
	store := &fakeCodeStore{
		ftsResults: []gormdb.CodeSearchResult{
			codeResult(10, "shared.go", 0.9),
			codeResult(20, "fts_only.go", 0.5),
		},
		vecResults: []gormdb.CodeSearchResult{
			codeResult(10, "shared.go", 0.95),
			codeResult(30, "vec_only.go", 0.8),
		},
	}

	ctx := context.Background()
	hits, err := CodeHybridSearch(ctx, "proj", "query", 10, store, CodeHybridOptions{
		QueryVec: []float32{0.1, 0.2, 0.3},
	})
	require.NoError(t, err)
	require.NotEmpty(t, hits)

	// The shared chunk (ID 10) must rank first.
	require.Equal(t, int64(10), hits[0].ID, "shared chunk must rank first via RRF")

	// Vector score preferred: shared chunk Score should be 0.95 (vec leg), not 0.9 (FTS).
	require.InDelta(t, 0.95, hits[0].Score, 0.001, "vector similarity must be preferred for Score when present")

	// IDs 20 and 30 must both appear (order between them follows RRF).
	ids := make(map[int64]bool)
	for _, h := range hits {
		ids[h.ID] = true
	}
	require.True(t, ids[20], "FTS-only chunk must appear in results")
	require.True(t, ids[30], "vector-only chunk must appear in results")
}

// TestCodeHybridSearch_FTSOnlyWhenQueryVecEmpty asserts that passing an empty
// QueryVec skips the vector leg entirely and returns FTS results.
func TestCodeHybridSearch_FTSOnlyWhenQueryVecEmpty(t *testing.T) {
	store := &fakeCodeStore{
		ftsResults: []gormdb.CodeSearchResult{
			codeResult(1, "a.go", 0.8),
			codeResult(2, "b.go", 0.6),
		},
		vecResults: []gormdb.CodeSearchResult{
			codeResult(99, "should_not_appear.go", 0.99),
		},
	}

	ctx := context.Background()
	hits, err := CodeHybridSearch(ctx, "proj", "query", 10, store, CodeHybridOptions{
		QueryVec: nil, // empty → FTS-only
	})
	require.NoError(t, err)
	require.NotEmpty(t, hits)

	// Vector leg must NOT have been called.
	require.False(t, store.vecCalled, "vector leg must be skipped when QueryVec is nil")

	// FTS leg must have been called.
	require.True(t, store.ftsCalled, "FTS leg must run when QueryVec is nil")

	// ID 99 (vector-only) must not appear.
	for _, h := range hits {
		require.NotEqual(t, int64(99), h.ID, "vector-only result must not appear in FTS-only mode")
	}
}

// TestCodeHybridSearch_DenseOnlySkipsFTS asserts that DenseOnly=true calls only
// the vector leg and returns vector results, skipping FTS entirely.
func TestCodeHybridSearch_DenseOnlySkipsFTS(t *testing.T) {
	store := &fakeCodeStore{
		ftsResults: []gormdb.CodeSearchResult{
			codeResult(99, "fts_should_not_appear.go", 0.9),
		},
		vecResults: []gormdb.CodeSearchResult{
			codeResult(1, "vec_a.go", 0.85),
			codeResult(2, "vec_b.go", 0.75),
		},
	}

	ctx := context.Background()
	hits, err := CodeHybridSearch(ctx, "proj", "query", 10, store, CodeHybridOptions{
		QueryVec:  []float32{0.1, 0.2},
		DenseOnly: true,
	})
	require.NoError(t, err)
	require.NotEmpty(t, hits)

	// FTS leg must NOT have been called.
	require.False(t, store.ftsCalled, "FTS leg must be skipped when DenseOnly is true")

	// Vector leg must have been called.
	require.True(t, store.vecCalled, "vector leg must run when DenseOnly is true")

	// ID 99 (FTS-only) must not appear.
	for _, h := range hits {
		require.NotEqual(t, int64(99), h.ID, "FTS-only result must not appear in DenseOnly mode")
	}
}

// TestCodeHybridSearch_FTSErrorDegradesToVector asserts that when the FTS leg
// returns an error, CodeHybridSearch does not fail but returns the vector leg's
// results.
func TestCodeHybridSearch_FTSErrorDegradesToVector(t *testing.T) {
	store := &fakeCodeStore{
		ftsErr: errors.New("FTS index unavailable"),
		vecResults: []gormdb.CodeSearchResult{
			codeResult(5, "vec_fallback.go", 0.9),
		},
	}

	ctx := context.Background()
	hits, err := CodeHybridSearch(ctx, "proj", "query", 10, store, CodeHybridOptions{
		QueryVec: []float32{0.1, 0.2},
	})
	require.NoError(t, err, "FTS error must degrade gracefully, not propagate")
	require.NotEmpty(t, hits)
	require.Equal(t, int64(5), hits[0].ID, "vector result must be returned after FTS error")
}

// TestCodeHybridSearch_VectorErrorDegradesToFTS asserts that when the vector leg
// returns an error, CodeHybridSearch does not fail but returns the FTS leg's
// results.
func TestCodeHybridSearch_VectorErrorDegradesToFTS(t *testing.T) {
	store := &fakeCodeStore{
		ftsResults: []gormdb.CodeSearchResult{
			codeResult(7, "fts_fallback.go", 0.8),
		},
		vecErr: errors.New("embedding service timeout"),
	}

	ctx := context.Background()
	hits, err := CodeHybridSearch(ctx, "proj", "query", 10, store, CodeHybridOptions{
		QueryVec: []float32{0.1, 0.2},
	})
	require.NoError(t, err, "vector error must degrade gracefully, not propagate")
	require.NotEmpty(t, hits)
	require.Equal(t, int64(7), hits[0].ID, "FTS result must be returned after vector error")
}

// TestCodeHybridSearch_BothLegsErrorReturnsEmpty asserts that when both legs
// fail the result is empty (nil) with no error.
func TestCodeHybridSearch_BothLegsErrorReturnsEmpty(t *testing.T) {
	store := &fakeCodeStore{
		ftsErr: errors.New("FTS down"),
		vecErr: errors.New("vector down"),
	}

	ctx := context.Background()
	hits, err := CodeHybridSearch(ctx, "proj", "query", 10, store, CodeHybridOptions{
		QueryVec: []float32{0.1, 0.2},
	})
	require.NoError(t, err, "dual leg failure must not propagate an error")
	require.Empty(t, hits, "empty result expected when both legs fail")
}

// TestCodeHybridSearch_LimitRespected asserts that the returned slice is capped
// at the requested limit even when both legs return more results.
func TestCodeHybridSearch_LimitRespected(t *testing.T) {
	var ftsResults, vecResults []gormdb.CodeSearchResult
	for i := int64(0); i < 30; i++ {
		ftsResults = append(ftsResults, codeResult(i, fmt.Sprintf("f%d.go", i), float64(30-i)))
	}
	for i := int64(20); i < 50; i++ {
		vecResults = append(vecResults, codeResult(i, fmt.Sprintf("v%d.go", i), float64(50-i)))
	}

	store := &fakeCodeStore{ftsResults: ftsResults, vecResults: vecResults}

	ctx := context.Background()
	hits, err := CodeHybridSearch(ctx, "proj", "query", 5, store, CodeHybridOptions{
		QueryVec: []float32{0.1},
	})
	require.NoError(t, err)
	require.LessOrEqual(t, len(hits), 5, "result must be capped at limit=5")
}

// TestCodeHybridSearch_InvalidInputErrors asserts that empty projectID or query
// return an error.
func TestCodeHybridSearch_InvalidInputErrors(t *testing.T) {
	store := &fakeCodeStore{}
	ctx := context.Background()

	_, err := CodeHybridSearch(ctx, "", "query", 10, store, CodeHybridOptions{})
	require.Error(t, err, "empty projectID must return error")

	_, err = CodeHybridSearch(ctx, "proj", "", 10, store, CodeHybridOptions{})
	require.Error(t, err, "empty query must return error")
}
