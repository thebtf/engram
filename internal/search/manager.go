// Package search provides unified search capabilities for engram.
package search

import (
	"context"
	"hash/fnv"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/embedding"
	graphpkg "github.com/thebtf/engram/internal/graph"
	"github.com/thebtf/engram/internal/vector"
	"github.com/thebtf/engram/pkg/models"
	"github.com/thebtf/engram/pkg/strutil"
	"github.com/rs/zerolog/log"
	"golang.org/x/sync/singleflight"
)

// multiSpaceRegex matches multiple consecutive whitespace characters.
// Pre-compiled for performance in normalizeQuery.
var multiSpaceRegex = regexp.MustCompile(`\s+`)

// Search configuration constants.
const (
	// Cache configuration
	defaultCacheTTL        = 30 * time.Second // Short TTL for freshness
	defaultCacheMaxSize    = 200              // Max cached results
	cacheEvictionPercent   = 10               // Evict 10% when cache is full
	cacheEvictionThreshold = 80               // Start eviction scan at 80% capacity

	// Latency tracking
	latencyHistogramCap  = 1000      // Max latency samples for histogram
	slowQueryThresholdNs = 100 * 1e6 // 100ms threshold for slow query logging

	// Query frequency tracking
	maxFrequencyEntries    = 1000           // Max queries to track for warming
	frequencyEvictionBatch = 100            // Remove 10% when frequency map is full
	staleQueryThreshold    = 24 * time.Hour // Remove queries older than 24 hours
	recentQueryWindow      = time.Hour      // Only consider queries from last hour for warming

	// Cache warming configuration
	cacheWarmingInitDelay    = 30 * time.Second // Delay before starting warming
	cacheWarmingInterval     = 20 * time.Second // Run warming cycle every 20 seconds
	frequencyCleanupInterval = 5 * time.Minute  // Cleanup stale entries every 5 minutes
	cacheCleanupInterval     = time.Minute      // Cleanup expired cache every minute
	warmingQueryTimeout      = 5 * time.Second  // Timeout for warming queries
	warmingBatchSize         = 5                // Warm top 5 queries per cycle
	minRecencyFactor         = 0.1              // Minimum recency factor for scoring

	// Default query limits
	defaultQueryLimit = 20
	maxQueryLimit     = 100
	defaultOrderBy    = "date_desc"

	// Truncation lengths
	queryLogTruncateLen   = 50  // Truncate query in logs
	titleTruncateLen      = 100 // Truncate titles in results
	warmingLogTruncateLen = 30  // Truncate query in warming logs
)

// SearchMetrics tracks search performance statistics.
type SearchMetrics struct {
	latencyHistogram  []int64
	TotalSearches     int64
	VectorSearches    int64
	FilterSearches    int64
	TotalLatencyNs    int64
	VectorLatencyNs   int64
	FilterLatencyNs   int64
	CacheHits         int64
	CoalescedRequests int64
	SearchErrors      int64
	FTSShortCircuits  int64
	histogramMu       sync.Mutex
}

// GetStats returns the current search statistics.
func (m *SearchMetrics) GetStats() map[string]any {
	totalSearches := atomic.LoadInt64(&m.TotalSearches)
	totalLatency := atomic.LoadInt64(&m.TotalLatencyNs)
	vectorSearches := atomic.LoadInt64(&m.VectorSearches)
	vectorLatency := atomic.LoadInt64(&m.VectorLatencyNs)
	filterSearches := atomic.LoadInt64(&m.FilterSearches)
	filterLatency := atomic.LoadInt64(&m.FilterLatencyNs)

	avgLatencyMs := float64(0)
	if totalSearches > 0 {
		avgLatencyMs = float64(totalLatency) / float64(totalSearches) / 1e6
	}

	avgVectorLatencyMs := float64(0)
	if vectorSearches > 0 {
		avgVectorLatencyMs = float64(vectorLatency) / float64(vectorSearches) / 1e6
	}

	avgFilterLatencyMs := float64(0)
	if filterSearches > 0 {
		avgFilterLatencyMs = float64(filterLatency) / float64(filterSearches) / 1e6
	}

	return map[string]any{
		"total_searches":        totalSearches,
		"vector_searches":       vectorSearches,
		"filter_searches":       filterSearches,
		"cache_hits":            atomic.LoadInt64(&m.CacheHits),
		"coalesced_requests":    atomic.LoadInt64(&m.CoalescedRequests),
		"search_errors":         atomic.LoadInt64(&m.SearchErrors),
		"fts_short_circuits":    atomic.LoadInt64(&m.FTSShortCircuits),
		"avg_latency_ms":        avgLatencyMs,
		"avg_vector_latency_ms": avgVectorLatencyMs,
		"avg_filter_latency_ms": avgFilterLatencyMs,
	}
}

