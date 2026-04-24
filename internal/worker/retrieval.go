package worker

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/thebtf/engram/internal/worker/sdk"
	"github.com/thebtf/engram/pkg/models"
)

const (
	// NoiseFloorScore is the minimum composite score an observation must exceed to count as a
	// genuine semantic match. Scores above 0.05 indicate meaningful relevance.
	NoiseFloorScore = 0.05
)

// RetrievalOptions configures shared semantic retrieval.
type RetrievalOptions struct {
	MaxResults   int
	SessionID    string
	UseLLMFilter bool
	FilePaths    []string
}

type retrievalContextKey struct{}

type retrievalContextState struct {
	agentID  string
	cwd      string
	metadata *retrievalMetadata
}

type retrievalMetadata struct {
	threshold         float64
	expandedQueries   []string // v5: query expansion removed; always single original query
	detectedIntent    string
	usedVector        bool
	totalResults      int
	staleCount        int
	freshCount        int
	duplicatesRemoved int
}

type retrievalScope struct {
	Project string
	AgentID string
}

type retrievalHooks struct {
	retrieveRelevant               func(ctx context.Context, project, query string, opts RetrievalOptions) ([]*models.Observation, map[int64]float64, error)
	getProjectThreshold            func(ctx context.Context, project string, globalDefault float64) float64
	getObservationsByIDs           func(ctx context.Context, ids []int64, orderBy string, limit int) ([]*models.Observation, error)
	searchObservationsFTSFiltered  func(ctx context.Context, query string, scopeFilter retrievalScope, limit int) ([]*models.Observation, error)
	getRecentObservationsFiltered  func(ctx context.Context, scopeFilter retrievalScope, limit int) ([]*models.Observation, error)
	getDiversityScores             func(ctx context.Context, ids []int64) (map[int64]float64, error)
	getRecentSessionIDs            func(ctx context.Context, project string, since time.Time) (map[string]bool, error)
	getTopImportanceObservations   func(ctx context.Context, project string, limit int) ([]*models.Observation, error)
	filterByRelevance              func(ctx context.Context, candidates []*models.Observation, project, taskContext string) []int64
	getRecentUserPromptsByProject  func(ctx context.Context, project string, limit int) ([]*models.UserPromptWithSession, error)
	readSignalCountForPath         func(sessionID, filePath string) int
	filePathObservations           func(ctx context.Context, project, filePath string, limit int) ([]*models.Observation, error)
	getEntityObservationsBySession func(ctx context.Context, sessionID string) ([]*models.Observation, error)
	// getLastPromptBySession returns the most recent user prompt for a specific session.
	// When nil, loadLastUserPromptBySession falls back to project-wide lookup.
	getLastPromptBySession func(ctx context.Context, project, sessionID string) (*models.UserPromptWithSession, error)
}

func withRetrievalRequest(ctx context.Context, agentID, cwd string, metadata *retrievalMetadata) context.Context {
	return context.WithValue(ctx, retrievalContextKey{}, retrievalContextState{agentID: agentID, cwd: cwd, metadata: metadata})
}

func retrievalStateFromContext(ctx context.Context) retrievalContextState {
	state, _ := ctx.Value(retrievalContextKey{}).(retrievalContextState)
	return state
}

// RetrieveRelevant runs the shared retrieval pipeline for prompt search and inject relevant sections.
func (s *Service) RetrieveRelevant(ctx context.Context, project, query string, opts RetrievalOptions) ([]*models.Observation, map[int64]float64, error) {
	if s.retrievalHooks != nil && s.retrievalHooks.retrieveRelevant != nil {
		return s.retrievalHooks.retrieveRelevant(ctx, project, query, opts)
	}

	limit := opts.MaxResults
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	state := retrievalStateFromContext(ctx)
	metadata := state.metadata
	threshold := s.getProjectThreshold(ctx, project)
	expandedQueries, detectedIntent := s.expandQueries(ctx, query)
	if metadata != nil {
		metadata.threshold = threshold
		metadata.expandedQueries = expandedQueries
		metadata.detectedIntent = detectedIntent
	}

	similarityScores := make(map[int64]float64)
	baseSimilarityScores := make(map[int64]float64)
	observations := make([]*models.Observation, 0)

	// Vector search removed in v5 (content_chunks table dropped). Use FTS-only retrieval.
	scopeFilter := retrievalScope{Project: project, AgentID: state.agentID}
	fallbackObservations, fallbackErr := s.searchFallbackObservations(ctx, query, scopeFilter, limit)
	if fallbackErr != nil {
		return nil, nil, fallbackErr
	}
	observations = fallbackObservations
	_ = baseSimilarityScores // no vector scores in v5

	freshObservations, staleCount := s.filterFreshObservations(ctx, observations, state.cwd)
	freshCount := len(freshObservations)

	// Default 0.9: observations with cosine similarity >= 0.9 are considered duplicates
	// Clustering threshold removed in v5 (US11) — fixed default 0.9.
	const clusteringThreshold = 0.9
	clusteredObservations := clusterObservations(freshObservations, clusteringThreshold)
	duplicatesRemoved := len(freshObservations) - len(clusteredObservations)
	// search.ApplyCompositeScoring / ApplyLaneWeights / ApplyDiversityPenalty /
	// ApplySessionBoost all lived in internal/search which was dropped in v5 (US9).
	// With no vector scores (similarityScores is always empty in v5 FTS-only mode)
	// these scoring passes were no-ops anyway — observations are already ordered by
	// FTS rank + importance from the DB query.
	if len(similarityScores) > 0 && len(clusteredObservations) > 0 {
		sort.Slice(clusteredObservations, func(i, j int) bool {
			return similarityScores[clusteredObservations[i].ID] > similarityScores[clusteredObservations[j].ID]
		})
	}
	// InjectionFloor config field removed in v5; fill-to-floor is disabled.
	// When similarityScores is empty (FTS-only mode, no vector scores) the
	// score-gated loop would always yield zero, so fall back to observation count.
	totalResults := len(clusteredObservations)
	if len(similarityScores) > 0 {
		totalResults = 0
		for _, observation := range clusteredObservations {
			if score, exists := similarityScores[observation.ID]; exists && score > NoiseFloorScore {
				totalResults++
			}
		}
	}
	if limit > 0 && len(clusteredObservations) > limit {
		clusteredObservations = clusteredObservations[:limit]
	}
	if opts.UseLLMFilter {
		clusteredObservations = s.applyLLMFilter(ctx, project, query, clusteredObservations)
	}
	if metadata != nil {
		metadata.usedVector = false // vector search removed in v5
		metadata.totalResults = totalResults
		metadata.staleCount = staleCount
		metadata.freshCount = freshCount
		metadata.duplicatesRemoved = duplicatesRemoved
	}
	return clusteredObservations, similarityScores, nil
}

