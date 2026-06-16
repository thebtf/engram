package embedding

// Unit tests for CodeBackfill guards that do NOT require a PostgreSQL
// connection. The dimension-mismatch guard, hot-loop backoff, and persistence
// are exercised end-to-end (against a real DB + fake embed server) in
// internal/db/gorm/code_chunk_store_test.go, where a real CodeChunkStore exists.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pgvector/pgvector-go"

	db_gorm "github.com/thebtf/engram/internal/db/gorm"
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

// ----------------------------------------------------------------------------
// Fake-driven loop tests for runCodeBackfill. These exercise the guard paths
// (zero-vectors backoff, dim-mismatch backoff, happy-path persistence) without
// a real DB or embed server, by driving the loop through the codeChunkSource +
// embedder interfaces.
// ----------------------------------------------------------------------------

// fakeCodeSource serves a fixed set of un-embedded chunks and records embeddings
// written back via UpdateEmbedding (removing them from the un-embedded set).
type fakeCodeSource struct {
	mu          chan struct{} // 1-slot semaphore as a simple mutex
	rows        map[int64]*db_gorm.CodeChunk
	listCalls   int32
	updateCalls int32
}

func newFakeCodeSource(rows []*db_gorm.CodeChunk) *fakeCodeSource {
	m := make(map[int64]*db_gorm.CodeChunk, len(rows))
	for _, r := range rows {
		m[r.ID] = r
	}
	s := &fakeCodeSource{mu: make(chan struct{}, 1), rows: m}
	s.mu <- struct{}{}
	return s
}

func (f *fakeCodeSource) lock()   { <-f.mu }
func (f *fakeCodeSource) unlock() { f.mu <- struct{}{} }

func (f *fakeCodeSource) ListUnembedded(_ context.Context, limit int) ([]*db_gorm.CodeChunk, error) {
	atomic.AddInt32(&f.listCalls, 1)
	f.lock()
	defer f.unlock()
	out := make([]*db_gorm.CodeChunk, 0, limit)
	for _, r := range f.rows {
		if len(out) >= limit {
			break
		}
		out = append(out, r)
	}
	return out, nil
}

func (f *fakeCodeSource) UpdateEmbedding(_ context.Context, id int64, _ pgvector.Vector) error {
	atomic.AddInt32(&f.updateCalls, 1)
	f.lock()
	defer f.unlock()
	delete(f.rows, id) // embedded → no longer un-embedded
	return nil
}

// fakeEmbedder returns a canned vector per text. vecLen controls the dimension
// (1536 for the happy path, a wrong value to drive the dim guard, 0 texts→nil
// for the zero-vectors path when emptyReturn is set).
type fakeEmbedder struct {
	vecLen      int
	emptyReturn bool
	calls       int32
}

func (e *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	atomic.AddInt32(&e.calls, 1)
	if e.emptyReturn {
		return [][]float32{}, nil // 200-OK with empty data array
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = make([]float32, e.vecLen)
	}
	return out, nil
}

// runWithDeadline runs runCodeBackfill in a goroutine and fails if it does not
// return within d (i.e. it hot-looped instead of backing off / completing).
func runWithDeadline(t *testing.T, d time.Duration, ctx context.Context, src codeChunkSource, emb embedder, rec *BackfillRecorder) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- runCodeBackfill(ctx, src, emb, 50, rec) }()
	select {
	case err := <-done:
		return err
	case <-time.After(d):
		t.Fatalf("runCodeBackfill did not return within %s — likely hot-looping", d)
		return nil
	}
}

// TestRunCodeBackfill_HappyPath persists every chunk and then returns nil once
// ListUnembedded is empty.
func TestRunCodeBackfill_HappyPath(t *testing.T) {
	t.Parallel()
	src := newFakeCodeSource([]*db_gorm.CodeChunk{
		{ID: 1, Content: "func A(){}"},
		{ID: 2, Content: "func B(){}"},
	})
	emb := &fakeEmbedder{vecLen: expectedDim}
	rec := &BackfillRecorder{}

	err := runWithDeadline(t, 5*time.Second, context.Background(), src, emb, rec)
	if err != nil {
		t.Fatalf("runCodeBackfill = %v, want nil", err)
	}
	if got := atomic.LoadInt32(&src.updateCalls); got != 2 {
		t.Errorf("UpdateEmbedding calls = %d, want 2", got)
	}
	if len(src.rows) != 0 {
		t.Errorf("un-embedded rows remaining = %d, want 0", len(src.rows))
	}
}

// TestRunCodeBackfill_ZeroVectorsBacksOff is the regression test for the HIGH
// finding: a 200-OK empty-vector response must NOT hot-loop. We cancel the ctx
// during the backoff sleep; the loop must return ctx.Err() promptly rather than
// spinning. Without the backoff fix this test fails by deadline (hot loop).
func TestRunCodeBackfill_ZeroVectorsBacksOff(t *testing.T) {
	t.Parallel()
	src := newFakeCodeSource([]*db_gorm.CodeChunk{{ID: 1, Content: "x"}})
	emb := &fakeEmbedder{emptyReturn: true} // always returns zero vectors
	rec := &BackfillRecorder{}

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel shortly after the first embed call so we land inside the 5s backoff.
	go func() {
		for atomic.LoadInt32(&emb.calls) == 0 {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()

	err := runWithDeadline(t, 3*time.Second, ctx, src, emb, rec)
	if err != context.Canceled {
		t.Fatalf("runCodeBackfill = %v, want context.Canceled (loop must back off, not spin)", err)
	}
	// The row must remain un-embedded (nothing was persisted).
	if got := atomic.LoadInt32(&src.updateCalls); got != 0 {
		t.Errorf("UpdateEmbedding calls = %d, want 0 (zero-vector batch persists nothing)", got)
	}
	// At most a couple of embed calls before cancel — definitely not a hot loop.
	if got := atomic.LoadInt32(&emb.calls); got > 3 {
		t.Errorf("embed calls = %d, want <= 3 (backoff active, not hot-looping)", got)
	}
}

// TestRunCodeBackfill_DimMismatchBacksOff drives the dimension guard: every
// returned vector has the wrong length, so batchSuccess stays 0 and the loop
// must back off (not spin). Cancel during the backoff and expect ctx.Canceled.
func TestRunCodeBackfill_DimMismatchBacksOff(t *testing.T) {
	t.Parallel()
	src := newFakeCodeSource([]*db_gorm.CodeChunk{{ID: 1, Content: "x"}})
	emb := &fakeEmbedder{vecLen: 8} // wrong dim (not 1536)
	rec := &BackfillRecorder{}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for atomic.LoadInt32(&emb.calls) == 0 {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()

	err := runWithDeadline(t, 3*time.Second, ctx, src, emb, rec)
	if err != context.Canceled {
		t.Fatalf("runCodeBackfill = %v, want context.Canceled (dim-mismatch must back off, not spin)", err)
	}
	if got := atomic.LoadInt32(&src.updateCalls); got != 0 {
		t.Errorf("UpdateEmbedding calls = %d, want 0 (wrong-dim vectors must not persist)", got)
	}
}