// ApplyCompositeScoring re-ranks observations using multi-signal scoring.
// Formula: score = similarity × recencyDecay × typeWeight × max(importance, 0.3) × sourceBoost × feedbackBoost
// This ensures that recent, high-importance decisions rank above old generic discoveries.
// Explicit saves (store_memory) never decay and receive a source boost.
func ApplyCompositeScoring(observations []*models.Observation, similarityScores map[int64]float64) {
	now := time.Now()

	// Type weights: behavioral rules and decisions have highest impact.
	typeWeights := map[models.ObservationType]float64{
		"guidance":  1.8, // Behavioral rules — highest value (user corrections, workflow preferences)
		"decision":  1.4,
		"bugfix":    1.3,
		"feature":   1.2,
		"pattern":   1.2,
		"refactor":  0.9,
		"discovery": 0.8,
		"change":    0.7,
	}

	// Per-type half-life in days. Longer = slower decay = more persistent.
	// Guidance rules are near-permanent — they represent user preferences that rarely change.
	typeHalfLife := map[models.ObservationType]float64{
		"guidance":  365, // Behavioral rules — near-permanent (user preferences evolve slowly)
		"decision":  30,
		"pattern":   30,
		"bugfix":    14,
		"feature":   14,
		"discovery": 7,
		"change":    7,
		"refactor":  7,
	}

	for _, obs := range observations {
		sim := similarityScores[obs.ID]
		if sim == 0 {
			sim = 0.5 // default if no similarity score
		}

		// Recency decay: per-type half-life; manual saves (store_memory) never decay
		var recency float64
		if obs.SourceType == models.SourceManual {
			// Explicit saves are permanent until suppressed — no decay
			recency = 1.0
		} else {
			halfLife := 7.0 // default half-life in days for unknown types
			if hl, ok := typeHalfLife[obs.Type]; ok {
				halfLife = hl
			}
			ageDays := now.Sub(time.Unix(obs.CreatedAtEpoch/1000, 0)).Hours() / 24.0
			recency = math.Pow(0.5, ageDays/halfLife)
			// Floor at 0.05 so old but very important observations don't disappear
			if recency < 0.05 {
				recency = 0.05
			}
		}

		// Type weight
		tw := 1.0
		if w, ok := typeWeights[obs.Type]; ok {
			tw = w
		}

		// Importance (floor at 0.3 so unscored observations aren't penalized to zero)
		imp := obs.ImportanceScore
		if imp < 0.3 {
			imp = 0.3
		}

		// Source type boost: explicit saves and behavioral rules are higher value
		sourceBoost := 1.0
		if obs.SourceType == models.SourceManual {
			sourceBoost = 1.5
		}
		// Guidance observations from LLM extraction also get a boost
		if obs.Type == "guidance" && sourceBoost < 1.3 {
			sourceBoost = 1.3
		}

		// User feedback boost: +1 = useful, -1 = not useful
		feedbackBoost := 1.0 + float64(obs.UserFeedback)*0.1
		if feedbackBoost < 0.5 {
			feedbackBoost = 0.5
		}
		if feedbackBoost > 2.0 {
			feedbackBoost = 2.0
		}

		// Composite score replaces raw similarity
		compositeScore := sim * recency * tw * imp * sourceBoost * feedbackBoost
		similarityScores[obs.ID] = compositeScore
	}
}

// ApplySessionBoost multiplies composite scores for observations that belong to recently active sessions.
// recentSessionIDs is a map[sdk_session_id]bool built once per search call (see ObservationStore.GetRecentSessionIDs).
// boostFactor should be > 1.0 (e.g. 1.3).
func ApplySessionBoost(observations []*models.Observation, scores map[int64]float64, recentSessionIDs map[string]bool, boostFactor float64) {
	if len(recentSessionIDs) == 0 || boostFactor <= 1.0 {
		return
	}
	for _, obs := range observations {
		if obs.SDKSessionID != "" && recentSessionIDs[obs.SDKSessionID] {
			if current, exists := scores[obs.ID]; exists {
				scores[obs.ID] = current * boostFactor
			}
		}
	}
}

// ApplyDiversityPenalty adjusts scores based on injection diversity.
// High diversity (observation injected across many projects) = generic = penalty.
// Scope=global observations are exempt (they are intentionally cross-project).
// Only scope=project observations with diversity > 0.5 are penalized.
// Penalty formula: score *= 1.0 - (diversity - 0.5) * 0.4, floored at multiplier 0.8.
func ApplyDiversityPenalty(observations []*models.Observation, scores map[int64]float64, diversityScores map[int64]float64) {
	if len(diversityScores) == 0 {
		return
	}
	for _, obs := range observations {
		// Exempt global-scope observations
		if obs.Scope == models.ScopeGlobal {
			continue
		}
		diversity, ok := diversityScores[obs.ID]
		if !ok || diversity <= 0.5 {
			continue
		}
		// Apply penalty: the more generic (high diversity), the lower the score.
		// Cap multiplier floor at 0.8 to avoid over-penalizing.
		multiplier := 1.0 - (diversity-0.5)*0.4
		if multiplier < 0.8 {
			multiplier = 0.8
		}
		if current, exists := scores[obs.ID]; exists {
			scores[obs.ID] = current * multiplier
		}
	}
}

// Manager provides unified search across PostgreSQL and pgvector.
type Manager struct {
	ctx                  context.Context
	searchGroup          singleflight.Group
	cancel               context.CancelFunc
	vectorClient         vector.Client
	metrics              *SearchMetrics
	promptStore          *gorm.PromptStore
	observationStore     *gorm.ObservationStore
	summaryStore         *gorm.SummaryStore
	graphStore           graphpkg.GraphStore
	documentStore        *gorm.DocumentStore
	embedSvc             *embedding.Service
	projectSettingsStore *gorm.ProjectSettingsStore
	resultCache          map[string]*cachedResult
	queryFrequency       map[string]*queryFrequencyInfo
	cacheTTL             time.Duration
	cacheMaxSize         int
	resultCacheMu        sync.RWMutex
	queryFrequencyMu     sync.RWMutex
}

// queryFrequencyInfo tracks how often a query is used.
type queryFrequencyInfo struct {
	lastUsed   time.Time
	lastCached time.Time
	params     SearchParams
	count      int64
}

// cachedResult stores a cached search result with expiry.
type cachedResult struct {
	result    *UnifiedSearchResult
	expiresAt time.Time
}

// NewManager creates a new search manager.
func NewManager(
	observationStore *gorm.ObservationStore,
	summaryStore *gorm.SummaryStore,
	promptStore *gorm.PromptStore,
	vectorClient vector.Client,
) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		observationStore: observationStore,
		summaryStore:     summaryStore,
		promptStore:      promptStore,
		vectorClient:     vectorClient,
		metrics:          &SearchMetrics{latencyHistogram: make([]int64, 0, latencyHistogramCap)},
		ctx:              ctx,
		cancel:           cancel,
		resultCache:      make(map[string]*cachedResult),
		cacheTTL:         defaultCacheTTL,
		cacheMaxSize:     defaultCacheMaxSize,
		queryFrequency:   make(map[string]*queryFrequencyInfo),
	}
	// Start cache cleanup goroutine
	go m.cleanupCacheLoop()
	// Start cache warming goroutine
	go m.cacheWarmingLoop()
	return m
}

// SetGraphStore sets the graph store for graph-augmented search expansion.
func (m *Manager) SetGraphStore(gs graphpkg.GraphStore) {
	m.graphStore = gs
}

// SetDocumentStore sets the document store and embedding service for document search.
func (m *Manager) SetDocumentStore(ds *gorm.DocumentStore, es *embedding.Service) {
	m.documentStore = ds
	m.embedSvc = es
}

