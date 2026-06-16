package embedding

// Import-cycle analysis (CR-004):
//
//   internal/embedding → (does NOT import) internal/db/gorm
//   internal/db/gorm   → (does NOT import) internal/embedding
//
// Placing CodeBackfill here adds internal/db/gorm as a new dependency of
// internal/embedding. No cycle is introduced because the dependency graph
// remains a DAG: db/gorm has no path back to embedding.
// internal/worker/service.go already imports both packages independently.

import (
	"context"
	"strconv"
	"time"

	"github.com/pgvector/pgvector-go"
	"github.com/rs/zerolog/log"

	db_gorm "github.com/thebtf/engram/internal/db/gorm"
)

// expectedDim is the vector dimension required by the code_chunks table.
// It is an alias for the package-wide SSOT EmbeddingDim (see dim.go) — both
// content_chunks and code_chunks share one dimension because one embedding model
// serves both. Persisting a wrong-dimension vector would corrupt the pgvector
// column or fail at INSERT time, so vectors that do not match are skipped with a
// logged warning.
const expectedDim = EmbeddingDim

// codeChunkSource is the minimal CodeChunkStore surface the backfill loop needs.
// *db_gorm.CodeChunkStore satisfies it; tests supply a fake so the loop logic
// (batching, dim guard, hot-loop backoff) is exercisable without a real DB.
type codeChunkSource interface {
	ListUnembedded(ctx context.Context, limit int) ([]*db_gorm.CodeChunk, error)
	UpdateEmbedding(ctx context.Context, id int64, vec pgvector.Vector) error
}

