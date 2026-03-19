// Package worker provides analytics REST handlers for the dashboard.
package worker

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/rs/zerolog/log"
)

// handleGetTrends godoc
// @Summary Get temporal trends
// @Description Analyzes observation creation patterns over time, grouped by day, week, or hour_of_day.
// @Tags Analytics
// @Produce json
// @Security ApiKeyAuth
// @Param project query string false "Filter by project"
// @Param period query string false "Group by period: daily, weekly, hourly (default daily)"
// @Param days query int false "Number of days to analyze (default 30, max 365)"
// @Success 200 {object} object
// @Failure 400 {string} string "bad request"
// @Failure 500 {string} string "internal error"
// @Router /api/analytics/trends [get]
func (s *Service) handleGetTrends(w http.ResponseWriter, r *http.Request) {
	if s.observationStore == nil {
		http.Error(w, "observation store not available", http.StatusServiceUnavailable)
		return
	}

	project := r.URL.Query().Get("project")
	if err := ValidateProjectName(project); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Map period param to group_by value used in the logic
	period := r.URL.Query().Get("period")
	groupBy := "day"
	switch period {
	case "weekly":
		groupBy = "week"
	case "hourly":
		groupBy = "hour_of_day"
	case "daily", "":
		groupBy = "day"
	default:
		http.Error(w, "invalid period: must be 'daily', 'weekly', or 'hourly'", http.StatusBadRequest)
		return
	}

	days := 30
	if val := r.URL.Query().Get("days"); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil && parsed > 0 && parsed <= 365 {
			days = parsed
		}
	}

	// Fetch observations created within the analysis window using a time cutoff.
	cutoffEpoch := time.Now().AddDate(0, 0, -days).UnixMilli()
	obs, err := s.observationStore.GetObservationsSinceEpoch(r.Context(), project, cutoffEpoch)
	if err != nil {
		log.Error().Err(err).Msg("get observations for trends failed")
		http.Error(w, "get observations: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Calculate time range
	now := time.Now()
	startTime := now.AddDate(0, 0, -days)

	// Group observations by time bucket (already filtered to window by DB query)
	buckets := make(map[string]int)
	typeDistribution := make(map[string]int)
	conceptCounts := make(map[string]int)
	totalInRange := 0

	for _, o := range obs {
		totalInRange++

		created := time.UnixMilli(o.CreatedAtEpoch)
		var key string
		switch groupBy {
		case "week":
			year, week := created.ISOWeek()
			key = fmt.Sprintf("%d-W%02d", year, week)
		case "hour_of_day":
			key = fmt.Sprintf("%02d:00", created.Hour())
		default: // day
			key = created.Format("2006-01-02")
		}
		buckets[key]++

		// Track type distribution
		typeDistribution[string(o.Type)]++

		// Track top concepts
		for _, c := range o.Concepts {
			conceptCounts[c]++
		}
	}

	// Find peak period
	peakPeriod := ""
	peakCount := 0
	for k, v := range buckets {
		if v > peakCount {
			peakCount = v
			peakPeriod = k
		}
	}

	// Sort and get top concepts
	type conceptEntry struct {
		name  string
		count int
	}
	topConcepts := make([]conceptEntry, 0, len(conceptCounts))
	for name, count := range conceptCounts {
		topConcepts = append(topConcepts, conceptEntry{name, count})
	}
	slices.SortFunc(topConcepts, func(a, b conceptEntry) int {
		return b.count - a.count // descending by count
	})
	if len(topConcepts) > 10 {
		topConcepts = topConcepts[:10]
	}
	topConceptsMap := make([]map[string]any, len(topConcepts))
	for i, c := range topConcepts {
		topConceptsMap[i] = map[string]any{"concept": c.name, "count": c.count}
	}

	dailyAvg := float64(0)
	if days > 0 {
		dailyAvg = float64(totalInRange) / float64(days)
	}

	writeJSON(w, map[string]any{
		"period": map[string]any{
			"start":    startTime.Format("2006-01-02"),
			"end":      now.Format("2006-01-02"),
			"days":     days,
			"group_by": groupBy,
		},
		"summary": map[string]any{
			"total_observations": totalInRange,
			"daily_average":      dailyAvg,
			"peak_period":        peakPeriod,
			"peak_count":         peakCount,
		},
		"distribution":      buckets,
		"type_distribution": typeDistribution,
		"top_concepts":      topConceptsMap,
	})
}