// SetProjectSettingsStore sets the project settings store for per-project adaptive thresholds.
func (m *Manager) SetProjectSettingsStore(ps *gorm.ProjectSettingsStore) {
	m.projectSettingsStore = ps
}

// GetProjectThreshold returns the per-project relevance threshold.
// Falls back to globalDefault if no project-specific setting exists.
func (m *Manager) GetProjectThreshold(ctx context.Context, project string, globalDefault float64) float64 {
	if m.projectSettingsStore == nil {
		return globalDefault
	}
	threshold, err := m.projectSettingsStore.GetThreshold(ctx, project)
	if err != nil {
		return globalDefault
	}
	// If the stored threshold equals the default (0.3), honor globalDefault
	// in case the operator configured a higher global threshold.
	if threshold == 0.3 {
		return globalDefault
	}
	return threshold
}

// Close stops background goroutines and cleans up resources.
func (m *Manager) Close() {
	if m.cancel != nil {
		m.cancel()
	}
}

// cleanupCacheLoop periodically removes expired cache entries.
func (m *Manager) cleanupCacheLoop() {
	ticker := time.NewTicker(cacheCleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.cleanupExpiredCache()
		}
	}
}

// cleanupExpiredCache removes expired entries from the cache.
func (m *Manager) cleanupExpiredCache() {
	m.resultCacheMu.Lock()
	defer m.resultCacheMu.Unlock()

	now := time.Now()
	for key, cached := range m.resultCache {
		if now.After(cached.expiresAt) {
			delete(m.resultCache, key)
		}
	}
}

// cacheWarmingLoop periodically warms the cache for frequently used queries.
func (m *Manager) cacheWarmingLoop() {
	// Wait a bit before starting to allow system to stabilize
	select {
	case <-m.ctx.Done():
		return
	case <-time.After(cacheWarmingInitDelay):
	}

	warmingTicker := time.NewTicker(cacheWarmingInterval)
	cleanupTicker := time.NewTicker(frequencyCleanupInterval)
	defer warmingTicker.Stop()
	defer cleanupTicker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-warmingTicker.C:
			m.warmFrequentQueries()
		case <-cleanupTicker.C:
			m.cleanupStaleFrequencyEntries()
		}
	}
}

// cleanupStaleFrequencyEntries removes query frequency entries older than staleQueryThreshold.
// This prevents memory bloat from queries that haven't been used in a long time.
func (m *Manager) cleanupStaleFrequencyEntries() {
	m.queryFrequencyMu.Lock()
	now := time.Now()
	var keysToDelete []string
	for k, v := range m.queryFrequency {
		if now.Sub(v.lastUsed) > staleQueryThreshold {
			keysToDelete = append(keysToDelete, k)
		}
	}
	for _, k := range keysToDelete {
		delete(m.queryFrequency, k)
	}
	m.queryFrequencyMu.Unlock()

	if len(keysToDelete) > 0 {
		log.Debug().Int("removed", len(keysToDelete)).Msg("Cleaned up stale query frequency entries")
	}
}

// warmFrequentQueries pre-executes frequently used queries to warm the cache.
func (m *Manager) warmFrequentQueries() {
	m.queryFrequencyMu.RLock()
	// Find top N most frequent queries that aren't recently cached
	type queryScore struct {
		info  *queryFrequencyInfo
		key   string
		score float64
	}
	candidates := make([]queryScore, 0, len(m.queryFrequency))
	now := time.Now()

	for key, info := range m.queryFrequency {
		// Only consider queries used recently
		if now.Sub(info.lastUsed) > recentQueryWindow {
			continue
		}
		// Only warm if not recently cached (cache about to expire or already expired)
		timeSinceLastCache := now.Sub(info.lastCached)
		if timeSinceLastCache < m.cacheTTL/2 {
			continue
		}

		// Score: frequency * recency factor
		recencyFactor := 1.0 - (now.Sub(info.lastUsed).Seconds() / recentQueryWindow.Seconds())
		if recencyFactor < minRecencyFactor {
			recencyFactor = minRecencyFactor
		}
		score := float64(info.count) * recencyFactor

		candidates = append(candidates, queryScore{key: key, info: info, score: score})
	}
	m.queryFrequencyMu.RUnlock()

	// Sort by score descending using O(n log n) algorithm
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})

	// Warm top queries
	warmCount := min(warmingBatchSize, len(candidates))
	for i := range warmCount {
		candidate := candidates[i]
		ctx, cancel := context.WithTimeout(context.Background(), warmingQueryTimeout)
		result, err := m.executeSearch(ctx, candidate.info.params)
		cancel()

		if err == nil && result != nil {
			result = filterCredentials(result)
			cacheKey := m.getCacheKey(candidate.info.params)
			m.putInCache(cacheKey, result)

			// Update last cached time
			m.queryFrequencyMu.Lock()
			if info, ok := m.queryFrequency[candidate.key]; ok {
				info.lastCached = time.Now()
			}
			m.queryFrequencyMu.Unlock()

			log.Debug().
				Str("query", truncate(candidate.info.params.Query, warmingLogTruncateLen)).
				Float64("score", candidate.score).
				Msg("Cache warmed for frequent query")
		}
	}
}

// trackQueryFrequency records query usage for cache warming decisions.
func (m *Manager) trackQueryFrequency(params SearchParams) {
	key := m.getCacheKey(params)

	m.queryFrequencyMu.Lock()

	if info, ok := m.queryFrequency[key]; ok {
		info.count++
		info.lastUsed = time.Now()
		m.queryFrequencyMu.Unlock()
		return // Fast path: no eviction needed
	}

	m.queryFrequency[key] = &queryFrequencyInfo{
		params:   params,
		count:    1,
		lastUsed: time.Now(),
	}

	// Limit frequency map size to prevent memory bloat
	mapLen := len(m.queryFrequency)
	if mapLen <= maxFrequencyEntries {
		m.queryFrequencyMu.Unlock()
		return // Fast path: no eviction needed
	}

	// Collect keys and times for eviction (still under lock, but fast)
	type entry struct {
		lastUsed time.Time
		key      string
	}
	entries := make([]entry, 0, mapLen)
	for k, v := range m.queryFrequency {
		entries = append(entries, entry{key: k, lastUsed: v.lastUsed})
	}
	m.queryFrequencyMu.Unlock()

	// Sort outside lock to reduce contention (O(n log n))
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].lastUsed.Before(entries[j].lastUsed)
	})

	// Collect keys to delete
	deleteCount := min(frequencyEvictionBatch, len(entries))
	keysToDelete := make([]string, deleteCount)
	for i := range deleteCount {
		keysToDelete[i] = entries[i].key
	}

	// Re-acquire lock only for deletion (brief critical section)
	m.queryFrequencyMu.Lock()
	for _, k := range keysToDelete {
		delete(m.queryFrequency, k)
	}
	m.queryFrequencyMu.Unlock()
}