// embedder is the minimal embed surface the backfill loop needs. *Client
// satisfies it; tests supply a fake returning canned (or wrong-dim, or empty)
// vectors to drive the guard paths.
type embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
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
func CodeBackfill(ctx context.Context, store *db_gorm.CodeChunkStore, client *Client, batchSize int, rec *BackfillRecorder) error {
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
// a fake source and embedder.
func runCodeBackfill(ctx context.Context, store codeChunkSource, client embedder, batchSize int, rec *BackfillRecorder) error {
	if batchSize <= 0 {
		batchSize = 50
	}

	processed := 0
	for {
		// Respect context cancellation at the top of every iteration, mirroring
		// the pattern in Backfill to ensure the goroutine exits promptly on
		// server shutdown.
		select {
		case <-ctx.Done():
			log.Info().Int("processed", processed).Msg("code backfill: interrupted")
			return ctx.Err()
		default:
		}

		// Work query: rows with embedding IS NULL, id ASC for deterministic order.
		chunks, err := store.ListUnembedded(ctx, batchSize)
		if err != nil {
			log.Error().Err(err).Msg("code backfill: list unembedded failed")
			if rec != nil {
				rec.RecordFailure(0, err.Error())
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
			}
			continue
		}
		if len(chunks) == 0 {
			log.Info().Int("total_processed", processed).Msg("code backfill: complete")
			return nil
		}

		// Build the text batch in chunk order so vector index i matches chunk i.
		texts := make([]string, len(chunks))
		for i, c := range chunks {
			texts[i] = c.Content
		}

		vectors, err := client.Embed(ctx, texts)
		if err != nil {
			log.Error().Err(err).Msg("code backfill: embed batch failed")
			if rec != nil {
				rec.RecordFailure(0, err.Error())
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
			}
			continue
		}
		if len(vectors) == 0 {
			// Zero vectors from a 200-OK embed response (empty data array) makes
			// zero progress and is reached BEFORE the per-row persist loop, so it
			// bypasses the batchSuccess==0 backoff below. Back off here too —
			// otherwise this requeries the same NULL rows and hammers the embed
			// API at full loop speed (one ListUnembedded+Embed per iteration).
			log.Warn().Int("batch_size", len(texts)).Msg("code backfill: embed returned zero vectors, backing off")
			if rec != nil {
				rec.RecordFailure(0, "embed API returned zero vectors")
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
			}
			continue
		}

		// Persist each vector. Per-row dimension guard: a wrong-dim vector would
		// corrupt the pgvector column or fail at DB level — skip and record.
		// dimMismatch counts rows rejected specifically for a wrong dimension —
		// a DETERMINISTIC, model-level config error (vs transient embed/DB
		// failures). When a whole batch fails for this reason the loop disables
		// itself below rather than retrying the same rows forever.
		batchSuccess := 0
		dimMismatch := 0
		for i, chunk := range chunks {
			if i >= len(vectors) {
				// Embed returned fewer vectors than texts; remaining rows stay
				// unembedded and will be retried in the next iteration.
				log.Warn().
					Int64("chunk_id", chunk.ID).
					Int("expected_index", i).
					Int("vectors_returned", len(vectors)).
					Msg("code backfill: vector missing for chunk, deferring")
				break
			}
			vec := vectors[i]
			if len(vec) == 0 {
				log.Warn().Int64("chunk_id", chunk.ID).Msg("code backfill: empty vector for chunk, skipping")
				if rec != nil {
					rec.RecordFailure(0, "empty vector returned by embed API")
				}
				continue
			}
			if len(vec) != expectedDim {
				// Dimension mismatch guard: persisting a vector with the wrong
				// dimension silently corrupts pgvector search or fails at INSERT.
				log.Error().
					Int64("chunk_id", chunk.ID).
					Int("got_dim", len(vec)).
					Int("expected_dim", expectedDim).
					Msg("code backfill: dimension mismatch, skipping chunk to avoid corrupt embedding")
				if rec != nil {
					rec.RecordFailure(0, "dimension mismatch: expected 1536, got "+strconv.Itoa(len(vec)))
				}
				dimMismatch++
				continue
			}
			if err := store.UpdateEmbedding(ctx, chunk.ID, pgvector.NewVector(vec)); err != nil {
				log.Error().Err(err).Int64("chunk_id", chunk.ID).Msg("code backfill: UpdateEmbedding failed")
				if rec != nil {
					rec.RecordFailure(0, err.Error())
				}
				continue
			}
			batchSuccess++
		}

		if rec != nil && batchSuccess > 0 {
			rec.RecordSuccess(batchSuccess)
		}
		processed += batchSuccess

		// Deterministic-config guard: if EVERY row this batch was rejected for a
		// dimension mismatch, the configured embed model's output dimension is not
		// 1536 and never will be — retrying cannot help. This is the realistic OQ-5
		// misconfiguration where an operator points ENGRAM_EMBEDDING_URL at their
		// existing memory model (content_chunks is vector(4096) vs code_chunks
		// vector(1536)). DISABLE code backfill with a fatal-config log rather than
		// resending the same chunks to the external API forever and inflating
		// failure telemetry. Memory backfill (separate goroutine) is unaffected.
		// Returning nil (not an error) keeps the goroutine wrapper quiet — this is
		// a deliberate, logged stop, not a crash.
		if batchSuccess == 0 && dimMismatch == len(chunks) {
			log.Error().
				Int("batch_size", len(chunks)).
				Int("expected_dim", expectedDim).
				Msg("code backfill: every chunk rejected for dimension mismatch — embed model output dim != 1536; " +
					"disabling code backfill (check ENGRAM_EMBEDDING_MODEL — code_chunks requires a 1536-dim model)")
			if rec != nil {
				rec.RecordFailure(0, "code backfill disabled: embed model dimension != 1536")
			}
			return nil
		}

		// Hot-loop guard: a non-empty batch that persisted ZERO embeddings (for
		// reasons OTHER than a uniform dim mismatch — transient embed errors, a
		// flaky DB, a mix of failures) leaves those rows embedding IS NULL, so the
		// next ListUnembedded returns the SAME rows. Without a backoff this spins
		// at full CPU and hammers the embed API. Sleep so the retry is rate-limited;
		// a transient cause still recovers on the next pass.
		if batchSuccess == 0 {
			log.Warn().Int("batch_size", len(chunks)).
				Msg("code backfill: batch persisted zero embeddings (all rows guard-rejected); backing off")
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
			}
			continue
		}

		if processed%100 == 0 || len(chunks) < batchSize {
			log.Info().Int("processed", processed).Msg("code backfill: progress")
		}
	}
}
