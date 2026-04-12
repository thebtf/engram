package worker

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/db/gorm"
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

func TestRetrieveRelevant_PassesFilePathsToVectorFilter(t *testing.T) {
	service := newRetrievalTestService()
	var capturedWhere vector.WhereFilter
	service.retrievalHooks.vectorQuery = func(_ context.Context, _ string, _ int, where vector.WhereFilter) ([]vector.QueryResult, error) {
		capturedWhere = where
		return []vector.QueryResult{}, nil
	}

	_, _, err := service.RetrieveRelevant(context.Background(), "engram", "query", RetrievalOptions{
		MaxResults: 5,
		FilePaths:  []string{"foo.go", "bar.go"},
	})
	require.NoError(t, err)

	found := false
	for _, clause := range capturedWhere.Clauses {
		if len(clause.OrGroup) != 2 {
			continue
		}
		left := clause.OrGroup[0]
		right := clause.OrGroup[1]
		if left.Column == "files_modified" && left.Operator == "?|" && right.Column == "files_read" && right.Operator == "?|" {
			require.Equal(t, []string{"foo.go", "bar.go"}, left.Value)
			require.Equal(t, []string{"foo.go", "bar.go"}, right.Value)
			found = true
			break
		}
	}
	require.True(t, found, "expected file-scope OR clause in vector filter")
}

func TestRetrieveRelevant_WithoutFilePaths_OmitsFileScopeClause(t *testing.T) {
	service := newRetrievalTestService()
	var capturedWhere vector.WhereFilter
	service.retrievalHooks.vectorQuery = func(_ context.Context, _ string, _ int, where vector.WhereFilter) ([]vector.QueryResult, error) {
		capturedWhere = where
		return []vector.QueryResult{}, nil
	}

	_, _, err := service.RetrieveRelevant(context.Background(), "engram", "query", RetrievalOptions{MaxResults: 5})
	require.NoError(t, err)

	for _, clause := range capturedWhere.Clauses {
		if clause.Column == "files_modified" || clause.Column == "files_read" {
			t.Fatalf("unexpected direct file-scope clause: %+v", clause)
		}
		for _, nested := range clause.OrGroup {
			if nested.Column == "files_modified" || nested.Column == "files_read" {
				t.Fatalf("unexpected nested file-scope clause: %+v", nested)
			}
		}
	}
}

func TestRetrieveRelevant_TypedLanesRespectPerTypeThresholds(t *testing.T) {
	service := newRetrievalTestService()
	service.config.TypeLanesEnabled = true
	service.config.TypeSearchLanes = config.DefaultTypeSearchLanes
	service.retrievalHooks.vectorQuery = func(_ context.Context, _ string, _ int, _ vector.WhereFilter) ([]vector.QueryResult, error) {
		return []vector.QueryResult{
			{Similarity: 0.22, Metadata: map[string]any{"sqlite_id": float64(1), "doc_type": "observation", "project": "engram"}},
			{Similarity: 0.50, Metadata: map[string]any{"sqlite_id": float64(2), "doc_type": "observation", "project": "engram"}},
		}, nil
	}
	service.retrievalHooks.getObservationsByIDs = func(_ context.Context, _ []int64, _ string, _ int) ([]*models.Observation, error) {
		guidance := newObservation(1, "Guidance")
		guidance.Type = models.ObsTypeGuidance
		decision := newObservation(2, "Decision")
		decision.Type = models.ObsTypeDecision
		return []*models.Observation{guidance, decision}, nil
	}

	observations, _, err := service.RetrieveRelevant(context.Background(), "engram", "query", RetrievalOptions{MaxResults: 10})
	require.NoError(t, err)
	require.Len(t, observations, 1)
	require.Equal(t, int64(1), observations[0].ID)
}

