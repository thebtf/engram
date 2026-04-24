package worker

import (
	"context"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/pkg/models"
)

func (s *Service) applyLLMFilter(ctx context.Context, project, query string, observations []*models.Observation) []*models.Observation {
	if len(observations) == 0 {
		return observations
	}
	candidates := observations
	var relevantIDs []int64
	if s.retrievalHooks != nil && s.retrievalHooks.filterByRelevance != nil {
		relevantIDs = s.retrievalHooks.filterByRelevance(ctx, candidates, project, query)
	} else {
		// llmFilter removed in v5 (US9) — no LLM-based relevance filter path.
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
	// Prompt history storage was removed in v5. Without a retrieval hook there is no
	// honest backend for recent prompt lookup.
	return nil, nil
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
