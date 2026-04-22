// Package worker provides import, export, and archive HTTP handlers.
package worker

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/thebtf/engram/internal/db/gorm"
)

// BulkImportRequest is the request body for bulk observation import.
type BulkImportRequest struct {
	Project      string          `json:"project"`
	SessionID    string          `json:"session_id,omitempty"`
	Observations json.RawMessage `json:"observations"`
}

// ArchiveRequest is the request body for archiving observations.
type ArchiveRequest struct {
	Project    string  `json:"project,omitempty"`
	Reason     string  `json:"reason,omitempty"`
	IDs        []int64 `json:"ids,omitempty"`
	MaxAgeDays int     `json:"max_age_days,omitempty"`
}

func writeRemovedInV5Error(w http.ResponseWriter, path string) {
	w.WriteHeader(http.StatusNotImplemented)
	writeJSON(w, map[string]any{
		"error":   "removed_in_v5",
		"message": "Observation import/export/archive endpoints were removed in v5. Use the v5 memory, rules, credentials, documents, and issues APIs instead.",
		"path":    path,
	})
}

// handleBulkImport godoc
// @Summary Bulk import observations
// @Description This observation-era import endpoint was removed in v5.
// @Tags Import/Export
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body BulkImportRequest true "Observations to import"
// @Failure 400 {string} string "bad request"
// @Failure 501 {object} map[string]interface{}
// @Router /api/observations/bulk-import [post]
func (s *Service) handleBulkImport(w http.ResponseWriter, r *http.Request) {
	var req BulkImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.Project == "" {
		http.Error(w, "project is required", http.StatusBadRequest)
		return
	}

	if err := ValidateProjectName(req.Project); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if len(req.Observations) == 0 {
		http.Error(w, "observations is required", http.StatusBadRequest)
		return
	}

	writeRemovedInV5Error(w, r.URL.Path)
}

