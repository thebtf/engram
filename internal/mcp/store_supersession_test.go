package mcp

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/config"
	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/dedup"
	"github.com/thebtf/engram/internal/vector"
	"github.com/thebtf/engram/pkg/models"
)

type storeSupersessionTestVectorClient struct {
	results []vector.QueryResult
}

func (c storeSupersessionTestVectorClient) AddDocuments(context.Context, []vector.Document) error {
	return nil
}

func (c storeSupersessionTestVectorClient) DeleteDocuments(context.Context, []string) error {
	return nil
}

func (c storeSupersessionTestVectorClient) Query(context.Context, string, int, vector.WhereFilter) ([]vector.QueryResult, error) {
	return c.results, nil
}

func (c storeSupersessionTestVectorClient) IsConnected() bool {
	return true
}

func (c storeSupersessionTestVectorClient) Close() error {
	return nil
}

func (c storeSupersessionTestVectorClient) Count(context.Context) (int64, error) {
	return int64(len(c.results)), nil
}

func (c storeSupersessionTestVectorClient) ModelVersion() string {
	return "test-model"
}

func (c storeSupersessionTestVectorClient) NeedsRebuild(context.Context) (bool, string) {
	return false, ""
}

func (c storeSupersessionTestVectorClient) GetStaleVectors(context.Context) ([]vector.StaleVectorInfo, error) {
	return nil, nil
}

func (c storeSupersessionTestVectorClient) GetHealthStats(context.Context) (*vector.HealthStats, error) {
	return &vector.HealthStats{}, nil
}

func (c storeSupersessionTestVectorClient) GetCacheStats() vector.CacheStatsSnapshot {
	return vector.CacheStatsSnapshot{}
}

func (c storeSupersessionTestVectorClient) GetMetrics(context.Context) vector.VectorMetricsSnapshot {
	return vector.VectorMetricsSnapshot{}
}

func (c storeSupersessionTestVectorClient) DeleteByObservationID(context.Context, int64) error {
	return nil
}

func disableStorePathSupersession(t *testing.T) {
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

func newStoreSupersessionTestServer(t *testing.T, vectorClient vector.Client) (*Server, *dbgorm.ObservationStore, *dbgorm.RelationStore) {
	t.Helper()

	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping integration test")
	}

	store, err := dbgorm.NewStore(dbgorm.Config{DSN: dsn, MaxConns: 2})
	require.NoError(t, err)

	observationStore := dbgorm.NewObservationStore(store, nil)
	relationStore := dbgorm.NewRelationStore(store)
	server := NewServer(nil, "test", observationStore, nil, relationStore, nil, vectorClient, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	t.Cleanup(func() {
		observationStore.Close()
		require.NoError(t, store.Close())
	})

	return server, observationStore, relationStore
}

func seedObservation(t *testing.T, ctx context.Context, observationStore *dbgorm.ObservationStore, project, title, narrative string) int64 {
	t.Helper()

	id, _, err := observationStore.StoreObservation(ctx, "seed-"+uuid.NewString(), project, &models.ParsedObservation{
		Type:       models.ObsTypeDiscovery,
		SourceType: models.SourceManual,
		MemoryType: models.MemTypeContext,
		Title:      title,
		Narrative:  narrative,
		Scope:      models.ScopeProject,
	}, 0, 0)
	require.NoError(t, err)
	return id
}

func updateVectorClient(existingID int64, project string) vector.Client {
	return storeSupersessionTestVectorClient{results: []vector.QueryResult{{
		Similarity: 0.80,
		Metadata: map[string]any{
			"sqlite_id": float64(existingID),
			"doc_type":  string(vector.DocTypeObservation),
			"project":   project,
		},
	}}}
}

func TestHandleStoreMemory_DisabledStorePathSupersessionKeepsExistingObservationActive(t *testing.T) {
	disableStorePathSupersession(t)

	ctx := context.Background()
	project := "store-memory-" + uuid.NewString()
	server, observationStore, relationStore := newStoreSupersessionTestServer(t, nil)
	existingID := seedObservation(t, ctx, observationStore, project, "Existing observation", "Existing content")
	server.vectorClient = updateVectorClient(existingID, project)

	resultJSON, err := server.handleStoreMemory(ctx, json.RawMessage(`{"project":"`+project+`","content":"New contradictory content","title":"New observation"}`))
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal([]byte(resultJSON), &result))
	require.Equal(t, "UPDATE", result["action"])
	_, hasSupersededID := result["superseded_id"]
	require.False(t, hasSupersededID)

	storedExisting, err := observationStore.GetObservationByID(ctx, existingID)
	require.NoError(t, err)
	require.False(t, storedExisting.IsSuperseded)

	newID := int64(result["id"].(float64))
	relations, err := relationStore.GetOutgoingRelations(ctx, newID)
	require.NoError(t, err)
	require.Len(t, relations, 0)
}

func TestStoreExtractedObservation_DisabledStorePathSupersessionKeepsExistingObservationActive(t *testing.T) {
	disableStorePathSupersession(t)

	ctx := context.Background()
	project := "extract-" + uuid.NewString()
	server, observationStore, relationStore := newStoreSupersessionTestServer(t, nil)
	existingID := seedObservation(t, ctx, observationStore, project, "Existing extracted observation", "Existing extracted narrative")
	server.vectorClient = updateVectorClient(existingID, project)

	action, obsID, err := server.storeExtractedObservation(ctx, project, &models.ParsedObservation{
		Type:       models.ObsTypeDiscovery,
		SourceType: models.SourceLLMDerived,
		MemoryType: models.MemTypeInsight,
		Title:      "Extracted observation",
		Narrative:  "A new extracted narrative that lands in the update similarity band.",
		Concepts:   []string{"pattern"},
		Scope:      models.ScopeProject,
	})
	require.NoError(t, err)
	require.Equal(t, dedup.ActionUpdate, action)
	require.NotZero(t, obsID)

	storedExisting, err := observationStore.GetObservationByID(ctx, existingID)
	require.NoError(t, err)
	require.False(t, storedExisting.IsSuperseded)

	relations, err := relationStore.GetOutgoingRelations(ctx, obsID)
	require.NoError(t, err)
	require.Len(t, relations, 0)
}
