// Package worker provides maintenance REST handlers for the dashboard.
package worker

import (
	"encoding/json"
	"net/http"

	"github.com/rs/zerolog/log"
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

	s.maintenanceService.RunNow(r.Context())

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
