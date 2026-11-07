package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/pgvector/pgvector-go"
	"github.com/rs/zerolog/log"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/embedding"
	"github.com/thebtf/engram/internal/principalmemory"
	"github.com/thebtf/engram/internal/redaction"
	"github.com/thebtf/engram/pkg/models"
	gormlib "gorm.io/gorm"
)

type reviseMemoryContextRequest struct {
	Content string `json:"content" binding:"required" minLength:"1"`
	Version int    `json:"version" binding:"required" minimum:"1"`
}

type reviseMemoryContextResponse struct {
	*models.Memory
	DomainWarning     *principalmemory.DomainWriteWarning `json:"domain_warning,omitempty"`
	DomainAuditStatus string                              `json:"domain_audit_status,omitempty"`
}

type memoryContextHistoryResponse struct {
	Revisions []gormdb.MemoryRevision `json:"revisions"`
}

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

// handleReviseMemoryContext godoc
// @Summary Revise a typed memory card
// @Description Requires the authenticated owner and current version. Omit context and project for enrolled self; project context requires the card's unique native canonical membership, not a read alias, and self/global must omit project. Legacy rows return 404 without changing their context or history. A stale version returns 409; after an uncertain outcome read the current card before retrying. The current domain write policy applies: reject mode records a denial audit and returns 403 without a revision; an audit failure does not mutate the card. Warn mode records an audit and allows the revision with domain_warning and domain_audit_status. The prior body and update audit are retained atomically. Success is JSON and errors are plain text; Swagger 2 lists media types for the whole operation.
// @Tags Memories
// @Accept json
// @Produce json,plain
// @Security ApiKeyAuth
// @Param id path int true "Memory ID"
// @Param context query string false "Owning context; omission selects self unless project is supplied" Enums(self,project,global)
// @Param project query string false "Existing canonical project UUID; required for project and forbidden for self/global"
// @Param body body reviseMemoryContextRequest true "Current version and replacement content"
// @Success 200 {object} reviseMemoryContextResponse
// @Failure 400 {string} string "invalid selector, content or version"
// @Failure 401 {string} string "authentication or explicit keycard validation failed"
// @Failure 403 {string} string "principal context access denied or domain write rejected"
// @Failure 404 {string} string "context or manageable active typed card not found"
// @Failure 409 {string} string "stale version; read current card to reconcile"
// @Failure 500 {string} string "internal error; domain audit failure does not mutate the card"
// @Failure 503 {string} string "memory store unavailable"
// @Router /api/memories/{id} [patch]
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
	var request reviseMemoryContextRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Content == "" || request.Version <= 0 {
		http.Error(w, "content and positive version are required", http.StatusBadRequest)
		return
	}
	request.Content, err = s.redactMemoryContent(request.Content)
	if err != nil || request.Content == "" {
		http.Error(w, "content fully redacted", http.StatusBadRequest)
		return
	}
	caller := memoryVisibilityCaller(r.Context(), "")
	var domainDecision *principalmemory.DomainWriteDecision
	guard := func(ctx context.Context, tx *gormlib.DB, current *models.Memory) (bool, error) {
		var err error
		domainDecision, err = s.checkDomainWriteREST(ctx, tx, current, caller.SessionID)
		if errors.Is(err, principalmemory.ErrDomainWriteRejected) && domainDecision != nil && !domainDecision.Allowed && domainDecision.AuditStatus == principalmemory.AuditStatusWritten {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return domainDecision == nil || domainDecision.Allowed, nil
	}
	updated, err := s.memoryStore.ReviseInContext(r.Context(), id, owning, project, caller, request.Version, request.Content, s.redactMemoryContent, guard)
	if err != nil {
		if errors.Is(err, gormdb.ErrMemoryRevisionDenied) {
			http.Error(w, "domain write rejected", http.StatusForbidden)
			return
		}
		if errors.Is(err, redaction.ErrContentFullyRedacted) {
			http.Error(w, "content fully redacted", http.StatusBadRequest)
			return
		}
		if errors.Is(err, gormdb.ErrMemoryVersionConflict) {
			http.Error(w, "memory version conflict; read current version to reconcile", http.StatusConflict)
			return
		}
		writeMemoryContextError(w, err)
		return
	}
	s.initMu.RLock()
	embClient, embStore := s.embeddingClient, s.embeddingStore
	s.initMu.RUnlock()
	if embClient != nil && embStore != nil {
		go func(memory *models.Memory) {
			ctx := context.Background()
			vectors, err := embClient.Embed(ctx, []string{memory.Content})
			if err != nil {
				log.Error().Err(err).Int64("memory_id", memory.ID).Msg("revision embedding failed")
				return
			}
			if len(vectors) == 0 || len(vectors[0]) == 0 {
				return
			}
			_, err = embStore.StoreChunksForVersion(ctx, memory.ID, memory.Version, []embedding.Chunk{{
				MemoryID: memory.ID, Text: memory.Content, Model: embClient.Model(), Embedding: pgvector.NewVector(vectors[0]),
			}})
			if err != nil {
				log.Error().Err(err).Int64("memory_id", memory.ID).Msg("revision embedding store failed")
			}
		}(updated)
	}
	writeStoreMemoryResponse(w, updated, domainDecision)
}

// handleMemoryContextHistory godoc
// @Summary Read retained prior versions of a typed memory card
// @Description Current-card context and privacy checks apply before prior bodies are returned. Omit context and project for enrolled self; project context requires one existing canonical project and self/global must omit project. Results contain prior versions in ascending order; the current body is read separately.
// @Tags Memories
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Memory ID"
// @Param context query string false "Owning context; omission selects self unless project is supplied" Enums(self,project,global)
// @Param project query string false "Existing canonical project UUID; required for project and forbidden for self/global"
// @Param after_version query int false "Return versions strictly greater than this nonnegative value" minimum(0)
// @Param limit query int false "Maximum prior versions; default 50, range 1..100" minimum(1) maximum(100)
// @Success 200 {object} memoryContextHistoryResponse
// @Failure 400 {string} string "invalid selector or revision page"
// @Failure 401 {string} string "authentication or explicit keycard validation failed"
// @Failure 403 {string} string "principal context access denied"
// @Failure 404 {string} string "context or visible card not found"
// @Failure 500 {string} string "internal error"
// @Failure 503 {string} string "memory store unavailable"
// @Router /api/memories/{id}/revisions [get]
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