func TestRetrieveRelevant_TypedLanesWeightMultiplication(t *testing.T) {
	service := newRetrievalTestService()
	service.config.TypeLanesEnabled = true
	service.config.TypeSearchLanes = map[string]config.SearchLaneConfig{
		"feature": {
			MinScore:       0.10,
			TopK:           10,
			RerankerWeight: 2.0,
		},
		"bugfix": {
			MinScore:       0.10,
			TopK:           10,
			RerankerWeight: 0.5,
		},
		"default": {
			MinScore:       0.10,
			TopK:           10,
			RerankerWeight: 1.0,
		},
	}
	service.retrievalHooks.vectorQuery = func(_ context.Context, _ string, _ int, _ vector.WhereFilter) ([]vector.QueryResult, error) {
		return []vector.QueryResult{
			{Similarity: 0.50, Metadata: map[string]any{"sqlite_id": float64(1), "doc_type": "observation", "project": "engram"}},
			{Similarity: 0.50, Metadata: map[string]any{"sqlite_id": float64(2), "doc_type": "observation", "project": "engram"}},
		}, nil
	}
	service.retrievalHooks.getObservationsByIDs = func(_ context.Context, ids []int64, _ string, _ int) ([]*models.Observation, error) {
		feature := newObservation(1, "Feature")
		feature.Type = models.ObsTypeFeature
		bugfix := newObservation(2, "Bugfix")
		bugfix.Type = models.ObsTypeBugfix
		return []*models.Observation{feature, bugfix}, nil
	}

	_, scores, err := service.RetrieveRelevant(context.Background(), "engram", "query", RetrievalOptions{MaxResults: 10})
	require.NoError(t, err)
	require.Greater(t, scores[1], scores[2], "feature should get higher lane weight than bugfix")
}

func TestRetrieveRelevant_TypedLanesDisabledPreservesSingleLaneBehavior(t *testing.T) {
	service := newRetrievalTestService()
	service.config.TypeLanesEnabled = false
	service.retrievalHooks.vectorQuery = func(_ context.Context, _ string, _ int, _ vector.WhereFilter) ([]vector.QueryResult, error) {
		return []vector.QueryResult{
			{Similarity: 0.22, Metadata: map[string]any{"sqlite_id": float64(1), "doc_type": "observation", "project": "engram"}},
			{Similarity: 0.50, Metadata: map[string]any{"sqlite_id": float64(2), "doc_type": "observation", "project": "engram"}},
		}, nil
	}
	service.retrievalHooks.getObservationsByIDs = func(_ context.Context, _ []int64, _ string, _ int) ([]*models.Observation, error) {
		guidance := newObservation(1, "Guidance")
		guidance.Type = models.ObsTypeGuidance
		decision := newObservation(2, "Decision")
		decision.Type = models.ObsTypeDecision
		return []*models.Observation{guidance, decision}, nil
	}

	observations, _, err := service.RetrieveRelevant(context.Background(), "engram", "query", RetrievalOptions{MaxResults: 10})
	require.NoError(t, err)
	require.Len(t, observations, 2)
}

func TestExtractSessionEntitySeeds_UsesPromptAndSessionEntities(t *testing.T) {
	service := newRetrievalTestService()
	service.retrievalHooks.getLastPromptBySession = func(_ context.Context, project, sessionID string) (*models.UserPromptWithSession, error) {
		require.Equal(t, "engram", project)
		require.Equal(t, "session-1", sessionID)
		return &models.UserPromptWithSession{UserPrompt: models.UserPrompt{PromptText: "authctx billing.go"}}, nil
	}
	service.retrievalHooks.getEntityObservationsBySession = func(_ context.Context, sessionID string) ([]*models.Observation, error) {
		require.Equal(t, "session-1", sessionID)
		entity1 := newObservation(10, "authctx")
		entity1.Type = models.ObsTypeEntity
		entity1.FilesRead = []string{"internal/authctx.go"}
		entity2 := newObservation(11, "billing")
		entity2.Type = models.ObsTypeEntity
		entity2.FilesModified = []string{"pkg/billing.go"}
		entity3 := newObservation(12, "extra")
		entity3.Type = models.ObsTypeEntity
		nonEntity := newObservation(13, "authctx")
		nonEntity.Type = models.ObsTypeDecision
		return []*models.Observation{entity1, entity2, entity3, nonEntity}, nil
	}

	seeds := service.ExtractSessionEntitySeeds(context.Background(), "session-1", "engram")
	require.Equal(t, []int64{10, 11}, seeds)
}

