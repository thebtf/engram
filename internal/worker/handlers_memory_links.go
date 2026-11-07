package worker

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/thebtf/engram/pkg/models"
)

type createMemoryLinkRequest struct {
	TargetID        int64           `json:"target_id"`
	Relation        string          `json:"relation"`
	TargetVersion   *int            `json:"target_version"`
	RequestKey      json.RawMessage `json:"request_key"`
	SourceSessionID string          `json:"source_session_id"`
}

// memoryLinkRequestSchema describes the decoded JSON request, not json.RawMessage's Go representation.
type memoryLinkRequestSchema struct {
	TargetID        int64  `json:"target_id" binding:"required" minimum:"1"`
	Relation        string `json:"relation" binding:"required" enums:"related_to,references"`
	TargetVersion   *int   `json:"target_version" minimum:"1"`
	RequestKey      string `json:"request_key"`
	SourceSessionID string `json:"source_session_id"`
}

// memoryLinkResponseSchema mirrors MemoryLink.MarshalJSON, including its nested author.
type memoryLinkResponseSchema struct {
	ID              int64               `json:"id"`
	SourceID        int64               `json:"source_id"`
	TargetID        int64               `json:"target_id"`
	Relation        string              `json:"relation" enums:"related_to,references"`
	TargetVersion   *int                `json:"target_version"`
	Author          models.MemoryAuthor `json:"author"`
	SourceSessionID string              `json:"source_session_id"`
}

type memoryLinkPageSchema struct {
	Links      []memoryLinkResponseSchema `json:"links"`
	NextCursor *int64                     `json:"next_cursor"`
}

func (s *Service) selfLinkContext(w http.ResponseWriter, r *http.Request) (models.MemoryContext, bool) {
	if s.memoryStore == nil {
		http.Error(w, "memory store unavailable", http.StatusServiceUnavailable)
		return models.MemoryContext{}, false
	}
	if _, ok := r.URL.Query()["project"]; ok {
		http.Error(w, "invalid memory context", http.StatusBadRequest)
		return models.MemoryContext{}, false
	}
	if _, ok := r.URL.Query()["context"]; ok {
		http.Error(w, "invalid memory context", http.StatusBadRequest)
		return models.MemoryContext{}, false
	}
	owning, err := s.resolveMemoryOwningContext(r, "", false, "", false)
	if err != nil {
		writeMemoryContextError(w, err)
		return owning, false
	}
	return owning, true
}

func memoryLinkPathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, errMemoryContextInvalid
	}
	return id, nil
}

// handleCreateMemoryLink godoc
// @Summary Link two self-memory cards
// @Description Requires an explicitly validated keycard with an enrolled canonical principal; project and context query parameters are forbidden. Both cards must be active and manageable by this keycard; workstation-private restrictions apply and links grant no access. Auth-disabled mode is not privacy isolation. The source and target must differ. related_to is unordered and cannot have a non-null target_version; references is directional and may pin a positive current or retained target revision. An omitted or null target_version leaves the link unpinned. Omit request_key or supply a nonempty JSON string of at most 1024 UTF-8 bytes; null is invalid. Keys are scoped to the canonical principal and normalized assertion. A duplicate assertion returns 200 with its original author and untrusted source_session_id provenance, including when another key is supplied; a key reused for another assertion or a removed link returns 409. New assertions return 201. source_session_id is caller-supplied untrusted provenance, not identity.
// @Tags Memories
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Source memory ID" minimum(1)
// @Param body body memoryLinkRequestSchema true "Link assertion"
// @Success 201 {object} memoryLinkResponseSchema "New assertion"
// @Success 200 {object} memoryLinkResponseSchema "Existing assertion"
// @Failure 400 {string} string "Invalid selector, ID, JSON, relation, revision or request key"
// @Failure 401 {string} string "Authentication or explicit keycard validation failed"
// @Failure 403 {string} string "Enrolled principal required"
// @Failure 404 {string} string "Active manageable endpoint or target revision not found"
// @Failure 409 {string} string "Request key conflicts or its link was removed"
// @Failure 500 {string} string "Internal error"
// @Failure 503 {string} string "Memory store unavailable"
// @Router /api/memories/{id}/links [post]
func (s *Service) handleCreateMemoryLink(w http.ResponseWriter, r *http.Request) {
	owning, ok := s.selfLinkContext(w, r)
	if !ok {
		return
	}
	id, err := memoryLinkPathID(r)
	if err != nil {
		writeMemoryContextError(w, err)
		return
	}
	var request createMemoryLinkRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "invalid link", http.StatusBadRequest)
		return
	}
	key, err := memoryRequestKey(request.RequestKey)
	if err != nil {
		http.Error(w, "invalid request_key", http.StatusBadRequest)
		return
	}
	if request.TargetID <= 0 || request.TargetID == id || (request.Relation != "related_to" && request.Relation != "references") || (request.TargetVersion != nil && (*request.TargetVersion <= 0 || request.Relation != "references")) {
		http.Error(w, "invalid link", http.StatusBadRequest)
		return
	}
	link, created, err := s.memoryStore.CreateSelfLink(r.Context(), owning, memoryVisibilityCaller(r.Context(), ""), id, request.TargetID, request.Relation, request.TargetVersion, key, request.SourceSessionID)
	if err != nil {
		writeMemoryContextError(w, err)
		return
	}
	if created {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
	}
	writeJSON(w, link)
}

