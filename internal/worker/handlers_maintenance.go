// Package worker provides maintenance REST handlers for the dashboard.
package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/pattern"
)

// consolidationRequest is the JSON body for POST /api/maintenance/consolidation.
type consolidationRequest struct {
	Cycle string `json:"cycle"` // "all", "decay", "associations", or "forgetting"
}

// handleTriggerConsolidation godoc
// @Summary Trigger consolidation cycle
// @Description Runs a consolidation cycle (decay, associations, forgetting, or all).
// @Tags Maintenance
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body consolidationRequest false "Consolidation options (defaults to all)"
// @Success 200 {object} object
// @Failure 400 {string} string "bad request"
// @Failure 500 {string} string "internal error"
// @Router /api/maintenance/consolidation [post]
func (s *Service) handleTriggerConsolidation(w http.ResponseWriter, r *http.Request) {
	if s.consolidationScheduler == nil {
		http.Error(w, "consolidation scheduler not available", http.StatusServiceUnavailable)
		return
	}

	var req consolidationRequest
	// Body is optional; default to "all"
	if r.Body != nil && r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	if req.Cycle == "" {
		req.Cycle = "all"
	}

	var err error
	switch req.Cycle {
	case "all":
		err = s.consolidationScheduler.RunAll(r.Context())
	case "decay":
		err = s.consolidationScheduler.RunDecay(r.Context())
	case "associations":
		err = s.consolidationScheduler.RunAssociations(r.Context())
	case "forgetting":
		err = s.consolidationScheduler.RunForgetting(r.Context())
	default:
		http.Error(w, "unknown cycle: "+req.Cycle+" (use 'all', 'decay', 'associations', or 'forgetting')", http.StatusBadRequest)
		return
	}

	if err != nil {
		log.Error().Err(err).Str("cycle", req.Cycle).Msg("consolidation cycle failed")
		http.Error(w, "consolidation "+req.Cycle+" cycle failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{
		"status": "completed",
		"cycle":  req.Cycle,
	})
}

// handleRunMaintenance godoc
// @Summary Trigger full maintenance run
// @Description Triggers a full maintenance cycle (cleanup, optimize) in the background.
// @Tags Maintenance
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} object
// @Failure 500 {string} string "internal error"
// @Router /api/maintenance/run [post]
func (s *Service) handleRunMaintenance(w http.ResponseWriter, r *http.Request) {
	if s.maintenanceService == nil {
		http.Error(w, "maintenance service not available", http.StatusServiceUnavailable)
		return
	}

	// Use background context: the request context is cancelled after the
	// response is sent, which would prematurely abort the background job.
	s.maintenanceService.RunNow(context.Background())

	// Clean up old search query logs (90-day retention) in the background.
	go func() {
		s.initMu.RLock()
		sqlStore := s.searchQueryLogStore
		s.initMu.RUnlock()

		if sqlStore == nil {
			return
		}
		deleted, err := sqlStore.Cleanup(context.Background(), 90*24*time.Hour)
		if err != nil {
			log.Warn().Err(err).Msg("search query log cleanup failed")
		} else if deleted > 0 {
			log.Info().Int64("deleted", deleted).Msg("cleaned up old search query log entries")
		}
	}()

	// Clean up old retrieval stats logs (90-day retention) in the background.
	go func() {
		s.initMu.RLock()
		rsStore := s.retrievalStatsLogStore
		s.initMu.RUnlock()

		if rsStore == nil {
			return
		}
		deleted, err := rsStore.Cleanup(context.Background(), 90*24*time.Hour)
		if err != nil {
			log.Warn().Err(err).Msg("retrieval stats log cleanup failed")
		} else if deleted > 0 {
			log.Info().Int64("deleted", deleted).Msg("cleaned up old retrieval stats log entries")
		}
	}()

	// Clean up old injection log entries (90-day retention) in the background.
	go func() {
		s.initMu.RLock()
		obsStore := s.observationStore
		s.initMu.RUnlock()

		if obsStore == nil {
			return
		}
		if err := cleanupInjectionLog(context.Background(), obsStore); err != nil {
			log.Warn().Err(err).Msg("injection log cleanup failed")
		}
	}()

	writeJSON(w, map[string]any{
		"status":  "triggered",
		"message": "Maintenance run started in background",
	})
}