func (s *Service) getProjectThreshold(ctx context.Context, project string) float64 {
	globalDefault := 0.3
	if s.config != nil && s.config.ContextRelevanceThreshold > 0 {
		globalDefault = s.config.ContextRelevanceThreshold
	}
	if s.retrievalHooks != nil && s.retrievalHooks.getProjectThreshold != nil {
		return s.retrievalHooks.getProjectThreshold(ctx, project, globalDefault)
	}
	// searchMgr removed in v5 (US9); return config default.
	return globalDefault
}

// expandQueries returns the original query as a single-element slice.
// Query expansion (HyDE, multi-query) was removed in v5 (US9/US11).
func (s *Service) expandQueries(_ context.Context, query string) ([]string, string) {
	return []string{query}, ""
}

func (s *Service) searchFallbackObservations(ctx context.Context, query string, scopeFilter retrievalScope, limit int) ([]*models.Observation, error) {
	if s.retrievalHooks != nil && s.retrievalHooks.searchObservationsFTSFiltered != nil {
		observations, err := s.retrievalHooks.searchObservationsFTSFiltered(ctx, query, scopeFilter, limit)
		if err == nil || s.retrievalHooks.getRecentObservationsFiltered == nil {
			return observations, err
		}
	}
	if s.retrievalHooks != nil && s.retrievalHooks.getRecentObservationsFiltered != nil {
		return s.retrievalHooks.getRecentObservationsFiltered(ctx, scopeFilter, limit)
	}

	if limit <= 0 {
		limit = DefaultSearchLimit
	}

	fetchLimit := limit
	trimmedQuery := strings.TrimSpace(query)
	if trimmedQuery != "" {
		const candidateMultiplier = 10
		const minCandidatePool = 1000
		fetchLimit = limit * candidateMultiplier
		if fetchLimit < minCandidatePool {
			fetchLimit = minCandidatePool
		}
	}

	observations := make([]*models.Observation, 0, fetchLimit)
	if s.memoryStore != nil && scopeFilter.Project != "" {
		memories, err := s.memoryStore.List(ctx, scopeFilter.Project, fetchLimit)
		if err != nil {
			return nil, err
		}
		observations = append(observations, memoriesToObservations(memories)...)
	}
	if s.behavioralRulesStore != nil {
		var projectPtr *string
		if scopeFilter.Project != "" {
			project := scopeFilter.Project
			projectPtr = &project
		}
		rules, err := s.behavioralRulesStore.List(ctx, projectPtr, fetchLimit)
		if err != nil {
			return nil, err
		}
		observations = append(observations, behavioralRulesToObservations(rules)...)
	}
	if len(observations) == 0 {
		return []*models.Observation{}, nil
	}

	if trimmedQuery != "" {
		queryLower := strings.ToLower(trimmedQuery)
		filtered := observations[:0]
		for _, observation := range observations {
			if observationMatchesFallbackQuery(observation, queryLower) {
				filtered = append(filtered, observation)
			}
		}
		observations = filtered
	}

	sort.SliceStable(observations, func(i, j int) bool {
		return observations[i].CreatedAtEpoch > observations[j].CreatedAtEpoch
	})
	if len(observations) > limit {
		observations = observations[:limit]
	}
	return observations, nil
}

func observationMatchesFallbackQuery(observation *models.Observation, queryLower string) bool {
	if observation == nil {
		return false
	}
	if strings.Contains(strings.ToLower(observation.Title.String), queryLower) {
		return true
	}
	if strings.Contains(strings.ToLower(observation.Narrative.String), queryLower) {
		return true
	}
	for _, concept := range observation.Concepts {
		if strings.Contains(strings.ToLower(concept), queryLower) {
			return true
		}
	}
	return false
}

func (s *Service) filterFreshObservations(ctx context.Context, observations []*models.Observation, cwd string) ([]*models.Observation, int) {
	if cwd == "" {
		return observations, 0
	}
	freshObservations := make([]*models.Observation, 0, len(observations))
	staleCount := 0
	for _, observation := range observations {
		if len(observation.FileMtimes) == 0 {
			freshObservations = append(freshObservations, observation)
			continue
		}
		paths := make([]string, 0, len(observation.FileMtimes))
		for path := range observation.FileMtimes {
			paths = append(paths, path)
		}
		if observation.CheckStaleness(sdk.GetFileMtimes(paths, cwd)) {
			staleCount++
			s.queueStaleVerification(observation.ID, cwd)
			continue
		}
		freshObservations = append(freshObservations, observation)
	}
	return freshObservations, staleCount
}
