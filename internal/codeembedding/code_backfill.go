// Package codeembedding backfills embeddings for persisted code chunks.
package codeembedding

import (
	"context"
	"strconv"
	"time"

	"github.com/pgvector/pgvector-go"
	"github.com/rs/zerolog/log"

	db_gorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/embedding"
)

// expectedDim is the vector dimension required by the code_chunks table.
// It is an alias for the package-wide SSOT EmbeddingDim (see dim.go) — both
// content_chunks and code_chunks share one dimension because one embedding model
// serves both. Persisting a wrong-dimension vector would corrupt the pgvector
// column or fail at INSERT time, so vectors that do not match are skipped with a
// logged warning.
const expectedDim = embedding.EmbeddingDim

// codeChunkSource is the minimal CodeChunkStore surface the backfill loop needs.
// *db_gorm.CodeChunkStore satisfies it; tests supply a fake so the loop logic
// (batching, dim guard, hot-loop backoff) is exercisable without a real DB.
type codeChunkSource interface {
	ListUnembedded(ctx context.Context, limit int) ([]*db_gorm.CodeChunk, error)
	UpdateEmbedding(ctx context.Context, id int64, vec pgvector.Vector) error
}

// embedder is the minimal embed surface the backfill loop needs. *embedding.Client
// satisfies it; tests supply a fake returning canned (or wrong-dim, or empty)
// vectors to drive the guard paths.
type embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

type codeEmbeddingBatch struct {
	chunks  []*db_gorm.CodeChunk
	vectors [][]float32
}

// CodeBackfill processes existing code_chunks rows whose embedding IS NULL,
// embedding them in batches and persisting the resulting vectors via
// CodeChunkStore.UpdateEmbedding. The loop is interruptible via ctx cancellation.
//
// This is the code-side mirror of Backfill (memory chunks). It runs as a
// long-lived background goroutine spawned by internal/worker/service.go in the
// same else-branch that gates on ENGRAM_EMBEDDING_URL being set.
//
// rec may be nil; when non-nil it receives per-batch success and failure counts
// for process-lifetime telemetry surfaced by /api/stats/vnext.
//
// batchSize <= 0 defaults to 50. Returns nil when all rows have been embedded,
// ctx.Err() on cancellation, or a fatal store error.
//
// CodeBackfill is the production entrypoint: it nil-checks the concrete
// dependencies and delegates to runCodeBackfill, which is written against
// interfaces so the loop is unit-testable with fakes.
func CodeBackfill(ctx context.Context, store *db_gorm.CodeChunkStore, client *embedding.Client, batchSize int, rec *embedding.BackfillRecorder) error {
	if store == nil {
		return nil
	}
	if client == nil {
		return nil
	}
	return runCodeBackfill(ctx, store, client, batchSize, rec)
}

// runCodeBackfill is the interface-driven loop body. See CodeBackfill for the
// behavioural contract; this split exists only so tests can drive the loop with
// fakes.
func runCodeBackfill(ctx context.Context, store codeChunkSource, client embedder, batchSize int, rec *embedding.BackfillRecorder) error {
	if batchSize <= 0 {
		batchSize = 50
	}
	processed := 0
	for {
		var complete bool
		var err error
		processed, complete, err = runCodeBackfillBatch(ctx, store, client, batchSize, rec, processed)
		if err != nil {
			return err
		}
		if complete {
			return nil
		}
	}
}

func runCodeBackfillBatch(ctx context.Context, store codeChunkSource, client embedder, batchSize int, rec *embedding.BackfillRecorder, processed int) (int, bool, error) {
	if err := ctx.Err(); err != nil {
		log.Info().Int("processed", processed).Msg("code backfill: interrupted")
		return processed, false, err
	}
	batch, complete, retry, err := loadCodeEmbeddingBatch(ctx, store, client, batchSize, rec)
	if err != nil {
		return processed, false, err
	}
	if complete {
		log.Info().Int("total_processed", processed).Msg("code backfill: complete")
		return processed, true, nil
	}
	if retry {
		return processed, false, nil
	}
	batchSuccess, complete, err := persistCodeEmbeddingBatchResult(ctx, store, batch, rec)
	if err != nil {
		return processed, false, err
	}
	processed += batchSuccess
	if complete {
		return processed, true, nil
	}
	if processed%100 == 0 || len(batch.chunks) < batchSize {
		log.Info().Int("processed", processed).Msg("code backfill: progress")
	}
	return processed, false, nil
}

