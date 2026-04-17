package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

func newIngestTestService(t *testing.T) (*Service, *dbgorm.ObservationStore) {
	t.Helper()

	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping integration test")
	}

	store, err := dbgorm.NewStore(dbgorm.Config{DSN: dsn, MaxConns: 2})
	require.NoError(t, err)

	observationStore := dbgorm.NewObservationStore(store, nil)
	rawEventStore := dbgorm.NewRawEventStore(store)
	service := &Service{
		observationStore: observationStore,
		rawEventStore:    rawEventStore,
	}

	t.Cleanup(func() {
		observationStore.Close()
		require.NoError(t, store.Close())
	})

	return service, observationStore
}

func seedIngestObservation(t *testing.T, ctx context.Context, observationStore *dbgorm.ObservationStore, project string) int64 {
	t.Helper()

	id, _, err := observationStore.StoreObservation(ctx, "seed-"+uuid.NewString(), project, &models.ParsedObservation{
		Type:       models.ObsTypeChange,
		SourceType: models.SourceToolVerified,
		MemoryType: models.MemTypeContext,
		Title:      "Existing ingest observation",
		Narrative:  "Existing observation used to simulate an UPDATE dedup result.",
		Scope:      models.ScopeProject,
	}, 0, 0)
	require.NoError(t, err)
	return id
}

func TestHandleIngestEvent_StoresObservationAndReturnsAccepted(t *testing.T) {
	ctx := context.Background()
	project := "ingest-" + uuid.NewString()
	service, observationStore := newIngestTestService(t)
	_ = seedIngestObservation(t, ctx, observationStore, project)

	body, err := json.Marshal(IngestRequest{
		SessionID:     "session-" + uuid.NewString(),
		Project:       project,
		ToolName:      "Edit",
		ToolInput:     map[string]any{"file_path": "internal/example.go", "new_string": "updated content"},
		ToolResult:    strings.Repeat("updated a critical authentication handler with additional validation. ", 2),
		WorkstationID: "test-workstation",
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/events/ingest", bytes.NewReader(body))
	w := httptest.NewRecorder()
	service.handleIngestEvent(w, req)

	require.Equal(t, http.StatusAccepted, w.Code)

	var result map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	require.Equal(t, "accepted", result["status"])
	require.NotZero(t, int64(result["obs_id"].(float64)))
}
