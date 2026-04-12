package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/pkg/models"
)

func TestHandleMemoryTriggers_RepeatedReadReturnsDecisionAndPatternContext(t *testing.T) {
	service := newRetrievalTestService()
	service.retrievalHooks.readSignalCountForPath = func(sessionID, filePath string) int {
		require.Equal(t, "session-read-1", sessionID)
		require.Equal(t, "internal/auth.go", filePath)
		return 3
	}
	service.retrievalHooks.filePathObservations = func(_ context.Context, _ string, _ string, limit int) ([]*models.Observation, error) {
		require.Equal(t, 20, limit)
		return []*models.Observation{
			newTriggerObservation(11, models.ObsTypeDecision, "Decision", "Auth design decision", nil),
			newTriggerObservation(12, models.ObsTypeDiscovery, "Non-pattern discovery", "Should be filtered out", nil),
			newTriggerObservation(13, models.ObsTypeDiscovery, "Pattern discovery", "Useful pattern", []string{"pattern"}),
			newTriggerObservation(14, models.ObsTypeDecision, "Second decision", "Another decision", nil),
		}, nil
	}

	body, err := json.Marshal(MemoryTriggerRequest{
		Tool:      "Read",
		Params:    map[string]any{"file_path": "internal/auth.go"},
		Project:   "engram",
		SessionID: "session-read-1",
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/memory/triggers", bytes.NewReader(body))
	w := httptest.NewRecorder()

	service.handleMemoryTriggers(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var matches []MemoryTriggerMatch
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &matches))
	require.Len(t, matches, 3)
	for _, match := range matches {
		require.Equal(t, "context", match.Kind)
	}
	require.Equal(t, int64(11), matches[0].ObservationID)
	require.Equal(t, int64(13), matches[1].ObservationID)
	require.Equal(t, int64(14), matches[2].ObservationID)
}

func TestHandleMemoryTriggers_ReadBelowThresholdReturnsEmptyArray(t *testing.T) {
	service := newRetrievalTestService()
	service.retrievalHooks.readSignalCountForPath = func(sessionID, filePath string) int {
		require.Equal(t, "session-read-2", sessionID)
		require.Equal(t, "internal/auth.go", filePath)
		return 2
	}
	service.retrievalHooks.filePathObservations = func(_ context.Context, _ string, _ string, _ int) ([]*models.Observation, error) {
		t.Fatal("file path query should not run below threshold")
		return nil, nil
	}

	body, err := json.Marshal(MemoryTriggerRequest{
		Tool:      "Read",
		Params:    map[string]any{"file_path": "internal/auth.go"},
		Project:   "engram",
		SessionID: "session-read-2",
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
