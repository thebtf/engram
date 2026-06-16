package embedding

import (
	"context"
	"fmt"
	"time"

	"github.com/pgvector/pgvector-go"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// Backfill processes existing memories that don't have embedding chunks yet.
// Runs in batches, interruptible via context cancellation.
// rec may be nil; when non-nil it receives per-batch success and failure counts
// for process-lifetime telemetry surfaced by /api/stats/vnext.
func Backfill(ctx context.Context, db *gorm.DB, client *Client, store *Store, batchSize int, rec *BackfillRecorder) error {
	if db == nil {
		return fmt.Errorf("backfill: db required")
	}
	if client == nil || store == nil {
		return fmt.Errorf("backfill: client and store required")
	}
	if batchSize <= 0 {
		batchSize = 50
	}

	processed := 0
	for {
		select {
		case <-ctx.Done():
			log.Info().Int("processed", processed).Msg("backfill: interrupted")
			return ctx.Err()
		default:
		}

		// Find memories without chunks
		var memoryIDs []int64
		err := db.WithContext(ctx).Raw(`
			SELECT m.id FROM memories m
			LEFT JOIN content_chunks c ON c.memory_id = m.id
			WHERE m.deleted_at IS NULL AND c.id IS NULL
			ORDER BY m.created_at DESC
			LIMIT ?
		`, batchSize).Scan(&memoryIDs).Error
		if err != nil {
			return fmt.Errorf("backfill: query un-embedded: %w", err)
		}
		if len(memoryIDs) == 0 {
			log.Info().Int("total_processed", processed).Msg("backfill: complete")
			return nil
		}

		// Load memory content
		type memRow struct {
			ID      int64
			Content string
		}
		var rows []memRow
		if err := db.WithContext(ctx).Raw(
			"SELECT id, content FROM memories WHERE id IN ? AND deleted_at IS NULL", memoryIDs,
		).Scan(&rows).Error; err != nil {
			return fmt.Errorf("backfill: load content: %w", err)
		}

		// Batch embed
		texts := make([]string, len(rows))
		for i, r := range rows {
			texts[i] = r.Content
		}
		vectors, err := client.Embed(ctx, texts)
		if err != nil {
			log.Error().Err(err).Msg("backfill: embed batch failed")
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
			log.Warn().Int("batch_size", len(texts)).Msg("backfill: embed returned zero vectors, skipping batch")
			continue
		}

		// Build chunks. The vector-dimension guard lives in StoreChunks (the single
		// write chokepoint to content_chunks), so a wrong-sized vector is dropped there
		// with a logged warning rather than failing the whole vector(EmbeddingDim) batch.
		chunks := make([]Chunk, 0, len(rows))
		for i, r := range rows {
			if i < len(vectors) && len(vectors[i]) > 0 {
				chunks = append(chunks, Chunk{
					MemoryID:  r.ID,
					Seq:       0,
					Text:      r.Content,
					Embedding: pgvector.NewVector(vectors[i]),
					Model:     client.Model(),
				})
			}
		}
		if len(chunks) == 0 {
			log.Warn().Int("batch_size", len(texts)).Msg("backfill: no valid chunks produced, skipping batch")
			continue
		}
		if err := store.StoreChunks(ctx, chunks); err != nil {
			log.Error().Err(err).Msg("backfill: store chunks failed")
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

		if rec != nil {
			rec.RecordSuccess(len(chunks))
		}
		processed += len(chunks)
		if processed%100 == 0 || len(memoryIDs) < batchSize {
			log.Info().Int("processed", processed).Msg("backfill: progress")
		}
	}
}