func TestExtractSessionEntitySeeds_LimitsToFiveUniqueIDs(t *testing.T) {
	service := newRetrievalTestService()
	service.retrievalHooks.getLastPromptBySession = func(_ context.Context, _, _ string) (*models.UserPromptWithSession, error) {
		return &models.UserPromptWithSession{UserPrompt: models.UserPrompt{PromptText: "a b c d e f g"}}, nil
	}
	service.retrievalHooks.getEntityObservationsBySession = func(_ context.Context, _ string) ([]*models.Observation, error) {
		observations := make([]*models.Observation, 0, 7)
		for i, title := range []string{"a", "b", "c", "d", "e", "f", "g"} {
			entity := newObservation(int64(i+1), title)
			entity.Type = models.ObsTypeEntity
			observations = append(observations, entity)
		}
		return observations, nil
	}

	seeds := service.ExtractSessionEntitySeeds(context.Background(), "session-1", "engram")
	require.Equal(t, []int64{1, 2, 3, 4, 5}, seeds)
}

func TestRetrieveRelevant_InjectGraphBFSEnabled_FusesGraphNeighbors(t *testing.T) {
	service := newRetrievalTestService()
	service.config.InjectGraphBFSEnabled = true
	service.config.TypeLanesEnabled = true
	service.config.TypeSearchLanes = map[string]config.SearchLaneConfig{
		"default": {
			MinScore:       0.55,
			TopK:           10,
			RerankerWeight: 1.0,
		},
	}
	service.retrievalHooks.getLastPromptBySession = func(_ context.Context, _, _ string) (*models.UserPromptWithSession, error) {
		return &models.UserPromptWithSession{UserPrompt: models.UserPrompt{PromptText: "authctx"}}, nil
	}
	service.retrievalHooks.getEntityObservationsBySession = func(_ context.Context, _ string) ([]*models.Observation, error) {
		entity := newObservation(100, "authctx")
		entity.Type = models.ObsTypeEntity
		entity.FilesRead = []string{"internal/auth.go"}
		return []*models.Observation{entity}, nil
	}
	service.retrievalHooks.getGraphNeighbors = func(_ context.Context, obsID int64, maxHops int, limit int) ([]int64, error) {
		require.Equal(t, int64(100), obsID)
		require.Equal(t, 2, maxHops)
		require.Equal(t, 10, limit)
		return []int64{0, 42, 42, -1}, nil
	}
	service.retrievalHooks.vectorQuery = func(_ context.Context, _ string, _ int, _ vector.WhereFilter) ([]vector.QueryResult, error) {
		return []vector.QueryResult{
			{Similarity: 0.56, Metadata: map[string]any{"sqlite_id": float64(1), "doc_type": "observation", "project": "engram"}},
		}, nil
	}
	service.retrievalHooks.searchObservationsFTSFiltered = func(_ context.Context, _ string, _ gorm.ScopeFilter, _ int) ([]*models.Observation, error) {
		return []*models.Observation{}, nil
	}
	service.retrievalHooks.getObservationsByIDs = func(_ context.Context, ids []int64, _ string, _ int) ([]*models.Observation, error) {
		result := make([]*models.Observation, 0, len(ids))
		for _, id := range ids {
			obs := newObservation(id, "Graph seeded")
			obs.Type = models.ObsTypeDiscovery
			result = append(result, obs)
		}
		return result, nil
	}

	observations, scores, err := service.RetrieveRelevant(context.Background(), "engram", "auth query", RetrievalOptions{MaxResults: 10, SessionID: "session-1"})
	require.NoError(t, err)
	require.Len(t, observations, 1)
	require.Equal(t, int64(1), observations[0].ID)
	require.NotZero(t, scores[42])
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
