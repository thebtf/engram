package graph

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/pkg/models"
)

const (
	writerChannelCap = 1000
	writerBatchSize  = 100
	writerFlushMS    = 500
)

// AsyncGraphWriter buffers relation edges and writes them to a GraphStore
// asynchronously. It never blocks the PostgreSQL write path.
type AsyncGraphWriter struct {
	store  GraphStore
	ch     chan RelationEdge
	wg     sync.WaitGroup
	cancel context.CancelFunc

	// Metrics
	mu       sync.Mutex
	enqueued int64
	written  int64
	dropped  int64
}

// NewAsyncGraphWriter creates a writer that batches edges to the graph store.
func NewAsyncGraphWriter(store GraphStore) *AsyncGraphWriter {
	ctx, cancel := context.WithCancel(context.Background())
	w := &AsyncGraphWriter{
		store:  store,
		ch:     make(chan RelationEdge, writerChannelCap),
		cancel: cancel,
	}
	w.wg.Add(1)
	go w.loop(ctx)
	return w
}

// Enqueue adds edges derived from stored relations to the write buffer.
// Non-blocking: drops edges if the channel is full.
func (w *AsyncGraphWriter) Enqueue(relations []*models.ObservationRelation) {
	for _, rel := range relations {
		edge := RelationEdge{
			SourceID:     rel.SourceID,
			TargetID:     rel.TargetID,
			RelationType: rel.RelationType,
			Confidence:   rel.Confidence,
		}

		select {
		case w.ch <- edge:
			w.mu.Lock()
			w.enqueued++
			w.mu.Unlock()
		default:
			w.mu.Lock()
			w.dropped++
			w.mu.Unlock()
			log.Warn().Msg("AsyncGraphWriter: channel full, dropping edge")
		}
	}
}

// Close drains the channel and waits for the writer goroutine to finish.
func (w *AsyncGraphWriter) Close() {
	w.cancel()
	w.wg.Wait()
}

// Stats returns write statistics.
func (w *AsyncGraphWriter) Stats() (enqueued, written, dropped int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.enqueued, w.written, w.dropped
}

func (w *AsyncGraphWriter) loop(ctx context.Context) {
	defer w.wg.Done()

	batch := make([]RelationEdge, 0, writerBatchSize)
	ticker := time.NewTicker(writerFlushMS * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case edge, ok := <-w.ch:
			if !ok {
				w.flush(batch)
				return
			}
			batch = append(batch, edge)
			if len(batch) >= writerBatchSize {
				w.flush(batch)
				batch = batch[:0]
			}

		case <-ticker.C:
			if len(batch) > 0 {
				w.flush(batch)
				batch = batch[:0]
			}

		case <-ctx.Done():
			// Drain remaining edges from channel
			close(w.ch)
			for edge := range w.ch {
				batch = append(batch, edge)
			}
			w.flush(batch)
			return
		}
	}
}

func (w *AsyncGraphWriter) flush(batch []RelationEdge) {
	if len(batch) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := w.store.StoreEdgesBatch(ctx, batch)
	if err != nil {
		log.Error().Err(err).Int("count", len(batch)).Msg("AsyncGraphWriter: batch write failed")
		return
	}

	w.mu.Lock()
	w.written += int64(len(batch))
	w.mu.Unlock()
}
