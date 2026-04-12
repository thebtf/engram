package worker

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/vector"
	"github.com/thebtf/engram/pkg/models"
)

func TestHandleMemoryTriggers_EditSemanticMatchReturnsWarningAndUsesFileScope(t *testing.T) {
	service := newRetrievalTestService()
	var capturedWhere vector.WhereFilter

	service.retrievalHooks.vectorQuery = func(_ context.Context, query string, limit int, where vector.WhereFilter) ([]vector.QueryResult, error) {
		capturedWhere = where
		require.Contains(t, query, "internal/auth.go")
		return []vector.QueryResult{{
			Similarity: 0.91,
			Metadata: map[string]any{
				"sqlite_id": float64(7),
				"doc_type":  "observation",
				"project":   "engram",
				"scope":     "project",
			},
		}}, nil
	}
	service.retrievalHooks.getObservationsByIDs = func(_ context.Context, ids []int64, _ string, _ int) ([]*models.Observation, error) {
		require.Equal(t, []int64{7}, ids)
		return []*models.Observation{
			newTriggerObservation(7, models.ObsTypeBugfix, "Fix auth bug", "This edit previously failed in auth.go", nil),
		}, nil
	}

	body, err := json.Marshal(MemoryTriggerRequest{
		Tool:      "Edit",
		Params:    map[string]any{"file_path": "internal/auth.go", "new_string": "add auth validation"},
		Project:   "engram",
		SessionID: "session-1",
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/memory/triggers", bytes.NewReader(body))
	w := httptest.NewRecorder()

	service.handleMemoryTriggers(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var matches []MemoryTriggerMatch
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &matches))
	require.Len(t, matches, 1)
	require.Equal(t, "warning", matches[0].Kind)
	require.Equal(t, int64(7), matches[0].ObservationID)
	require.Contains(t, matches[0].Blurb, "auth.go")

	require.True(t, hasTriggerProjectScopeClause(capturedWhere, "engram"), "expected project/global scope clause")
	require.False(t, hasTriggerFileScopeClause(capturedWhere, "internal/auth.go"), "pgvector backend does not persist file path metadata in vectors table")
}

func TestHandleMemoryTriggers_EditSemanticMatchFiltersTypesAndCapsTop3(t *testing.T) {
	service := newRetrievalTestService()

	service.retrievalHooks.vectorQuery = func(_ context.Context, _ string, _ int, _ vector.WhereFilter) ([]vector.QueryResult, error) {
		return []vector.QueryResult{
			{Similarity: 0.95, Metadata: map[string]any{"sqlite_id": float64(1), "doc_type": "observation", "project": "engram", "scope": "project"}},
			{Similarity: 0.94, Metadata: map[string]any{"sqlite_id": float64(2), "doc_type": "observation", "project": "engram", "scope": "project"}},
			{Similarity: 0.93, Metadata: map[string]any{"sqlite_id": float64(3), "doc_type": "observation", "project": "engram", "scope": "project"}},
			{Similarity: 0.92, Metadata: map[string]any{"sqlite_id": float64(4), "doc_type": "observation", "project": "engram", "scope": "project"}},
			{Similarity: 0.91, Metadata: map[string]any{"sqlite_id": float64(5), "doc_type": "observation", "project": "engram", "scope": "project"}},
		}, nil
	}
	service.retrievalHooks.getObservationsByIDs = func(_ context.Context, ids []int64, _ string, _ int) ([]*models.Observation, error) {
		require.Equal(t, []int64{1, 2, 3, 4, 5}, ids)
		return []*models.Observation{
			newTriggerObservation(4, models.ObsTypePitfall, "Pitfall", "Known pitfall", nil),
			newTriggerObservation(1, models.ObsTypeBugfix, "Bugfix", "Known bugfix", nil),
			newTriggerObservation(5, models.ObsTypeBugfix, "Another bugfix", "Should be capped out", nil),
			newTriggerObservation(2, models.ObsTypeDiscovery, "Discovery", "Should be filtered out", nil),
			newTriggerObservation(3, models.ObsTypeGuidance, "Guidance", "General guidance", nil),
		}, nil
	}

	body, err := json.Marshal(MemoryTriggerRequest{
		Tool:      "Write",
		Params:    map[string]any{"file_path": "internal/auth.go", "content": "updated auth pipeline"},
		Project:   "engram",
		SessionID: "session-1",
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/memory/triggers", bytes.NewReader(body))
	w := httptest.NewRecorder()

	service.handleMemoryTriggers(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var matches []MemoryTriggerMatch
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &matches))
	require.Len(t, matches, 3)
	require.Equal(t, int64(1), matches[0].ObservationID)
	require.Equal(t, "warning", matches[0].Kind)
	require.Equal(t, int64(3), matches[1].ObservationID)
	require.Equal(t, "context", matches[1].Kind)
	require.Equal(t, int64(4), matches[2].ObservationID)
	require.Equal(t, "warning", matches[2].Kind)
}

func TestHandleMemoryTriggers_TimeoutReturnsEmptyArray(t *testing.T) {
	service := newRetrievalTestService()
	service.retrievalHooks.vectorQuery = func(ctx context.Context, _ string, _ int, _ vector.WhereFilter) ([]vector.QueryResult, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}

	body, err := json.Marshal(MemoryTriggerRequest{
		Tool:      "Edit",
		Params:    map[string]any{"file_path": "internal/auth.go", "new_string": "add auth validation"},
		Project:   "engram",
		SessionID: "session-1",
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/memory/triggers", bytes.NewReader(body))
	w := httptest.NewRecorder()

	service.handleMemoryTriggers(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var matches []MemoryTriggerMatch
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &matches))
	require.NotNil(t, matches)
	require.Len(t, matches, 0)
}

func newTriggerObservation(id int64, obsType models.ObservationType, title, narrative string, concepts []string) *models.Observation {
	return &models.Observation{
		ID:        id,
		Type:      obsType,
		Title:     sql.NullString{String: title, Valid: title != ""},
		Narrative: sql.NullString{String: narrative, Valid: narrative != ""},
		Concepts:  concepts,
		Scope:     models.ScopeProject,
		Project:   "engram",
	}
}

func hasTriggerProjectScopeClause(where vector.WhereFilter, project string) bool {
	for _, clause := range where.Clauses {
		if len(clause.OrGroup) != 2 {
			continue
		}
		left := clause.OrGroup[0]
		right := clause.OrGroup[1]
		if left.Column == "project" && left.Operator == "=" && left.Value == project &&
			right.Column == "scope" && right.Operator == "=" && right.Value == "global" {
			return true
		}
	}
	return false
}

func hasTriggerFileScopeClause(where vector.WhereFilter, filePath string) bool {
	for _, clause := range where.Clauses {
		if len(clause.OrGroup) != 2 {
			continue
		}
		left := clause.OrGroup[0]
		right := clause.OrGroup[1]
		if left.Column == "files_modified" && left.Operator == "?|" &&
			right.Column == "files_read" && right.Operator == "?|" {
			leftPaths, leftOK := left.Value.([]string)
			rightPaths, rightOK := right.Value.([]string)
			if leftOK && rightOK && len(leftPaths) == 1 && len(rightPaths) == 1 && leftPaths[0] == filePath && rightPaths[0] == filePath {
				return true
			}
		}
	}
	return false
}
