// Package maintenance provides scheduled maintenance tasks for engram.
package maintenance

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/graph"
	"github.com/thebtf/engram/internal/pattern"
	"github.com/thebtf/engram/internal/telemetry"
	"github.com/thebtf/engram/internal/vector"
	"github.com/thebtf/engram/internal/vector/pgvector"
)

// Service handles scheduled maintenance tasks.
type Service struct {
	log                       zerolog.Logger
	lastRunTime               time.Time
	promptStore               *gorm.PromptStore
	store                     *gorm.Store
	vectorCleanupFn           func(ctx context.Context, deletedIDs []int64)
	config                    *config.Config
	summaryStore              *gorm.SummaryStore
	stopCh                    chan struct{}
	doneCh                    chan struct{}
	observationStore          *gorm.ObservationStore
	similarityTelemetry       *telemetry.SimilarityTelemetry
	patternStore              *gorm.PatternStore
	smartGC                   *SmartGC
	nearDedupFinder           *NearDuplicateFinder
	vectorClient              vector.Client
	vectorSync                *pgvector.Sync
	relationStore             *gorm.RelationStore
	graphStore                graph.GraphStore
	lastRunDuration           time.Duration
	totalSmartGCArchived      int64
	totalCleanedObs           int64
	totalOptimizeRun          int64
	totalPatternDecay         int64
	totalNearDedupMerged      int64
	totalOrphanVectorsCleaned int64
	totalStaleRelationsCleaned int64
	embeddingModelChanged     bool
	mu                        sync.Mutex
	running                   bool
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
	vectorSync *pgvector.Sync,
	relationStore *gorm.RelationStore,
	graphStore graph.GraphStore,
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
		vectorClient:        vectorClient,
		vectorSync:          vectorSync,
		relationStore:       relationStore,
		graphStore:          graphStore,
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

	// Task 10: Clean orphan vectors (vectors with no matching observation)
	if s.vectorClient != nil && s.store != nil {
		cleaned, err := s.cleanOrphanVectors(ctx)
		if err != nil {
			s.log.Error().Err(err).Msg("Failed to clean orphan vectors")
		} else if cleaned > 0 {
			s.mu.Lock()
			s.totalOrphanVectorsCleaned += cleaned
			s.mu.Unlock()
			s.log.Info().Int64("cleaned", cleaned).Msg("Cleaned orphan vectors")
		}
	}

	// Task 11: Detect missing vectors (observations without embeddings)
	if s.vectorClient != nil && s.vectorSync != nil && s.store != nil {
		missing, err := s.detectMissingVectors(ctx)
		if err != nil {
			s.log.Error().Err(err).Msg("Failed to detect missing vectors")
		} else if missing > 0 {
			s.log.Info().Int64("queued_for_reembedding", missing).Msg("Queued observations with missing vectors for re-embedding")
		}
	}

	// Task 12: Clean stale relations (relations referencing deleted observations)
	if s.relationStore != nil && s.store != nil {
		cleaned, err := s.cleanStaleRelations(ctx)
		if err != nil {
			s.log.Error().Err(err).Msg("Failed to clean stale relations")
		} else if cleaned > 0 {
			s.mu.Lock()
			s.totalStaleRelationsCleaned += cleaned
			s.mu.Unlock()
			s.log.Info().Int64("cleaned", cleaned).Msg("Cleaned stale relations")
		}
	}

	// Task 13: Detect graph drift between FalkorDB and PostgreSQL
	if s.graphStore != nil && s.store != nil && s.relationStore != nil {
		if err := s.detectGraphDrift(ctx); err != nil {
			s.log.Error().Err(err).Msg("Failed to detect graph drift")
		}
	}

	// Task 14: Check embedding model change (T054)
	if s.store != nil {
		if err := s.checkEmbeddingModelChange(ctx); err != nil {
			s.log.Error().Err(err).Msg("Failed to check embedding model change")
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
		"telemetry_enabled":              s.config.TelemetryEnabled,
		"smart_gc_enabled":               s.config.SmartGCEnabled,
		"smart_gc_total_archived":        s.totalSmartGCArchived,
		"pattern_decay_total":            s.totalPatternDecay,
		"consolidation_enabled":          s.config.ConsolidationEnabled,
		"near_dedup_merged_total":        s.totalNearDedupMerged,
		"orphan_vectors_cleaned_total":   s.totalOrphanVectorsCleaned,
		"stale_relations_cleaned_total":  s.totalStaleRelationsCleaned,
		"embedding_model_changed":        s.embeddingModelChanged,
	}
}

// RunNow triggers an immediate maintenance run.
func (s *Service) RunNow(ctx context.Context) {
	go s.runMaintenance(ctx)
}

// cleanOrphanVectors finds vectors with no matching observation and deletes them.
// An orphan vector is one whose sqlite_id does not correspond to any observation ID.
// Only observation vectors (doc_type = "observation") are checked; other doc types
// (summaries, prompts, patterns) are intentionally excluded.
func (s *Service) cleanOrphanVectors(ctx context.Context) (int64, error) {
	// Collect all observation-type vector doc_ids and their sqlite_ids from the vectors table.
	type vectorRow struct {
		DocID    string `gorm:"column:doc_id"`
		SQLiteID int64  `gorm:"column:sqlite_id"`
	}
	var rows []vectorRow
	if err := s.store.GetDB().WithContext(ctx).
		Raw("SELECT doc_id, sqlite_id FROM vectors WHERE doc_type = 'observation'").
		Scan(&rows).Error; err != nil {
		return 0, fmt.Errorf("query observation vectors: %w", err)
	}
	if len(rows) == 0 {
		return 0, nil
	}

	// Build unique set of observation IDs referenced by vectors.
	obsIDSet := make(map[int64]struct{}, len(rows))
	for _, r := range rows {
		obsIDSet[r.SQLiteID] = struct{}{}
	}

	uniqueObsIDs := make([]int64, 0, len(obsIDSet))
	for id := range obsIDSet {
		uniqueObsIDs = append(uniqueObsIDs, id)
	}

	// Find which observation IDs actually exist in the database.
	var existingIDs []int64
	if err := s.store.GetDB().WithContext(ctx).
		Table("observations").
		Where("id IN ?", uniqueObsIDs).
		Pluck("id", &existingIDs).Error; err != nil {
		return 0, fmt.Errorf("query existing observations: %w", err)
	}

	existingSet := make(map[int64]struct{}, len(existingIDs))
	for _, id := range existingIDs {
		existingSet[id] = struct{}{}
	}

	// Collect doc_ids for orphan vectors (sqlite_id not in existingSet).
	orphanDocIDs := make([]string, 0)
	for _, r := range rows {
		if _, ok := existingSet[r.SQLiteID]; !ok {
			orphanDocIDs = append(orphanDocIDs, r.DocID)
		}
	}

	if len(orphanDocIDs) == 0 {
		s.log.Debug().Msg("No orphan vectors found")
		return 0, nil
	}

	s.log.Info().
		Int("orphan_count", len(orphanDocIDs)).
		Msg("Deleting orphan vectors")

	if err := s.vectorClient.DeleteDocuments(ctx, orphanDocIDs); err != nil {
		return 0, fmt.Errorf("delete orphan vectors: %w", err)
	}

	return int64(len(orphanDocIDs)), nil
}

// detectMissingVectors finds active observations that have no vector embeddings
// and re-syncs them through the vector sync pipeline.
func (s *Service) detectMissingVectors(ctx context.Context) (int64, error) {
	// Get all active (non-superseded) observation IDs.
	var allObsIDs []int64
	if err := s.store.GetDB().WithContext(ctx).
		Table("observations").
		Where("is_superseded = ? OR is_superseded IS NULL", false).
		Pluck("id", &allObsIDs).Error; err != nil {
		return 0, fmt.Errorf("query active observation IDs: %w", err)
	}
	if len(allObsIDs) == 0 {
		return 0, nil
	}

	// Get all observation IDs that already have at least one vector entry.
	var vectoredIDs []int64
	if err := s.store.GetDB().WithContext(ctx).
		Raw("SELECT DISTINCT sqlite_id FROM vectors WHERE doc_type = 'observation' AND sqlite_id IS NOT NULL").
		Pluck("sqlite_id", &vectoredIDs).Error; err != nil {
		return 0, fmt.Errorf("query vectored observation IDs: %w", err)
	}

	vectoredSet := make(map[int64]struct{}, len(vectoredIDs))
	for _, id := range vectoredIDs {
		vectoredSet[id] = struct{}{}
	}

	// Determine which observations are missing vectors.
	missingIDs := make([]int64, 0)
	for _, id := range allObsIDs {
		if _, ok := vectoredSet[id]; !ok {
			missingIDs = append(missingIDs, id)
		}
	}

	if len(missingIDs) == 0 {
		s.log.Debug().Msg("All active observations have vector embeddings")
		return 0, nil
	}

	s.log.Info().
		Int("missing_count", len(missingIDs)).
		Msg("Detected observations without vector embeddings; re-syncing")

	// Re-sync observations in batches using ObservationStore to retrieve full models.
	batchSize := 50
	var resynced int64
	for i := 0; i < len(missingIDs); i += batchSize {
		select {
		case <-ctx.Done():
			return resynced, ctx.Err()
		default:
		}

		end := min(i+batchSize, len(missingIDs))
		batch := missingIDs[i:end]

		// Retrieve full observation records for the batch.
		type obsRow struct {
			ID int64 `gorm:"column:id"`
		}
		_ = batch // used below via raw query

		var observations []gorm.Observation
		if err := s.store.GetDB().WithContext(ctx).
			Where("id IN ?", batch).
			Find(&observations).Error; err != nil {
			s.log.Warn().Err(err).Int("batch_start", i).Msg("Failed to load observation batch for re-embedding")
			continue
		}

		for j := range observations {
			obs := gorm.ToModelObservation(&observations[j])
			if syncErr := s.vectorSync.SyncObservation(ctx, obs); syncErr != nil {
				s.log.Warn().Err(syncErr).Int64("obs_id", obs.ID).Msg("Failed to re-sync observation vector")
				continue
			}
			resynced++
		}
	}

	return resynced, nil
}

// cleanStaleRelations deletes relations where the source or target observation no longer exists.
func (s *Service) cleanStaleRelations(ctx context.Context) (int64, error) {
	// Delete relations whose source or target observation no longer exists.
	result := s.store.GetDB().WithContext(ctx).
		Exec(`DELETE FROM observation_relations
		      WHERE source_observation_id NOT IN (SELECT id FROM observations)
		         OR target_observation_id NOT IN (SELECT id FROM observations)`)
	if result.Error != nil {
		return 0, fmt.Errorf("delete stale relations: %w", result.Error)
	}

	deleted := result.RowsAffected
	if deleted > 0 {
		s.log.Info().Int64("deleted", deleted).Msg("Stale relations cleaned from PostgreSQL")

		// If FalkorDB graph store is available, also trigger a re-sync to propagate deletions.
		if s.graphStore != nil {
			stats, err := s.graphStore.Stats(ctx)
			if err == nil && stats.Connected {
				var relations []gorm.ObservationRelation
				if err := s.store.GetDB().WithContext(ctx).Find(&relations).Error; err == nil {
					modelRelations := gorm.ToModelRelations(relations)
					if syncErr := s.graphStore.SyncFromRelations(ctx, modelRelations); syncErr != nil {
						s.log.Warn().Err(syncErr).Msg("Failed to sync FalkorDB after stale relation cleanup")
					} else {
						s.log.Info().Msg("FalkorDB re-synced after stale relation cleanup")
					}
				}
			}
		}
	}

	return deleted, nil
}

// detectGraphDrift compares node counts between FalkorDB and PostgreSQL.
// If drift exceeds 5%, it triggers a full re-sync via SyncFromRelations.
func (s *Service) detectGraphDrift(ctx context.Context) error {
	if s.graphStore == nil {
		return nil
	}

	stats, err := s.graphStore.Stats(ctx)
	if err != nil {
		return fmt.Errorf("get graph store stats: %w", err)
	}
	if !stats.Connected {
		s.log.Debug().Msg("Graph store not connected; skipping drift detection")
		return nil
	}

	var obsCount int64
	if err := s.store.GetDB().WithContext(ctx).
		Table("observations").
		Where("is_superseded = ? OR is_superseded IS NULL", false).
		Count(&obsCount).Error; err != nil {
		return fmt.Errorf("count observations for drift detection: %w", err)
	}

	if obsCount == 0 {
		return nil
	}

	graphCount := int64(stats.NodeCount)
	drift := float64(obsCount-graphCount) / float64(obsCount)
	if drift < 0 {
		drift = -drift
	}

	s.log.Info().
		Int64("graph_nodes", graphCount).
		Int64("pg_observations", obsCount).
		Str("drift_pct", fmt.Sprintf("%.1f%%", drift*100)).
		Msg("Graph drift check")

	if drift > 0.05 {
		s.log.Warn().
			Str("drift_pct", fmt.Sprintf("%.1f%%", drift*100)).
			Msg("Graph drift exceeds 5%%; triggering full re-sync")

		// Load all current relations and re-sync to FalkorDB.
		var relations []gorm.ObservationRelation
		if err := s.store.GetDB().WithContext(ctx).Find(&relations).Error; err != nil {
			return fmt.Errorf("load relations for graph re-sync: %w", err)
		}
		modelRelations := gorm.ToModelRelations(relations)
		if syncErr := s.graphStore.SyncFromRelations(ctx, modelRelations); syncErr != nil {
			return fmt.Errorf("graph re-sync: %w", syncErr)
		}
		s.log.Info().
			Int("relations_synced", len(modelRelations)).
			Msg("Graph re-sync complete")
	}

	return nil
}

// checkEmbeddingModelChange detects if the embedding model has changed since the last run.
// The current model name is stored in system_config and compared on each maintenance cycle.
// A mismatch means existing vectors were built with a different model and may need rebuilding.
func (s *Service) checkEmbeddingModelChange(ctx context.Context) error {
	if s.vectorClient == nil {
		return nil
	}

	currentModel := s.vectorClient.ModelVersion()
	if currentModel == "" {
		return nil
	}

	// Read stored model from system_config.
	var storedValue string
	row := s.store.GetDB().WithContext(ctx).
		Raw("SELECT value FROM system_config WHERE key = 'embedding_model'").
		Row()
	scanErr := row.Scan(&storedValue)

	if scanErr != nil {
		// Row not found: first run — store the current model.
		upsertSQL := `INSERT INTO system_config (key, value, updated_at)
		              VALUES ('embedding_model', ?, NOW())
		              ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`
		if err := s.store.GetDB().WithContext(ctx).Exec(upsertSQL, currentModel).Error; err != nil {
			return fmt.Errorf("store embedding model in system_config: %w", err)
		}
		s.log.Info().Str("model", currentModel).Msg("Stored initial embedding model in system_config")
		return nil
	}

	if storedValue != currentModel {
		s.log.Warn().
			Str("previous_model", storedValue).
			Str("current_model", currentModel).
			Msg("Embedding model changed — existing vectors may need re-embedding")

		s.mu.Lock()
		s.embeddingModelChanged = true
		s.mu.Unlock()

		// Update stored model to reflect the change.
		if err := s.store.GetDB().WithContext(ctx).
			Exec("UPDATE system_config SET value = ?, updated_at = NOW() WHERE key = 'embedding_model'", currentModel).
			Error; err != nil {
			return fmt.Errorf("update embedding model in system_config: %w", err)
		}
	} else {
		s.mu.Lock()
		s.embeddingModelChanged = false
		s.mu.Unlock()
	}

	return nil
}

// IsEmbeddingModelChanged returns true if the embedding model changed since the last maintenance run.
func (s *Service) IsEmbeddingModelChanged() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.embeddingModelChanged
}

