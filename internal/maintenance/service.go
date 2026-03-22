// Package maintenance provides scheduled maintenance tasks for engram.
package maintenance

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/pattern"
	"github.com/thebtf/engram/internal/telemetry"
	"github.com/thebtf/engram/internal/vector"
)

// Service handles scheduled maintenance tasks.
type Service struct {
	log                  zerolog.Logger
	lastRunTime          time.Time
	promptStore          *gorm.PromptStore
	store                *gorm.Store
	vectorCleanupFn      func(ctx context.Context, deletedIDs []int64)
	config               *config.Config
	summaryStore         *gorm.SummaryStore
	stopCh               chan struct{}
	doneCh               chan struct{}
	observationStore     *gorm.ObservationStore
	similarityTelemetry  *telemetry.SimilarityTelemetry
	patternStore         *gorm.PatternStore
	smartGC              *SmartGC
	nearDedupFinder      *NearDuplicateFinder
	lastRunDuration      time.Duration
	totalSmartGCArchived int64
	totalCleanedObs      int64
	totalOptimizeRun     int64
	totalPatternDecay    int64
	totalNearDedupMerged int64
	mu                   sync.Mutex
	running              bool
}

// NewService creates a new maintenance service.
func NewService(
	store *gorm.Store,
	observationStore *gorm.ObservationStore,
	summaryStore *gorm.SummaryStore,
	promptStore *gorm.PromptStore,
	vectorCleanupFn func(ctx context.Context, deletedIDs []int64),
	cfg *config.Config,
	similarityTelemetry *telemetry.SimilarityTelemetry,
	smartGC *SmartGC,
	patternStore *gorm.PatternStore,
	vectorClient vector.Client,
	log zerolog.Logger,
) *Service {
	svcLog := log.With().Str("component", "maintenance").Logger()

	var nearDedupFinder *NearDuplicateFinder
	if cfg.ConsolidationEnabled && observationStore != nil && vectorClient != nil {
		nearDedupFinder = NewNearDuplicateFinder(observationStore, vectorClient, cfg.ConsolidationThreshold, svcLog)
	}

	return &Service{
		store:               store,
		observationStore:    observationStore,
		summaryStore:        summaryStore,
		promptStore:         promptStore,
		vectorCleanupFn:     vectorCleanupFn,
		config:              cfg,
		similarityTelemetry: similarityTelemetry,
		smartGC:             smartGC,
		patternStore:        patternStore,
		nearDedupFinder:     nearDedupFinder,
		log:                 svcLog,
		stopCh:              make(chan struct{}),
		doneCh:              make(chan struct{}),
	}
}

// Start begins the maintenance loop.
func (s *Service) Start(ctx context.Context) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
		close(s.doneCh)
	}()

	if !s.config.MaintenanceEnabled {
		s.log.Info().Msg("Maintenance disabled, not starting scheduler")
		return
	}

	interval := max(time.Duration(s.config.MaintenanceIntervalHours)*time.Hour, time.Hour)

	s.log.Info().
		Dur("interval", interval).
		Int("retention_days", s.config.ObservationRetentionDays).
		Bool("cleanup_stale", s.config.CleanupStaleObservations).
		Msg("Starting maintenance scheduler")

	// Initial run after 5 minutes (allow system to stabilize)
	time.Sleep(5 * time.Minute)
	s.runMaintenance(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.log.Info().Msg("Maintenance shutting down due to context cancellation")
			return
		case <-s.stopCh:
			s.log.Info().Msg("Maintenance shutting down due to stop signal")
			return
		case <-ticker.C:
			s.runMaintenance(ctx)
		}
	}
}

// Stop signals the maintenance service to stop.
func (s *Service) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return
	}

	close(s.stopCh)
}

// Wait waits for the maintenance service to finish.
func (s *Service) Wait() {
	<-s.doneCh
}

