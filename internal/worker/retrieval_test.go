package worker

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/vector"
	"github.com/thebtf/engram/pkg/models"
)

func TestRetrieveRelevant_NilVectorClient_ReturnsEmpty(t *testing.T) {
	service := newRetrievalTestService()
	observations, similarityScores, err := service.RetrieveRelevant(context.Background(), "engram", "missing", RetrievalOptions{MaxResults: 5})
	require.NoError(t, err)
	require.Empty(t, observations)
	require.Empty(t, similarityScores)
}

func TestRetrieveRelevant_EmptyVectorResults_ReturnsEmpty(t *testing.T) {
	service := newRetrievalTestService()
	service.retrievalHooks.vectorQuery = func(_ context.Context, _ string, _ int, _ vector.WhereFilter) ([]vector.QueryResult, error) {
		return []vector.QueryResult{}, nil
	}
	observations, similarityScores, err := service.RetrieveRelevant(context.Background(), "engram", "nothing", RetrievalOptions{MaxResults: 5})
	require.NoError(t, err)
	require.Empty(t, observations)
	require.Empty(t, similarityScores)
}

func TestRetrieveRelevant_MaxResultsCapsOutput(t *testing.T) {
	service := newRetrievalTestService()
	service.retrievalHooks.vectorQuery = func(_ context.Context, _ string, _ int, _ vector.WhereFilter) ([]vector.QueryResult, error) {
		return []vector.QueryResult{
			{Similarity: 0.91, Metadata: map[string]any{"sqlite_id": float64(1), "doc_type": "observation", "project": "engram"}},
			{Similarity: 0.88, Metadata: map[string]any{"sqlite_id": float64(2), "doc_type": "observation", "project": "engram"}},
			{Similarity: 0.86, Metadata: map[string]any{"sqlite_id": float64(3), "doc_type": "observation", "project": "engram"}},
		}, nil
	}
	service.retrievalHooks.getObservationsByIDs = func(_ context.Context, ids []int64, _ string, _ int) ([]*models.Observation, error) {
		return []*models.Observation{
			newObservation(ids[0], "Alpha"),
			newObservation(ids[1], "Beta"),
			newObservation(ids[2], "Gamma"),
		}, nil
	}
	observations, _, err := service.RetrieveRelevant(context.Background(), "engram", "cap", RetrievalOptions{MaxResults: 2})
	require.NoError(t, err)
	require.Len(t, observations, 2)
	require.Equal(t, int64(1), observations[0].ID)
	require.Equal(t, int64(2), observations[1].ID)
}

func TestRetrieveRelevant_DeduplicatesObservationIDs(t *testing.T) {
	service := newRetrievalTestService()
	var fetchedIDs []int64
	service.retrievalHooks.vectorQuery = func(_ context.Context, _ string, _ int, _ vector.WhereFilter) ([]vector.QueryResult, error) {
		return []vector.QueryResult{
			{Similarity: 0.9, Metadata: map[string]any{"sqlite_id": float64(7), "doc_type": "observation", "project": "engram"}},
			{Similarity: 0.85, Metadata: map[string]any{"sqlite_id": float64(7), "doc_type": "observation", "project": "engram"}},
			{Similarity: 0.8, Metadata: map[string]any{"sqlite_id": float64(9), "doc_type": "observation", "project": "engram"}},
		}, nil
	}
	service.retrievalHooks.getObservationsByIDs = func(_ context.Context, ids []int64, _ string, _ int) ([]*models.Observation, error) {
		fetchedIDs = append([]int64(nil), ids...)
		return []*models.Observation{newObservation(7, "First"), newObservation(9, "Second")}, nil
	}
	observations, _, err := service.RetrieveRelevant(context.Background(), "engram", "dedup", RetrievalOptions{MaxResults: 5})
	require.NoError(t, err)
	require.Equal(t, []int64{7, 9}, fetchedIDs)
	require.Len(t, observations, 2)
}

func TestRetrieveRelevant_LLMFilterHonorsSilence(t *testing.T) {
	service := newRetrievalTestService()
	service.retrievalHooks.vectorQuery = func(_ context.Context, _ string, _ int, _ vector.WhereFilter) ([]vector.QueryResult, error) {
		return []vector.QueryResult{
			{Similarity: 0.93, Metadata: map[string]any{"sqlite_id": float64(1), "doc_type": "observation", "project": "engram"}},
			{Similarity: 0.89, Metadata: map[string]any{"sqlite_id": float64(2), "doc_type": "observation", "project": "engram"}},
		}, nil
	}
	service.retrievalHooks.getObservationsByIDs = func(_ context.Context, _ []int64, _ string, _ int) ([]*models.Observation, error) {
		return []*models.Observation{newObservation(1, "Alpha"), newObservation(2, "Beta")}, nil
	}
	service.retrievalHooks.filterByRelevance = func(_ context.Context, _ []*models.Observation, _, _ string) []int64 {
		return []int64{}
	}
	observations, _, err := service.RetrieveRelevant(context.Background(), "engram", "silence", RetrievalOptions{MaxResults: 5, UseLLMFilter: true})
	require.NoError(t, err)
	require.Empty(t, observations)
}

func newRetrievalTestService() *Service {
	cfg := config.Default()
	cfg.InjectionFloor = 0
	cfg.SessionBoost = 1.0
	cfg.ContextRelevanceThreshold = 0.3
	return &Service{
		config:         cfg,
		retrievalHooks: &retrievalHooks{},
		retrievalStats: map[string]*RetrievalStats{},
	}
}

func newObservation(id int64, title string) *models.Observation {
	return &models.Observation{
		ID:    id,
		Title: sql.NullString{String: title, Valid: title != ""},
	}
}
