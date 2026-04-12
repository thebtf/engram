package worker

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
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

func normalizeObservationIDs(raw []int64) []int64 {
	ids := make([]int64, 0, len(raw))
	seen := make(map[int64]struct{}, len(raw))
	for _, id := range raw {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func (s *Service) lookupGraphSeedNeighbors(ctx context.Context, seedID int64) ([]int64, error) {
	if seedID <= 0 {
		return nil, nil
	}
	if s.retrievalHooks != nil && s.retrievalHooks.getGraphNeighbors != nil {
		rawIDs, err := s.retrievalHooks.getGraphNeighbors(ctx, seedID, 2, 10)
		if err != nil {
			return nil, err
		}
		return normalizeObservationIDs(rawIDs), nil
	}
	if s.graphStore == nil {
		return nil, nil
	}
	neighbors, err := s.graphStore.GetNeighbors(ctx, seedID, 2, 10)
	if err != nil {
		return nil, err
	}
	rawIDs := make([]int64, 0, len(neighbors))
	for _, neighbor := range neighbors {
		rawIDs = append(rawIDs, neighbor.ObsID)
	}
	return normalizeObservationIDs(rawIDs), nil
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

// loadLastUserPromptBySession returns the most recent user prompt for the given session.
// When sessionID is non-empty, the result is scoped to that session: only prompts whose
// claude_session_id matches sessionID are considered. When sessionID is empty, it falls
// back to the most recent prompt across the entire project (cold-start path).
// This ensures that inject-query derivation is session-scoped so that session A cannot
// be seeded by session B's last prompt.
func (s *Service) loadLastUserPromptBySession(ctx context.Context, project, sessionID string, projectLimit int) (*models.UserPromptWithSession, error) {
	if sessionID != "" {
		// Hook path: allows tests to inject session-specific behaviour.
		if s.retrievalHooks != nil && s.retrievalHooks.getLastPromptBySession != nil {
			return s.retrievalHooks.getLastPromptBySession(ctx, project, sessionID)
		}
		// DB path: fetch recent prompts for the project, then return the first one
		// that belongs to this session. The DB query already orders by created_at DESC,
		// so the first matching entry is the most-recent prompt for the session.
		prompts, err := s.loadRecentUserPromptsByProject(ctx, project, projectLimit)
		if err != nil {
			return nil, err
		}
		for _, p := range prompts {
			if p.UserPrompt.ClaudeSessionID == sessionID {
				return p, nil
			}
		}
		// No prompt found for this session — fall through to project-wide fallback below.
	}
	// Cold-start or no session: return the most-recent prompt for the project.
	prompts, err := s.loadRecentUserPromptsByProject(ctx, project, 1)
	if err != nil {
		return nil, err
	}
	if len(prompts) == 0 {
		return nil, nil
	}
	return prompts[0], nil
}

func (s *Service) ExtractSessionEntitySeeds(ctx context.Context, sessionID, project string) []int64 {
	if sessionID == "" || project == "" {
		return nil
	}

	normalize := func(raw string) string {
		return strings.TrimSpace(strings.ToLower(raw))
	}
	promptTokenSet := make(map[string]struct{})
	appendPromptToken := func(raw string) {
		raw = normalize(raw)
		if raw == "" {
			return
		}
		promptTokenSet[raw] = struct{}{}
	}

	if prompt, err := s.loadLastUserPromptBySession(ctx, project, sessionID, 20); err == nil && prompt != nil {
		for _, token := range strings.Fields(prompt.PromptText) {
			appendPromptToken(token)
		}
	}

	var entityObservations []*models.Observation
	if s.retrievalHooks != nil && s.retrievalHooks.getEntityObservationsBySession != nil {
		all, err := s.retrievalHooks.getEntityObservationsBySession(ctx, sessionID)
		if err != nil {
			log.Debug().Err(err).Str("session_id", sessionID).Msg("failed to get entity observations via hook")
		} else {
			for _, obs := range all {
				if obs != nil && obs.Type == models.ObsTypeEntity {
					entityObservations = append(entityObservations, obs)
				}
			}
		}
	} else if s.observationStore != nil {
		allSessionObservations, err := s.observationStore.GetObservationsBySession(ctx, sessionID)
		if err != nil {
			log.Debug().Err(err).Str("session_id", sessionID).Msg("failed to get session observations")
		}
		for _, obs := range allSessionObservations {
			if obs != nil && obs.Type == models.ObsTypeEntity {
				entityObservations = append(entityObservations, obs)
			}
		}
	}

	seedIDs := make([]int64, 0, 5)
	for _, obs := range entityObservations {
		if obs == nil || obs.ID <= 0 {
			continue
		}

		matched := false
		if _, ok := promptTokenSet[normalize(obs.Title.String)]; ok {
			matched = true
		}
		if !matched {
			for _, path := range obs.FilesRead {
				if _, ok := promptTokenSet[normalize(filepath.Base(path))]; ok {
					matched = true
					break
				}
			}
		}
		if !matched {
			for _, path := range obs.FilesModified {
				if _, ok := promptTokenSet[normalize(filepath.Base(path))]; ok {
					matched = true
					break
				}
			}
		}
		if matched {
			seedIDs = append(seedIDs, obs.ID)
		}
	}

	if len(seedIDs) == 0 {
		return nil
	}
	unique := make(map[int64]struct{}, len(seedIDs))
	result := make([]int64, 0, len(seedIDs))
	for _, id := range seedIDs {
		if _, ok := unique[id]; ok {
			continue
		}
		unique[id] = struct{}{}
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	if len(result) > 5 {
		result = result[:5]
	}
	return result
}
