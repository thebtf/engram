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
	"github.com/thebtf/engram/internal/config"
	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/vector"
	"github.com/thebtf/engram/pkg/models"
)

type ingestSupersessionTestVectorClient struct {
	results []vector.QueryResult
}

func (c ingestSupersessionTestVectorClient) AddDocuments(context.Context, []vector.Document) error {
	return nil
}

func (c ingestSupersessionTestVectorClient) DeleteDocuments(context.Context, []string) error {
	return nil
}

func (c ingestSupersessionTestVectorClient) Query(context.Context, string, int, vector.WhereFilter) ([]vector.QueryResult, error) {
	return c.results, nil
}

func (c ingestSupersessionTestVectorClient) IsConnected() bool {
	return true
}

func (c ingestSupersessionTestVectorClient) Close() error {
	return nil
}

func (c ingestSupersessionTestVectorClient) Count(context.Context) (int64, error) {
	return int64(len(c.results)), nil
}

func (c ingestSupersessionTestVectorClient) ModelVersion() string {
	return "test-model"
}

func (c ingestSupersessionTestVectorClient) NeedsRebuild(context.Context) (bool, string) {
	return false, ""
}

func (c ingestSupersessionTestVectorClient) GetStaleVectors(context.Context) ([]vector.StaleVectorInfo, error) {
	return nil, nil
}

func (c ingestSupersessionTestVectorClient) GetHealthStats(context.Context) (*vector.HealthStats, error) {
	return &vector.HealthStats{}, nil
}

func (c ingestSupersessionTestVectorClient) GetCacheStats() vector.CacheStatsSnapshot {
	return vector.CacheStatsSnapshot{}
}

func (c ingestSupersessionTestVectorClient) GetMetrics(context.Context) vector.VectorMetricsSnapshot {
	return vector.VectorMetricsSnapshot{}
}

func (c ingestSupersessionTestVectorClient) DeleteByObservationID(context.Context, int64) error {
	return nil
}

func disableIngestStorePathSupersession(t *testing.T) {
	t.Helper()

	original, hadOriginal := os.LookupEnv("ENGRAM_STORE_PATH_SUPERSESSION_ENABLED")
	require.NoError(t, os.Setenv("ENGRAM_STORE_PATH_SUPERSESSION_ENABLED", "false"))
	_, _, err := config.Reload()
	require.NoError(t, err)

	t.Cleanup(func() {
		var restoreErr error
		if hadOriginal {
			restoreErr = os.Setenv("ENGRAM_STORE_PATH_SUPERSESSION_ENABLED", original)
		} else {
			restoreErr = os.Unsetenv("ENGRAM_STORE_PATH_SUPERSESSION_ENABLED")
		}
		require.NoError(t, restoreErr)
		_, _, err := config.Reload()
		require.NoError(t, err)
	})
}

func newIngestSupersessionTestService(t *testing.T, vectorClient vector.Client) (*Service, *dbgorm.ObservationStore) {
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
		vectorClient:     vectorClient,
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

func TestHandleIngestEvent_DisabledStorePathSupersessionKeepsExistingObservationActive(t *testing.T) {
	disableIngestStorePathSupersession(t)

	ctx := context.Background()
	project := "ingest-" + uuid.NewString()
	service, observationStore := newIngestSupersessionTestService(t, nil)
	existingID := seedIngestObservation(t, ctx, observationStore, project)
	service.vectorClient = ingestSupersessionTestVectorClient{results: []vector.QueryResult{{
		Similarity: 0.80,
		Metadata: map[string]any{
			"sqlite_id": float64(existingID),
			"doc_type":  string(vector.DocTypeObservation),
			"project":   project,
		},
	}}}

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

	storedExisting, err := observationStore.GetObservationByID(ctx, existingID)
	require.NoError(t, err)
	require.False(t, storedExisting.IsSuperseded)
}