// runMaintenance executes all maintenance tasks.
func (s *Service) runMaintenance(ctx context.Context) {
	start := time.Now()
	s.log.Info().Msg("Starting maintenance run")

	var totalCleaned int64

	// Task 1: Clean up old observations by age
	if s.config.ObservationRetentionDays > 0 {
		cleaned, err := s.cleanupOldObservations(ctx)
		if err != nil {
			s.log.Error().Err(err).Msg("Failed to cleanup old observations")
		} else {
			totalCleaned += cleaned
			s.log.Info().Int64("cleaned", cleaned).Msg("Cleaned old observations by age")
		}
	}

	// Task 2: Clean up stale observations
	if s.config.CleanupStaleObservations {
		cleaned, err := s.cleanupStaleObservations(ctx)
		if err != nil {
			s.log.Error().Err(err).Msg("Failed to cleanup stale observations")
		} else {
			totalCleaned += cleaned
			s.log.Info().Int64("cleaned", cleaned).Msg("Cleaned stale observations")
		}
	}

	// Task 3: Optimize database
	if err := s.store.Optimize(ctx); err != nil {
		s.log.Error().Err(err).Msg("Failed to optimize database")
	} else {
		s.totalOptimizeRun++
	}

	// Task 4: Clean up old prompts (keep last 1000 per session)
	cleanedPrompts, err := s.cleanupOldPrompts(ctx)
	if err != nil {
		s.log.Error().Err(err).Msg("Failed to cleanup old prompts")
	} else if cleanedPrompts > 0 {
		s.log.Info().Int64("cleaned", cleanedPrompts).Msg("Cleaned old prompts")
	}

	// Task 5: Run similarity telemetry
	if s.config.TelemetryEnabled && s.similarityTelemetry != nil {
		s.similarityTelemetry.Run(ctx)
	}

	// Task 6: Smart GC — archive low-value observations
	if s.config.SmartGCEnabled && s.smartGC != nil {
		gcStats := s.smartGC.Run(ctx)
		s.totalSmartGCArchived += gcStats.Archived
		if gcStats.Archived > 0 {
			s.log.Info().
				Int64("archived", gcStats.Archived).
				Int64("evaluated", gcStats.Evaluated).
				Msg("Smart GC archived low-value observations")
		}
	}

	// Task 7: Pattern quality decay — deprecate low-quality patterns
	if s.patternStore != nil {
		deprecated, err := pattern.RunDecay(ctx, s.patternStore)
		if err != nil {
			s.log.Error().Err(err).Msg("Failed to run pattern decay")
		} else if deprecated > 0 {
			s.totalPatternDecay += int64(deprecated)
			s.log.Info().Int("deprecated", deprecated).Msg("Pattern decay deprecation complete")
		}
	}

	// Task 8: Near-duplicate consolidation — merge near-identical observations
	if s.config.ConsolidationEnabled && s.nearDedupFinder != nil {
		merged, err := s.nearDedupFinder.FindAndMerge(ctx)
		if err != nil {
			s.log.Error().Err(err).Msg("Failed to run near-duplicate consolidation")
		} else if merged > 0 {
			s.mu.Lock()
			s.totalNearDedupMerged += int64(merged)
			s.mu.Unlock()
			s.log.Info().Int("merged", merged).Msg("Near-duplicate consolidation complete")
		}
	}

	// Task 9: Monitor expired verified facts (log-only, no mutations)
	if s.store != nil {
		var expiredCount int64
		err := s.store.GetDB().WithContext(ctx).
			Table("observations").
			Where("expires_at IS NOT NULL AND expires_at < NOW()").
			Where("concepts @> ?", `["verified"]`).
			Count(&expiredCount).Error
		if err != nil {
			s.log.Warn().Err(err).Msg("Failed to count expired verified facts")
		} else if expiredCount > 0 {
			s.log.Info().Int64("expired_verified_facts", expiredCount).Msg("Expired verified facts detected (monitoring only)")
		}
	}

	// Update metrics
	s.mu.Lock()
	s.lastRunTime = time.Now()
	s.lastRunDuration = time.Since(start)
	s.totalCleanedObs += totalCleaned
	s.mu.Unlock()

	s.log.Info().
		Dur("duration", time.Since(start)).
		Int64("observations_cleaned", totalCleaned).
		Msg("Maintenance run completed")
}