// RecentQuery represents a recently executed search query.
type RecentQuery struct {
	LastUsed time.Time `json:"last_used"`
	Query    string    `json:"query"`
	Project  string    `json:"project,omitempty"`
	Type     string    `json:"type,omitempty"`
	Count    int64     `json:"count"`
}

// GetRecentQueries returns the most recent search queries, sorted by last used time.
func (m *Manager) GetRecentQueries(limit int) []RecentQuery {
	if limit <= 0 {
		limit = defaultQueryLimit
	}
	if limit > maxQueryLimit {
		limit = maxQueryLimit
	}

	m.queryFrequencyMu.RLock()
	defer m.queryFrequencyMu.RUnlock()

	// Collect all queries
	queries := make([]RecentQuery, 0, len(m.queryFrequency))
	for _, info := range m.queryFrequency {
		queries = append(queries, RecentQuery{
			Query:    info.params.Query,
			Project:  info.params.Project,
			Type:     info.params.Type,
			Count:    info.count,
			LastUsed: info.lastUsed,
		})
	}

	// Sort by last used (most recent first)
	sort.Slice(queries, func(i, j int) bool {
		return queries[i].LastUsed.After(queries[j].LastUsed)
	})

	// Limit results
	if len(queries) > limit {
		queries = queries[:limit]
	}

	return queries
}

// GetFrequentQueries returns the most frequently used search queries.
func (m *Manager) GetFrequentQueries(limit int) []RecentQuery {
	if limit <= 0 {
		limit = defaultQueryLimit
	}
	if limit > maxQueryLimit {
		limit = maxQueryLimit
	}

	m.queryFrequencyMu.RLock()
	defer m.queryFrequencyMu.RUnlock()

	// Only include queries used recently
	now := time.Now()
	queries := make([]RecentQuery, 0, len(m.queryFrequency))
	for _, info := range m.queryFrequency {
		if now.Sub(info.lastUsed) > recentQueryWindow {
			continue
		}
		queries = append(queries, RecentQuery{
			Query:    info.params.Query,
			Project:  info.params.Project,
			Type:     info.params.Type,
			Count:    info.count,
			LastUsed: info.lastUsed,
		})
	}

	// Sort by count (highest first)
	sort.Slice(queries, func(i, j int) bool {
		return queries[i].Count > queries[j].Count
	})

	// Limit results
	if len(queries) > limit {
		queries = queries[:limit]
	}

	return queries
}

// normalizeQuery normalizes a search query for consistent cache keys.
// Converts to lowercase, trims whitespace, and collapses multiple spaces.
// Uses pre-compiled regex for O(n) performance instead of O(n*m) loop.
func normalizeQuery(query string) string {
	// Lowercase for case-insensitive matching
	query = strings.ToLower(query)
	// Collapse multiple whitespace into single space using pre-compiled regex
	query = multiSpaceRegex.ReplaceAllString(query, " ")
	// Trim leading/trailing whitespace (after collapsing)
	return strings.TrimSpace(query)
}

// getCacheKey generates a cache key from search params.
// Uses direct string concatenation instead of JSON marshal for better performance.
// Queries are normalized for consistent cache hits across whitespace variations.
func (m *Manager) getCacheKey(params SearchParams) string {
	// Normalize query for consistent cache keys
	normalizedQuery := normalizeQuery(params.Query)

	// Hash directly without intermediate string allocation.
	// FNV-64a is fast and collision-safe for cache keys.
	h := fnv.New64a()

	// Write each field directly to the hasher with separators
	h.Write([]byte(normalizedQuery))
	h.Write([]byte{'|'})
	h.Write([]byte(params.Type))
	h.Write([]byte{'|'})
	h.Write([]byte(params.Project))
	h.Write([]byte{'|'})
	h.Write([]byte(params.ObsType))
	h.Write([]byte{'|'})
	h.Write([]byte(params.Concepts))
	h.Write([]byte{'|'})
	h.Write([]byte(params.Files))
	h.Write([]byte{'|'})
	h.Write([]byte(strconv.FormatInt(params.DateStart, 10)))
	h.Write([]byte{'|'})
	h.Write([]byte(strconv.FormatInt(params.DateEnd, 10)))
	h.Write([]byte{'|'})
	h.Write([]byte(params.OrderBy))
	h.Write([]byte{'|'})
	h.Write([]byte(strconv.Itoa(params.Limit)))
	h.Write([]byte{'|'})
	h.Write([]byte(strconv.Itoa(params.Offset)))
	h.Write([]byte{'|'})
	h.Write([]byte(params.Format))
	h.Write([]byte{'|'})
	h.Write([]byte(params.Scope))
	h.Write([]byte{'|'})
	if params.IncludeGlobal {
		h.Write([]byte{'1'})
	} else {
		h.Write([]byte{'0'})
	}
	h.Write([]byte{'|'})
	if params.ExcludeSuperseded {
		h.Write([]byte{'1'})
	} else {
		h.Write([]byte{'0'})
	}

	return strconv.FormatUint(h.Sum64(), 36) // Base36 for compact representation
}

// getFromCache retrieves a result from cache if valid.
func (m *Manager) getFromCache(key string) (*UnifiedSearchResult, bool) {
	m.resultCacheMu.RLock()
	defer m.resultCacheMu.RUnlock()

	if cached, ok := m.resultCache[key]; ok {
		if time.Now().Before(cached.expiresAt) {
			atomic.AddInt64(&m.metrics.CacheHits, 1)
			return cached.result, true
		}
	}
	return nil, false
}