// handleGetMaintenanceStats godoc
// @Summary Get maintenance statistics
// @Description Returns maintenance service statistics including last run time, duration, and configuration.
// @Tags Maintenance
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} object
// @Failure 500 {string} string "internal error"
// @Router /api/maintenance/stats [get]
func (s *Service) handleGetMaintenanceStats(w http.ResponseWriter, _ *http.Request) {
	if s.maintenanceService == nil {
		http.Error(w, "maintenance service not available", http.StatusServiceUnavailable)
		return
	}

	stats := s.maintenanceService.Stats()
	writeJSON(w, stats)
}

// handleConsistencyCheck returns counts of orphan data across all subsystems.
// Read-only — does not delete or modify anything.
// @Router /api/maintenance/consistency [get]
func (s *Service) handleConsistencyCheck(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	obsStore := s.observationStore
	s.initMu.RUnlock()

	if obsStore == nil {
		http.Error(w, "stores not available", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	db := obsStore.GetDB()
	result := map[string]any{}

	var orphanVectors int64
	db.WithContext(ctx).Raw(`
		SELECT COUNT(*) FROM vectors v
		WHERE v.doc_type = 'observation'
		AND NOT EXISTS (SELECT 1 FROM observations o WHERE o.id = v.sqlite_id)
	`).Scan(&orphanVectors)
	result["orphan_vectors"] = orphanVectors

	var missingVectors int64
	db.WithContext(ctx).Raw(`
		SELECT COUNT(*) FROM observations o
		WHERE (o.status IS NULL OR o.status = 'active') AND o.is_suppressed = false
		AND NOT EXISTS (SELECT 1 FROM vectors v WHERE v.sqlite_id = o.id AND v.doc_type = 'observation')
	`).Scan(&missingVectors)
	result["observations_without_vectors"] = missingVectors

	var staleRelations int64
	db.WithContext(ctx).Raw(`
		SELECT COUNT(*) FROM observation_relations r
		WHERE NOT EXISTS (SELECT 1 FROM observations o WHERE o.id = r.source_id)
		OR NOT EXISTS (SELECT 1 FROM observations o WHERE o.id = r.target_id)
	`).Scan(&staleRelations)
	result["stale_relations"] = staleRelations

	var suppressed int64
	db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM observations WHERE is_suppressed = true`).Scan(&suppressed)
	result["suppressed_observations"] = suppressed

	var total, active int64
	db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM observations`).Scan(&total)
	db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM observations WHERE (status IS NULL OR status = 'active') AND is_suppressed = false`).Scan(&active)
	result["total_observations"] = total
	result["active_observations"] = active
	result["healthy"] = orphanVectors == 0 && missingVectors == 0 && staleRelations == 0

	writeJSON(w, result)
}

// handleBackfillRelations godoc
// @Summary Backfill relations for existing observations
// @Description Triggers relation detection for existing observations that were created before the relation detector was enabled. Runs asynchronously in the background.
// @Tags Maintenance
// @Produce json
// @Security ApiKeyAuth
// @Param project query string false "Project to backfill (all projects if empty)"
// @Param batch_size query int false "Batch size (default 50)"
// @Success 200 {object} object
// @Failure 503 {string} string "relation detector not available"
// @Router /api/maintenance/backfill-relations [post]
func (s *Service) handleBackfillRelations(w http.ResponseWriter, r *http.Request) {
	if s.relationDetector == nil {
		http.Error(w, "relation detector not available (requires embedding + vector search)", http.StatusServiceUnavailable)
		return
	}

	project := r.URL.Query().Get("project")
	batchSize := 50
	if bs := r.URL.Query().Get("batch_size"); bs != "" {
		if parsed, err := strconv.Atoi(bs); err == nil && parsed > 0 && parsed <= 500 {
			batchSize = parsed
		}
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()

		processed, relations, err := s.relationDetector.BackfillRelations(ctx, project, batchSize, func(p, t int) {
			log.Info().Int("processed", p).Int("total", t).Msg("Relation backfill progress")
		})
		if err != nil {
			log.Error().Err(err).Int("processed", processed).Msg("Relation backfill failed")
		} else {
			log.Info().Int("processed", processed).Int("relations", relations).Msg("Relation backfill complete")
		}
	}()

	writeJSON(w, map[string]any{
		"started":    true,
		"message":    "Relation backfill started in background",
		"project":    project,
		"batch_size": batchSize,
	})
}

// handlePurgePatterns godoc
// @Summary Bulk deprecate low-quality patterns
// @Description Runs pattern quality decay and deprecates patterns with dynamic quality below 0.10. Optionally preview with dry_run.
// @Tags Maintenance
// @Produce json
// @Security ApiKeyAuth
// @Param dry_run query bool false "Preview without deprecating"
// @Success 200 {object} map[string]interface{}
// @Failure 503 {string} string "pattern store not initialized"
// @Failure 500 {string} string "internal error"
// @Router /api/maintenance/purge-patterns [post]
func (s *Service) handlePurgePatterns(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	store := s.patternStore
	s.initMu.RUnlock()

	if store == nil {
		http.Error(w, "pattern store not initialized", http.StatusServiceUnavailable)
		return
	}

	dryRun := r.URL.Query().Get("dry_run") == "true"

	if dryRun {
		// Preview mode: compute quality scores without deprecating.
		const maxPatterns = 10000
		patterns, err := store.GetActivePatterns(r.Context(), maxPatterns, 0, "")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		maxFreq := 1
		for _, p := range patterns {
			if p.Frequency > maxFreq {
				maxFreq = p.Frequency
			}
		}

		wouldDeprecate := 0
		for _, p := range patterns {
			baseQuality := pattern.QualityScore(p.Frequency, p.Confidence, len(p.Projects), maxFreq)

			var lastSeen time.Time
			if p.LastSeenAt != "" {
				lastSeen, _ = time.Parse(time.RFC3339, p.LastSeenAt)
			}
			if lastSeen.IsZero() && p.CreatedAt != "" {
				lastSeen, _ = time.Parse(time.RFC3339, p.CreatedAt)
			}

			daysSince := time.Since(lastSeen).Hours() / 24
			decayMultiplier := pattern.TemporalDecay(daysSince)
			dynamicQuality := baseQuality * decayMultiplier

			if dynamicQuality < pattern.DeprecateThreshold {
				wouldDeprecate++
			}
		}

		writeJSON(w, map[string]any{
			"dry_run":          true,
			"total_active":     len(patterns),
			"would_deprecate":  wouldDeprecate,
		})
		return
	}

	deprecated, err := pattern.RunDecay(r.Context(), store)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{
		"status":     "completed",
		"deprecated": deprecated,
	})
}

// handlePatternCleanup godoc
// @Summary Delete zero-quality deprecated patterns
// @Description Deletes deprecated patterns whose dynamic quality has reached 0 (past 90-day terminal phase).
// @Tags Maintenance
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} map[string]interface{}
// @Failure 503 {string} string "pattern store not initialized"
// @Failure 500 {string} string "internal error"
// @Router /api/maintenance/pattern-cleanup [post]
func (s *Service) handlePatternCleanup(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	store := s.patternStore
	s.initMu.RUnlock()

	if store == nil {
		http.Error(w, "pattern store not initialized", http.StatusServiceUnavailable)
		return
	}

	// Get all deprecated patterns.
	deprecated, err := store.GetDeprecatedPatterns(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Find max frequency across deprecated patterns for quality normalization.
	maxFreq := 1
	for _, p := range deprecated {
		if p.Frequency > maxFreq {
			maxFreq = p.Frequency
		}
	}

	// Delete only patterns whose dynamic quality has reached exactly 0 (past 90-day terminal phase).
	deleted := 0
	for _, p := range deprecated {
		var lastSeen time.Time
		if p.LastSeenAt != "" {
			lastSeen, _ = time.Parse(time.RFC3339, p.LastSeenAt)
		}
		if lastSeen.IsZero() && p.CreatedAt != "" {
			lastSeen, _ = time.Parse(time.RFC3339, p.CreatedAt)
		}

		daysSince := time.Since(lastSeen).Hours() / 24
		dynamicQuality := pattern.DynamicQuality(p.Frequency, p.Confidence, len(p.Projects), maxFreq, daysSince)

		if dynamicQuality == pattern.DeleteThreshold {
			if err := store.DeletePattern(r.Context(), p.ID); err != nil {
				log.Warn().Err(err).Int64("pattern_id", p.ID).Msg("Failed to delete zero-quality pattern")
				continue
			}
			deleted++
		}
	}

	writeJSON(w, map[string]any{
		"status":             "completed",
		"deprecated_checked": len(deprecated),
		"deleted":            deleted,
	})
}

// handlePurgeRebuild godoc
// @Summary Selective purge and rebuild
// @Description Truncates derived tables and deletes auto-extracted observations,
// preserving manual observations and credentials. After purge, run backfill to
// rebuild from session history with improved extraction quality.
// @Tags Maintenance
// @Produce json
// @Security ApiKeyAuth
// @Param dry_run query bool false "Preview what would be purged without executing"
// @Success 200 {object} map[string]interface{}
// @Failure 503 {string} string "store not ready"
// @Failure 500 {string} string "internal error"
// @Router /api/maintenance/purge-rebuild [post]
func (s *Service) handlePurgeRebuild(w http.ResponseWriter, r *http.Request) {
	dryRun := r.URL.Query().Get("dry_run") == "true"

	s.initMu.RLock()
	store := s.store
	s.initMu.RUnlock()

	if store == nil {
		http.Error(w, "store not ready", http.StatusServiceUnavailable)
		return
	}

	db := store.GetDB()

	if dryRun {
		var autoExtracted int64
		db.Raw("SELECT COUNT(*) FROM observations WHERE source_type != 'manual' AND type != 'credential'").Scan(&autoExtracted)

		var vectorCount int64
		db.Raw("SELECT COUNT(*) FROM vectors").Scan(&vectorCount)

		var patternCount int64
		db.Raw("SELECT COUNT(*) FROM patterns").Scan(&patternCount)

		var relationsCount int64
		db.Raw("SELECT COUNT(*) FROM observation_relations").Scan(&relationsCount)

		writeJSON(w, map[string]any{
			"dry_run":            true,
			"auto_extracted_obs": autoExtracted,
			"vectors":            vectorCount,
			"patterns":           patternCount,
			"relations":          relationsCount,
			"preserved":          "source_type='manual' OR type='credential'",
		})
		return
	}

	results := map[string]int64{}

	// Truncate derived tables that will be rebuilt by backfill.
	derivedTables := []string{
		"vectors",
		"patterns",
		"observation_relations",
		"session_summaries",
		"user_prompts",
		"injection_log",
	}
	for _, table := range derivedTables {
		if err := db.Exec("TRUNCATE " + table + " RESTART IDENTITY CASCADE").Error; err != nil {
			log.Warn().Err(err).Str("table", table).Msg("purge-rebuild: truncate failed")
		} else {
			results[table+"_truncated"] = 1
		}
	}

	// Delete auto-extracted observations; preserve manual entries and credentials.
	del := db.Exec("DELETE FROM observations WHERE source_type != 'manual' AND type != 'credential'")
	if del.Error != nil {
		http.Error(w, "failed to purge observations: "+del.Error.Error(), http.StatusInternalServerError)
		return
	}
	results["observations_deleted"] = del.RowsAffected

	log.Info().
		Int64("observations_deleted", del.RowsAffected).
		Msg("purge-rebuild: completed")

	writeJSON(w, map[string]any{
		"status":  "purged",
		"results": results,
		"next":    "Run backfill to rebuild from session history",
	})
}

// cleanupInjectionLog removes injection log entries older than 90 days.
func cleanupInjectionLog(ctx context.Context, obsStore interface {
	CleanupInjectionLog(ctx context.Context, retentionDays int) (int64, error)
}) error {
	deleted, err := obsStore.CleanupInjectionLog(ctx, 90)
	if err != nil {
		return err
	}
	if deleted > 0 {
		log.Info().Int64("deleted", deleted).Msg("cleaned up old injection log entries")
	}
	return nil
}

