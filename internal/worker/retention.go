package worker

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"
)

// auditRetainer is satisfied by *gorm.AuditStore and by test mocks.
// It isolates the retention path from the concrete store so unit tests
// can verify the call without a live database connection.
type auditRetainer interface {
	DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error)
}

// startRetentionCron launches a daily background cleanup goroutine that:
// 1. Deletes injection_log rows older than 90 days
// 2. Deletes citation_log rows older than 90 days
// 3. Deletes audit_log rows older than 90 days (T005)
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

// runRetentionCleanup deletes raw injection_log, citation_log, and audit_log
// rows whose timestamp is older than 90 days. Intended to be called from
// startRetentionCron.
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

	// T005: audit_log 90-day retention (FR-D2, EC-D4).
	// auditRetainer is satisfied by *gorm.AuditStore; testAuditRetainer is
	// used by unit tests to avoid a live DB connection.
	var ar auditRetainer
	if s.testAuditRetainer != nil {
		ar = s.testAuditRetainer
	} else if s.auditStore != nil {
		ar = s.auditStore
	}
	if ar != nil {
		n, err := ar.DeleteOlderThan(ctx, cutoff)
		if err != nil {
			log.Error().Err(err).Msg("retention: failed to cleanup audit_log")
		} else if n > 0 {
			log.Info().Int64("rows_deleted", n).Msg("retention: cleaned audit_log")
		}
	}
}