// putInCache stores a result in the cache.
// Optimized to skip expensive scans when cache is below capacity threshold.
func (m *Manager) putInCache(key string, result *UnifiedSearchResult) {
	m.resultCacheMu.Lock()
	defer m.resultCacheMu.Unlock()

	now := time.Now()
	cacheLen := len(m.resultCache)

	// Only scan for expired entries when at threshold+ capacity (amortized cleanup)
	evictionThreshold := (m.cacheMaxSize * cacheEvictionThreshold) / 100
	if cacheLen >= evictionThreshold {
		// Evict expired entries
		for k, v := range m.resultCache {
			if now.After(v.expiresAt) {
				delete(m.resultCache, k)
			}
		}
		cacheLen = len(m.resultCache) // Update after eviction
	}

	// If still at capacity after removing expired, use simple FIFO-style eviction
	// Go map iteration order is random, which provides good cache behavior
	if cacheLen >= m.cacheMaxSize {
		// Evict percentage using random-order iteration (O(n) single pass)
		evictCount := max(m.cacheMaxSize*cacheEvictionPercent/100, 1)
		evicted := 0
		for k := range m.resultCache {
			delete(m.resultCache, k)
			evicted++
			if evicted >= evictCount {
				break
			}
		}
	}

	m.resultCache[key] = &cachedResult{
		result:    result,
		expiresAt: now.Add(m.cacheTTL),
	}
}

// Metrics returns the search metrics for monitoring.
func (m *Manager) Metrics() *SearchMetrics {
	return m.metrics
}

// CacheStats returns current cache statistics.
func (m *Manager) CacheStats() map[string]any {
	m.resultCacheMu.RLock()
	cacheSize := len(m.resultCache)
	m.resultCacheMu.RUnlock()

	return map[string]any{
		"size":     cacheSize,
		"max_size": m.cacheMaxSize,
		"ttl_sec":  m.cacheTTL.Seconds(),
	}
}

// ClearCache clears the result cache. Useful for testing or after data changes.
func (m *Manager) ClearCache() {
	m.resultCacheMu.Lock()
	m.resultCache = make(map[string]*cachedResult)
	m.resultCacheMu.Unlock()
}

// SearchParams contains parameters for unified search.
type SearchParams struct {
	Format            string
	Type              string
	Project           string
	ObsType           string
	Concepts          string
	Files             string
	Query             string
	Scope             string
	OrderBy           string
	DateStart         int64
	Offset            int
	Limit             int
	DateEnd           int64
	IncludeGlobal     bool
	ExcludeSuperseded bool
}

// SearchResult represents a unified search result.
// Field order optimized for memory alignment (fieldalignment).
type SearchResult struct {
	Metadata  map[string]any `json:"metadata,omitempty"`
	Type      string         `json:"type"`
	Title     string         `json:"title,omitempty"`
	Content   string         `json:"content,omitempty"`
	Project   string         `json:"project"`
	Scope     string         `json:"scope,omitempty"`
	ID        int64          `json:"id"`
	CreatedAt int64          `json:"created_at_epoch"`
	Score     float64        `json:"score,omitempty"`
}

// UnifiedSearchResult contains the combined search results.
type UnifiedSearchResult struct {
	Query      string         `json:"query,omitempty"`
	Results    []SearchResult `json:"results"`
	TotalCount int            `json:"total_count"`
}

// UnifiedSearch performs a unified search across all document types.
// Uses caching and request coalescing for optimal performance.
func (m *Manager) UnifiedSearch(ctx context.Context, params SearchParams) (*UnifiedSearchResult, error) {
	start := time.Now()
	defer func() {
		latency := time.Since(start).Nanoseconds()
		atomic.AddInt64(&m.metrics.TotalSearches, 1)
		atomic.AddInt64(&m.metrics.TotalLatencyNs, latency)

		// Sample latency for histogram (reservoir sampling)
		m.metrics.histogramMu.Lock()
		if len(m.metrics.latencyHistogram) < latencyHistogramCap {
			m.metrics.latencyHistogram = append(m.metrics.latencyHistogram, latency)
		}
		m.metrics.histogramMu.Unlock()

		// Log slow queries
		if latency > slowQueryThresholdNs {
			log.Warn().
				Str("query", truncate(params.Query, queryLogTruncateLen)).
				Dur("latency", time.Duration(latency)).
				Str("type", params.Type).
				Msg("Slow search query")
		}
	}()

	if params.Limit <= 0 {
		params.Limit = defaultQueryLimit
	}
	if params.Limit > maxQueryLimit {
		params.Limit = maxQueryLimit
	}
	if params.OrderBy == "" {
		params.OrderBy = defaultOrderBy
	}

	// Check cache first
	cacheKey := m.getCacheKey(params)
	if cached, ok := m.getFromCache(cacheKey); ok {
		return cached, nil
	}

	// Use singleflight to coalesce concurrent identical requests.
	// DoChan + detached context prevents a cancelled caller from poisoning
	// the shared execution for all coalesced waiters.
	ch := m.searchGroup.DoChan(cacheKey, func() (any, error) {
		execCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return m.executeSearch(execCtx, params)
	})

	var searchResult *UnifiedSearchResult
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}
		searchResult = res.Val.(*UnifiedSearchResult)
	}

	// Never return credential observations in search results (leak prevention).
	// Return a new struct to avoid mutating the shared singleflight result.
	filtered := filterCredentials(searchResult)

	// Cache the filtered result
	m.putInCache(cacheKey, filtered)

	// Track query frequency for cache warming
	m.trackQueryFrequency(params)

	return filtered, nil
}

// filterCredentials removes credential observations from search results.
// Credentials are only accessible via the dedicated get_credential MCP tool.
// Returns a new *UnifiedSearchResult to avoid mutating the shared singleflight result,
// which may be concurrently read by other waiters.
func filterCredentials(result *UnifiedSearchResult) *UnifiedSearchResult {
	if result == nil {
		return nil
	}
	filtered := make([]SearchResult, 0, len(result.Results))
	for _, r := range result.Results {
		if r.Type == "observation" {
			if obsType, _ := r.Metadata["obs_type"].(string); obsType == string(models.ObsTypeCredential) {
				continue
			}
		} else if r.Type == "credential" {
			continue
		}
		filtered = append(filtered, r)
	}
	out := *result // shallow copy preserves all metadata fields
	out.Results = filtered
	out.TotalCount = len(filtered)
	return &out
}

