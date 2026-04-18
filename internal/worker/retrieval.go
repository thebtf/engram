package worker

import (
	"context"
	"sort"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/search"
	"github.com/thebtf/engram/internal/search/expansion"
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
	expandedQueries   []expansion.ExpandedQuery
	detectedIntent    string
	usedVector        bool
	totalResults      int
	staleCount        int
	freshCount        int
	duplicatesRemoved int
}

type retrievalHooks struct {
	retrieveRelevant               func(ctx context.Context, project, query string, opts RetrievalOptions) ([]*models.Observation, map[int64]float64, error)
	getProjectThreshold            func(ctx context.Context, project string, globalDefault float64) float64
	getObservationsByIDs           func(ctx context.Context, ids []int64, orderBy string, limit int) ([]*models.Observation, error)
	searchObservationsFTSFiltered  func(ctx context.Context, query string, scopeFilter gorm.ScopeFilter, limit int) ([]*models.Observation, error)
	getRecentObservationsFiltered  func(ctx context.Context, scopeFilter gorm.ScopeFilter, limit int) ([]*models.Observation, error)
	expandViaGraph                 func(ctx context.Context, scoredIDs []search.ScoredID, limit int) []search.ScoredID
	getDiversityScores             func(ctx context.Context, ids []int64) (map[int64]float64, error)
	getRecentSessionIDs            func(ctx context.Context, project string, since time.Time) (map[string]bool, error)
	getTopImportanceObservations   func(ctx context.Context, project string, limit int) ([]*models.Observation, error)
	filterByRelevance              func(ctx context.Context, candidates []*models.Observation, project, taskContext string) []int64
	getRecentUserPromptsByProject  func(ctx context.Context, project string, limit int) ([]*models.UserPromptWithSession, error)
	readSignalCountForPath         func(sessionID, filePath string) int
	filePathObservations           func(ctx context.Context, project, filePath string, limit int) ([]*models.Observation, error)
	getEntityObservationsBySession func(ctx context.Context, sessionID string) ([]*models.Observation, error)
	getGraphNeighbors              func(ctx context.Context, obsID int64, maxHops int, limit int) ([]int64, error)
	// getLastPromptBySession returns the most recent user prompt for a specific session.
	// When nil, loadLastUserPromptBySession falls back to project-wide lookup.
	getLastPromptBySession func(ctx context.Context, project, sessionID string) (*models.UserPromptWithSession, error)
}

func (s *Service) typeLanesEnabled() bool {
	return s.config != nil && s.config.TypeLanesEnabled
}

func (s *Service) laneConfigForType(obsType models.ObservationType) (cfg struct {
	MinScore       float64
	TopK           int
	RerankerWeight float64
}) {
	toLaneConfig := func(minScore float64, topK int, rerankerWeight float64) struct {
		MinScore       float64
		TopK           int
		RerankerWeight float64
	} {
		return struct {
			MinScore       float64
			TopK           int
			RerankerWeight float64
		}{MinScore: minScore, TopK: topK, RerankerWeight: rerankerWeight}
	}

	if s.config == nil || len(s.config.TypeSearchLanes) == 0 {
		if lane, ok := search.DefaultSearchLanes[string(obsType)]; ok {
			return toLaneConfig(lane.MinScore, lane.TopK, lane.RerankerWeight)
		}
		lane := search.DefaultSearchLanes["default"]
		return toLaneConfig(lane.MinScore, lane.TopK, lane.RerankerWeight)
	}
	if lane, ok := s.config.TypeSearchLanes[string(obsType)]; ok {
		return toLaneConfig(lane.MinScore, lane.TopK, lane.RerankerWeight)
	}
	if lane, ok := s.config.TypeSearchLanes["default"]; ok {
		return toLaneConfig(lane.MinScore, lane.TopK, lane.RerankerWeight)
	}
	lane := search.DefaultSearchLanes["default"]
	return toLaneConfig(lane.MinScore, lane.TopK, lane.RerankerWeight)
}

func (s *Service) laneWeightMap() map[models.ObservationType]float64 {
	weights := make(map[models.ObservationType]float64)
	for name, lane := range search.DefaultSearchLanes {
		weights[models.ObservationType(name)] = lane.RerankerWeight
	}
	if s.config != nil && len(s.config.TypeSearchLanes) > 0 {
		for name, lane := range s.config.TypeSearchLanes {
			weights[models.ObservationType(name)] = lane.RerankerWeight
		}
	}
	return weights
}

func (s *Service) typedLaneMinScore() float64 {
	minScore := 0.0
	found := false
	applyLane := func(score float64) {
		if score <= 0 {
			return
		}
		if !found || score < minScore {
			minScore = score
			found = true
		}
	}

	if s.config != nil && len(s.config.TypeSearchLanes) > 0 {
		for _, lane := range s.config.TypeSearchLanes {
			applyLane(lane.MinScore)
		}
	} else {
		for _, lane := range search.DefaultSearchLanes {
			applyLane(lane.MinScore)
		}
	}

	if !found {
		return search.DefaultSearchLanes["default"].MinScore
	}
	return minScore
}

