package worker

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/pkg/models"
)

// TestRetrieveRelevant_NoHooks_ReturnsEmpty verifies that without hooks or static stores,
// RetrieveRelevant still returns a stable empty result.
func TestRetrieveRelevant_NoHooks_ReturnsEmpty(t *testing.T) {
	service := newRetrievalTestService()
	observations, similarityScores, err := service.RetrieveRelevant(context.Background(), "engram", "missing", RetrievalOptions{MaxResults: 5})
	require.NoError(t, err)
	require.Empty(t, observations)
	require.Empty(t, similarityScores)
}

func TestObservationMatchesFallbackQuery_MatchesTitleNarrativeAndConcepts(t *testing.T) {
	observation := &models.Observation{
		Title:     sql.NullString{String: "Authentication failure", Valid: true},
		Narrative: sql.NullString{String: "Billing sync retried", Valid: true},
		Concepts:  []string{"security", "match-tag"},
	}

	require.True(t, observationMatchesFallbackQuery(observation, "auth"))
	require.True(t, observationMatchesFallbackQuery(observation, "billing"))
	require.True(t, observationMatchesFallbackQuery(observation, "match-tag"))
	require.False(t, observationMatchesFallbackQuery(observation, "missing"))
}

// TestRetrieveRelevant_FTSFallback_ReturnsObservations verifies FTS-based retrieval
// (the only search path in v5 after vector storage was removed).
func TestRetrieveRelevant_FTSFallback_ReturnsObservations(t *testing.T) {
	service := newRetrievalTestService()
	service.retrievalHooks.searchObservationsFTSFiltered = func(_ context.Context, _ string, _ retrievalScope, _ int) ([]*models.Observation, error) {
		return []*models.Observation{
			newObservation(1, "Alpha"),
			newObservation(2, "Beta"),
		}, nil
	}
	observations, _, err := service.RetrieveRelevant(context.Background(), "engram", "query", RetrievalOptions{MaxResults: 5})
	require.NoError(t, err)
	require.Len(t, observations, 2)
}

// TestRetrieveRelevant_MaxResultsCapsOutput verifies that MaxResults caps the returned list.
func TestRetrieveRelevant_MaxResultsCapsOutput(t *testing.T) {
	service := newRetrievalTestService()
	// The mock respects the limit parameter so the assertion can be exact.
	// Disambiguated titles keep term-based clustering from collapsing these entries.
	distinctTitles := []string{"Alpha migration schema", "Gravity kernel panic", "Lunar lattice cipher", "Rhizome radio silence", "Spectral pivot anomaly"}
	service.retrievalHooks.searchObservationsFTSFiltered = func(_ context.Context, _ string, _ retrievalScope, limit int) ([]*models.Observation, error) {
		obs := make([]*models.Observation, 0, limit)
		for i := 1; i <= limit && i-1 < len(distinctTitles); i++ {
			obs = append(obs, newObservation(int64(i), distinctTitles[i-1]))
		}
		return obs, nil
	}
	observations, _, err := service.RetrieveRelevant(context.Background(), "engram", "cap", RetrievalOptions{MaxResults: 2})
	require.NoError(t, err)
	require.Len(t, observations, 2)
}

// TestRetrieveRelevant_LLMFilterHonorsSilence verifies LLM filter returning empty silences results.
func TestRetrieveRelevant_LLMFilterHonorsSilence(t *testing.T) {
	service := newRetrievalTestService()
	service.retrievalHooks.searchObservationsFTSFiltered = func(_ context.Context, _ string, _ retrievalScope, _ int) ([]*models.Observation, error) {
		return []*models.Observation{newObservation(1, "Alpha"), newObservation(2, "Beta")}, nil
	}
	service.retrievalHooks.filterByRelevance = func(_ context.Context, _ []*models.Observation, _, _ string) []int64 {
		return []int64{}
	}
	observations, _, err := service.RetrieveRelevant(context.Background(), "engram", "silence", RetrievalOptions{MaxResults: 5, UseLLMFilter: true})
	require.NoError(t, err)
	require.Empty(t, observations)
}

// TestExtractSessionEntitySeeds_UsesPromptAndSessionEntities verifies entity seed extraction.
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

// TestExtractSessionEntitySeeds_LimitsToFiveUniqueIDs verifies the 5-seed limit.
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

func newRetrievalTestService() *Service {
	cfg := config.Default()
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
