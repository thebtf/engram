// Package worker provides context and search-related HTTP handlers.
package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/reranking"
	"github.com/thebtf/engram/internal/search"
	"github.com/thebtf/engram/internal/search/expansion"
	"github.com/thebtf/engram/internal/vector"
	"github.com/thebtf/engram/internal/worker/sdk"
	"github.com/thebtf/engram/pkg/models"
	"github.com/rs/zerolog/log"
)

// agentEffectivenessThreshold is the minimum number of agent-specific injections required
// to substitute the global effectiveness score with the agent-specific one.
const agentEffectivenessThreshold = 10

// applyStrategy reorders or filters observations according to the named injection strategy.
// agentStats is an optional map of observation_id -> AgentObservationStat used by the
// effectiveness-weighted strategy to personalise scores per agent. Pass nil to use global scores only.
// It returns a new slice; the original is not mutated.
func applyStrategy(strategy string, observations []*models.Observation, agentStats map[int64]gorm.AgentObservationStat) []*models.Observation {
	if len(observations) == 0 {
		return observations
	}
	switch strategy {
	case "effectiveness-weighted":
		// Sort by blend of importance_score (0.5) + effectiveness_score (0.5).
		// When agent-specific stats have >= agentEffectivenessThreshold injections,
		// substitute the global effectiveness_score with the agent-specific rate.
		out := make([]*models.Observation, len(observations))
		copy(out, observations)
		effectivenessFor := func(obs *models.Observation) float64 {
			if agentStats != nil {
				if stat, ok := agentStats[obs.ID]; ok && stat.Injections >= agentEffectivenessThreshold {
					if stat.Injections > 0 {
						return float64(stat.Successes) / float64(stat.Injections)
					}
					return 0
				}
			}
			return obs.EffectivenessScore
		}
		sort.SliceStable(out, func(i, j int) bool {
			si := out[i].ImportanceScore*0.5 + effectivenessFor(out[i])*0.5
			sj := out[j].ImportanceScore*0.5 + effectivenessFor(out[j])*0.5
			return si > sj
		})
		return out

	case "recency-boosted":
		// Re-sort: observations < 24h old get 2x score multiplier
		twentyFourHoursAgo := time.Now().UnixMilli() - 24*60*60*1000
		out := make([]*models.Observation, len(observations))
		copy(out, observations)
		type weighted struct {
			obs   *models.Observation
			score float64
		}
		ws := make([]weighted, len(out))
		for i, obs := range out {
			score := obs.ImportanceScore
			if obs.CreatedAtEpoch > twentyFourHoursAgo {
				score *= 2.0
			}
			ws[i] = weighted{obs: obs, score: score}
		}
		sort.SliceStable(ws, func(i, j int) bool {
			return ws[i].score > ws[j].score
		})
		result := make([]*models.Observation, len(ws))
		for i, w := range ws {
			result[i] = w.obs
		}
		return result

	case "diverse":
		// Keep max 2 observations per concept (first concept tag), interleaved
		// Group by first concept
		grouped := make(map[string][]*models.Observation)
		order := make([]string, 0)
		for _, obs := range observations {
			key := ""
			if len(obs.Concepts) > 0 {
				key = string(obs.Concepts[0])
			}
			if _, exists := grouped[key]; !exists {
				order = append(order, key)
			}
			if len(grouped[key]) < 2 {
				grouped[key] = append(grouped[key], obs)
			}
		}
		// Interleave: take one from each group in round-robin until all exhausted
		out := make([]*models.Observation, 0, len(observations))
		maxRound := 2
		for round := 0; round < maxRound; round++ {
			for _, key := range order {
				if round < len(grouped[key]) {
					out = append(out, grouped[key][round])
				}
			}
		}
		return out

	default:
		// "baseline": no change
		return observations
	}
}