func (s *Service) applyTypedLaneSelection(observations []*models.Observation, rankingScores, thresholdScores map[int64]float64, limit int) []*models.Observation {
	grouped := make(map[models.ObservationType][]*models.Observation)
	for _, obs := range observations {
		grouped[obs.Type] = append(grouped[obs.Type], obs)
	}

	selected := make([]*models.Observation, 0, len(observations))
	seen := make(map[int64]struct{})
	for obsType, items := range grouped {
		lane := s.laneConfigForType(obsType)
		filtered := make([]*models.Observation, 0, len(items))
		for _, obs := range items {
			score, ok := thresholdScores[obs.ID]
			if !ok {
				score = rankingScores[obs.ID]
			}
			if score < lane.MinScore {
				continue
			}
			filtered = append(filtered, obs)
		}
		sort.Slice(filtered, func(i, j int) bool {
			return rankingScores[filtered[i].ID] > rankingScores[filtered[j].ID]
		})
		if lane.TopK > 0 && len(filtered) > lane.TopK {
			filtered = filtered[:lane.TopK]
		}
		for _, obs := range filtered {
			if _, ok := seen[obs.ID]; ok {
				continue
			}
			seen[obs.ID] = struct{}{}
			selected = append(selected, obs)
			if limit > 0 && len(selected) >= limit {
				return selected
			}
		}
	}
	return selected
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
	scopeFilter := gorm.ScopeFilter{Project: project, AgentID: state.agentID}
	fallbackObservations, fallbackErr := s.searchFallbackObservations(ctx, query, scopeFilter, limit)
	if fallbackErr != nil {
		return nil, nil, fallbackErr
	}
	observations = fallbackObservations
	_ = baseSimilarityScores // no vector scores in v5

	// Graph BFS fusion blends graph neighbor hits with the primary retrieval list (FTS in v5,
	// vector scores in future modes). Building the base list from observations (rank-ordered)
	// instead of from similarityScores preserves FTS hits that have no numeric score.
	if s.config != nil && s.config.InjectGraphBFSEnabled {
		seedIDs := s.ExtractSessionEntitySeeds(ctx, opts.SessionID, project)
		if len(seedIDs) > 0 {
			graphList := make([]search.ScoredID, 0, len(seedIDs))
			for _, seedID := range seedIDs {
				neighborIDs, err := s.lookupGraphSeedNeighbors(ctx, seedID)
				if err != nil {
					continue
				}
				for _, neighborID := range neighborIDs {
					graphList = append(graphList, search.ScoredID{ID: neighborID, DocType: "observation", Score: 0.2})
				}
			}
			if len(graphList) > 0 {
				// Base list: prefer explicit scores when we have them (future vector mode),
				// otherwise rank observations by their current order (FTS result rank).
				vectorList := make([]search.ScoredID, 0, len(observations)+len(similarityScores))
				if len(similarityScores) > 0 {
					for id, score := range similarityScores {
						vectorList = append(vectorList, search.ScoredID{ID: id, DocType: "observation", Score: score})
					}
					sort.Slice(vectorList, func(i, j int) bool {
						return vectorList[i].Score > vectorList[j].Score
					})
				} else {
					for idx, obs := range observations {
						// Rank-based score; higher rank → higher score so RRF weights early FTS hits more.
						vectorList = append(vectorList, search.ScoredID{ID: obs.ID, DocType: "observation", Score: 1.0 / float64(idx+1)})
					}
				}
				fused := search.RRF(vectorList, graphList)
				fusedIDs := make([]int64, 0, len(fused))
				for _, item := range fused {
					if item.DocType == "observation" {
						fusedIDs = append(fusedIDs, item.ID)
						similarityScores[item.ID] = item.Score
					}
				}
				if len(fusedIDs) > 0 {
					fusedObs, err := s.fetchObservationsByID(ctx, fusedIDs, "", limit)
					if err == nil && len(fusedObs) > 0 {
						observations = fusedObs
					}
				}
			}
		}
	}

	freshObservations, staleCount := s.filterFreshObservations(ctx, observations, state.cwd)
	freshCount := len(freshObservations)

	// Default 0.9: observations with cosine similarity >= 0.9 are considered duplicates
	// and collapsed into a single cluster. Overridable via config.ClusteringThreshold.
	clusteringThreshold := 0.9
	if s.config != nil && s.config.ClusteringThreshold > 0 {
		clusteringThreshold = s.config.ClusteringThreshold
	}
	clusteredObservations := clusterObservations(freshObservations, clusteringThreshold)
	duplicatesRemoved := len(freshObservations) - len(clusteredObservations)
	clusteredObservations = s.expandGraphNeighbors(ctx, clusteredObservations, similarityScores, limit)
	laneThresholdScores := similarityScores
	if len(baseSimilarityScores) > 0 {
		laneThresholdScores = baseSimilarityScores
	}
	// Only apply type-lane filtering when we have numeric scores; an empty
	// score-map (FTS-only mode) would eliminate every observation.
	if s.typeLanesEnabled() && len(clusteredObservations) > 0 && len(laneThresholdScores) > 0 {
		clusteredObservations = s.applyTypedLaneSelection(clusteredObservations, similarityScores, laneThresholdScores, limit)
	}
	if len(clusteredObservations) > 0 {
		search.ApplyCompositeScoring(clusteredObservations, similarityScores)
		if s.typeLanesEnabled() {
			search.ApplyLaneWeights(clusteredObservations, similarityScores, s.laneWeightMap())
		}
		if diversityScores, diversityErr := s.lookupDiversityScores(ctx, clusteredObservations); diversityErr == nil && len(diversityScores) > 0 {
			search.ApplyDiversityPenalty(clusteredObservations, similarityScores, diversityScores)
		}
	}
	if sessionBoost := s.sessionBoostFactor(); sessionBoost > 1.0 && len(clusteredObservations) > 0 {
		twoHoursAgo := time.Now().Add(-2 * time.Hour)
		if recentSessions, sessionErr := s.lookupRecentSessionIDs(ctx, project, twoHoursAgo); sessionErr == nil {
			search.ApplySessionBoost(clusteredObservations, similarityScores, recentSessions, sessionBoost)
		}
	}
	if len(similarityScores) > 0 && len(clusteredObservations) > 0 {
		sort.Slice(clusteredObservations, func(i, j int) bool {
			return similarityScores[clusteredObservations[i].ID] > similarityScores[clusteredObservations[j].ID]
		})
	}
	injectionFloor := 0
	if s.config != nil {
		injectionFloor = s.config.InjectionFloor
	}
	if injectionFloor > 0 {
		clusteredObservations = fillToFloor(ctx, injectionFloor, clusteredObservations, nil,
			func(fillCtx context.Context, fillLimit int) ([]*models.Observation, error) {
				return s.getTopImportanceObservations(fillCtx, project, fillLimit)
			})
	}
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
	if s.searchMgr == nil {
		return globalDefault
	}
	return s.searchMgr.GetProjectThreshold(ctx, project, globalDefault)
}

