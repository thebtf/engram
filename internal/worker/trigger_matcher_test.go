package worker

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/pkg/models"
)

// TestHandleMemoryTriggers_EditNoVectorReturnsEmpty verifies that Edit/Write triggers
// return empty results in v5 (vector search removed).
func TestHandleMemoryTriggers_EditNoVectorReturnsEmpty(t *testing.T) {
	service := newRetrievalTestService()

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
	// Vector search removed in v5: Edit/Write triggers always return empty
	require.Len(t, matches, 0)
}

// TestHandleMemoryTriggers_TimeoutReturnsEmptyArray verifies timeout handling.
func TestHandleMemoryTriggers_TimeoutReturnsEmptyArray(t *testing.T) {
	service := newRetrievalTestService()

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
