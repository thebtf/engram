package worker

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"
)

// startRetentionCron launches a daily background cleanup goroutine that:
// 1. Deletes injection_log rows older than 90 days
// 2. Deletes citation_log rows older than 90 days
//
// The first run is delayed by 5 minutes after startup so it does not compete
// with the main initialization path. Subsequent runs fire every 24 hours.
func (s *Service) startRetentionCron(ctx context.Context) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		timer := time.NewTimer(5 * time.Minute)
		defer timer.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				s.runRetentionCleanup(ctx)
				timer.Reset(24 * time.Hour)
			}
		}
	}()
}

// runRetentionCleanup deletes raw injection_log and citation_log rows whose
// timestamp is older than 90 days. Intended to be called from startRetentionCron.
func (s *Service) runRetentionCleanup(ctx context.Context) {
	cutoff := time.Now().Add(-90 * 24 * time.Hour)

	if s.injectionLogStore != nil {
		n, err := s.injectionLogStore.DeleteOlderThan(ctx, cutoff)
		if err != nil {
			log.Error().Err(err).Msg("retention: failed to cleanup injection_log")
		} else if n > 0 {
			log.Info().Int64("rows_deleted", n).Msg("retention: cleaned injection_log")
		}
	}

	if s.citationLogStore != nil {
		n, err := s.citationLogStore.DeleteOlderThan(ctx, cutoff)
		if err != nil {
			log.Error().Err(err).Msg("retention: failed to cleanup citation_log")
		} else if n > 0 {
			log.Info().Int64("rows_deleted", n).Msg("retention: cleaned citation_log")
		}
	}
}
