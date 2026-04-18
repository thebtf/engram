// Package worker provides memory REST handlers for the dashboard.
package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	gormlib "gorm.io/gorm"

	"github.com/thebtf/engram/pkg/models"
)

// storeMemoryRequest is the JSON body for POST /api/memories.
type storeMemoryRequest struct {
	Project     string   `json:"project"`
	Content     string   `json:"content"`
	Tags        []string `json:"tags,omitempty"`
	SourceAgent string   `json:"source_agent,omitempty"`
}

// handleStoreMemoryExplicit godoc
// @Summary Store an explicit memory note
// @Description Creates a new memory entry for the given project.
// @Tags Memories
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body storeMemoryRequest true "Memory to store"
// @Success 201 {object} models.Memory
// @Failure 400 {string} string "bad request"
// @Failure 503 {string} string "service unavailable"
// @Failure 500 {string} string "internal error"
// @Router /api/memories [post]
func (s *Service) handleStoreMemoryExplicit(w http.ResponseWriter, r *http.Request) {
	if s.memoryStore == nil {
		http.Error(w, "memory store not available", http.StatusServiceUnavailable)
		return
	}

	var req storeMemoryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.Project == "" {
		http.Error(w, "project is required", http.StatusBadRequest)
		return
	}
	if req.Content == "" {
		http.Error(w, "content is required", http.StatusBadRequest)
		return
	}

	mem := &models.Memory{
		Project:     req.Project,
		Content:     req.Content,
		Tags:        req.Tags,
		SourceAgent: req.SourceAgent,
	}

	created, err := s.memoryStore.Create(r.Context(), mem)
	if err != nil {
		log.Error().Err(err).Str("project", req.Project).Msg("store memory failed")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, created)
}

// handleListMemories godoc
// @Summary List memory notes for a project
// @Description Returns stored memories for the given project, newest first.
// @Tags Memories
// @Produce json
// @Security ApiKeyAuth
// @Param project query string true "Project identifier"
// @Param limit query int false "Maximum number of results (default 50)"
// @Success 200 {array} models.Memory
// @Failure 400 {string} string "project is required"
// @Failure 503 {string} string "service unavailable"
// @Failure 500 {string} string "internal error"
// @Router /api/memories [get]
func (s *Service) handleListMemories(w http.ResponseWriter, r *http.Request) {
	if s.memoryStore == nil {
		http.Error(w, "memory store not available", http.StatusServiceUnavailable)
		return
	}

	project := r.URL.Query().Get("project")
	if project == "" {
		http.Error(w, "project is required", http.StatusBadRequest)
		return
	}

	const maxLimit = 500
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			http.Error(w, "limit must be a positive integer", http.StatusBadRequest)
			return
		}
		if n > maxLimit {
			http.Error(w, fmt.Sprintf("limit must not exceed %d", maxLimit), http.StatusBadRequest)
			return
		}
		limit = n
	}

	mems, err := s.memoryStore.List(r.Context(), project, limit)
	if err != nil {
		log.Error().Err(err).Str("project", project).Msg("list memories failed")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Return an empty array rather than null when there are no results.
	if mems == nil {
		mems = []*models.Memory{}
	}

	writeJSON(w, mems)
}

// handleDeleteMemoryByID godoc
// @Summary Delete a memory note by ID
// @Description Soft-deletes a memory entry by its numeric ID.
// @Tags Memories
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Memory ID"
// @Success 200 {object} map[string]string
// @Failure 400 {string} string "invalid id"
// @Failure 404 {string} string "not found"
// @Failure 503 {string} string "service unavailable"
// @Failure 500 {string} string "internal error"
// @Router /api/memories/{id} [delete]
func (s *Service) handleDeleteMemoryByID(w http.ResponseWriter, r *http.Request) {
	if s.memoryStore == nil {
		http.Error(w, "memory store not available", http.StatusServiceUnavailable)
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid memory id", http.StatusBadRequest)
		return
	}

	if err := s.memoryStore.Delete(r.Context(), id); err != nil {
		if errors.Is(err, gormlib.ErrRecordNotFound) {
			http.Error(w, "memory not found", http.StatusNotFound)
			return
		}
		log.Error().Err(err).Int64("id", id).Msg("delete memory failed")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]string{"status": "ok"})
}