// executeSearch performs the actual search without caching/coalescing.
func (m *Manager) executeSearch(ctx context.Context, params SearchParams) (*UnifiedSearchResult, error) {
	// Document-only search when type="documents".
	if params.Type == "documents" {
		return m.documentSearch(ctx, params)
	}

	// Use hybrid search (FTS + vector with RRF fusion) when a query and vector client are available.
	if params.Query != "" && m.vectorClient != nil && m.vectorClient.IsConnected() {
		result, err := m.hybridSearch(ctx, params)
		if err != nil {
			return nil, err
		}
		// Append document results for default (untyped) searches.
		if params.Type == "" {
			m.appendDocumentResults(ctx, params, result)
		}
		return result, nil
	}

	// Fall back to structured filter search when no query or vector unavailable.
	result, err := m.filterSearch(ctx, params)
	if err != nil {
		return nil, err
	}
	if params.Type == "" {
		m.appendDocumentResults(ctx, params, result)
	}
	return result, nil
}

// documentSearch performs a vector-only search across document chunks.
func (m *Manager) documentSearch(ctx context.Context, params SearchParams) (*UnifiedSearchResult, error) {
	if m.documentStore == nil || m.embedSvc == nil || params.Query == "" {
		return &UnifiedSearchResult{Query: params.Query}, nil
	}

	queryEmb, err := m.embedSvc.Embed(params.Query)
	if err != nil {
		log.Warn().Err(err).Msg("Document search: embedding failed")
		return &UnifiedSearchResult{Query: params.Query}, nil
	}

	limit := params.Limit
	if limit <= 0 {
		limit = 10
	}

	chunks, err := m.documentStore.SearchChunks(ctx, queryEmb, "", limit)
	if err != nil {
		log.Warn().Err(err).Msg("Document search: query failed")
		return &UnifiedSearchResult{Query: params.Query}, nil
	}

	results := make([]SearchResult, 0, len(chunks))
	for _, c := range chunks {
		results = append(results, SearchResult{
			Type:    "document",
			Title:   c.Hash[:12] + "#" + strconv.Itoa(c.Seq),
			Content: c.Text,
		})
	}

	return &UnifiedSearchResult{
		Query:      params.Query,
		Results:    results,
		TotalCount: len(results),
	}, nil
}

// appendDocumentResults appends document chunk results to an existing search result.
func (m *Manager) appendDocumentResults(ctx context.Context, params SearchParams, result *UnifiedSearchResult) {
	if m.documentStore == nil || m.embedSvc == nil || params.Query == "" {
		return
	}

	docResult, err := m.documentSearch(ctx, SearchParams{
		Query: params.Query,
		Limit: 5, // Limit document results in mixed search
	})
	if err != nil || len(docResult.Results) == 0 {
		return
	}

	result.Results = append(result.Results, docResult.Results...)
	result.TotalCount += docResult.TotalCount
}

