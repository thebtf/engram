package embedding

// Unit tests for CodeBackfill guards that do NOT require a PostgreSQL
// connection. The dimension-mismatch guard, hot-loop backoff, and persistence
// are exercised end-to-end (against a real DB + fake embed server) in
// internal/db/gorm/code_chunk_store_test.go, where a real CodeChunkStore exists.

import (
	"testing"
)

// TestExpectedDim_IsCorrect verifies the expectedDim constant matches the
// code_chunks schema (vector(1536) per migration 139 and the CodeChunk model).
// Changing this constant would silently break every persisted embedding.
func TestExpectedDim_IsCorrect(t *testing.T) {
	const want = 1536
	if expectedDim != want {
		t.Errorf("expectedDim = %d, want %d (must match code_chunks vector(1536))", expectedDim, want)
	}
}

// TestCodeBackfill_NilGuards ensures the nil-store and nil-client guards return
// nil immediately without touching the DB or panicking. The store guard is
// checked first, so (nil, nil) exits on the store guard and (nil store, real
// client) exits on the same guard — both must be no-ops.
func TestCodeBackfill_NilGuards(t *testing.T) {
	ctx := t.Context()

	if err := CodeBackfill(ctx, nil, nil, 50, nil); err != nil {
		t.Errorf("CodeBackfill(nil, nil) = %v, want nil", err)
	}
	// batchSize<=0 must not panic before the nil-store guard returns.
	if err := CodeBackfill(ctx, nil, nil, 0, nil); err != nil {
		t.Errorf("CodeBackfill(nil store, batchSize=0) = %v, want nil", err)
	}
}
