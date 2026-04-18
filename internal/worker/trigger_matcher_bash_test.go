package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/privacy"
	"gorm.io/gorm/logger"

	"github.com/thebtf/engram/pkg/models"
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

func TestHandleMemoryTriggers_BashCommandPrefixMatchesTop3Warnings(t *testing.T) {
	observationStore, store, cleanup := testObservationStoreForWorker(t)
	defer cleanup()

	ctx := context.Background()
	project := fmt.Sprintf("engram-trigger-%d", time.Now().UnixNano())
	sdkSessionID := fmt.Sprintf("claude-bash-%d", time.Now().UnixNano())
	sessionStore := dbgorm.NewSessionStore(store)
	_, err := sessionStore.CreateSDKSession(ctx, sdkSessionID, project, "")
	require.NoError(t, err)

	seedBashTriggerObservation(t, observationStore, sdkSessionID, project, models.ObsTypeBugfix, "Force push incident", []string{"git push --force"})
	seedBashTriggerObservation(t, observationStore, sdkSessionID, project, models.ObsTypePitfall, "Main branch overwrite", []string{"git push --force origin main"})
	seedBashTriggerObservation(t, observationStore, sdkSessionID, project, models.ObsTypeBugfix, "Protected branch failure", []string{"git push --force origin"})
	seedBashTriggerObservation(t, observationStore, sdkSessionID, project, models.ObsTypePitfall, "Should be capped", []string{"git push --force-with-lease"})
	seedBashTriggerObservation(t, observationStore, sdkSessionID, project, models.ObsTypeDiscovery, "Discovery should be filtered", []string{"git push --force"})

	service := &Service{observationStore: observationStore}
	body, err := json.Marshal(MemoryTriggerRequest{
		Tool:      "Bash",
		Params:    map[string]any{"command": "git push --force origin main"},
		Project:   project,
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
	for _, match := range matches {
		require.Equal(t, "warning", match.Kind)
		require.NotZero(t, match.ObservationID)
		require.NotEmpty(t, match.Blurb)
	}
}

func TestHandleMemoryTriggers_BashCommandWithoutMatchReturnsEmptyArray(t *testing.T) {
	observationStore, store, cleanup := testObservationStoreForWorker(t)
	defer cleanup()

	ctx := context.Background()
	project := fmt.Sprintf("engram-trigger-%d", time.Now().UnixNano())
	sdkSessionID := fmt.Sprintf("claude-bash-empty-%d", time.Now().UnixNano())
	sessionStore := dbgorm.NewSessionStore(store)
	_, err := sessionStore.CreateSDKSession(ctx, sdkSessionID, project, "")
	require.NoError(t, err)

	seedBashTriggerObservation(t, observationStore, sdkSessionID, project, models.ObsTypeBugfix, "Unrelated build failure", []string{"go build ./..."})

	service := &Service{observationStore: observationStore}
	body, err := json.Marshal(MemoryTriggerRequest{
		Tool:      "Bash",
		Params:    map[string]any{"command": "ls -la"},
		Project:   project,
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

func testObservationStoreForWorker(t *testing.T) (*dbgorm.ObservationStore, *dbgorm.Store, func()) {
	t.Helper()

	dsn := getenvDatabaseDSN(t)
	store, err := dbgorm.NewStore(dbgorm.Config{DSN: dsn, MaxConns: 2, LogLevel: logger.Silent})
	require.NoError(t, err)

	observationStore := dbgorm.NewObservationStore(store, nil)
	cleanup := func() {
		observationStore.Close()
		require.NoError(t, store.Close())
	}
	return observationStore, store, cleanup
}

func getenvDatabaseDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping integration test")
	}
	return dsn
}

func seedBashTriggerObservation(t *testing.T, observationStore *dbgorm.ObservationStore, sdkSessionID, project string, obsType models.ObservationType, title string, commands []string) int64 {
	t.Helper()
	id, _, err := observationStore.StoreObservation(context.Background(), sdkSessionID, project, &models.ParsedObservation{
		Type:        obsType,
		Title:       title,
		Narrative:   title + " narrative",
		CommandsRun: commands,
		SourceType:  models.SourceToolVerified,
	}, 1, 0)
	require.NoError(t, err)
	return id
}
