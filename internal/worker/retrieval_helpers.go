package worker

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/search"
	"github.com/thebtf/engram/pkg/models"
)

func (s *Service) expandGraphNeighbors(ctx context.Context, observations []*models.Observation, similarityScores map[int64]float64, limit int) []*models.Observation {
	if len(observations) == 0 {
		return observations
	}
	var expanded []search.ScoredID
	if s.retrievalHooks != nil && s.retrievalHooks.expandViaGraph != nil {
		scoredIDs := make([]search.ScoredID, 0, len(observations))
		for _, observation := range observations {
			scoredIDs = append(scoredIDs, search.ScoredID{ID: observation.ID, DocType: "observation", Score: similarityScores[observation.ID]})
		}
		expanded = s.retrievalHooks.expandViaGraph(ctx, scoredIDs, limit)
	} else if s.searchMgr != nil {
		scoredIDs := make([]search.ScoredID, 0, len(observations))
		for _, observation := range observations {
			scoredIDs = append(scoredIDs, search.ScoredID{ID: observation.ID, DocType: "observation", Score: similarityScores[observation.ID]})
		}
		expanded = s.searchMgr.ExpandViaGraph(ctx, scoredIDs, limit)
	}
	if len(expanded) == 0 {
		return observations
	}
	existingIDs := make(map[int64]bool, len(observations))
	newIDs := make([]int64, 0, len(expanded))
	for _, observation := range observations {
		existingIDs[observation.ID] = true
	}
	for _, scoredID := range expanded {
		if !existingIDs[scoredID.ID] && scoredID.DocType == "observation" {
			newIDs = append(newIDs, scoredID.ID)
			similarityScores[scoredID.ID] = scoredID.Score
		}
	}
	if len(newIDs) == 0 {
		return observations
	}
	graphObservations, err := s.fetchObservationsByID(ctx, newIDs, "", 0)
	if err != nil || len(graphObservations) == 0 {
		return observations
	}
	return append(observations, graphObservations...)
}

func (s *Service) lookupDiversityScores(ctx context.Context, observations []*models.Observation) (map[int64]float64, error) {
	ids := make([]int64, 0, len(observations))
	for _, observation := range observations {
		ids = append(ids, observation.ID)
	}
	if s.retrievalHooks != nil && s.retrievalHooks.getDiversityScores != nil {
		return s.retrievalHooks.getDiversityScores(ctx, ids)
	}
	if s.observationStore == nil {
		return nil, nil
	}
	return s.observationStore.GetDiversityScores(ctx, ids)
}

func (s *Service) lookupRecentSessionIDs(ctx context.Context, project string, since time.Time) (map[string]bool, error) {
	if s.retrievalHooks != nil && s.retrievalHooks.getRecentSessionIDs != nil {
		return s.retrievalHooks.getRecentSessionIDs(ctx, project, since)
	}
	if s.observationStore == nil {
		return nil, nil
	}
	return s.observationStore.GetRecentSessionIDs(ctx, project, since)
}

func (s *Service) sessionBoostFactor() float64 {
	if s.config == nil || s.config.SessionBoost <= 0 {
		return 1.0
	}
	return s.config.SessionBoost
}

func (s *Service) getTopImportanceObservations(ctx context.Context, project string, limit int) ([]*models.Observation, error) {
	if s.retrievalHooks != nil && s.retrievalHooks.getTopImportanceObservations != nil {
		return s.retrievalHooks.getTopImportanceObservations(ctx, project, limit)
	}
	if s.observationStore == nil {
		return nil, nil
	}
	return s.observationStore.GetTopImportanceObservations(ctx, project, limit)
}

func (s *Service) applyLLMFilter(ctx context.Context, project, query string, observations []*models.Observation) []*models.Observation {
	if len(observations) == 0 {
		return observations
	}
	candidates := observations
	if s.config != nil && s.config.LLMFilterCandidates > 0 && len(candidates) > s.config.LLMFilterCandidates {
		candidates = candidates[:s.config.LLMFilterCandidates]
	}
	var relevantIDs []int64
	if s.retrievalHooks != nil && s.retrievalHooks.filterByRelevance != nil {
		relevantIDs = s.retrievalHooks.filterByRelevance(ctx, candidates, project, query)
	} else if s.llmFilter != nil {
		relevantIDs = s.llmFilter.FilterByRelevance(ctx, candidates, project, query)
	} else {
		return observations
	}
	if len(relevantIDs) == 0 {
		log.Info().Str("project", project).Int("total_considered", len(candidates)).Msg("LLM filter silenced injection")
		return []*models.Observation{}
	}
	idSet := make(map[int64]struct{}, len(relevantIDs))
	for _, id := range relevantIDs {
		idSet[id] = struct{}{}
	}
	filtered := make([]*models.Observation, 0, len(relevantIDs))
	for _, observation := range observations {
		if _, exists := idSet[observation.ID]; exists {
			filtered = append(filtered, observation)
		}
	}
	if len(filtered) != len(observations) {
		log.Info().Str("project", project).Int("before", len(observations)).Int("after", len(filtered)).Msg("LLM filter applied")
	}
	return filtered
}

func (s *Service) loadRecentUserPromptsByProject(ctx context.Context, project string, limit int) ([]*models.UserPromptWithSession, error) {
	if s.retrievalHooks != nil && s.retrievalHooks.getRecentUserPromptsByProject != nil {
		return s.retrievalHooks.getRecentUserPromptsByProject(ctx, project, limit)
	}
	if s.promptStore == nil {
		return nil, nil
	}
	return s.promptStore.GetRecentUserPromptsByProject(ctx, project, limit)
}
