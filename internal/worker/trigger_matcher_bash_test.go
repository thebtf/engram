package worker

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/privacy"
)

func TestHandleMemoryTriggers_BashSecretDetectionReturnsEmptyArray(t *testing.T) {
	service := newRetrievalTestService()

	bearerToken := strings.Join([]string{"sk", "test", "secret", "token", "1234567890"}, "-")
	secretCommand := "curl -H 'Authorization: Bearer " + bearerToken + "' https://example.com"
	body, err := json.Marshal(MemoryTriggerRequest{
		Tool:      "Bash",
		Params:    map[string]any{"command": secretCommand},
		Project:   "engram",
		SessionID: "session-1",
	})
	require.NoError(t, err)
	require.True(t, privacy.ContainsSecrets(secretCommand))

	req := httptest.NewRequest(http.MethodPost, "/api/memory/triggers", bytes.NewReader(body))
	w := httptest.NewRecorder()

	service.handleMemoryTriggers(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var matches []MemoryTriggerMatch
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &matches))
	require.NotNil(t, matches)
	require.Len(t, matches, 0)
}

func TestHandleMemoryTriggers_BashCommandTriggersRemovedInV5(t *testing.T) {
	service := &Service{}
	body, err := json.Marshal(MemoryTriggerRequest{
		Tool:      "Bash",
		Params:    map[string]any{"command": "git push --force origin main"},
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

func TestHandleMemoryTriggers_BashCommandWithoutMatchReturnsEmptyArray(t *testing.T) {
	service := &Service{}
	body, err := json.Marshal(MemoryTriggerRequest{
		Tool:      "Bash",
		Params:    map[string]any{"command": "ls -la"},
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
