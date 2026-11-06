package worker

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-chi/chi/v5"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

func (s *Service) memoryRevisionSelector(r *http.Request) (int64, models.MemoryContext, string, error) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, models.MemoryContext{}, "", errMemoryContextInvalid
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return 0, models.MemoryContext{}, "", errMemoryContextInvalid
	}
	projects, hasProject := q["project"]
	contexts, hasContext := q["context"]
	if (hasProject && len(projects) != 1) || (hasContext && len(contexts) != 1) {
		return 0, models.MemoryContext{}, "", errMemoryContextInvalid
	}
	project, kind := "", ""
	if hasProject {
		project = projects[0]
	}
	if hasContext {
		kind = contexts[0]
	}
	owning, err := s.resolveMemoryOwningContext(r, kind, hasContext, project, hasProject)
	return id, owning, project, err
}

func (s *Service) handleReviseMemoryContext(w http.ResponseWriter, r *http.Request) {
	if s.memoryStore == nil {
		http.Error(w, "memory store unavailable", http.StatusServiceUnavailable)
		return
	}
	id, owning, project, err := s.memoryRevisionSelector(r)
	if err != nil {
		writeMemoryContextError(w, err)
		return
	}
	var request struct {
		Content string `json:"content"`
		Version int    `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Content == "" || request.Version <= 0 {
		http.Error(w, "content and positive version are required", http.StatusBadRequest)
		return
	}
	updated, err := s.memoryStore.ReviseInContext(r.Context(), id, owning, project, memoryVisibilityCaller(r.Context(), ""), request.Version, request.Content)
	if err != nil {
		if errors.Is(err, gormdb.ErrMemoryVersionConflict) {
			http.Error(w, "memory version conflict; read current version to reconcile", http.StatusConflict)
			return
		}
		writeMemoryContextError(w, err)
		return
	}
	writeJSON(w, jsonSafeMemory(updated))
}

func (s *Service) handleMemoryContextHistory(w http.ResponseWriter, r *http.Request) {
	if s.memoryStore == nil {
		http.Error(w, "memory store unavailable", http.StatusServiceUnavailable)
		return
	}
	id, owning, project, err := s.memoryRevisionSelector(r)
	if err != nil {
		writeMemoryContextError(w, err)
		return
	}
	query := r.URL.Query()
	after, limit := 0, 50
	if values, ok := query["after_version"]; ok {
		if len(values) != 1 {
			http.Error(w, "invalid revision page", http.StatusBadRequest)
			return
		}
		after, err = strconv.Atoi(values[0])
		if err != nil || after < 0 {
			http.Error(w, "invalid revision page", http.StatusBadRequest)
			return
		}
	}
	if values, ok := query["limit"]; ok {
		if len(values) != 1 {
			http.Error(w, "invalid revision page", http.StatusBadRequest)
			return
		}
		limit, err = strconv.Atoi(values[0])
		if err != nil || limit < 1 || limit > 100 {
			http.Error(w, "invalid revision page", http.StatusBadRequest)
			return
		}
	}
	rows, err := s.memoryStore.HistoryInContext(r.Context(), id, owning, project, memoryVisibilityCaller(r.Context(), ""), after, limit)
	if err != nil {
		writeMemoryContextError(w, err)
		return
	}
	writeJSON(w, map[string]any{"revisions": rows})
}
