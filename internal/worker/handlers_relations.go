// Package worker provides the main worker service for engram.
package worker

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/thebtf/engram/pkg/models"
)

// DefaultRelationsLimit is the default number of relations to return.
const DefaultRelationsLimit = 50

// handleGetRelations godoc
// @Summary Get observation relations
// @Description Returns all relations for a specific observation with full details.
// @Tags Relations
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Observation ID"
// @Success 200 {array} models.RelationWithDetails
// @Failure 400 {string} string "invalid observation id"
// @Failure 500 {string} string "internal error"
// @Router /api/observations/{id}/relations [get]
func (s *Service) handleGetRelations(w http.ResponseWriter, r *http.Request) {
	// Relations detail endpoint removed in v5 (observation persistence dropped).
	http.Error(w, "relations detail endpoint removed in v5", http.StatusGone)
}

// handleGetRelationGraph godoc
// @Summary Get relation graph
// @Description Returns the relation graph for an observation up to the specified depth.
// @Tags Relations
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Observation ID"
// @Param depth query int false "Graph traversal depth (default 2, max 5)"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {string} string "invalid observation id"
// @Failure 500 {string} string "internal error"
// @Router /api/observations/{id}/graph [get]
func (s *Service) handleGetRelationGraph(w http.ResponseWriter, r *http.Request) {
	// Relation graph endpoint removed in v5 (observation persistence dropped).
	http.Error(w, "relation graph endpoint removed in v5", http.StatusGone)
}

// relatedObservationRef is the v5-compatible response shape for related-observation
// lookups. In v5 we return only the ID and the stored relation confidence rather
// than the full observation payload, because observation rows no longer exist.
type relatedObservationRef struct {
	ID         int64   `json:"id"`
	Confidence float64 `json:"confidence"`
}

// handleGetRelatedObservations godoc
// @Summary Get related observations
// @Description Returns related observation IDs with their actual relation confidence in v5.
// @Tags Relations
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Observation ID"
// @Param min_confidence query number false "Minimum confidence threshold (default 0.4, range 0-1)"
// @Success 200 {array} relatedObservationRef
// @Failure 400 {string} string "invalid observation id"
// @Failure 500 {string} string "internal error"
// @Router /api/observations/{id}/related [get]
func (s *Service) handleGetRelatedObservations(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid observation id", http.StatusBadRequest)
		return
	}

	// Default confidence threshold matches the retrieval pipeline's soft floor.
	minConfidence := 0.4
	if confStr := r.URL.Query().Get("min_confidence"); confStr != "" {
		if c, err := strconv.ParseFloat(confStr, 64); err == nil && c >= 0 && c <= 1 {
			minConfidence = c
		}
	}

	relations, err := s.relationStore.GetRelationsByObservationID(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Deduplicate: when a relation appears with both source=id and target=id,
	// keep the highest confidence entry for the partner node.
	refs := make([]relatedObservationRef, 0, len(relations))
	seen := make(map[int64]int, len(relations))
	for _, relation := range relations {
		if relation == nil || relation.Confidence < minConfidence {
			continue
		}

		// Resolve the partner ID — skip self-referential edges.
		relatedID := relation.SourceID
		if relatedID == id {
			relatedID = relation.TargetID
		}
		if relatedID == id {
			continue
		}

		if idx, ok := seen[relatedID]; ok {
			// Already seen this partner — keep the higher confidence score.
			if relation.Confidence > refs[idx].Confidence {
				refs[idx].Confidence = relation.Confidence
			}
			continue
		}

		seen[relatedID] = len(refs)
		refs = append(refs, relatedObservationRef{
			ID:         relatedID,
			Confidence: relation.Confidence,
		})
	}

	writeJSON(w, refs)
}

// handleGetRelationsByType godoc
// @Summary Get relations by type
// @Description Returns all relations of a specific type (e.g., supersedes, contradicts, extends).
// @Tags Relations
// @Produce json
// @Security ApiKeyAuth
// @Param type path string true "Relation type"
// @Param limit query int false "Number of results (default 50, max 100)"
// @Success 200 {array} models.ObservationRelation
// @Failure 400 {string} string "invalid relation type"
// @Failure 500 {string} string "internal error"
// @Router /api/relations/type/{type} [get]
func (s *Service) handleGetRelationsByType(w http.ResponseWriter, r *http.Request) {
	relType := chi.URLParam(r, "type")

	// Validate before hitting the DB — unknown types would return empty results
	// silently, making API misuse invisible.
	validType := false
	for _, t := range models.AllRelationTypes {
		if string(t) == relType {
			validType = true
			break
		}
	}
	if !validType {
		http.Error(w, "invalid relation type", http.StatusBadRequest)
		return
	}

	limit := DefaultRelationsLimit
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 100 {
			limit = l
		}
	}

	relations, err := s.relationStore.GetRelationsByType(r.Context(), models.RelationType(relType), limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if relations == nil {
		relations = []*models.ObservationRelation{}
	}

	writeJSON(w, relations)
}

// handleGetRelationStats godoc
// @Summary Get relation statistics
// @Description Returns statistics about relations including total count, high confidence count, and breakdown by type.
// @Tags Relations
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} map[string]interface{}
// @Failure 500 {string} string "internal error"
// @Router /api/relations/stats [get]
func (s *Service) handleGetRelationStats(w http.ResponseWriter, r *http.Request) {
	totalCount, err := s.relationStore.GetTotalRelationCount(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// 0.7 matches the threshold used by the knowledge graph dashboard widget.
	highConfRelations, err := s.relationStore.GetHighConfidenceRelations(r.Context(), 0.7, 1000)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Per-type breakdown used by the graph dashboard.
	typeCounts := make(map[string]int)
	for _, t := range models.AllRelationTypes {
		relations, err := s.relationStore.GetRelationsByType(r.Context(), t, 1000)
		if err == nil {
			typeCounts[string(t)] = len(relations)
		}
	}

	writeJSON(w, map[string]interface{}{
		"total_count":         totalCount,
		"high_confidence":     len(highConfRelations),
		"by_type":             typeCounts,
		"min_confidence_used": 0.4,
	})
}
