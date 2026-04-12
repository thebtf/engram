package worker

import (
	"context"
	"sort"
	"strconv"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/reranking"
	"github.com/thebtf/engram/internal/search"
	"github.com/thebtf/engram/internal/search/expansion"
	"github.com/thebtf/engram/internal/vector"
	"github.com/thebtf/engram/internal/worker/sdk"
	"github.com/thebtf/engram/pkg/models"
)

const (
	// NoiseFloorScore is the minimum composite score an observation must exceed to count as a
	// genuine semantic match. In high-dimensional embedding spaces, nearly all observations pass
	// the raw vector similarity threshold; only scores above 0.05 indicate meaningful relevance.
	NoiseFloorScore = 0.05

	// vectorPreFilterFactor widens the vector similarity filter by 10% below the project threshold.
	// This preserves borderline-relevant observations so the reranker can re-evaluate them before
	// they are discarded. A value < 1.0 means "start filtering slightly below the threshold".
	vectorPreFilterFactor = 0.9
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
	retrieveRelevant              func(ctx context.Context, project, query string, opts RetrievalOptions) ([]*models.Observation, map[int64]float64, error)
	getProjectThreshold           func(ctx context.Context, project string, globalDefault float64) float64
	vectorQuery                   func(ctx context.Context, query string, limit int, where vector.WhereFilter) ([]vector.QueryResult, error)
	getObservationsByIDs          func(ctx context.Context, ids []int64, orderBy string, limit int) ([]*models.Observation, error)
	searchObservationsFTSFiltered func(ctx context.Context, query string, scopeFilter gorm.ScopeFilter, limit int) ([]*models.Observation, error)
	getRecentObservationsFiltered func(ctx context.Context, scopeFilter gorm.ScopeFilter, limit int) ([]*models.Observation, error)
	rerank                        func(query string, candidates []reranking.Candidate, limit int) ([]reranking.RerankResult, error)
	rerankByScore                 func(query string, candidates []reranking.Candidate, limit int) ([]reranking.RerankResult, error)
	expandViaGraph                func(ctx context.Context, scoredIDs []search.ScoredID, limit int) []search.ScoredID
	getDiversityScores            func(ctx context.Context, ids []int64) (map[int64]float64, error)
	getRecentSessionIDs           func(ctx context.Context, project string, since time.Time) (map[string]bool, error)
	getTopImportanceObservations  func(ctx context.Context, project string, limit int) ([]*models.Observation, error)
	filterByRelevance             func(ctx context.Context, candidates []*models.Observation, project, taskContext string) []int64
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
	usedVector := false
	vectorSearchFailed := false
	if s.hasVectorRetrieval() {
		where := vector.BuildWhereFilter(vector.DocTypeObservation, project, false, opts.FilePaths)
		allVectorResults := make([]vector.QueryResult, 0, len(expandedQueries)*limit*2)
		vectorErrors := 0
		for _, expandedQuery := range expandedQueries {
			vectorResults, vectorErr := s.runVectorQuery(ctx, expandedQuery.Query, limit*2, where)
			if vectorErr != nil {
				vectorErrors++
				log.Debug().Err(vectorErr).Str("query", expandedQuery.Query).Msg("Vector query failed")
				continue
			}
			for index := range vectorResults {
				vectorResults[index].Similarity *= expandedQuery.Weight
			}
			allVectorResults = append(allVectorResults, vectorResults...)
		}
		if vectorErrors > 0 && vectorErrors == len(expandedQueries) {
			vectorSearchFailed = true
			log.Warn().Int("errors", vectorErrors).Str("project", project).Msg("All vector queries failed, falling back to FTS")
		}
		if len(allVectorResults) > 0 {
			prefilterThreshold := threshold
			if s.typeLanesEnabled() {
				if laneThreshold := s.typedLaneMinScore(); laneThreshold > 0 && laneThreshold < prefilterThreshold {
					prefilterThreshold = laneThreshold
				}
			}
			filteredResults := vector.FilterByThreshold(allVectorResults, prefilterThreshold*vectorPreFilterFactor, 0)
			for _, result := range filteredResults {
				id := vector.ExtractRowID(result.Metadata)
				if existingScore, exists := similarityScores[id]; !exists || result.Similarity > existingScore {
					similarityScores[id] = result.Similarity
				}
				if existingScore, exists := baseSimilarityScores[id]; !exists || result.Similarity > existingScore {
					baseSimilarityScores[id] = result.Similarity
				}
			}
			observationIDs := vector.ExtractObservationIDs(filteredResults, project)
			if len(observationIDs) > 0 {
				fetched, fetchErr := s.fetchObservationsByID(ctx, observationIDs, "date_desc", limit)
				if fetchErr != nil {
					return nil, nil, fetchErr
				}
				if len(fetched) > 0 {
					observations = fetched
					usedVector = true
				}
			}
		}
	}
	if !usedVector || len(observations) == 0 {
		if vectorSearchFailed {
			log.Info().Str("project", project).Msg("Using FTS fallback due to vector search failure")
		}
		scopeFilter := gorm.ScopeFilter{Project: project, AgentID: state.agentID}
		fallbackObservations, fallbackErr := s.searchFallbackObservations(ctx, query, scopeFilter, limit)
		if fallbackErr != nil {
			return nil, nil, fallbackErr
		}
		observations = fallbackObservations
	}

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
				vectorList := make([]search.ScoredID, 0, len(similarityScores))
				for id, score := range similarityScores {
					vectorList = append(vectorList, search.ScoredID{ID: id, DocType: "observation", Score: score})
				}
				sort.Slice(vectorList, func(i, j int) bool {
					return vectorList[i].Score > vectorList[j].Score
				})
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
	if usedVector && len(freshObservations) > 0 {
		freshObservations = s.applyReranking(query, freshObservations, similarityScores)
	}

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
	if s.typeLanesEnabled() && len(clusteredObservations) > 0 {
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
	totalResults := 0
	for _, observation := range clusteredObservations {
		if score, exists := similarityScores[observation.ID]; exists && score > NoiseFloorScore {
			totalResults++
		}
	}
	if limit > 0 && len(clusteredObservations) > limit {
		clusteredObservations = clusteredObservations[:limit]
	}
	if opts.UseLLMFilter {
		clusteredObservations = s.applyLLMFilter(ctx, project, query, clusteredObservations)
	}
	if metadata != nil {
		metadata.usedVector = usedVector
		metadata.totalResults = totalResults
		metadata.staleCount = staleCount
		metadata.freshCount = freshCount
		metadata.duplicatesRemoved = duplicatesRemoved
	}
	return clusteredObservations, similarityScores, nil
}

func (s *Service) hasVectorRetrieval() bool {
	return (s.retrievalHooks != nil && s.retrievalHooks.vectorQuery != nil) || (s.vectorClient != nil && s.vectorClient.IsConnected())
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
	cfg.EnableVocabularyExpansion = false
	if s.config != nil {
		cfg.EnableHyDE = s.config.HyDEEnabled
	}
	expandedQueries := s.queryExpander.Expand(expandCtx, query, cfg)
	if len(expandedQueries) == 0 {
		return []expansion.ExpandedQuery{{Query: query, Weight: 1.0, Source: "original"}}, ""
	}
	return expandedQueries, string(expandedQueries[0].Intent)
}

func (s *Service) runVectorQuery(ctx context.Context, query string, limit int, where vector.WhereFilter) ([]vector.QueryResult, error) {
	if s.retrievalHooks != nil && s.retrievalHooks.vectorQuery != nil {
		return s.retrievalHooks.vectorQuery(ctx, query, limit, where)
	}
	if s.vectorClient == nil {
		return nil, nil
	}
	return s.vectorClient.Query(ctx, query, limit, where)
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

func (s *Service) applyReranking(query string, observations []*models.Observation, similarityScores map[int64]float64) []*models.Observation {
	// noRerankAvailable is true when neither a hook-based nor a direct reranker is configured.
	noRerankAvailable := s.reranker == nil &&
		(s.retrievalHooks == nil || (s.retrievalHooks.rerank == nil && s.retrievalHooks.rerankByScore == nil))
	if len(observations) == 0 || noRerankAvailable {
		return observations
	}
	candidates := make([]reranking.Candidate, len(observations))
	for index, observation := range observations {
		content := observation.Title.String
		if observation.Narrative.Valid && observation.Narrative.String != "" {
			content = observation.Title.String + " " + observation.Narrative.String
		}
		candidates[index] = reranking.Candidate{ID: strconv.FormatInt(observation.ID, 10), Content: content, Score: similarityScores[observation.ID], Metadata: map[string]any{"obs_idx": index}}
	}
	rerankResults, err := s.rerankResults(query, candidates)
	if err != nil || len(rerankResults) == 0 {
		if err != nil {
			log.Warn().Err(err).Msg("Cross-encoder reranking failed, using original order")
		}
		return observations
	}
	observationByID := make(map[int64]*models.Observation, len(observations))
	for _, observation := range observations {
		observationByID[observation.ID] = observation
	}
	reordered := make([]*models.Observation, 0, len(rerankResults))
	for _, result := range rerankResults {
		id, parseErr := strconv.ParseInt(result.ID, 10, 64)
		if parseErr != nil {
			continue
		}
		similarityScores[id] = result.CombinedScore
		if observation, exists := observationByID[id]; exists {
			reordered = append(reordered, observation)
		}
	}
	if len(reordered) == 0 {
		return observations
	}
	return reordered
}

func (s *Service) rerankResults(query string, candidates []reranking.Candidate) ([]reranking.RerankResult, error) {
	limit := 0
	pureMode := false
	if s.config != nil {
		limit = s.config.RerankingResults
		pureMode = s.config.RerankingPureMode
	}
	if s.retrievalHooks != nil {
		if pureMode && s.retrievalHooks.rerankByScore != nil {
			return s.retrievalHooks.rerankByScore(query, candidates, limit)
		}
		if s.retrievalHooks.rerank != nil {
			return s.retrievalHooks.rerank(query, candidates, limit)
		}
	}
	if s.reranker == nil {
		return nil, nil
	}
	if pureMode {
		return s.reranker.RerankByScore(query, candidates, limit)
	}
	return s.reranker.Rerank(query, candidates, limit)
}
