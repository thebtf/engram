package worker

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestHandleIngestEvent_ReturnsRemovedInV5(t *testing.T) {
	service := &Service{}

	body, err := json.Marshal(IngestRequest{
		SessionID:     "session-" + uuid.NewString(),
		Project:       "ingest-" + uuid.NewString(),
		ToolName:      "Edit",
		ToolInput:     map[string]any{"file_path": "internal/example.go", "new_string": "updated content"},
		ToolResult:    strings.Repeat("updated a critical authentication handler with additional validation. ", 2),
		WorkstationID: "test-workstation",
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/events/ingest", bytes.NewReader(body))
	w := httptest.NewRecorder()
	service.handleIngestEvent(w, req)

	require.Equal(t, http.StatusNotImplemented, w.Code)

	var result map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	require.Equal(t, "removed_in_v5", result["status"])
	require.Equal(t, "event ingest endpoint was removed in v5", result["error"])
	require.Equal(t, "", result["tool_name"])
	require.Equal(t, "", result["workstation_id"])
}

func TestHandleIngestEvent_DoesNotReadInvalidBody(t *testing.T) {
	service := &Service{}
	req := httptest.NewRequest(http.MethodPost, "/api/events/ingest", strings.NewReader("{"))
	w := httptest.NewRecorder()

	service.handleIngestEvent(w, req)

	require.Equal(t, http.StatusNotImplemented, w.Code)
	var result map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	require.Equal(t, "removed_in_v5", result["status"])
	require.Equal(t, "", result["tool_name"])
}
