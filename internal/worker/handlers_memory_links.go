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