// handleSearchByPrompt godoc
// @Summary Search observations by prompt
// @Description Searches observations relevant to a user prompt using hybrid vector + FTS search with query expansion, cross-encoder reranking, and clustering. Supports both GET (query params) and POST (JSON body) to avoid URL length limits.
// @Tags Search
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param project query string false "Project filter"
// @Param query query string false "Search query"
// @Param cwd query string false "Working directory (ignored server-side)"
// @Param agent_id query string false "Agent ID (acts as project scope if project empty)"
// @Param limit query int false "Number of results (default 50, max 200)"
// @Param body body object false "POST body: {project, query, agent_id, cwd, limit}"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {string} string "project and query required"
// @Failure 500 {string} string "internal error"
// @Router /api/context/search [get]
// @Router /api/context/search [post]
func (s *Service) handleSearchByPrompt(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	query := r.URL.Query().Get("query")
	cwd := r.URL.Query().Get("cwd")
	agentID := r.URL.Query().Get("agent_id")

	// For POST requests, allow JSON body to override query params.
	var obsTypeFilter string
	if r.Method == http.MethodPost && r.Body != nil {
		var body struct {
			Project string `json:"project"`
			Query   string `json:"query"`
			Cwd     string `json:"cwd"`
			AgentID string `json:"agent_id"`
			ObsType string `json:"obs_type"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			if body.Project != "" {
				project = body.Project
			}
			if body.Query != "" {
				query = body.Query
			}
			if body.Cwd != "" {
				cwd = body.Cwd
			}
			if body.AgentID != "" {
				agentID = body.AgentID
			}
			if body.ObsType != "" {
				obsTypeFilter = body.ObsType
			}
			// agent_id acts as project scope for OpenClaw agents without filesystem context
			if project == "" && agentID != "" {
				project = agentID
			}
		}
	}

	// Also accept agent_id as query param fallback for project
	if project == "" && agentID != "" {
		project = agentID
	}

	if project == "" || query == "" {
		http.Error(w, "project and query required", http.StatusBadRequest)
		return
	}

	// Server-side: ignore client-provided cwd to prevent filesystem probing (S9-003).
	// File mtime staleness checks are only meaningful on the client; the server has no
	// access to client filesystems.
	cwd = ""

	// Validate project name to prevent path traversal
	if err := ValidateProjectName(project); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	limit := gorm.ParseLimitParamWithMax(r, DefaultSearchLimit, 200)
	searchStart := time.Now()

	var observations []*models.Observation
	var err error
	var usedVector bool
	similarityScores := make(map[int64]float64) // Track similarity per observation

	// Get threshold settings: prefer per-project adaptive threshold, fall back to global config.
	threshold := s.searchMgr.GetProjectThreshold(r.Context(), project, s.config.ContextRelevanceThreshold)
	maxResults := s.config.ContextMaxPromptResults

	// Generate expanded queries if query expander is available
	// Use timeout context to prevent query expansion from blocking
	var expandedQueries []expansion.ExpandedQuery
	var detectedIntent string
	if s.queryExpander != nil {
		expandCtx, expandCancel := context.WithTimeout(r.Context(), time.Duration(s.config.QueryExpansionTimeoutMS)*time.Millisecond)
		cfg := expansion.DefaultConfig()
		cfg.EnableVocabularyExpansion = false // Vocabulary expansion is optional
		cfg.EnableHyDE = s.config.HyDEEnabled
		expandedQueries = s.queryExpander.Expand(expandCtx, query, cfg)
		expandCancel() // Cancel immediately after use (defer not needed - no panic possible between creation and here)
		if len(expandedQueries) > 0 {
			detectedIntent = string(expandedQueries[0].Intent)
		}
	}
	if len(expandedQueries) == 0 {
		// Fallback to just the original query
		expandedQueries = []expansion.ExpandedQuery{
			{Query: query, Weight: 1.0, Source: "original"},
		}
	}

	// Try vector search first if available
	var vectorSearchFailed bool
	if s.vectorClient != nil && s.vectorClient.IsConnected() {
		where := vector.BuildWhereFilter(vector.DocTypeObservation, project, false)

		// Search with each expanded query and merge results
		// Pre-allocate with estimated capacity to avoid repeated reallocation
		estimatedCapacity := len(expandedQueries) * limit * 2
		allVectorResults := make([]vector.QueryResult, 0, estimatedCapacity)
		queryWeights := make(map[string]float64, len(expandedQueries))
		var vectorErrors int

		for _, eq := range expandedQueries {
			vectorResults, vecErr := s.vectorClient.Query(r.Context(), eq.Query, limit*2, where)
			if vecErr != nil {
				vectorErrors++
				log.Debug().Err(vecErr).Str("query", eq.Query).Msg("Vector query failed")
			} else if len(vectorResults) > 0 {
				// Apply weight to similarity scores before merging
				for i := range vectorResults {
					vectorResults[i].Similarity *= eq.Weight
				}
				allVectorResults = append(allVectorResults, vectorResults...)
				queryWeights[eq.Query] = eq.Weight
			}
		}

		// Track if vector search had issues
		if vectorErrors > 0 && vectorErrors == len(expandedQueries) {
			vectorSearchFailed = true
			log.Warn().Int("errors", vectorErrors).Str("project", project).Msg("All vector queries failed, falling back to FTS")
		}

		if len(allVectorResults) > 0 {
			// Filter by relevance threshold before extracting IDs
			// Use a slightly lower threshold for expanded queries
			effectiveThreshold := threshold * 0.9 // Allow slightly lower scores for expanded queries
			filteredResults := vector.FilterByThreshold(allVectorResults, effectiveThreshold, 0)

			// Build similarity map for filtered results (keeping highest weighted score per observation)
			for _, vr := range filteredResults {
				if sqliteID, ok := vr.Metadata["sqlite_id"].(float64); ok {
					id := int64(sqliteID)
					// Keep the highest score for each observation
					if existing, exists := similarityScores[id]; !exists || vr.Similarity > existing {
						similarityScores[id] = vr.Similarity
					}
				}
			}

			// Extract observation IDs with project/scope filtering using shared helper
			obsIDs := vector.ExtractObservationIDs(filteredResults, project)

			if len(obsIDs) > 0 {
				// Fetch full observations from database
				observations, err = s.observationStore.GetObservationsByIDs(r.Context(), obsIDs, "date_desc", limit)
				if err == nil {
					usedVector = true
				}
			}
		}
	}

	// Fall back to FTS if vector search not available, failed, or returned no results
	if !usedVector || len(observations) == 0 {
		if vectorSearchFailed {
			log.Info().Str("project", project).Msg("Using FTS fallback due to vector search failure")
		}
		scopeFilter := gorm.ScopeFilter{Project: project, AgentID: agentID}
		observations, err = s.observationStore.SearchObservationsFTSFiltered(r.Context(), query, scopeFilter, limit)
		if err != nil {
			// FTS might fail if query has special chars, try without
			log.Warn().Err(err).Str("query", query).Msg("FTS search failed, falling back to recent")
			observations, err = s.observationStore.GetRecentObservationsFiltered(r.Context(), scopeFilter, limit)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
	}

	// Filter by observation type if requested (e.g., obs_type=guidance for behavioral rules)
	if obsTypeFilter != "" {
		filtered := make([]*models.Observation, 0, len(observations))
		for _, obs := range observations {
			if string(obs.Type) == obsTypeFilter {
				filtered = append(filtered, obs)
			}
		}
		observations = filtered
	}

	// Fast staleness filter - NO verification (that's too slow for interactive use)
	// Just check mtimes and exclude obviously stale observations
	var staleCount int
	freshObservations := make([]*models.Observation, 0, len(observations))

	for _, obs := range observations {
		if len(obs.FileMtimes) > 0 && cwd != "" {
			var paths []string
			for path := range obs.FileMtimes {
				paths = append(paths, path)
			}
			currentMtimes := sdk.GetFileMtimes(paths, cwd)

			if obs.CheckStaleness(currentMtimes) {
				// Stale - exclude but don't verify (too slow)
				// Queue for background verification instead
				staleCount++
				s.queueStaleVerification(obs.ID, cwd)
				continue
			}
		}
		freshObservations = append(freshObservations, obs)
	}

	// Apply cross-encoder reranking if available
	if s.reranker != nil && len(freshObservations) > 0 && usedVector {
		// Build candidates from observations with their bi-encoder scores
		candidates := make([]reranking.Candidate, len(freshObservations))
		for i, obs := range freshObservations {
			// Use strings.Builder for efficient concatenation
			var content string
			if obs.Narrative.Valid && obs.Narrative.String != "" {
				var sb strings.Builder
				sb.Grow(len(obs.Title.String) + 1 + len(obs.Narrative.String))
				sb.WriteString(obs.Title.String)
				sb.WriteByte(' ')
				sb.WriteString(obs.Narrative.String)
				content = sb.String()
			} else {
				content = obs.Title.String
			}
			candidates[i] = reranking.Candidate{
				ID:       strconv.FormatInt(obs.ID, 10), // Faster than fmt.Sprintf
				Content:  content,
				Score:    similarityScores[obs.ID],
				Metadata: map[string]any{"obs_idx": i},
			}
		}

		// Rerank using cross-encoder - use pure mode or combined scores
		var rerankResults []reranking.RerankResult
		var rerankErr error
		if s.config.RerankingPureMode {
			rerankResults, rerankErr = s.reranker.RerankByScore(query, candidates, s.config.RerankingResults)
		} else {
			rerankResults, rerankErr = s.reranker.Rerank(query, candidates, s.config.RerankingResults)
		}
		if rerankErr != nil {
			log.Warn().Err(rerankErr).Msg("Cross-encoder reranking failed, using original order")
		} else if len(rerankResults) > 0 {
			// Update similarity scores with reranked scores
			for _, rr := range rerankResults {
				if id, err := strconv.ParseInt(rr.ID, 10, 64); err == nil {
					similarityScores[id] = rr.CombinedScore
				}
			}

			// Reorder observations based on rerank results
			reorderedObs := make([]*models.Observation, 0, len(rerankResults))
			obsMap := make(map[int64]*models.Observation)
			for _, obs := range freshObservations {
				obsMap[obs.ID] = obs
			}
			for _, rr := range rerankResults {
				if id, err := strconv.ParseInt(rr.ID, 10, 64); err == nil {
					if obs, ok := obsMap[id]; ok {
						reorderedObs = append(reorderedObs, obs)
					}
				}
			}
			freshObservations = reorderedObs

			log.Debug().
				Int("candidates", len(candidates)).
				Int("returned", len(rerankResults)).
				Msg("Cross-encoder reranking complete")
		}
	}

	// Cluster similar observations to remove duplicates
	clusteredObservations := clusterObservations(freshObservations, s.config.ClusteringThreshold)
	duplicatesRemoved := len(freshObservations) - len(clusteredObservations)

	// Graph expansion: enrich results with FalkorDB neighbors (same as hybridSearch path).
	// Runs after clustering, before composite scoring, so neighbors get scored equally.
	if len(clusteredObservations) > 0 && s.searchMgr != nil {
		scoredIDs := make([]search.ScoredID, 0, len(clusteredObservations))
		for _, obs := range clusteredObservations {
			scoredIDs = append(scoredIDs, search.ScoredID{
				ID:      obs.ID,
				DocType: "observation",
				Score:   similarityScores[obs.ID],
			})
		}
		expanded := s.searchMgr.ExpandViaGraph(r.Context(), scoredIDs, limit)
		// Merge new IDs back: fetch any expanded observations not already in clusteredObservations
		existingIDs := make(map[int64]bool, len(clusteredObservations))
		for _, obs := range clusteredObservations {
			existingIDs[obs.ID] = true
		}
		var newIDs []int64
		for _, sid := range expanded {
			if !existingIDs[sid.ID] && sid.DocType == "observation" {
				newIDs = append(newIDs, sid.ID)
				similarityScores[sid.ID] = sid.Score // propagate decayed score
			}
		}
		if len(newIDs) > 0 {
			graphObs, err := s.observationStore.GetObservationsByIDs(r.Context(), newIDs, "", 0)
			if err == nil && len(graphObs) > 0 {
				clusteredObservations = append(clusteredObservations, graphObs...)
			}
		}
	}

	// Apply composite scoring (recency × type × importance) as a post-processing step.
	// This re-weights scores already computed by vector search or cross-encoder reranking.
	if len(clusteredObservations) > 0 {
		search.ApplyCompositeScoring(clusteredObservations, similarityScores)

		// Apply injection diversity penalty: observations injected across many projects = generic = penalize
		if s.observationStore != nil {
			ids := make([]int64, 0, len(clusteredObservations))
			for _, obs := range clusteredObservations {
				ids = append(ids, obs.ID)
			}
			if diversityScores, err := s.observationStore.GetDiversityScores(r.Context(), ids); err == nil && len(diversityScores) > 0 {
				search.ApplyDiversityPenalty(clusteredObservations, similarityScores, diversityScores)
			}
		}
	}

	// Apply cross-session priming boost: observations from recently active sessions score higher.
	// Fetch once per search call and check membership to avoid per-observation queries.
	if s.config.SessionBoost > 1.0 && len(clusteredObservations) > 0 {
		twoHoursAgo := time.Now().Add(-2 * time.Hour)
		if recentSessions, sessErr := s.observationStore.GetRecentSessionIDs(r.Context(), project, twoHoursAgo); sessErr == nil {
			search.ApplySessionBoost(clusteredObservations, similarityScores, recentSessions, s.config.SessionBoost)
		}
	}

	// Sort by composite score (highest first)
	if len(similarityScores) > 0 && len(clusteredObservations) > 0 {
		sort.Slice(clusteredObservations, func(i, j int) bool {
			scoreI := similarityScores[clusteredObservations[i].ID]
			scoreJ := similarityScores[clusteredObservations[j].ID]
			return scoreI > scoreJ
		})
	}

	// Injection floor: ensure at least N observations are returned regardless of threshold.
	// Fetch top-importance observations to fill the gap if the result set is too small.
	injectionFloor := s.config.InjectionFloor
	if injectionFloor <= 0 {
		injectionFloor = 3
	}
	if len(clusteredObservations) < injectionFloor && s.observationStore != nil {
		needed := injectionFloor - len(clusteredObservations)
		// Build set of already-included IDs for deduplication.
		includedIDs := make(map[int64]struct{}, len(clusteredObservations))
		for _, obs := range clusteredObservations {
			includedIDs[obs.ID] = struct{}{}
		}
		fillObs, fillErr := s.observationStore.GetTopImportanceObservations(r.Context(), project, needed+len(clusteredObservations))
		if fillErr == nil {
			for _, obs := range fillObs {
				if _, already := includedIDs[obs.ID]; !already {
					clusteredObservations = append(clusteredObservations, obs)
					includedIDs[obs.ID] = struct{}{}
					needed--
					if needed == 0 {
						break
					}
				}
			}
		}
	}

	// Count observations with meaningful composite scores (above noise floor).
	// Raw len(observations) is misleading — in high-dim embedding spaces,
	// nearly all observations pass the vector threshold. Only observations
	// with composite score > 0.05 are genuinely matched.
	totalResults := 0
	for _, obs := range clusteredObservations {
		if score, ok := similarityScores[obs.ID]; ok && score > 0.05 {
			totalResults++
		}
	}

	// Apply max results cap if configured
	if maxResults > 0 && len(clusteredObservations) > maxResults {
		clusteredObservations = clusteredObservations[:maxResults]
	}

	// Apply LLM behavioral relevance filter if enabled
	if s.llmFilter != nil && s.config.LLMFilterEnabled && len(clusteredObservations) > 0 {
		llmFilter := s.llmFilter
		// Take top candidates for LLM evaluation (avoid sending too many)
		candidates := clusteredObservations
		if s.config.LLMFilterCandidates > 0 && len(candidates) > s.config.LLMFilterCandidates {
			candidates = candidates[:s.config.LLMFilterCandidates]
		}
		relevantIDs := llmFilter.FilterByRelevance(r.Context(), candidates, project, query)
		if len(relevantIDs) > 0 && len(relevantIDs) < len(candidates) {
			// Build a fast lookup set
			idSet := make(map[int64]struct{}, len(relevantIDs))
			for _, id := range relevantIDs {
				idSet[id] = struct{}{}
			}
			filtered := make([]*models.Observation, 0, len(relevantIDs))
			for _, obs := range clusteredObservations {
				if _, ok := idSet[obs.ID]; ok {
					filtered = append(filtered, obs)
				}
			}
			log.Info().
				Str("project", project).
				Int("before", len(clusteredObservations)).
				Int("after", len(filtered)).
				Msg("LLM filter applied")
			clusteredObservations = filtered
		}
	}

	// Async: log which observations were injected into this context
	if s.observationStore != nil && len(clusteredObservations) > 0 {
		resultIDs := make([]int64, len(clusteredObservations))
		for i, obs := range clusteredObservations {
			resultIDs[i] = obs.ID
		}
		go func() {
			logCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := s.observationStore.LogInjections(logCtx, resultIDs, project, "", ""); err != nil {
				log.Debug().Err(err).Msg("Failed to log injections")
			}
		}()
	}

	// Record retrieval stats with staleness metrics
	s.recordRetrievalStatsExtended(project, int64(len(clusteredObservations)), 0, 0,
		int64(staleCount), int64(len(freshObservations)), int64(duplicatesRemoved), true)

	// Increment retrieval counts for scoring (async, non-blocking)
	if len(clusteredObservations) > 0 {
		ids := make([]int64, len(clusteredObservations))
		for i, obs := range clusteredObservations {
			ids[i] = obs.ID
		}
		s.incrementRetrievalCounts(ids)
	}

	log.Info().
		Str("project", project).
		Str("query", query).
		Str("intent", detectedIntent).
		Int("expansions", len(expandedQueries)).
		Int("found", len(clusteredObservations)).
		Int("stale_excluded", staleCount).
		Float64("threshold", threshold).
		Msg("Prompt-based observation search")

	// Build response with similarity scores
	obsWithScores := make([]map[string]any, len(clusteredObservations))
	for i, obs := range clusteredObservations {
		obsMap := obs.ToMap()
		if score, ok := similarityScores[obs.ID]; ok {
			obsMap["similarity"] = score
		}
		obsWithScores[i] = obsMap
	}

	// Build expansion info for response
	expansionInfo := make([]map[string]any, len(expandedQueries))
	for i, eq := range expandedQueries {
		expansionInfo[i] = map[string]any{
			"query":  eq.Query,
			"weight": eq.Weight,
			"source": eq.Source,
		}
	}

	// Track search misses for self-tuning analytics (inline — avoids unbounded goroutine spawn)
	if len(clusteredObservations) == 0 && query != "" {
		s.trackSearchMiss(project, query)
	}

	// Track this search for analytics
	s.trackSearchQuery(query, project, "observations", len(clusteredObservations), usedVector, float32(time.Since(searchStart).Milliseconds()))

	// Always-inject tier: fetch observations tagged "always-inject" regardless of query (FR-1, FR-6)
	alwaysInjectLimit := s.config.AlwaysInjectLimit
	if alwaysInjectLimit <= 0 {
		alwaysInjectLimit = 20
	}
	alwaysInjectObs, aiErr := s.observationStore.GetAlwaysInjectObservations(r.Context(), project, alwaysInjectLimit)
	if aiErr != nil {
		log.Debug().Err(aiErr).Msg("Failed to fetch always-inject observations for search")
	}

	writeJSON(w, map[string]any{
		"project":       project,
		"query":         query,
		"intent":        detectedIntent,
		"expansions":    expansionInfo,
		"observations":  obsWithScores,
		"always_inject": alwaysInjectObs,
		"threshold":     threshold,
		"max_results":   maxResults,
		"total_results": totalResults,
	})
}

// handleFileContext godoc
// @Summary Get file context
// @Description Returns observations relevant to specific files being worked on, using vector similarity search.
// @Tags Context
// @Produce json
// @Security ApiKeyAuth
// @Param project query string true "Project name"
// @Param files query string true "Comma-separated file paths (max 20)"
// @Param limit query int false "Results per file (default 10, max 50)"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {string} string "bad request"
// @Router /api/context/files [get]
func (s *Service) handleFileContext(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	if project == "" {
		http.Error(w, "project required", http.StatusBadRequest)
		return
	}

	filesParam := r.URL.Query().Get("files")
	if filesParam == "" {
		http.Error(w, "files required", http.StatusBadRequest)
		return
	}

	// Parse comma-separated file paths
	files := strings.Split(filesParam, ",")
	if len(files) == 0 {
		http.Error(w, "at least one file required", http.StatusBadRequest)
		return
	}

	// Limit to reasonable number of files
	maxFiles := 20
	if len(files) > maxFiles {
		files = files[:maxFiles]
	}

	// Get limit parameter (default 10 per file)
	limitStr := r.URL.Query().Get("limit")
	limit := 10
	if limitStr != "" {
		if parsed, err := strconv.Atoi(limitStr); err == nil && parsed > 0 && parsed <= 50 {
			limit = parsed
		}
	}

	// Search for observations related to each file in parallel
	ctx := r.Context()

	// Check if vector search is available
	if s.vectorClient == nil || !s.vectorClient.IsConnected() {
		writeJSON(w, map[string]any{
			"files":   files,
			"results": map[string]any{},
			"count":   0,
			"error":   "vector search not available",
		})
		return
	}

	// Prepare for parallel execution
	type fileResult struct {
		file    string
		results []map[string]any
		obsIDs  []int64 // Track observation IDs for deduplication
	}

	resultsChan := make(chan fileResult, len(files))
	sem := make(chan struct{}, 5) // Limit concurrency to 5 parallel searches
	var wg sync.WaitGroup

	for _, file := range files {
		file = strings.TrimSpace(file)
		if file == "" {
			continue
		}

		wg.Add(1)
		go func(file string) {
			defer wg.Done()
			sem <- struct{}{}        // Acquire semaphore
			defer func() { <-sem }() // Release semaphore

			// Build search query from file path
			query := buildFileQuery(file)

			where := vector.BuildWhereFilter(vector.DocTypeObservation, project, false)
			vectorResults, vecErr := s.vectorClient.Query(ctx, query, limit*2, where)
			if vecErr != nil {
				log.Warn().Err(vecErr).Str("file", file).Msg("Vector search failed for file context")
				return
			}

			// Extract observation IDs from vector results
			obsIDs := vector.ExtractObservationIDs(vectorResults, project)
			if len(obsIDs) == 0 {
				return
			}

			// Fetch observations
			observations, err := s.observationStore.GetObservationsByIDs(ctx, obsIDs, "score_desc", limit*2)
			if err != nil {
				log.Warn().Err(err).Str("file", file).Msg("Failed to fetch observations for file context")
				return
			}

			// Pre-build score map from vector results (O(n) instead of O(n²))
			scoreMap := make(map[int64]float64, len(vectorResults))
			var avgScore float64
			for _, vr := range vectorResults {
				avgScore += vr.Similarity
				// Parse observation ID from vector result ID (format: obs_{id}_{field})
				// Use index-based parsing to avoid slice allocation from strings.Split
				if len(vr.ID) > 4 && vr.ID[:4] == "obs_" {
					rest := vr.ID[4:] // Skip "obs_"
					underscoreIdx := strings.IndexByte(rest, '_')
					var idStr string
					if underscoreIdx >= 0 {
						idStr = rest[:underscoreIdx]
					} else {
						idStr = rest
					}
					if id, parseErr := strconv.ParseInt(idStr, 10, 64); parseErr == nil {
						// Keep highest score for each observation
						if existing, exists := scoreMap[id]; !exists || vr.Similarity > existing {
							scoreMap[id] = vr.Similarity
						}
					}
				}
			}
			if len(vectorResults) > 0 {
				avgScore /= float64(len(vectorResults))
			}

			fileResults := make([]map[string]any, 0, limit)
			var usedIDs []int64
			for _, obs := range observations {
				// Check project scope
				if obs.Scope == "project" && obs.Project != project {
					continue
				}

				// O(1) score lookup instead of O(n) nested loop
				score, found := scoreMap[obs.ID]
				if !found {
					// Use average score as fallback
					score = avgScore
				}

				// Only include if score is above threshold
				if score < 0.3 {
					continue
				}

				fileResults = append(fileResults, map[string]any{
					"id":        obs.ID,
					"title":     obs.Title.String,
					"type":      obs.Type,
					"narrative": obs.Narrative.String,
					"facts":     obs.Facts,
					"score":     score,
				})
				usedIDs = append(usedIDs, obs.ID)

				if len(fileResults) >= limit {
					break
				}
			}

			if len(fileResults) > 0 {
				resultsChan <- fileResult{file: file, results: fileResults, obsIDs: usedIDs}
			}
		}(file)
	}

	// Close channel when all goroutines complete
	go func() {
		wg.Wait()
		close(resultsChan)
	}()

	// Collect results and deduplicate
	allResults := make(map[string]any)
	seenObservationIDs := make(map[int64]bool)

	for res := range resultsChan {
		// Filter out duplicates that were already seen in other files
		dedupedResults := make([]map[string]any, 0, len(res.results))
		for i, r := range res.results {
			obsID := res.obsIDs[i]
			if !seenObservationIDs[obsID] {
				seenObservationIDs[obsID] = true
				dedupedResults = append(dedupedResults, r)
			}
		}
		if len(dedupedResults) > 0 {
			allResults[res.file] = dedupedResults
		}
	}

	writeJSON(w, map[string]any{
		"files":   files,
		"results": allResults,
		"count":   len(allResults),
	})
}

// estimateObsTokens estimates the token count for a single observation (full detail).
// Uses ~4 chars per token heuristic for English text.
func estimateObsTokens(obs *models.Observation) int {
	chars := len(obs.Title.String) + len(obs.Subtitle.String) + len(obs.Narrative.String)
	for _, fact := range obs.Facts {
		chars += len(fact)
	}
	// Add overhead for type tag, formatting, bullet points (~50 chars)
	chars += 50
	return (chars + 3) / 4 // ceil(chars/4)
}

// estimateObsTokensCondensed estimates tokens for condensed format (title + subtitle only).
func estimateObsTokensCondensed(obs *models.Observation) int {
	chars := len(obs.Title.String) + len(obs.Subtitle.String) + 30 // type tag + formatting
	return (chars + 3) / 4
}

// estimateTokens estimates total tokens for a slice of observations.
func estimateTokens(observations []*models.Observation) int {
	return estimateTokensWithLimit(observations, -1)
}

// estimateTokensWithLimit estimates tokens accounting for condensed format.
// First `fullCount` observations use full detail, the rest use condensed (title+subtitle).
// If fullCount < 0, all observations use full detail.
func estimateTokensWithLimit(observations []*models.Observation, fullCount int) int {
	total := 0
	for i, obs := range observations {
		if fullCount >= 0 && i >= fullCount {
			total += estimateObsTokensCondensed(obs)
		} else {
			total += estimateObsTokens(obs)
		}
	}
	return total
}

// trimToTokenBudget trims observations to fit within a token budget.
// Returns the trimmed slice, number of observations removed, and estimated token count.
func trimToTokenBudget(observations []*models.Observation, budget int) ([]*models.Observation, int, int) {
	if budget <= 0 || len(observations) == 0 {
		return observations, 0, estimateTokens(observations)
	}

	var totalTokens int
	for i, obs := range observations {
		tokens := estimateObsTokens(obs)
		if totalTokens+tokens > budget {
			return observations[:i], len(observations) - i, totalTokens
		}
		totalTokens += tokens
	}
	return observations, 0, totalTokens
}

// filterByIDs filters observations to only include those with IDs in the set.
func filterByIDs(observations []*models.Observation, ids map[int64]struct{}) []*models.Observation {
	result := make([]*models.Observation, 0, len(observations))
	for _, obs := range observations {
		if _, ok := ids[obs.ID]; ok {
			result = append(result, obs)
		}
	}
	return result
}

// compactObservation returns only the fields needed by the session-start hook.
func compactObservation(obs *models.Observation) map[string]any {
	m := map[string]any{
		"id":    obs.ID,
		"type":  obs.Type,
		"title": obs.Title.String,
	}
	if obs.Subtitle.Valid && obs.Subtitle.String != "" {
		m["subtitle"] = obs.Subtitle.String
	}
	if obs.Narrative.Valid && obs.Narrative.String != "" {
		m["narrative"] = obs.Narrative.String
	}
	if len(obs.Facts) > 0 {
		m["facts"] = obs.Facts
	}
	return m
}

// compactObservations converts a slice of observations to compact format.
// Uses compactObservationsWithLimit with fullCount=-1 (all full detail).
func compactObservations(observations []*models.Observation) []map[string]any {
	return compactObservationsWithLimit(observations, -1)
}

// compactObservationsWithLimit converts observations to compact format.
// First `fullCount` observations get full detail (narrative + facts).
// Remaining observations get condensed format (title + subtitle only).
// If fullCount < 0, all observations get full detail.
func compactObservationsWithLimit(observations []*models.Observation, fullCount int) []map[string]any {
	result := make([]map[string]any, len(observations))
	for i, obs := range observations {
		if fullCount >= 0 && i >= fullCount {
			// Condensed: only id, type, title, subtitle
			m := map[string]any{
				"id":    obs.ID,
				"type":  obs.Type,
				"title": obs.Title.String,
			}
			if obs.Subtitle.Valid && obs.Subtitle.String != "" {
				m["subtitle"] = obs.Subtitle.String
			}
			result[i] = m
		} else {
			result[i] = compactObservation(obs)
		}
	}
	return result
}

// buildFileQuery extracts meaningful search terms from a file path.
func buildFileQuery(filePath string) string {
	// Remove common prefixes and extensions
	path := strings.TrimPrefix(filePath, "/")

	// Extract the filename and directory
	parts := strings.Split(path, "/")
	meaningful := make([]string, 0, len(parts))

	for _, part := range parts {
		// Skip common directory names that aren't meaningful
		switch strings.ToLower(part) {
		case "src", "lib", "internal", "pkg", "cmd", "api", "app", "test", "tests", "spec", "specs":
			continue
		default:
			// Remove file extension
			if idx := strings.LastIndex(part, "."); idx > 0 {
				part = part[:idx]
			}
			// Convert camelCase/PascalCase to spaces
			part = splitCamelCase(part)
			// Convert snake_case to spaces
			part = strings.ReplaceAll(part, "_", " ")
			// Convert kebab-case to spaces
			part = strings.ReplaceAll(part, "-", " ")
			meaningful = append(meaningful, part)
		}
	}

	return strings.Join(meaningful, " ")
}

// splitCamelCase splits camelCase or PascalCase into separate words.
func splitCamelCase(s string) string {
	var result strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			result.WriteRune(' ')
		}
		result.WriteRune(r)
	}
	return result.String()
}

// applyActiveVersions replaces each observation's narrative with its active ObservationVersion
// narrative when one exists. Returns a new slice; original observation pointers are not mutated.
// Errors from the version store are silently logged — the original narrative is used as fallback.
func applyActiveVersions(ctx context.Context, vs *gorm.VersionStore, observations []*models.Observation) []*models.Observation {
	if len(observations) == 0 || vs == nil {
		return observations
	}

	result := make([]*models.Observation, len(observations))
	for i, obs := range observations {
		active, err := vs.GetActiveVersion(ctx, obs.ID)
		if err != nil {
			log.Debug().Err(err).Int64("obs_id", obs.ID).Msg("Failed to fetch active observation version; using original narrative")
			result[i] = obs
			continue
		}
		if active == nil {
			result[i] = obs
			continue
		}
		// Shallow copy — only swap the narrative field so the original model is not mutated.
		copy := *obs
		copy.Narrative.String = active.Narrative
		copy.Narrative.Valid = true
		result[i] = &copy
	}

	return result
}

// formatBulletOnly formats an observation as a minimal bullet point: "- [TYPE] title: key facts".
// No narrative is included. Suitable for high-density injection where context space is limited.
func formatBulletOnly(obs *models.Observation) string {
	obsType := string(obs.Type)
	title := ""
	if obs.Title.Valid {
		title = obs.Title.String
	}
	return "- [" + obsType + "] " + title
}

// formatConcise formats an observation with its title and the first 100 characters of the narrative.
// Balances density and readability for medium-priority observations.
func formatConcise(obs *models.Observation) string {
	obsType := string(obs.Type)
	title := ""
	if obs.Title.Valid {
		title = obs.Title.String
	}
	narrative := ""
	if obs.Narrative.Valid {
		n := obs.Narrative.String
		if len(n) > 100 {
			n = n[:100] + "..."
		}
		narrative = n
	}
	return "- [" + obsType + "] " + title + ": " + narrative
}

// formatStructured formats an observation as a structured XML-like tag.
// Useful for strategies that want machine-parseable injection format.
func formatStructured(obs *models.Observation) string {
	narrative := ""
	if obs.Narrative.Valid {
		narrative = obs.Narrative.String
	}
	return "<observation type=\"" + string(obs.Type) + "\" id=\"" + strconv.FormatInt(obs.ID, 10) + "\">" + narrative + "</observation>"
}

// handleContextInject godoc
// @Summary Inject context for session start
// @Description Returns context for injection at session start. Response includes recent (last 5), relevant (top 10 semantic), and guidance sections. Supports GET (deprecated) and POST. Critical startup path — optimized for speed.
// @Tags Context
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param project query string false "Project name (required)"
// @Param agent_id query string false "Agent ID (acts as project scope if project empty)"
// @Param format query string false "Response format: 'compact' for minimal payload"
// @Param body body object false "POST body: {project, agent_id, cwd, legacy_project, git_remote, relative_path}"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {string} string "project required"
// @Failure 500 {string} string "internal error"
// @Router /api/context/inject [post]
// @Router /api/context/inject [get]
func (s *Service) handleContextInject(w http.ResponseWriter, r *http.Request) {
	var project, agentID, cwd, legacyProject, gitRemote, relativePath, sessionID string

	if r.Method == http.MethodPost {
		var req struct {
			Project       string `json:"project"`
			AgentID       string `json:"agent_id"`
			Cwd           string `json:"cwd"`
			LegacyProject string `json:"legacy_project"`
			GitRemote     string `json:"git_remote"`
			RelativePath  string `json:"relative_path"`
			SessionID     string `json:"session_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		project = req.Project
		agentID = req.AgentID
		cwd = req.Cwd
		legacyProject = req.LegacyProject
		gitRemote = req.GitRemote
		relativePath = req.RelativePath
		sessionID = req.SessionID
	} else {
		// GET (deprecated — use POST)
		project = r.URL.Query().Get("project")
		agentID = r.URL.Query().Get("agent_id")
		cwd = r.URL.Query().Get("cwd")
		legacyProject = r.URL.Query().Get("legacy_project")
		gitRemote = r.URL.Query().Get("git_remote")
		relativePath = r.URL.Query().Get("relative_path")
		sessionID = r.URL.Query().Get("session_id")
	}

	// Fall back to agent_id as session proxy when no explicit session_id provided
	if sessionID == "" {
		sessionID = agentID
	}

	// agent_id acts as project scope for OpenClaw agents without filesystem context
	if project == "" && agentID != "" {
		project = agentID
	}
	if project == "" {
		http.Error(w, "project required", http.StatusBadRequest)
		return
	}

	// Validate project name to prevent path traversal
	if err := ValidateProjectName(project); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Server-side: ignore client-provided cwd to prevent filesystem probing (S9-003).
	// File mtime staleness checks are only meaningful on the client; the server has no
	// access to client filesystems.
	cwd = ""

	if legacyProject != "" && legacyProject != project {
		displayName := project
		if idx := strings.Index(project, "_"); idx > 0 {
			displayName = project[:idx]
		}
		go func() {
			if err := gorm.UpsertProject(context.Background(), s.store.DB, project, legacyProject, gitRemote, relativePath, displayName); err != nil {
				log.Warn().Err(err).Str("project", project).Str("legacy", legacyProject).Msg("project upsert failed")
			}
		}()
	}

	// Limit observations for fast startup (configurable, default 100)
	limit := s.config.ContextObservations
	if limit <= 0 {
		limit = DefaultContextLimit
	}

	// Full count determines how many observations get full detail (configurable, default 25)
	fullCount := s.config.ContextFullCount
	if fullCount <= 0 {
		fullCount = 25
	}

	ctx := r.Context()

	// --- Recent section: last 5 observations by created_at ---
	scopeFilter := gorm.ScopeFilter{Project: project, AgentID: agentID}
	recentRaw, err := s.observationStore.GetRecentObservationsFiltered(ctx, scopeFilter, 5)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Apply staleness filter to recent observations
	var staleCount int
	recentFresh := make([]*models.Observation, 0, len(recentRaw))
	for _, obs := range recentRaw {
		if len(obs.FileMtimes) > 0 {
			var paths []string
			for path := range obs.FileMtimes {
				paths = append(paths, path)
			}
			currentMtimes := sdk.GetFileMtimes(paths, cwd)
			if obs.CheckStaleness(currentMtimes) {
				staleCount++
				s.queueStaleVerification(obs.ID, cwd)
				continue
			}
		}
		recentFresh = append(recentFresh, obs)
	}

	// Build a set of IDs already in the recent section for deduplication
	recentIDs := make(map[int64]struct{}, len(recentFresh))
	for _, obs := range recentFresh {
		recentIDs[obs.ID] = struct{}{}
	}

	// --- Relevant section: semantic search results with temporal boost ---
	var relevantObservations []*models.Observation
	if s.vectorClient != nil && s.vectorClient.IsConnected() {
		query := project + " code development"
		where := vector.BuildWhereFilter(vector.DocTypeObservation, project, false)

		vectorResults, vecErr := s.vectorClient.Query(ctx, query, 20, where)
		if vecErr != nil {
			log.Debug().Err(vecErr).Str("project", project).Msg("Vector query failed for context inject relevant section")
		} else {
			obsIDs := vector.ExtractObservationIDs(vectorResults, project)
			if len(obsIDs) > 0 {
				fetched, fetchErr := s.observationStore.GetObservationsByIDs(ctx, obsIDs, "score_desc", 10)
				if fetchErr != nil {
					log.Debug().Err(fetchErr).Msg("Failed to fetch relevant observations for context inject")
				} else {
					// Apply temporal boost: observations created within last 24h get 1.5x weight.
					// Base scores are derived from fetch rank (position in score_desc result set).
					// Observations newer than 24h receive a 1.5x multiplier before re-ranking.
					now := time.Now().UnixMilli()
					twentyFourHoursAgo := now - 24*60*60*1000

					// Separate boosted (recent) and unboosted observations, deduplicate against recent section
					type scoredObs struct {
						obs   *models.Observation
						score float64
					}
					scored := make([]scoredObs, 0, len(fetched))
					for i, obs := range fetched {
						if _, alreadyInRecent := recentIDs[obs.ID]; alreadyInRecent {
							continue
						}
						// Base score inversely proportional to rank (higher rank = lower score)
						baseScore := 1.0 / float64(i+1)
						if obs.CreatedAtEpoch > twentyFourHoursAgo {
							baseScore *= 1.5
						}
						scored = append(scored, scoredObs{obs: obs, score: baseScore})
					}

					// Sort by boosted score descending
					sort.Slice(scored, func(i, j int) bool {
						return scored[i].score > scored[j].score
					})

					// Take top 10
					maxRelevant := 10
					if len(scored) < maxRelevant {
						maxRelevant = len(scored)
					}
					relevantObservations = make([]*models.Observation, maxRelevant)
					for i := 0; i < maxRelevant; i++ {
						relevantObservations[i] = scored[i].obs
					}
				}
			}
		}
	}

	// --- Guidance section: top guidance observations ---
	var guidanceObservations []*models.Observation
	guidanceRaw, guidanceErr := s.observationStore.GetGuidanceObservations(ctx, project, 5)
	if guidanceErr != nil {
		log.Debug().Err(guidanceErr).Str("project", project).Msg("Failed to fetch guidance observations")
	} else {
		// Apply staleness filter
		for _, obs := range guidanceRaw {
			if len(obs.FileMtimes) > 0 {
				var paths []string
				for path := range obs.FileMtimes {
					paths = append(paths, path)
				}
				currentMtimes := sdk.GetFileMtimes(paths, cwd)
				if obs.CheckStaleness(currentMtimes) {
					staleCount++
					s.queueStaleVerification(obs.ID, cwd)
					continue
				}
			}
			guidanceObservations = append(guidanceObservations, obs)
		}
	}

	// Add guidance IDs to recent dedup set
	for _, obs := range guidanceObservations {
		recentIDs[obs.ID] = struct{}{}
	}

	// --- Always-inject section: observations tagged with "always-inject" concept (FR-1, FR-6) ---
	var alwaysInjectObservations []*models.Observation
	alwaysInjectLimit := s.config.AlwaysInjectLimit
	if alwaysInjectLimit <= 0 {
		alwaysInjectLimit = 20
	}
	alwaysInjectRaw, aiErr := s.observationStore.GetAlwaysInjectObservations(ctx, project, alwaysInjectLimit)
	if aiErr != nil {
		log.Debug().Err(aiErr).Msg("Failed to fetch always-inject observations")
	} else {
		for _, obs := range alwaysInjectRaw {
			// Deduplicate against guidance and recent sections
			if _, already := recentIDs[obs.ID]; !already {
				alwaysInjectObservations = append(alwaysInjectObservations, obs)
				recentIDs[obs.ID] = struct{}{}
			}
		}
	}

	// --- Injection floor: ensure minimum observations across all sections ---
	// Count total distinct observations already collected.
	injectionFloor := s.config.InjectionFloor
	if injectionFloor <= 0 {
		injectionFloor = 3
	}
	totalInjected := len(recentFresh) + len(relevantObservations) + len(guidanceObservations) + len(alwaysInjectObservations)
	if totalInjected < injectionFloor && s.observationStore != nil {
		needed := injectionFloor - totalInjected
		fillObs, fillErr := s.observationStore.GetTopImportanceObservations(ctx, project, needed+totalInjected)
		if fillErr == nil {
			for _, obs := range fillObs {
				if _, already := recentIDs[obs.ID]; !already {
					recentFresh = append(recentFresh, obs)
					recentIDs[obs.ID] = struct{}{}
					needed--
					if needed == 0 {
						break
					}
				}
			}
		}
	}

	// --- Backward-compat observations field: full recent list + relevant deduped union ---
	// Get the full recent list (up to configured limit) for the legacy field
	allRecentRaw, err := s.observationStore.GetRecentObservationsFiltered(ctx, scopeFilter, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var allFreshObservations []*models.Observation
	for _, obs := range allRecentRaw {
		if len(obs.FileMtimes) > 0 {
			var paths []string
			for path := range obs.FileMtimes {
				paths = append(paths, path)
			}
			currentMtimes := sdk.GetFileMtimes(paths, cwd)
			if obs.CheckStaleness(currentMtimes) {
				staleCount++
				s.queueStaleVerification(obs.ID, cwd)
				continue
			}
		}
		allFreshObservations = append(allFreshObservations, obs)
	}

	// Merge relevant observations into the union (those not already in allFreshObservations)
	allFreshIDs := make(map[int64]struct{}, len(allFreshObservations))
	for _, obs := range allFreshObservations {
		allFreshIDs[obs.ID] = struct{}{}
	}
	unionObservations := make([]*models.Observation, len(allFreshObservations))
	copy(unionObservations, allFreshObservations)
	for _, obs := range relevantObservations {
		if _, exists := allFreshIDs[obs.ID]; !exists {
			unionObservations = append(unionObservations, obs)
		}
	}

	// Cluster the union to remove duplicates
	clusteredObservations := clusterObservations(unionObservations, s.config.ClusteringThreshold)
	duplicatesRemoved := len(unionObservations) - len(clusteredObservations)

	// Record retrieval stats with staleness metrics
	s.recordRetrievalStatsExtended(project, int64(len(clusteredObservations)), 0, 0,
		int64(staleCount), int64(len(allFreshObservations)), int64(duplicatesRemoved), false)

	// Increment retrieval counts for scoring (async, non-blocking)
	if len(clusteredObservations) > 0 {
		ids := make([]int64, len(clusteredObservations))
		for i, obs := range clusteredObservations {
			ids[i] = obs.ID
		}
		s.incrementRetrievalCounts(ids)
	}

	// Apply token budget: estimate tokens and trim observations to fit
	tokenBudget := s.config.ContextMaxTokens
	var tokenEstimate int
	var budgetTrimmed int

	if tokenBudget > 0 {
		// Estimate tokens per observation (~4 chars per token for English)
		// Reserve 20% of budget for guidance
		guidanceBudget := tokenBudget / 5
		mainBudget := tokenBudget - guidanceBudget

		// Trim guidance first
		guidanceObservations, _, _ = trimToTokenBudget(guidanceObservations, guidanceBudget)

		// Trim main observations
		var mainTrimmed int
		clusteredObservations, mainTrimmed, tokenEstimate = trimToTokenBudget(clusteredObservations, mainBudget)
		budgetTrimmed = mainTrimmed

		// Also trim recent and relevant sections to not exceed what's in clustered
		clusteredIDs := make(map[int64]struct{}, len(clusteredObservations))
		for _, obs := range clusteredObservations {
			clusteredIDs[obs.ID] = struct{}{}
		}
		recentFresh = filterByIDs(recentFresh, clusteredIDs)
		relevantObservations = filterByIDs(relevantObservations, clusteredIDs)
	} else {
		tokenEstimate = estimateTokens(clusteredObservations) + estimateTokens(guidanceObservations)
	}

	log.Info().
		Str("project", project).
		Int("total", len(allRecentRaw)).
		Int("fresh", len(allFreshObservations)).
		Int("clustered", len(clusteredObservations)).
		Int("duplicates", duplicatesRemoved).
		Int("stale_excluded", staleCount).
		Int("budget_trimmed", budgetTrimmed).
		Int("token_estimate", tokenEstimate).
		Int("recent_section", len(recentFresh)).
		Int("relevant_section", len(relevantObservations)).
		Int("guidance_section", len(guidanceObservations)).
		Msg("Context injection with clustering")

	// Fetch agent-specific effectiveness stats for relevant observations when agent_id is present.
	// Used by the effectiveness-weighted strategy to personalise injection ordering per agent.
	var agentStats map[int64]gorm.AgentObservationStat
	if agentID != "" && s.agentStatsStore != nil && len(relevantObservations) > 0 {
		obsIDs := make([]int64, len(relevantObservations))
		for i, obs := range relevantObservations {
			obsIDs[i] = obs.ID
		}
		if stats, err := s.agentStatsStore.GetAgentStats(ctx, agentID, obsIDs); err == nil {
			agentStats = stats
		} else {
			log.Debug().Err(err).Str("agent_id", agentID).Msg("Failed to fetch agent stats for injection strategy")
		}
	}

	// Apply A/B injection strategy (closed-loop learning FR-5).
	// Strategy is selected per-session and applied to the relevant observations section.
	// The strategy name is recorded on the session row for later comparison.
	var selectedStrategy string
	if s.strategySelector != nil {
		selectedStrategy = s.strategySelector.SelectStrategy(sessionID)
		relevantObservations = applyStrategy(selectedStrategy, relevantObservations, agentStats)
		log.Debug().Str("session", sessionID).Str("strategy", selectedStrategy).Msg("Injection strategy applied")
		// Record strategy on session (fire-and-forget)
		if sessionID != "" && s.sessionStore != nil {
			capturedStrategy := selectedStrategy
			capturedSID := sessionID
			sessionStore := s.sessionStore
			go func() {
				if err := sessionStore.UpdateInjectionStrategy(context.Background(), capturedSID, capturedStrategy); err != nil {
					log.Debug().Err(err).Str("session", capturedSID).Msg("Failed to record injection strategy on session")
				}
			}()
		}
	}

	// Apply active version substitution (APO-lite, Phase 5).
	// For each observation in guidance and always-inject sections, check whether an active
	// ObservationVersion exists. When one does, replace the narrative in a shallow copy so
	// the original model record is not mutated.
	s.initMu.RLock()
	versionStore := s.versionStore
	s.initMu.RUnlock()
	if versionStore != nil {
		guidanceObservations = applyActiveVersions(ctx, versionStore, guidanceObservations)
		alwaysInjectObservations = applyActiveVersions(ctx, versionStore, alwaysInjectObservations)
	}

	// Record injection events asynchronously (closed-loop learning Phase 1).
	// Fire-and-forget: injection tracking is non-critical; errors are silently dropped.
	if sessionID != "" && s.injectionStore != nil {
		capturedAlwaysInject := alwaysInjectObservations
		capturedRecent := recentFresh
		capturedRelevant := relevantObservations
		capturedSessionID := sessionID
		injStore := s.injectionStore
		go func() {
			var records []gorm.InjectionRecord
			for _, obs := range capturedAlwaysInject {
				records = append(records, gorm.InjectionRecord{ObservationID: obs.ID, SessionID: capturedSessionID, InjectionSection: "always_inject"})
			}
			for _, obs := range capturedRecent {
				records = append(records, gorm.InjectionRecord{ObservationID: obs.ID, SessionID: capturedSessionID, InjectionSection: "recent"})
			}
			for _, obs := range capturedRelevant {
				records = append(records, gorm.InjectionRecord{ObservationID: obs.ID, SessionID: capturedSessionID, InjectionSection: "relevant"})
			}
			if len(records) > 0 {
				_ = injStore.RecordInjections(context.Background(), records)
			}
		}()
	}

	// Check if compact format is requested
	compact := r.URL.Query().Get("format") == "compact"

	if compact {
		// Compact format: only fields the hook actually uses.
		// Main observations use fullCount limit — condensed entries skip narrative/facts.
		// Recalculate token estimate accounting for condensed format savings.
		compactTokenEstimate := estimateTokensWithLimit(clusteredObservations, fullCount) +
			estimateTokens(guidanceObservations)
		writeJSON(w, map[string]any{
			"strategy": selectedStrategy,
			"project":            project,
			"observations":       compactObservationsWithLimit(clusteredObservations, fullCount),
			"recent":             compactObservations(recentFresh),
			"relevant":           compactObservations(relevantObservations),
			"guidance":           compactObservations(guidanceObservations),
			"always_inject":      compactObservations(alwaysInjectObservations),
			"full_count":         fullCount,
			"stale_excluded":     staleCount,
			"duplicates_removed": duplicatesRemoved,
			"token_estimate":     compactTokenEstimate,
			"budget_trimmed":     budgetTrimmed,
		})
	} else {
		writeJSON(w, map[string]any{
			"project":            project,
			"strategy":           selectedStrategy,
			"observations":       clusteredObservations,
			"recent":             recentFresh,
			"relevant":           relevantObservations,
			"guidance":           guidanceObservations,
			"always_inject":      alwaysInjectObservations,
			"full_count":         fullCount,
			"stale_excluded":     staleCount,
			"duplicates_removed": duplicatesRemoved,
			"token_estimate":     tokenEstimate,
			"budget_trimmed":     budgetTrimmed,
		})
	}
}

// handleSearchDecisions godoc
// @Summary Search decisions
// @Description Searches observations using decision-optimized semantic search. Thin REST wrapper over the search manager's Decisions method.
// @Tags Search
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body object true "Search params: query, project (required), limit (optional)"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {string} string "query and project required"
// @Failure 500 {string} string "internal error"
// @Router /api/decisions/search [post]
func (s *Service) handleSearchDecisions(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Query   string `json:"query"`
		Project string `json:"project"`
		Limit   int    `json:"limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if body.Query == "" || body.Project == "" {
		http.Error(w, "query and project required", http.StatusBadRequest)
		return
	}
	if err := ValidateProjectName(body.Project); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	const maxDecisionSearchLimit = 100
	limit := body.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > maxDecisionSearchLimit {
		limit = maxDecisionSearchLimit
	}

	params := search.SearchParams{
		Query:   body.Query,
		Project: body.Project,
		Limit:   limit,
	}

	result, err := s.searchMgr.Decisions(r.Context(), params)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{
		"project":      body.Project,
		"query":        body.Query,
		"observations": result.Results,
		"total_count":  result.TotalCount,
	})
}

// handleContextCount godoc
// @Summary Get observation count
// @Description Returns the count of observations for a project (cached).
// @Tags Context
// @Produce json
// @Security ApiKeyAuth
// @Param project query string true "Project name"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {string} string "project required"
// @Failure 500 {string} string "internal error"
// @Router /api/context/count [get]
func (s *Service) handleContextCount(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	if project == "" {
		http.Error(w, "project required", http.StatusBadRequest)
		return
	}

	count, err := s.getCachedObservationCount(r.Context(), project)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{
		"project": project,
		"count":   count,
	})
}

// trackSearchMiss records a search query that returned zero results for analytics.
func (s *Service) trackSearchMiss(project, query string) {
	s.initMu.RLock()
	obsStore := s.observationStore
	s.initMu.RUnlock()
	if obsStore == nil {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	if err := obsStore.RecordSearchMiss(ctx, project, query); err != nil {
		log.Warn().Err(err).Str("project", project).Msg("failed to record search miss")
	}
}

// handleSearchMissAnalytics godoc
// @Summary Get search miss analytics
// @Description Returns aggregated analytics for search queries that returned zero results, useful for self-tuning.
// @Tags Search
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body object true "Params: project (optional — omit to aggregate across all projects), limit (optional)"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {string} string "invalid project name"
// @Failure 500 {string} string "internal error"
// @Failure 503 {string} string "store not available"
// @Router /api/analytics/search-misses [post]
func (s *Service) handleSearchMissAnalytics(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Project string `json:"project"`
		Limit   int    `json:"limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Project != "" {
		if err := ValidateProjectName(body.Project); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	const maxSearchMissStatsLimit = 200
	if body.Limit <= 0 {
		body.Limit = 50
	}
	if body.Limit > maxSearchMissStatsLimit {
		body.Limit = maxSearchMissStatsLimit
	}

	s.initMu.RLock()
	obsStore := s.observationStore
	s.initMu.RUnlock()
	if obsStore == nil {
		http.Error(w, "store not available", http.StatusServiceUnavailable)
		return
	}

	stats, err := obsStore.GetSearchMissStats(r.Context(), body.Project, body.Limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{
		"project":      body.Project,
		"miss_stats":   stats,
		"total_misses": len(stats),
	})
}