// handleListMemoryLinks godoc
// @Summary List links for a self-memory card
// @Description Requires an explicitly validated keycard with an enrolled canonical principal; project and context query parameters are forbidden. The requested card must be active and visible; only links with two active visible endpoints are returned, with workstation-private restrictions applied before pagination. related_to is returned from either endpoint; references only from its source. Links do not grant access. Auth-disabled mode is not privacy isolation. Results are ordered by link ID; next_cursor is the last returned link ID when more results exist, otherwise null. Pass it as cursor to continue. target_version is null when unpinned; source_session_id is untrusted caller-supplied provenance.
// @Tags Memories
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Memory ID" minimum(1)
// @Param limit query int false "Page size; defaults to 50" minimum(1) maximum(100)
// @Param cursor query int false "Positive next_cursor from the preceding page" minimum(1)
// @Success 200 {object} memoryLinkPageSchema
// @Failure 400 {string} string "Invalid selector, ID or page"
// @Failure 401 {string} string "Authentication or explicit keycard validation failed"
// @Failure 403 {string} string "Enrolled principal required"
// @Failure 404 {string} string "Active visible card not found"
// @Failure 500 {string} string "Internal error"
// @Failure 503 {string} string "Memory store unavailable"
// @Router /api/memories/{id}/links [get]
func (s *Service) handleListMemoryLinks(w http.ResponseWriter, r *http.Request) {
	owning, ok := s.selfLinkContext(w, r)
	if !ok {
		return
	}
	id, err := memoryLinkPathID(r)
	if err != nil {
		writeMemoryContextError(w, err)
		return
	}
	limit, after := 50, int64(0)
	query := r.URL.Query()
	if values, ok := query["limit"]; ok {
		if len(values) != 1 {
			http.Error(w, "invalid link page", http.StatusBadRequest)
			return
		}
		limit, err = strconv.Atoi(values[0])
		if err != nil || limit < 1 || limit > 100 {
			http.Error(w, "invalid link page", http.StatusBadRequest)
			return
		}
	}
	if values, ok := query["cursor"]; ok {
		if len(values) != 1 {
			http.Error(w, "invalid link page", http.StatusBadRequest)
			return
		}
		after, err = strconv.ParseInt(values[0], 10, 64)
		if err != nil || after <= 0 {
			http.Error(w, "invalid link page", http.StatusBadRequest)
			return
		}
	}
	links, next, err := s.memoryStore.ListSelfLinks(r.Context(), owning, memoryVisibilityCaller(r.Context(), ""), id, after, limit)
	if err != nil {
		writeMemoryContextError(w, err)
		return
	}
	writeJSON(w, map[string]any{"links": links, "next_cursor": next})
}

// handleDeleteMemoryLink godoc
// @Summary Remove a self-memory link
// @Description Requires an explicitly validated keycard with an enrolled canonical principal; project and context query parameters are forbidden. Both endpoints must be active and manageable by the keycard and the link must belong to its canonical principal. Workstation-private restrictions apply; auth-disabled mode is not privacy isolation. Removal does not delete either card. An already removed or inaccessible link returns 404; restoring an inactive endpoint does not restore a removed link.
// @Tags Memories
// @Security ApiKeyAuth
// @Param id path int true "Link ID" minimum(1)
// @Success 204 "Link removed"
// @Failure 400 {string} string "Invalid selector or ID"
// @Failure 401 {string} string "Authentication or explicit keycard validation failed"
// @Failure 403 {string} string "Enrolled principal required"
// @Failure 404 {string} string "Link or active manageable endpoint not found"
// @Failure 500 {string} string "Internal error"
// @Failure 503 {string} string "Memory store unavailable"
// @Router /api/memory-links/{id} [delete]
func (s *Service) handleDeleteMemoryLink(w http.ResponseWriter, r *http.Request) {
	owning, ok := s.selfLinkContext(w, r)
	if !ok {
		return
	}
	id, err := memoryLinkPathID(r)
	if err != nil {
		writeMemoryContextError(w, err)
		return
	}
	if err := s.memoryStore.RemoveSelfLink(r.Context(), owning, memoryVisibilityCaller(r.Context(), ""), id); err != nil {
		writeMemoryContextError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