// hybridSearch combines FTS (tsvector) and pgvector results using RRF fusion.
func (m *Manager) hybridSearch(ctx context.Context, params SearchParams) (*UnifiedSearchResult, error) {
	start := time.Now()
	defer func() {
		latency := time.Since(start).Nanoseconds()
		atomic.AddInt64(&m.metrics.VectorSearches, 1)
		atomic.AddInt64(&m.metrics.VectorLatencyNs, latency)
	}()

	// --- FTS path (observations only) ---
	var ftsList []ScoredID
	// ftsResultsCache holds FTS results for reuse as vector-error fallback in RRF fusion.
	var ftsResultsCache []gorm.ScoredObservation
	if m.observationStore != nil && (params.Type == "" || params.Type == "observations") {
		ftsResults, err := m.observationStore.SearchObservationsFTSScored(ctx, params.Query, params.Project, params.Limit*2)
		if err == nil && len(ftsResults) > 0 {
			ftsResultsCache = ftsResults
			ftsList = make([]ScoredID, len(ftsResults))
			for i, r := range ftsResults {
				ftsList[i] = ScoredID{
					ID:      r.Observation.ID,
					DocType: "observation",
					Score:   BM25Normalize(r.Score),
				}
			}
		}
	}

	// --- Strong-signal short-circuit ---
	// If BM25 top score >= 0.85 AND gap to #2 >= 0.15, FTS alone is high confidence.
	// Skip the expensive vector search entirely for immediate latency win.
	if len(ftsList) >= 1 && ftsList[0].Score >= 0.85 {
		gap := ftsList[0].Score
		if len(ftsList) >= 2 {
			gap = ftsList[0].Score - ftsList[1].Score
		}
		if gap >= 0.15 {
			log.Debug().
				Float64("top_score", ftsList[0].Score).
				Float64("gap", gap).
				Str("query", truncate(params.Query, queryLogTruncateLen)).
				Msg("BM25 short-circuit: skipping vector search")
			atomic.AddInt64(&m.metrics.FTSShortCircuits, 1)
			return m.buildResultFromFTS(ftsResultsCache, params)
		}
	}

	// --- Vector path ---
	var docType vector.DocType
	switch params.Type {
	case "observations":
		docType = vector.DocTypeObservation
	case "sessions":
		docType = vector.DocTypeSessionSummary
	case "prompts":
		docType = vector.DocTypeUserPrompt
	}
	var where vector.WhereFilter
	switch params.Scope {
	case "global":
		where = vector.BuildWhereFilter(docType, "", true)
	case "project":
		where = vector.BuildWhereFilter(docType, params.Project, false)
	default:
		where = vector.BuildWhereFilter(docType, params.Project, params.IncludeGlobal)
	}

	vectorResults, err := m.vectorClient.Query(ctx, params.Query, params.Limit*2, where)
	if err != nil {
		atomic.AddInt64(&m.metrics.SearchErrors, 1)
		if len(ftsList) > 0 {
			return m.buildResultFromFTS(ftsResultsCache, params)
		}
		return m.filterSearch(ctx, params)
	}

	// Build vector scored list.
	vectorList := make([]ScoredID, 0, len(vectorResults))
	for _, r := range vectorResults {
		var id int64
		if sid, ok := r.Metadata["sqlite_id"].(float64); ok {
			id = int64(sid)
		} else if sid, ok := r.Metadata["sqlite_id"].(int64); ok {
			id = sid
		}
		if id == 0 {
			continue
		}
		dt := "observation"
		if docType != "" {
			switch docType {
			case vector.DocTypeSessionSummary:
				dt = "session"
			case vector.DocTypeUserPrompt:
				dt = "prompt"
			}
		} else if dts, ok := r.Metadata["doc_type"].(string); ok {
			switch dts {
			case "session_summary":
				dt = "session"
			case "user_prompt":
				dt = "prompt"
			}
		}
		vectorList = append(vectorList, ScoredID{
			ID:      id,
			DocType: dt,
			Score:   r.Similarity,
		})
	}

	// --- RRF fusion ---
	fused := RRF(ftsList, vectorList)
	if len(fused) > params.Limit {
		fused = fused[:params.Limit]
	}

	// --- Graph expansion (optional) ---
	fused = m.expandViaGraph(ctx, fused, params.Limit)

	// Collect IDs by type.
	var obsIDs, summaryIDs, promptIDs []int64
	for _, item := range fused {
		switch item.DocType {
		case "observation":
			obsIDs = append(obsIDs, item.ID)
		case "session":
			summaryIDs = append(summaryIDs, item.ID)
		case "prompt":
			promptIDs = append(promptIDs, item.ID)
		}
	}

	// Fetch full records.
	var results []SearchResult

	// Create a map to lookup original RRF scores
	rrfScores := make(map[string]float64)
	for _, item := range fused {
	        key := item.DocType + ":" + strconv.FormatInt(item.ID, 10)
	        rrfScores[key] = item.Score
	}

	if len(obsIDs) > 0 && (params.Type == "" || params.Type == "observations") {
	        obs, err := m.observationStore.GetObservationsByIDs(ctx, obsIDs, params.OrderBy, 0)
	        if err != nil {
	                log.Warn().Err(err).Msg("hybridSearch: failed to fetch observations by IDs")
	        } else {
	                for _, o := range obs {
	                        if params.ExcludeSuperseded && o.IsSuperseded {
	                                continue
	                        }
	                        res := m.observationToResult(o, params.Format)
	                        key := "observation:" + strconv.FormatInt(o.ID, 10)
	                        if score, ok := rrfScores[key]; ok {
	                                res.Score = score
	                        }
	                        results = append(results, res)
	                }
	        }
	}

	if len(summaryIDs) > 0 && (params.Type == "" || params.Type == "sessions") {
	        summaries, err := m.summaryStore.GetSummariesByIDs(ctx, summaryIDs, params.OrderBy, 0)
	        if err != nil {
	                log.Warn().Err(err).Msg("hybridSearch: failed to fetch summaries by IDs")
	        } else {
	                for _, s := range summaries {
	                        res := m.summaryToResult(s, params.Format)
	                        key := "session:" + strconv.FormatInt(s.ID, 10)
	                        if score, ok := rrfScores[key]; ok {
	                                res.Score = score
	                        }
	                        results = append(results, res)
	                }
	        }
	}

	if len(promptIDs) > 0 && (params.Type == "" || params.Type == "prompts") {
	        prompts, err := m.promptStore.GetPromptsByIDs(ctx, promptIDs, params.OrderBy, 0)
	        if err != nil {
	                log.Warn().Err(err).Msg("hybridSearch: failed to fetch prompts by IDs")
	        } else {
	                for _, p := range prompts {
	                        res := m.promptToResult(p, params.Format)
	                        key := "prompt:" + strconv.FormatInt(p.ID, 10)
	                        if score, ok := rrfScores[key]; ok {
	                                res.Score = score
	                        }
	                        results = append(results, res)
	                }
	        }
	}

	// Sort results by original RRF score to restore ranking
	sort.Slice(results, func(i, j int) bool {
	        return results[i].Score > results[j].Score
	})

	// --- Shadow Scoring Engine ---
	if len(results) > 0 {
	        // Calculate shadow scores
	        type shadowResult struct {
	                originalIdx int
	                shadowScore float64
	                res         SearchResult
	        }

	        shadowRanked := make([]shadowResult, len(results))
	        for i, r := range results {
	                importanceContrib := 0.0
	                if r.Type == "observation" {
	                        if importance, ok := r.Metadata["importance_score"].(float64); ok {
	                                importanceContrib = importance * 0.05
	                        }
	                }
	                shadowRanked[i] = shadowResult{
	                        originalIdx: i,
	                        shadowScore: r.Score + importanceContrib,
	                        res:         r,
	                }
	        }

	        // Sort by shadow score
	        sort.Slice(shadowRanked, func(i, j int) bool {
	                return shadowRanked[i].shadowScore > shadowRanked[j].shadowScore
	        })

	        // Calculate differential (shift)
	        shifts := 0
	        for shadowIdx, sr := range shadowRanked {
	                if shadowIdx != sr.originalIdx {
	                        shifts++
	                }
	        }

	        if shifts > 0 {
	                log.Debug().
	                        Int("shifts", shifts).
	                        Int("total", len(results)).
	                        Msg("Shadow scoring produced differential ranking")
	        }
	}
	// -----------------------------

	return &UnifiedSearchResult{
	        Results:    results,
	        TotalCount: len(results),
	        Query:      params.Query,
	}, nil}

// buildResultFromFTS constructs a UnifiedSearchResult from pre-fetched FTS observations.
func (m *Manager) buildResultFromFTS(ftsResults []gorm.ScoredObservation, params SearchParams) (*UnifiedSearchResult, error) {
	results := make([]SearchResult, 0, len(ftsResults))
	for _, r := range ftsResults {
		if params.ExcludeSuperseded && r.Observation.IsSuperseded {
			continue
		}
		result := m.observationToResult(r.Observation, params.Format)
		result.Score = BM25Normalize(r.Score)
		results = append(results, result)
	}
	if len(results) > params.Limit {
		results = results[:params.Limit]
	}
	return &UnifiedSearchResult{
		Results:    results,
		TotalCount: len(results),
		Query:      params.Query,
	}, nil
}