// handleArchiveObservations godoc
// @Summary Archive observations
// @Description This observation-era archive endpoint was removed in v5.
// @Tags Import/Export
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body ArchiveRequest true "Archive criteria: ids, project, max_age_days, reason"
// @Failure 400 {string} string "bad request"
// @Failure 501 {object} map[string]interface{}
// @Router /api/observations/archive [post]
func (s *Service) handleArchiveObservations(w http.ResponseWriter, r *http.Request) {
	var req ArchiveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	if len(req.IDs) == 0 && req.Project == "" && req.MaxAgeDays <= 0 {
		http.Error(w, "either 'ids' or 'project'/'max_age_days' is required", http.StatusBadRequest)
		return
	}

	if req.Project != "" {
		if err := ValidateProjectName(req.Project); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	writeRemovedInV5Error(w, r.URL.Path)
}

// handleUnarchiveObservation godoc
// @Summary Unarchive observation
// @Description This observation-era unarchive endpoint was removed in v5.
// @Tags Import/Export
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Observation ID"
// @Failure 400 {string} string "invalid observation id"
// @Failure 501 {object} map[string]interface{}
// @Router /api/observations/{id}/unarchive [post]
func (s *Service) handleUnarchiveObservation(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	if _, err := strconv.ParseInt(idStr, 10, 64); err != nil {
		http.Error(w, "invalid observation id", http.StatusBadRequest)
		return
	}

	writeRemovedInV5Error(w, r.URL.Path)
}

// handleGetArchivedObservations godoc
// @Summary List archived observations
// @Description This observation-era archived-observations endpoint was removed in v5.
// @Tags Import/Export
// @Produce json
// @Security ApiKeyAuth
// @Param project query string false "Filter by project"
// @Param limit query int false "Number of results (default 100)"
// @Failure 400 {string} string "bad request"
// @Failure 501 {object} map[string]interface{}
// @Router /api/observations/archived [get]
func (s *Service) handleGetArchivedObservations(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	_ = gorm.ParseLimitParam(r, DefaultObservationsLimit)

	if project != "" {
		if err := ValidateProjectName(project); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	writeRemovedInV5Error(w, r.URL.Path)
}

// handleGetArchivalStats godoc
// @Summary Get archival statistics
// @Description This observation-era archival stats endpoint was removed in v5.
// @Tags Import/Export
// @Produce json
// @Security ApiKeyAuth
// @Param project query string false "Filter by project"
// @Failure 400 {string} string "bad request"
// @Failure 501 {object} map[string]interface{}
// @Router /api/observations/archival-stats [get]
func (s *Service) handleGetArchivalStats(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	if project != "" {
		if err := ValidateProjectName(project); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	writeRemovedInV5Error(w, r.URL.Path)
}

// handleExportObservations godoc
// @Summary Export observations
// @Description This observation-era export endpoint was removed in v5.
// @Tags Import/Export
// @Produce json
// @Security ApiKeyAuth
// @Param project query string false "Filter by project"
// @Param format query string false "Export format: json or csv (default json)"
// @Param scope query string false "Filter by scope: project, global"
// @Param type query string false "Filter by observation type"
// @Param limit query int false "Number of results (default 1000, max 5000)"
// @Failure 400 {string} string "bad request"
// @Failure 501 {object} map[string]interface{}
// @Router /api/observations/export [get]
func (s *Service) handleExportObservations(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "json"
	}
	scope := r.URL.Query().Get("scope")
	obsType := r.URL.Query().Get("type")
	_ = gorm.ParseLimitParamWithMax(r, 1000, 5000)

	if format != "json" && format != "csv" {
		http.Error(w, "format must be 'json' or 'csv'", http.StatusBadRequest)
		return
	}

	if project != "" {
		if err := ValidateProjectName(project); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	if scope != "" {
		if scope != "project" && scope != "global" {
			http.Error(w, "scope must be 'project' or 'global'", http.StatusBadRequest)
			return
		}
	}

	if obsType != "" && !IsValidObservationType(obsType) {
		http.Error(w, "invalid observation type", http.StatusBadRequest)
		return
	}

	writeRemovedInV5Error(w, r.URL.Path)
}

// BulkStatusRequest represents a request to update status for multiple observations.
type BulkStatusRequest struct {
	Action   string  `json:"action"`
	Reason   string  `json:"reason,omitempty"`
	IDs      []int64 `json:"ids"`
	Feedback int     `json:"feedback,omitempty"`
}

// handleBulkStatusUpdate godoc
// @Summary Bulk status update
// @Description This observation-era bulk status endpoint was removed in v5.
// @Tags Import/Export
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body BulkStatusRequest true "Bulk action request"
// @Failure 400 {string} string "bad request"
// @Failure 501 {object} map[string]interface{}
// @Router /api/observations/bulk-status [post]
func (s *Service) handleBulkStatusUpdate(w http.ResponseWriter, r *http.Request) {
	var req BulkStatusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	if len(req.IDs) == 0 {
		http.Error(w, "ids is required", http.StatusBadRequest)
		return
	}

	if len(req.IDs) > 500 {
		http.Error(w, "maximum 500 ids per request", http.StatusBadRequest)
		return
	}

	switch req.Action {
	case "supersede", "archive", "suppress":
	case "set_feedback":
		if req.Feedback < -1 || req.Feedback > 1 {
			http.Error(w, "feedback must be -1, 0, or 1", http.StatusBadRequest)
			return
		}
	default:
		http.Error(w, "action must be 'supersede', 'archive', 'suppress', or 'set_feedback'", http.StatusBadRequest)
		return
	}

	writeRemovedInV5Error(w, r.URL.Path)
}

// handleFindDuplicates godoc
// @Summary Find duplicate observations
// @Description This observation-era duplicate finder endpoint was removed in v5.
// @Tags Import/Export
// @Produce json
// @Security ApiKeyAuth
// @Param project query string false "Filter by project"
// @Param threshold query number false "Similarity threshold (default 0.6, range 0-1)"
// @Param limit query int false "Number of observations to check (default 100)"
// @Failure 400 {string} string "bad request"
// @Failure 501 {object} map[string]interface{}
// @Router /api/observations/duplicates [get]
func (s *Service) handleFindDuplicates(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	thresholdStr := r.URL.Query().Get("threshold")
	_ = gorm.ParseLimitParam(r, 100)

	if project != "" {
		if err := ValidateProjectName(project); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	if thresholdStr != "" {
		threshold, err := strconv.ParseFloat(thresholdStr, 64)
		if err != nil || threshold <= 0 || threshold >= 1 {
			http.Error(w, "threshold must be a number between 0 and 1", http.StatusBadRequest)
			return
		}
	}

	writeRemovedInV5Error(w, r.URL.Path)
}