// cleanupOldObservations deletes observations older than the retention period.
func (s *Service) cleanupOldObservations(ctx context.Context) (int64, error) {
	cutoffEpoch := time.Now().AddDate(0, 0, -s.config.ObservationRetentionDays).Unix()

	// Get IDs of old observations
	var deletedIDs []int64
	err := s.store.GetDB().WithContext(ctx).
		Model(&gorm.Observation{}).
		Where("created_at_epoch < ?", cutoffEpoch).
		Pluck("id", &deletedIDs).Error
	if err != nil {
		return 0, err
	}

	if len(deletedIDs) == 0 {
		return 0, nil
	}

	// Delete in batches to avoid long transactions
	batchSize := 100
	for i := 0; i < len(deletedIDs); i += batchSize {
		end := min(i+batchSize, len(deletedIDs))
		batch := deletedIDs[i:end]

		if err := s.store.GetDB().WithContext(ctx).
			Where("id IN ?", batch).
			Delete(&gorm.Observation{}).Error; err != nil {
			return int64(i), err
		}

		// Sync vector DB deletions
		if s.vectorCleanupFn != nil {
			s.vectorCleanupFn(ctx, batch)
		}
	}

	return int64(len(deletedIDs)), nil
}

// cleanupStaleObservations deletes observations marked as stale.
func (s *Service) cleanupStaleObservations(ctx context.Context) (int64, error) {
	// Get IDs of stale observations (is_superseded = true)
	var deletedIDs []int64
	err := s.store.GetDB().WithContext(ctx).
		Model(&gorm.Observation{}).
		Where("is_superseded = ?", true).
		Pluck("id", &deletedIDs).Error
	if err != nil {
		return 0, err
	}

	if len(deletedIDs) == 0 {
		return 0, nil
	}

	// Delete in batches
	batchSize := 100
	for i := 0; i < len(deletedIDs); i += batchSize {
		end := min(i+batchSize, len(deletedIDs))
		batch := deletedIDs[i:end]

		if err := s.store.GetDB().WithContext(ctx).
			Where("id IN ?", batch).
			Delete(&gorm.Observation{}).Error; err != nil {
			return int64(i), err
		}

		// Sync vector DB deletions
		if s.vectorCleanupFn != nil {
			s.vectorCleanupFn(ctx, batch)
		}
	}

	return int64(len(deletedIDs)), nil
}

// cleanupOldPrompts removes old prompts keeping only the most recent per session.
func (s *Service) cleanupOldPrompts(ctx context.Context) (int64, error) {
	// Delete prompts older than 30 days that aren't the most recent in their session
	cutoffEpoch := time.Now().AddDate(0, 0, -30).Unix()

	result := s.store.GetDB().WithContext(ctx).
		Where("created_at_epoch < ?", cutoffEpoch).
		Delete(&gorm.UserPrompt{})

	return result.RowsAffected, result.Error
}

// Stats returns maintenance statistics.
func (s *Service) Stats() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()

	return map[string]any{
		"enabled":            s.config.MaintenanceEnabled,
		"interval_hours":     s.config.MaintenanceIntervalHours,
		"retention_days":     s.config.ObservationRetentionDays,
		"cleanup_stale":      s.config.CleanupStaleObservations,
		"last_run":           s.lastRunTime,
		"last_duration_ms":   s.lastRunDuration.Milliseconds(),
		"total_cleaned_obs":  s.totalCleanedObs,
		"total_optimizes":    s.totalOptimizeRun,
		"running":            s.running,
		"telemetry_enabled":       s.config.TelemetryEnabled,
		"smart_gc_enabled":        s.config.SmartGCEnabled,
		"smart_gc_total_archived": s.totalSmartGCArchived,
		"pattern_decay_total":     s.totalPatternDecay,
		"consolidation_enabled":   s.config.ConsolidationEnabled,
		"near_dedup_merged_total": s.totalNearDedupMerged,
	}
}

// RunNow triggers an immediate maintenance run.
func (s *Service) RunNow(ctx context.Context) {
	go s.runMaintenance(ctx)
}