// filterSearch performs structured filter search via database.
func (m *Manager) filterSearch(ctx context.Context, params SearchParams) (*UnifiedSearchResult, error) {
	start := time.Now()
	defer func() {
		latency := time.Since(start).Nanoseconds()
		atomic.AddInt64(&m.metrics.FilterSearches, 1)
		atomic.AddInt64(&m.metrics.FilterLatencyNs, latency)
	}()

	var results []SearchResult

	// Search observations
	if params.Type == "" || params.Type == "observations" {
		var obs []*models.Observation
		var err error

		// Use active observations (excluding superseded) when requested
		if params.ExcludeSuperseded {
			obs, err = m.observationStore.GetActiveObservations(ctx, params.Project, params.Limit)
		} else {
			obs, err = m.observationStore.GetRecentObservations(ctx, params.Project, params.Limit)
		}

		if err != nil {
			log.Warn().Err(err).Str("project", params.Project).Msg("Failed to fetch observations in filter search")
		} else {
			for _, o := range obs {
				results = append(results, m.observationToResult(o, params.Format))
			}
		}
	}

	// Search summaries
	if params.Type == "" || params.Type == "sessions" {
		summaries, err := m.summaryStore.GetRecentSummaries(ctx, params.Project, params.Limit)
		if err != nil {
			log.Warn().Err(err).Str("project", params.Project).Msg("Failed to fetch summaries in filter search")
		} else {
			for _, s := range summaries {
				results = append(results, m.summaryToResult(s, params.Format))
			}
		}
	}

	// Apply limit
	if len(results) > params.Limit {
		results = results[:params.Limit]
	}

	return &UnifiedSearchResult{
		Results:    results,
		TotalCount: len(results),
	}, nil
}

// Decisions performs a semantic search optimized for finding decisions.
func (m *Manager) Decisions(ctx context.Context, params SearchParams) (*UnifiedSearchResult, error) {
	// Boost query with decision-related keywords
	if params.Query != "" {
		params.Query = params.Query + " decision chose architecture"
	}
	params.Type = "observations"
	return m.UnifiedSearch(ctx, params)
}

// Changes performs a semantic search optimized for finding code changes.
func (m *Manager) Changes(ctx context.Context, params SearchParams) (*UnifiedSearchResult, error) {
	// Boost query with change-related keywords
	if params.Query != "" {
		params.Query = params.Query + " changed modified refactored"
	}
	params.Type = "observations"
	return m.UnifiedSearch(ctx, params)
}

// HowItWorks performs a semantic search optimized for understanding architecture.
func (m *Manager) HowItWorks(ctx context.Context, params SearchParams) (*UnifiedSearchResult, error) {
	// Boost query with architecture-related keywords
	if params.Query != "" {
		params.Query = params.Query + " architecture design pattern implements"
	}
	params.Type = "observations"
	return m.UnifiedSearch(ctx, params)
}

// Helper methods

func (m *Manager) observationToResult(obs *models.Observation, format string) SearchResult {
        result := SearchResult{
                Type:      "observation",
                ID:        obs.ID,
                Project:   obs.Project,
                Scope:     string(obs.Scope),
                CreatedAt: obs.CreatedAtEpoch,
                Metadata: map[string]any{
                        "obs_type":         string(obs.Type),
                        "scope":            string(obs.Scope),
                        "memory_type":      string(obs.MemoryType),
                        "facts":            obs.Facts,
                        "rejected":         obs.Rejected,
                        "importance_score": obs.ImportanceScore,
                },
        }

        if obs.Title.Valid {
                result.Title = obs.Title.String
        }

        if format == "full" && obs.Narrative.Valid {
                result.Content = obs.Narrative.String
        }

        return result
}
func (m *Manager) summaryToResult(summary *models.SessionSummary, format string) SearchResult {
	result := SearchResult{
		Type:      "session",
		ID:        summary.ID,
		Project:   summary.Project,
		CreatedAt: summary.CreatedAtEpoch,
	}

	if summary.Request.Valid {
		result.Title = truncate(summary.Request.String, titleTruncateLen)
	}

	if format == "full" && summary.Learned.Valid {
		result.Content = summary.Learned.String
	}

	return result
}

func (m *Manager) promptToResult(prompt *models.UserPromptWithSession, format string) SearchResult {
	result := SearchResult{
		Type:      "prompt",
		ID:        prompt.ID,
		Project:   prompt.Project,
		CreatedAt: prompt.CreatedAtEpoch,
	}

	result.Title = truncate(prompt.PromptText, titleTruncateLen)

	if format == "full" {
		result.Content = prompt.PromptText
	}

	return result
}

var truncate = strutil.TruncateTrimmed

const (
	graphExpansionTopN    = 5
	graphExpansionMaxHops = 2
	graphExpansionLimit   = 10
	graphExpansionDecay   = 0.7
)

// expandViaGraph takes the top-N fused results and expands them via graph neighbors.
// Neighbor scores decay as 0.7^hops relative to the parent's score.
// Only observation-type results are expanded. New neighbors are merged into the
// fused list, re-sorted by score, and capped at the original limit.
func (m *Manager) expandViaGraph(ctx context.Context, fused []ScoredID, limit int) []ScoredID {
	if m.graphStore == nil {
		return fused
	}
	// Check that graphStore is actually connected (not noop).
	if err := m.graphStore.Ping(ctx); err != nil {
		return fused
	}

	// Build set of existing IDs to avoid duplicates.
	seen := make(map[int64]bool, len(fused))
	for _, f := range fused {
		seen[f.ID] = true
	}

	// Expand top-N observation results.
	topN := graphExpansionTopN
	if topN > len(fused) {
		topN = len(fused)
	}

	var expanded []ScoredID
	for i := 0; i < topN; i++ {
		item := fused[i]
		if item.DocType != "observation" {
			continue
		}
		neighbors, err := m.graphStore.GetNeighbors(ctx, item.ID, graphExpansionMaxHops, graphExpansionLimit)
		if err != nil {
			log.Debug().Err(err).Int64("obs_id", item.ID).Msg("graph expansion: GetNeighbors failed")
			continue
		}
		for _, n := range neighbors {
			if seen[n.ObsID] {
				continue
			}
			seen[n.ObsID] = true
			decayedScore := item.Score * math.Pow(graphExpansionDecay, float64(n.Hops))
			expanded = append(expanded, ScoredID{
				ID:      n.ObsID,
				DocType: "observation",
				Score:   decayedScore,
			})
		}
	}

	if len(expanded) == 0 {
		return fused
	}

	log.Debug().Int("graph_expansions", len(expanded)).Msg("graph expansion added neighbors")

	fused = append(fused, expanded...)
	sort.Slice(fused, func(i, j int) bool {
		return fused[i].Score > fused[j].Score
	})
	if len(fused) > limit {
		fused = fused[:limit]
	}
	return fused
}
