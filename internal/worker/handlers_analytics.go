// Package worker provides analytics REST handlers for the dashboard.
package worker

import (
	"net/http"
	"strconv"
)

// handleGetTrends godoc
// @Summary Get temporal trends
// @Description Observation-era temporal analytics were removed in v5; this endpoint remains for compatibility and returns an explicit deprecation payload.
// @Tags Analytics
// @Produce json
// @Security ApiKeyAuth
// @Param project query string false "Filter by project"
// @Param period query string false "Group by period: daily, weekly, hourly (default daily)"
// @Param days query int false "Number of days to analyze (default 30, max 365)"
// @Success 200 {object} object
// @Failure 400 {string} string "bad request"
// @Router /api/analytics/trends [get]
func (s *Service) handleGetTrends(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	if project != "" {
		if err := ValidateProjectName(project); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	days := 30
	if val := r.URL.Query().Get("days"); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil && parsed > 0 && parsed <= 365 {
			days = parsed
		}
	}

	period := r.URL.Query().Get("period")
	if period == "" {
		period = "daily"
	}
	if period != "daily" && period != "weekly" && period != "hourly" {
		http.Error(w, "invalid period: must be 'daily', 'weekly', or 'hourly'", http.StatusBadRequest)
		return
	}

	writeJSON(w, map[string]any{
		"project":    project,
		"period":     period,
		"days":       days,
		"deprecated": "observation-based trends analytics removed in v5",
		"summary": map[string]any{
			"total_observations": 0,
			"daily_average":      0,
			"peak_period":        "",
			"peak_count":         0,
		},
		"distribution":      map[string]int{},
		"type_distribution": map[string]int{},
		"top_concepts":      []map[string]any{},
	})
}