func (s *Service) expandQueries(ctx context.Context, query string) ([]expansion.ExpandedQuery, string) {
	if s.queryExpander == nil {
		return []expansion.ExpandedQuery{{Query: query, Weight: 1.0, Source: "original"}}, ""
	}
	expandCtx := ctx
	cancel := func() {}
	if s.config != nil && s.config.QueryExpansionTimeoutMS > 0 {
		expandCtx, cancel = context.WithTimeout(ctx, time.Duration(s.config.QueryExpansionTimeoutMS)*time.Millisecond)
	}
	defer cancel()
	cfg := expansion.DefaultConfig()
	if s.config != nil {
		cfg.EnableHyDE = s.config.HyDEEnabled
	}
	expandedQueries := s.queryExpander.Expand(expandCtx, query, cfg)
	if len(expandedQueries) == 0 {
		return []expansion.ExpandedQuery{{Query: query, Weight: 1.0, Source: "original"}}, ""
	}
	return expandedQueries, string(expandedQueries[0].Intent)
}

func (s *Service) fetchObservationsByID(ctx context.Context, ids []int64, orderBy string, limit int) ([]*models.Observation, error) {
	if s.retrievalHooks != nil && s.retrievalHooks.getObservationsByIDs != nil {
		return s.retrievalHooks.getObservationsByIDs(ctx, ids, orderBy, limit)
	}
	if s.observationStore == nil {
		return nil, nil
	}
	return s.observationStore.GetObservationsByIDs(ctx, ids, orderBy, limit)
}

func (s *Service) searchFallbackObservations(ctx context.Context, query string, scopeFilter gorm.ScopeFilter, limit int) ([]*models.Observation, error) {
	if s.retrievalHooks != nil && s.retrievalHooks.searchObservationsFTSFiltered != nil {
		observations, err := s.retrievalHooks.searchObservationsFTSFiltered(ctx, query, scopeFilter, limit)
		if err == nil || s.retrievalHooks.getRecentObservationsFiltered == nil {
			return observations, err
		}
	}
	if s.observationStore != nil {
		observations, err := s.observationStore.SearchObservationsFTSFiltered(ctx, query, scopeFilter, limit)
		if err == nil {
			return observations, nil
		}
		log.Warn().Err(err).Str("query", query).Msg("FTS search failed, falling back to recent")
	}
	if s.retrievalHooks != nil && s.retrievalHooks.getRecentObservationsFiltered != nil {
		return s.retrievalHooks.getRecentObservationsFiltered(ctx, scopeFilter, limit)
	}
	if s.observationStore == nil {
		return nil, nil
	}
	return s.observationStore.GetRecentObservationsFiltered(ctx, scopeFilter, limit)
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