func persistCodeEmbeddingBatchResult(ctx context.Context, store codeChunkSource, batch codeEmbeddingBatch, rec *embedding.BackfillRecorder) (int, bool, error) {
	batchSuccess, dimMismatch := persistCodeEmbeddingBatch(ctx, store, batch, rec)
	if rec != nil && batchSuccess > 0 {
		rec.RecordSuccess(batchSuccess)
	}
	if batchSuccess == 0 && dimMismatch == len(batch.chunks) {
		disableCodeBackfillForDimensionMismatch(len(batch.chunks), rec)
		return batchSuccess, true, nil
	}
	if batchSuccess != 0 {
		return batchSuccess, false, nil
	}
	log.Warn().Int("batch_size", len(batch.chunks)).Msg("code backfill: batch persisted zero embeddings (all rows guard-rejected); backing off")
	return batchSuccess, false, codeBackfillWait(ctx)
}

func loadCodeEmbeddingBatch(ctx context.Context, store codeChunkSource, client embedder, batchSize int, rec *embedding.BackfillRecorder) (codeEmbeddingBatch, bool, bool, error) {
	chunks, err := store.ListUnembedded(ctx, batchSize)
	if err != nil {
		log.Error().Err(err).Msg("code backfill: list unembedded failed")
		recordCodeBackfillFailure(rec, err.Error())
		return codeEmbeddingBatch{}, false, true, codeBackfillWait(ctx)
	}
	if len(chunks) == 0 {
		return codeEmbeddingBatch{}, true, false, nil
	}
	texts := make([]string, len(chunks))
	for index, chunk := range chunks {
		texts[index] = chunk.Content
	}
	vectors, err := client.Embed(ctx, texts)
	if err != nil {
		log.Error().Err(err).Msg("code backfill: embed batch failed")
		recordCodeBackfillFailure(rec, err.Error())
		return codeEmbeddingBatch{}, false, true, codeBackfillWait(ctx)
	}
	if len(vectors) == 0 {
		log.Warn().Int("batch_size", len(texts)).Msg("code backfill: embed returned zero vectors, backing off")
		recordCodeBackfillFailure(rec, "embed API returned zero vectors")
		return codeEmbeddingBatch{}, false, true, codeBackfillWait(ctx)
	}
	return codeEmbeddingBatch{chunks: chunks, vectors: vectors}, false, false, nil
}

func persistCodeEmbeddingBatch(ctx context.Context, store codeChunkSource, batch codeEmbeddingBatch, rec *embedding.BackfillRecorder) (int, int) {
	batchSuccess := 0
	dimMismatch := 0
	for index, chunk := range batch.chunks {
		if index >= len(batch.vectors) {
			log.Warn().Int64("chunk_id", chunk.ID).Int("expected_index", index).Int("vectors_returned", len(batch.vectors)).Msg("code backfill: vector missing for chunk, deferring")
			break
		}
		vector := batch.vectors[index]
		if len(vector) == 0 {
			log.Warn().Int64("chunk_id", chunk.ID).Msg("code backfill: empty vector for chunk, skipping")
			recordCodeBackfillFailure(rec, "empty vector returned by embed API")
			continue
		}
		if len(vector) != expectedDim {
			log.Error().Int64("chunk_id", chunk.ID).Int("got_dim", len(vector)).Int("expected_dim", expectedDim).Msg("code backfill: dimension mismatch, skipping chunk to avoid corrupt embedding")
			recordCodeBackfillFailure(rec, "dimension mismatch: expected 1536, got "+strconv.Itoa(len(vector)))
			dimMismatch++
			continue
		}
		if err := store.UpdateEmbedding(ctx, chunk.ID, pgvector.NewVector(vector)); err != nil {
			log.Error().Err(err).Int64("chunk_id", chunk.ID).Msg("code backfill: UpdateEmbedding failed")
			recordCodeBackfillFailure(rec, err.Error())
			continue
		}
		batchSuccess++
	}
	return batchSuccess, dimMismatch
}

func recordCodeBackfillFailure(rec *embedding.BackfillRecorder, reason string) {
	if rec != nil {
		rec.RecordFailure(0, reason)
	}
}

func disableCodeBackfillForDimensionMismatch(batchSize int, rec *embedding.BackfillRecorder) {
	log.Error().Int("batch_size", batchSize).Int("expected_dim", expectedDim).Msg("code backfill: every chunk rejected for dimension mismatch — embed model output dim != 1536; disabling code backfill (check ENGRAM_EMBEDDING_MODEL — code_chunks requires a 1536-dim model)")
	recordCodeBackfillFailure(rec, "code backfill disabled: embed model dimension != 1536")
}

func codeBackfillWait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Second):
		return nil
	}
}
