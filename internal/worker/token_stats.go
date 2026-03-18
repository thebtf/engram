// Package worker provides the buffered token stats flusher.
package worker

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"
)

// tokenStatsFlushInterval is how often accumulated token usage counts are flushed to the database.
const tokenStatsFlushInterval = 5 * time.Second

// startTokenStatsFlusher starts a background goroutine that reads token IDs from
// the stats channel and batches IncrementStats calls to the database every 5 seconds.
// This avoids per-request UPDATE overhead for high-throughput client token auth.
func (s *Service) startTokenStatsFlusher(ctx context.Context) {
	if s.tokenAuth == nil {
		return
	}

	ch := s.tokenAuth.StatsCh()
	if ch == nil {
		return
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		pending := make(map[string]int) // token ID -> request count
		ticker := time.NewTicker(tokenStatsFlushInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				// Final flush on shutdown
				s.flushTokenStats(pending)
				return

			case tokenID := <-ch:
				pending[tokenID]++

			case <-ticker.C:
				s.flushTokenStats(pending)
				// Reset the map
				for k := range pending {
					delete(pending, k)
				}
			}
		}
	}()
}

// flushTokenStats writes accumulated token usage counts to the database.
func (s *Service) flushTokenStats(counts map[string]int) {
	if len(counts) == 0 {
		return
	}

	s.initMu.RLock()
	store := s.tokenStore
	s.initMu.RUnlock()

	if store == nil {
		return
	}

	if err := store.BatchIncrementStats(context.Background(), counts); err != nil {
		log.Warn().Err(err).Int("tokens", len(counts)).Msg("auth: failed to flush token stats")
	}
}
