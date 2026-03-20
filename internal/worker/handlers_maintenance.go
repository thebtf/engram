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
		patterns, err := store.GetActivePatterns(r.Context(), maxPatterns)
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

			decayMultiplier := pattern.HybridDecay(lastSeen)
			dynamicQuality := baseQuality * decayMultiplier

			if dynamicQuality < 0.10 {
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