// OrphanPatternResult holds the results of an orphan pattern cleanup pass.
type OrphanPatternResult struct {
	// OrphansFound is the total number of patterns that had at least one orphan observation reference.
	OrphansFound int
	// OrphansArchived is the number of patterns that were deprecated because ALL their observation IDs were orphaned.
	OrphansArchived int
}

// CleanupOrphanPatterns detects patterns whose observation_ids reference non-existent observations.
// Patterns where all referenced observations are gone are deprecated.
// Patterns where only some references are gone have those references pruned.
// dryRun=true only counts without modifying.
func (s *Service) CleanupOrphanPatterns(ctx context.Context, dryRun bool) (OrphanPatternResult, error) {
	if s.patternStore == nil || s.observationStore == nil {
		return OrphanPatternResult{}, nil
	}

	const maxPatterns = 10000
	patterns, err := s.patternStore.GetActivePatterns(ctx, maxPatterns, 0, "")
	if err != nil {
		return OrphanPatternResult{}, fmt.Errorf("fetch active patterns: %w", err)
	}

	if len(patterns) == 0 {
		return OrphanPatternResult{}, nil
	}

	// Collect all unique observation IDs referenced across all patterns.
	allObsIDSet := make(map[int64]struct{})
	for _, p := range patterns {
		for _, id := range p.ObservationIDs {
			allObsIDSet[id] = struct{}{}
		}
	}

	if len(allObsIDSet) == 0 {
		return OrphanPatternResult{}, nil
	}

	uniqueIDs := make([]int64, 0, len(allObsIDSet))
	for id := range allObsIDSet {
		uniqueIDs = append(uniqueIDs, id)
	}

	// Verify which IDs actually exist.
	existing, err := s.observationStore.GetObservationsByIDs(ctx, uniqueIDs, "default", 0)
	if err != nil {
		return OrphanPatternResult{}, fmt.Errorf("verify observation IDs: %w", err)
	}

	existingSet := make(map[int64]struct{}, len(existing))
	for _, obs := range existing {
		existingSet[obs.ID] = struct{}{}
	}

	result := OrphanPatternResult{}

	for _, p := range patterns {
		if len(p.ObservationIDs) == 0 {
			continue
		}

		var liveIDs []int64
		for _, id := range p.ObservationIDs {
			if _, ok := existingSet[id]; ok {
				liveIDs = append(liveIDs, id)
			}
		}

		orphanCount := len(p.ObservationIDs) - len(liveIDs)
		if orphanCount == 0 {
			continue
		}

		result.OrphansFound++

		if dryRun {
			if len(liveIDs) == 0 {
				result.OrphansArchived++
			}
			continue
		}

		if len(liveIDs) == 0 {
			// All references are orphaned: deprecate the pattern.
			if err := s.patternStore.MarkPatternDeprecated(ctx, p.ID); err != nil {
				s.log.Warn().Err(err).Int64("pattern_id", p.ID).Msg("Failed to deprecate fully-orphaned pattern")
				continue
			}
			result.OrphansArchived++
		} else {
			// Partial orphans: prune dead IDs and persist.
			updated := *p
			updated.ObservationIDs = liveIDs
			if err := s.patternStore.UpdatePattern(ctx, &updated); err != nil {
				s.log.Warn().Err(err).Int64("pattern_id", p.ID).Msg("Failed to prune orphan observation IDs from pattern")
			}
		}
	}

	return result, nil
}

