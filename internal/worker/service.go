// Package worker provides the main worker service for engram.
package worker

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog/log"
	"github.com/soheilhy/cmux"
	httpSwagger "github.com/swaggo/http-swagger"
	"github.com/thebtf/engram/internal/chunking"
	gochunking "github.com/thebtf/engram/internal/chunking/golang"
	mdchunking "github.com/thebtf/engram/internal/chunking/markdown"
	"github.com/thebtf/engram/internal/collections"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/consolidation"
	"github.com/thebtf/engram/internal/crypto"
	"github.com/thebtf/engram/internal/db/gorm"
	graphpkg "github.com/thebtf/engram/internal/graph"
	"github.com/thebtf/engram/internal/graph/falkordb"
	"github.com/thebtf/engram/internal/grpcserver"
	"github.com/thebtf/engram/internal/learning"
	"github.com/thebtf/engram/internal/logbuf"
	"github.com/thebtf/engram/internal/maintenance"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/pattern"
	"github.com/thebtf/engram/internal/reranking"
	"github.com/thebtf/engram/internal/scoring"
	"github.com/thebtf/engram/internal/search"
	"github.com/thebtf/engram/internal/search/expansion"
	"github.com/thebtf/engram/internal/sessions"
	"github.com/thebtf/engram/internal/telemetry"
	"github.com/thebtf/engram/internal/update"
	"github.com/thebtf/engram/internal/watcher"
	"github.com/thebtf/engram/internal/worker/projectevents"
	"github.com/thebtf/engram/internal/worker/reaper"
	"github.com/thebtf/engram/internal/worker/sdk"
	"github.com/thebtf/engram/internal/worker/session"
	"github.com/thebtf/engram/internal/worker/sse"
	"github.com/thebtf/engram/pkg/models"
	googlegrpc "google.golang.org/grpc"
)

// Service configuration constants
const (
	// DefaultHTTPTimeout is the default timeout for HTTP requests.
	DefaultHTTPTimeout = 30 * time.Second

	// ReadyPollInterval is how often WaitReady checks initialization status.
	ReadyPollInterval = 50 * time.Millisecond

	// StaleQueueSize is the buffer size for background stale verification.
	StaleQueueSize = 100

	// QueueProcessInterval is how often the background queue processor runs.
	QueueProcessInterval = 2 * time.Second
)

// RetrievalStats tracks observation retrieval metrics.
type RetrievalStats struct {
	TotalRequests      int64 `json:"total_requests"`      // Total retrieval requests (inject + search)
	ObservationsServed int64 `json:"observations_served"` // Observations returned to clients
	VerifiedStale      int64 `json:"verified_stale"`      // Stale observations that passed verification
	DeletedInvalid     int64 `json:"deleted_invalid"`     // Invalid observations deleted
	SearchRequests     int64 `json:"search_requests"`     // Semantic search requests
	ContextInjections  int64 `json:"context_injections"`  // Session-start context injections
	StaleExcluded      int64 `json:"stale_excluded"`      // Observations excluded due to staleness check
	FreshCount         int64 `json:"fresh_count"`         // Observations that passed staleness check
	DuplicatesRemoved  int64 `json:"duplicates_removed"`  // Observations removed by clustering
	LastUpdated        int64 `json:"last_updated"`        // Unix timestamp of last update (atomic)
}

// maxRetrievalStatsProjects limits the number of projects tracked to prevent unbounded memory growth.
const maxRetrievalStatsProjects = 500

// retrievalStatsMaxAge is the maximum age for retrieval stats before cleanup (24 hours).
const retrievalStatsMaxAge = 24 * time.Hour

// maxRecentQueries is the maximum number of recent queries to track.
const maxRecentQueries = 100

// Service is the main worker service orchestrator.
type Service struct {
	startTime              time.Time
	ctx                    context.Context
	initError              error
	server                 *http.Server
	reranker               reranking.Reranker
	observationStore       *gorm.ObservationStore
	summaryStore           *gorm.SummaryStore
	promptStore            *gorm.PromptStore
	conflictStore          *gorm.ConflictStore
	patternStore           *gorm.PatternStore
	relationStore          *gorm.RelationStore
	graphStore             graphpkg.GraphStore
	graphWriter            *graphpkg.AsyncGraphWriter
	patternDetector        *pattern.Detector
	sessionManager         *session.Manager
	sseBroadcaster         *sse.Broadcaster
	processor              *sdk.Processor
	queryExpander          *expansion.Expander
	scoreCalculator        *scoring.Calculator
	recalculator           *scoring.Recalculator
	consolidationScheduler *consolidation.Scheduler
	mcpHealth              *mcp.MCPHealth
	searchMgr              *search.Manager
	collectionRegistry     *collections.Registry
	sessionIdxStore        *sessions.Store
	router                 *chi.Mux
	store                  *gorm.Store
	retrievalStats         map[string]*RetrievalStats
	sessionStore           *gorm.SessionStore
	rawEventStore          *gorm.RawEventStore
	tokenStore             *gorm.TokenStore
	ingestDedup            *deduplicationCache
	cancel                 context.CancelFunc
	cachedObsCounts        map[string]cachedCount
	config                 *config.Config
	staleQueue             chan staleVerifyRequest
	configWatcher          *watcher.Watcher
	updater                *update.Updater
	similarityTelemetry    *telemetry.SimilarityTelemetry
	maintenanceService     *maintenance.Service
	rateLimiter            *PerClientRateLimiter
	tokenAuth              *TokenAuth
	expensiveOpLimiter     *ExpensiveOperationLimiter
	logBuffer              *logbuf.RingBuffer
	backfillTracker        *backfillTracker
	grpcServer             *googlegrpc.Server
	searchQueryLogStore    *gorm.SearchQueryLogStore
	retrievalStatsLogStore *gorm.RetrievalStatsLogStore
	injectionStore         *gorm.InjectionStore
	agentStatsStore        *gorm.AgentStatsStore
	versionStore           *gorm.VersionStore
	llmFilter              *search.LLMFilter
	retrievalHooks         *retrievalHooks
	llmClient              learning.LLMClient
	strategySelector       *learning.StrategySelector
	authHandlers           *AuthHandlers
	version                string
	recentQueriesBuf       [maxRecentQueries]RecentSearchQuery
	wg                     sync.WaitGroup
	recentQueriesLen       int
	recentQueriesHead      int
	statsCacheTTL          time.Duration
	initMu                 sync.RWMutex
	retrievalStatsMu       sync.RWMutex
	recentQueriesMu        sync.RWMutex
	cachedObsCountsMu      sync.RWMutex
	staleQueueOnce         sync.Once
	ready                  atomic.Bool
	vault                  *crypto.Vault
	issueStore             *gorm.IssueStore
	credentialStore        *gorm.CredentialStore
	memoryStore            *gorm.MemoryStore
	behavioralRulesStore   *gorm.BehavioralRulesStore
	vaultOnce              sync.Once
	vaultErr               error
	promptCache            sync.Map // map[int64]promptCacheEntry — last user prompt per session
	eventBus               *projectevents.Bus
	projectReaper          *reaper.Reaper
}

// promptCacheEntry stores a user prompt with a timestamp for eviction.
type promptCacheEntry struct {
	Prompt    string
	Timestamp time.Time
}

// SetLastPrompt stores the most recent user prompt for a session.
func (s *Service) SetLastPrompt(sessionID int64, prompt string) {
	s.promptCache.Store(sessionID, promptCacheEntry{Prompt: prompt, Timestamp: time.Now()})
}

// GetLastPrompt retrieves the most recent user prompt for a session.
func (s *Service) GetLastPrompt(sessionID int64) string {
	if v, ok := s.promptCache.Load(sessionID); ok {
		return v.(promptCacheEntry).Prompt
	}
	return ""
}

// evictStalePrompts removes prompt cache entries older than 2 hours.
func (s *Service) evictStalePrompts() {
	cutoff := time.Now().Add(-2 * time.Hour)
	s.promptCache.Range(func(key, value any) bool {
		if entry, ok := value.(promptCacheEntry); ok && entry.Timestamp.Before(cutoff) {
			s.promptCache.Delete(key)
		}
		return true
	})
}

// cachedCount stores a cached count value with expiration.
type cachedCount struct {
	timestamp time.Time
	count     int
}

// getVault returns the shared Vault singleton, initializing it once on first call.
// All errors are cached — a misconfigured vault fails permanently (no retry).
func (s *Service) getVault() (*crypto.Vault, error) {
	s.vaultOnce.Do(func() {
		s.vault, s.vaultErr = crypto.NewVault(s.config)
	})
	return s.vault, s.vaultErr
}

// staleVerifyRequest represents a request to verify a stale observation in background
type staleVerifyRequest struct {
	cwd           string
	observationID int64
}

// RecentSearchQuery tracks a search query for analytics.
type RecentSearchQuery struct {
	Timestamp time.Time `json:"timestamp"`
	Query     string    `json:"query"`
	Project   string    `json:"project,omitempty"`
	Type      string    `json:"type,omitempty"`
	Results   int       `json:"results"`
}

// setupCallbacks configures callbacks on stores and processors.
func (s *Service) setupCallbacks(
	patternDetector *pattern.Detector,
	observationStore *gorm.ObservationStore,
	processor *sdk.Processor,
	sessionManager *session.Manager,
) {
	// Set vector sync callbacks on processor if available
	if processor != nil && patternDetector != nil {
		processor.SetSyncObservationFunc(func(obs *models.Observation) {
			// Trigger pattern detection for the new observation
			s.wg.Add(1) // Track goroutine for graceful shutdown
			go func(observation *models.Observation) {
				defer s.wg.Done()
				detectCtx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
				defer cancel()
				if result, err := patternDetector.AnalyzeObservation(detectCtx, observation); err != nil {
					// Don't log context canceled errors during shutdown
					if s.ctx.Err() == nil {
						log.Warn().Err(err).Int64("obs_id", observation.ID).Msg("Pattern detection failed")
					}
				} else if result.MatchedPattern != nil {
					log.Debug().
						Int64("pattern_id", result.MatchedPattern.ID).
						Str("pattern_name", result.MatchedPattern.Name).
						Bool("is_new", result.IsNewPattern).
						Msg("Pattern matched for observation")
				}
			}(obs)
		})
	}

	// Set callbacks for session lifecycle events
	if sessionManager != nil {
		sessionManager.SetOnSessionCreated(func(id int64) {
			s.broadcastProcessingStatus()
			s.sseBroadcaster.Broadcast(map[string]any{
				"type":   "session",
				"action": "created",
				"id":     id,
			})
		})
		sessionManager.SetOnSessionDeleted(func(id int64) {
			s.broadcastProcessingStatus()
			s.sseBroadcaster.Broadcast(map[string]any{
				"type":   "session",
				"action": "deleted",
				"id":     id,
			})
		})
	}
}

// NewService creates a new worker service with deferred initialization.
// The service starts immediately with health endpoint available,
// while database and SDK initialization happens in the background.
func NewService(version string, logBuffer *logbuf.RingBuffer) (*Service, error) {
	cfg := config.Get()

	// Create context
	ctx, cancel := context.WithCancel(context.Background())

	// Create router and SSE broadcaster (lightweight, no dependencies)
	router := chi.NewRouter()
	sseBroadcaster := sse.NewBroadcaster()

	// Determine install directory (plugin location)
	homeDir, _ := os.UserHomeDir()
	installDir := fmt.Sprintf("%s/.claude/plugins/marketplaces/engram", homeDir)

	// Create rate limiter with generous limits (100 req/sec, burst of 200)
	// These limits are per-client and allow for intensive CLI usage
	rateLimiter := NewPerClientRateLimiter(100.0, 200)

	tokenAuth, err := NewTokenAuth(config.GetWorkerToken())
	if err != nil {
		cancel()
		return nil, fmt.Errorf("init token auth: %w", err)
	}

	svc := &Service{
		version:            version,
		config:             cfg,
		sseBroadcaster:     sseBroadcaster,
		router:             router,
		ctx:                ctx,
		cancel:             cancel,
		startTime:          time.Now(),
		updater:            update.New(version, installDir),
		retrievalStats:     make(map[string]*RetrievalStats),
		rateLimiter:        rateLimiter,
		tokenAuth:          tokenAuth,
		expensiveOpLimiter: NewExpensiveOperationLimiter(),
		logBuffer:          logBuffer,
		backfillTracker:    newBackfillTracker(),
		cachedObsCounts:    make(map[string]cachedCount),
		statsCacheTTL:      time.Minute, // Cache stats for 1 minute
		ingestDedup:        newDeduplicationCache(5 * time.Minute),
		mcpHealth:          mcp.NewMCPHealth(),
		strategySelector:   learning.NewStrategySelector(cfg.InjectionStrategies, cfg.InjectionStrategyMode, cfg.DefaultStrategy),
		eventBus:           &projectevents.Bus{},
	}

	// Setup middleware and routes (health endpoint works immediately)
	svc.setupMiddleware()
	svc.setupRoutes()

	// Auth startup gate (ADR-0001): validate before starting heavy initialization.
	// Fail fast here so initializeAsync never runs without a token unless explicitly disabled.
	{
		token := config.GetWorkerToken()
		authDisabled := strings.EqualFold(strings.TrimSpace(os.Getenv("ENGRAM_AUTH_DISABLED")), "true")
		if token == "" && !authDisabled {
			cancel()
			return nil, fmt.Errorf("ENGRAM_AUTH_ADMIN_TOKEN (or ENGRAM_API_TOKEN) is not set — set it to secure your engram instance, or set ENGRAM_AUTH_DISABLED=true to explicitly run without authentication (NOT recommended for production)")
		}
	}

	// Start async initialization
	go svc.initializeAsync()

	return svc, nil
}

// createReranker creates a reranker based on the configured provider.
// Returns nil if creation fails (graceful degradation — reranking disabled).
func (s *Service) createReranker() reranking.Reranker {
	provider := s.config.RerankingProvider
	if provider == "" {
		provider = "api"
	}

	alpha := s.config.RerankingAlpha
	if alpha <= 0 || alpha > 1 {
		alpha = 0.7
	}

	switch provider {
	case "api":

		if s.config.RerankingAPIBaseURL == "" {
			log.Warn().Msg("Reranking API URL not set (ENGRAM_RERANKING_API_URL) - reranking disabled")
			return nil
		}

		timeout := time.Duration(s.config.RerankingTimeoutMS) * time.Millisecond
		if timeout <= 0 {
			timeout = 500 * time.Millisecond
		}

		ranker, err := reranking.NewAPIService(reranking.APIConfig{
			BaseURL:   s.config.RerankingAPIBaseURL,
			APIKey:    s.config.RerankingAPIKey,
			Model:     s.config.RerankingAPIModel,
			Alpha:     alpha,
			Timeout:   timeout,
			BatchSize: s.config.RerankingBatchSize,
		})
		if err != nil {
			log.Warn().Err(err).Msg("API reranker creation failed - reranking disabled")
			return nil
		}
		log.Info().
			Str("provider", "api").
			Str("model", s.config.RerankingAPIModel).
			Float64("alpha", alpha).
			Msg("API reranking enabled")
		return ranker

	default:
		log.Warn().Str("provider", provider).Msg("Unknown reranking provider - reranking disabled")
		return nil
	}
}

// createHyDEGenerator creates a HyDE generator if enabled and configured.
// Returns nil if HyDE is disabled or API config is missing (graceful no-op).
func (s *Service) createHyDEGenerator() *expansion.HyDEGenerator {
	if !s.config.HyDEEnabled {
		return nil
	}

	timeout := time.Duration(s.config.HyDETimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 800 * time.Millisecond
	}

	cfg := expansion.HyDEConfig{
		APIURL:    s.config.HyDEAPIURL,
		APIKey:    s.config.HyDEAPIKey,
		Model:     s.config.HyDEModel,
		MaxTokens: s.config.HyDEMaxTokens,
		Timeout:   timeout,
		CacheTTL:  5 * time.Minute,
	}

	gen := expansion.NewHyDEGenerator(cfg)
	log.Info().
		Str("model", cfg.Model).
		Bool("has_api", cfg.APIURL != "" && cfg.APIKey != "").
		Msg("HyDE query expansion enabled")
	return gen
}

// createChunkManager creates a chunking manager with all available language chunkers.
func (s *Service) createChunkManager() *chunking.Manager {
	opts := chunking.DefaultChunkOptions()
	chunkers := []chunking.Chunker{
		mdchunking.NewChunker(opts),
		gochunking.NewChunker(opts),
	}
	return chunking.NewManager(chunkers, opts)
}

// initializeAsync performs heavy initialization in the background.
func (s *Service) initializeAsync() {
	log.Info().Msg("Starting async initialization...")

	// Ensure data directory and settings exist
	if err := config.EnsureAll(); err != nil {
		s.setInitError(fmt.Errorf("ensure data dir: %w", err))
		return
	}

	// Initialize database (this includes migrations - can be slow)
	store, err := gorm.NewStore(gorm.Config{
		DSN:      s.config.DatabaseDSN,
		MaxConns: s.config.DatabaseMaxConns,
	})
	if err != nil {
		s.setInitError(fmt.Errorf("init database: %w", err))
		return
	}

	// Create store wrappers
	sessionStore := gorm.NewSessionStore(store)
	summaryStore := gorm.NewSummaryStore(store)
	promptStore := gorm.NewPromptStore(store, nil)
	conflictStore := gorm.NewConflictStore(store)
	patternStore := gorm.NewPatternStore(store)
	relationStore := gorm.NewRelationStore(store)

	// Initialize optional FalkorDB graph store
	var gs graphpkg.GraphStore = &graphpkg.NoopGraphStore{}
	var gw *graphpkg.AsyncGraphWriter
	cfg := config.Get()
	if cfg.GraphProvider == "falkordb" && cfg.FalkorDBAddr != "" {
		fdb, err := falkordb.NewFalkorDBGraphStore(cfg)
		if err != nil {
			log.Warn().Err(err).Msg("FalkorDB connection failed, falling back to noop graph store")
		} else {
			gs = fdb
			gw = graphpkg.NewAsyncGraphWriter(gs)
			relationStore.SetCallback(gw.Enqueue)
			log.Info().Msg("FalkorDB graph store enabled with async dual-write")
		}
	}

	// Create observation store
	observationStore := gorm.NewObservationStore(store, nil)

	// Create session manager
	sessionManager := session.NewManager(sessionStore)

	// Create reranking service if enabled (operates on text, no embedding needed)
	var reranker reranking.Reranker
	if s.config.RerankingEnabled {
		reranker = s.createReranker()
	}

	// Create SDK processor (optional - requires LLM API or Claude CLI)
	var processor *sdk.Processor
	proc, err := sdk.NewProcessor(observationStore, summaryStore)
	if err != nil {
		log.Warn().Err(err).Msg("SDK processor not available — set ENGRAM_LLM_URL for observation extraction")
	} else {
		processor = proc
		// Set broadcast callback for SSE events
		processor.SetBroadcastFunc(func(event map[string]any) {
			s.sseBroadcaster.Broadcast(event)
		})
		log.Info().Msg("SDK processor initialized")
	}

	// Create token store and wire into auth middleware
	tokenStore := gorm.NewTokenStore(store)

	// Create auth stores for email/password dashboard authentication (T007-T009).
	userStore := gorm.NewUserStore(store.DB)
	invitationStore := gorm.NewInvitationStore(store.DB)
	authSessionStore := gorm.NewAuthSessionStore(store.DB)

	// Create raw event store and ingest deduplication cache
	rawEventStore := gorm.NewRawEventStore(store)

	// Create injection store for closed-loop learning
	injectionStore := gorm.NewInjectionStore(store.GetDB())

	// Create agent stats store for Phase 4 agent-specific effectiveness tracking
	agentStatsStore := gorm.NewAgentStatsStore(store.GetDB())

	// Create version store for Phase 5 APO-lite observation rewrites
	versionStore := gorm.NewVersionStore(store.GetDB())

	// Create issue store for cross-project agent issues
	issueStore := gorm.NewIssueStore(store.GetDB())

	// Create reasoning trace store for System 2 memory (reasoning chains)
	reasoningStore := gorm.NewReasoningTraceStore(store)
	if processor != nil {
		processor.SetReasoningStore(reasoningStore)
	}

	// Create memory + behavioral rules + credential stores for US3 observations split.
	// All three stores are wired here (Commit E — T021).
	memoryStore := gorm.NewMemoryStore(store)
	behavioralRulesStore := gorm.NewBehavioralRulesStore(store)
	credentialStore := gorm.NewCredentialStore(store)

	// Set all the initialized components
	s.initMu.Lock()
	s.store = store
	s.sessionStore = sessionStore
	s.rawEventStore = rawEventStore
	s.injectionStore = injectionStore
	s.issueStore = issueStore
	s.credentialStore = credentialStore
	s.memoryStore = memoryStore
	s.behavioralRulesStore = behavioralRulesStore
	s.agentStatsStore = agentStatsStore
	s.versionStore = versionStore
	s.tokenStore = tokenStore
	s.observationStore = observationStore
	s.summaryStore = summaryStore
	s.promptStore = promptStore
	s.conflictStore = conflictStore
	s.patternStore = patternStore
	s.relationStore = relationStore
	s.graphStore = gs
	s.graphWriter = gw
	s.sessionManager = sessionManager
	s.processor = processor
	if processor != nil {
		processor.SetDedupConfig(cfg.DedupSimilarityThreshold, cfg.DedupWindowSize)
	}
	s.reranker = reranker
	s.initMu.Unlock()

	// Wire token store into auth middleware for client token lookups
	if s.tokenAuth != nil {
		s.tokenAuth.SetTokenStore(tokenStore)
	}

	// Wire email/password auth stores into TokenAuth middleware and create AuthHandlers.
	authHandlersInstance := NewAuthHandlers(userStore, invitationStore, authSessionStore)
	s.initMu.Lock()
	s.authHandlers = authHandlersInstance
	s.initMu.Unlock()
	if s.tokenAuth != nil {
		s.tokenAuth.SetAuthStores(userStore, authSessionStore)
		cfg := config.Get()
		s.tokenAuth.SetAuthentikConfig(cfg.AuthentikEnabled, cfg.AuthentikAutoProvision, cfg.AuthentikTrustedProxies)
	}

	// Start buffered token stats flusher (batches DB writes every 5s)
	s.startTokenStatsFlusher(s.ctx)

	// Background sync: populate FalkorDB from existing PostgreSQL relations.
	if _, ok := gs.(*graphpkg.NoopGraphStore); !ok && gs != nil {
		go s.syncGraphFromRelations()
	}

	// Initialize pattern detector
	patternDetector := pattern.NewDetector(patternStore, observationStore, pattern.DefaultConfig())

	s.initMu.Lock()
	s.patternDetector = patternDetector
	s.initMu.Unlock()

	// Setup callbacks on stores and processors
	s.setupCallbacks(patternDetector, observationStore, processor, sessionManager)

	// Initialize importance scoring system
	scoringConfig := models.DefaultScoringConfig()

	// Apply per-source half-life overrides from config (gstack-insights FR-2)
	scoringConfig.SourceHalfLives[models.SourceManual] = s.config.HalfLifeManual
	scoringConfig.SourceHalfLives[models.SourceUnknown] = s.config.HalfLifeSDK
	scoringConfig.SourceHalfLives[models.SourceBackfill] = s.config.HalfLifeSDK
	scoringConfig.SourceHalfLives[models.SourceTodoWrite] = s.config.HalfLifeSDK
	scoringConfig.SourceHalfLives[models.SourceLLMDerived] = s.config.HalfLifeLLM
	scoringConfig.SourceHalfLives[models.SourceCrossModel] = s.config.HalfLifeCrossModel
	scoringConfig.SourceHalfLives[models.SourceToolVerified] = s.config.HalfLifeAlgorithm * 1.5 // 21d = 14 * 1.5
	scoringConfig.SourceHalfLives[models.SourceToolRead] = s.config.HalfLifeAlgorithm           // 14d
	scoringConfig.SourceHalfLives[models.SourceWebFetch] = s.config.HalfLifeAlgorithm           // 14d
	scoringConfig.SourceHalfLives[models.SourceInstinctImport] = s.config.HalfLifeManual        // 30d

	// Load concept weights from database if available
	if weights, err := observationStore.GetConceptWeights(s.ctx); err == nil && len(weights) > 0 {
		scoringConfig.ConceptWeights = weights
		log.Info().Int("count", len(weights)).Msg("Loaded concept weights from database")
	}

	scoreCalculator := scoring.NewCalculator(scoringConfig)
	recalculator := scoring.NewRecalculator(observationStore, scoreCalculator, log.Logger)

	s.initMu.Lock()
	s.scoreCalculator = scoreCalculator
	s.recalculator = recalculator
	s.initMu.Unlock()

	// Start background recalculator
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		recalculator.Start(s.ctx)
	}()
	log.Info().Msg("Importance scoring system initialized")

	// Start consolidation scheduler
	relevanceCalc := scoring.NewRelevanceCalculator(nil) // default config
	assocEngine := consolidation.NewAssociationEngine(nil, consolidation.DefaultAssociationConfig(), log.Logger)
	schedCfg := consolidation.DefaultSchedulerConfig()
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("ENGRAM_FORGET_ENABLED"))); v == "false" || v == "0" {
		schedCfg.ForgetEnabled = false
	}
	consolidationScheduler := consolidation.NewScheduler(
		relevanceCalc,
		assocEngine,
		observationStore,
		relationStore,
		schedCfg,
		log.Logger,
	)
	s.initMu.Lock()
	s.consolidationScheduler = consolidationScheduler
	s.initMu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		consolidationScheduler.Start(s.ctx)
	}()
	log.Info().Msg("Consolidation scheduler started")

	// Start pattern detector background analysis
	if patternDetector != nil {
		patternDetector.Start()
		log.Info().Msg("Pattern recognition engine started")
	}

	// Initialize Smart GC and maintenance service
	var smartGC *maintenance.SmartGC
	if cfg.SmartGCEnabled {
		smartGC = maintenance.NewSmartGC(
			store, observationStore, nil,
			scoreCalculator, cfg, log.Logger,
		)
	}
	maintenanceSvc := maintenance.NewService(
		store, observationStore, injectionStore, summaryStore, promptStore,
		cfg, s.similarityTelemetry, smartGC, patternStore,
		nil, nil, relationStore, gs,
		sessionStore, agentStatsStore,
		s.llmClient,
		log.Logger,
	)
	maintenanceSvc.OnProgress = func(subtask string, index, total int, status, message string) {
		s.sseBroadcaster.Broadcast(map[string]any{
			"type":    "maintenance_progress",
			"subtask": subtask,
			"index":   index,
			"total":   total,
			"status":  status,
			"message": message,
		})
	}
	maintenanceSvc.OnComplete = func(summary maintenance.CompletionSummary) {
		s.sseBroadcaster.Broadcast(map[string]any{
			"type":          "maintenance_complete",
			"duration_ms":   summary.DurationMs,
			"subtask_count": summary.SubtaskCount,
			"summary": map[string]any{
				"merged":   summary.Merged,
				"archived": summary.Archived,
				"pruned":   summary.Pruned,
			},
		})
	}

	s.initMu.Lock()
	s.maintenanceService = maintenanceSvc
	s.initMu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		maintenanceSvc.Start(s.ctx)
	}()
	log.Info().Bool("smart_gc", cfg.SmartGCEnabled).Msg("Maintenance service started")

	// Periodic prompt cache eviction (Learning Memory v3)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.evictStalePrompts()
			case <-s.ctx.Done():
				return
			}
		}
	}()

	// Initialize collection registry
	collectionRegistry, colErr := collections.Load(config.GetCollectionConfigPath())
	if colErr != nil {
		log.Warn().Err(colErr).Msg("Failed to load collection config, collections disabled")
		collectionRegistry = &collections.Registry{}
	}

	// Initialize session index store (clients push transcripts via REST API)
	sessionIdxStore := sessions.NewStore(store)

	// Initialize search query log store for persistent analytics
	searchQueryLogStore := gorm.NewSearchQueryLogStore(store.GetDB())

	// Initialize retrieval stats log store with batched flush
	retrievalStatsLogStore := gorm.NewRetrievalStatsLogStore(store.GetDB())

	// Initialize shared LLM client (used for pattern insights and LLM filter)
	{
		llmCfg := learning.DefaultOpenAIConfig()
		sharedLLM := learning.NewOpenAIClient(llmCfg)
		if sharedLLM.IsConfigured() {
			s.initMu.Lock()
			s.llmClient = sharedLLM
			s.initMu.Unlock()
			log.Info().Str("model", llmCfg.Model).Msg("LLM client initialized for pattern insights")
		}
	}

	// Initialize LLM filter if enabled
	if cfg.LLMFilterEnabled {
		llmCfg := learning.DefaultOpenAIConfig()
		if cfg.LLMFilterModel != "" {
			llmCfg.Model = cfg.LLMFilterModel
		}
		llmClient := learning.NewOpenAIClient(llmCfg)
		if llmClient.IsConfigured() {
			filterTimeout := time.Duration(cfg.LLMFilterTimeoutMS) * time.Millisecond
			s.initMu.Lock()
			s.llmFilter = search.NewLLMFilter(llmClient, filterTimeout)
			s.initMu.Unlock()
			log.Info().
				Str("model", llmCfg.Model).
				Int("timeout_ms", cfg.LLMFilterTimeoutMS).
				Int("candidates", cfg.LLMFilterCandidates).
				Msg("LLM behavioral relevance filter enabled")
		} else {
			log.Warn().Msg("LLM filter enabled but LLM not configured (set ENGRAM_LLM_URL + ENGRAM_LLM_API_KEY)")
		}
	}

	// Initialize search manager for MCP tools
	searchMgr := search.NewManager(observationStore, summaryStore, promptStore)

	// Initialize MCP server and SSE handler (serves /sse and /message on the worker port)
	// Create document store for collection MCP tools
	documentStore := gorm.NewDocumentStore(store)

	// Create chunking manager for document ingestion
	chunkManager := s.createChunkManager()

	// Create versioned document store for collaborative document MCP tools (migration 051).
	versionedDocumentStore := gorm.NewVersionedDocumentStore(store)

	mcpServer := mcp.NewServer(
		searchMgr,
		s.version,
		observationStore,
		patternStore,
		relationStore,
		sessionStore,
		scoreCalculator,
		recalculator,
		maintenanceSvc,
		collectionRegistry,
		sessionIdxStore,
		consolidationScheduler,
		documentStore,
		chunkManager,
	)
	mcpServer.SetInjectionStore(injectionStore)
	// Wire graph store into MCP server and search manager.
	if s.graphStore != nil {
		mcpServer.SetGraphStore(s.graphStore)
		searchMgr.SetGraphStore(s.graphStore)
	}

	// Wire backfill status into MCP server.
	mcpServer.SetBackfillStatusFunc(func() (any, error) {
		return s.backfillTracker.snapshot(), nil
	})

	// Wire versioned document store into MCP server for collaborative document tools.
	mcpServer.SetVersionedDocumentStore(versionedDocumentStore)

	// Wire reasoning trace store into MCP server for System 2 memory recall.
	mcpServer.SetReasoningStore(reasoningStore)
	mcpServer.SetIssueStore(issueStore)

	// Wire memory + behavioral rules stores (US3 Commit C).
	// These power the new static-entity MCP tools store_rule / list_rules and
	// will be used by Commit E when handleStoreMemory / handleRecall are
	// switched from observations to memories/behavioral_rules.
	mcpServer.SetMemoryStore(memoryStore)
	mcpServer.SetBehavioralRulesStore(behavioralRulesStore)

	// Wire gRPC server: create adapter over mcpServer and register with the server.
	// initMu protects s.grpcServer — the cmux goroutine polls for it.
	adapter := &mcpHandlerAdapter{mcpServer: mcpServer}
	grpcSrv, grpcInternalSrv := grpcserver.New(adapter)
	grpcInternalSrv.SetDB(store.DB)
	grpcInternalSrv.SetBus(s.eventBus)
	s.initMu.Lock()
	s.grpcServer = grpcSrv
	s.initMu.Unlock()

	s.initMu.Lock()
	s.searchMgr = searchMgr
	s.collectionRegistry = collectionRegistry
	s.sessionIdxStore = sessionIdxStore
	s.searchQueryLogStore = searchQueryLogStore
	s.retrievalStatsLogStore = retrievalStatsLogStore
	s.initMu.Unlock()

	// Mark as ready
	s.ready.Store(true)
	log.Info().Msg("Async initialization complete - service ready")

	// Start project reaper (hourly cleanup of hard-expired soft-deleted projects).
	projectReaper := reaper.New(store.DB)
	s.projectReaper = projectReaper
	projectReaper.Start(s.ctx)

	// Start queue processor if SDK processor is available
	if processor != nil {
		s.wg.Add(1)
		go s.processQueue()
	}

	// Start file watchers for auto-recreation on deletion
	s.startWatchers()
}

// startWatchers initializes and starts file watchers for database and config.
func (s *Service) startWatchers() {
	// Database file watcher is not applicable for PostgreSQL (no local file to watch).

	// Watch config file for changes (triggers process exit for restart)
	configPath := config.SettingsPath()
	configWatcher, err := watcher.New(configPath, func() {
		log.Warn().Str("path", configPath).Msg("Config file changed, reloading...")
		s.reloadConfig()
	})
	if err != nil {
		log.Warn().Err(err).Msg("Failed to create config watcher")
	} else {
		s.configWatcher = configWatcher
		if err := configWatcher.Start(); err != nil {
			log.Warn().Err(err).Msg("Failed to start config watcher")
		} else {
			log.Info().Str("path", configPath).Msg("Config file watcher started")
		}
	}
}

// reloadConfig hot-reloads configuration from disk without process restart.
// Uses config.Reload() to atomically swap the global config. Services that
// call config.Get() per-request will pick up new values automatically.
// Structural changes (port, token) log a warning — manual restart needed.
func (s *Service) reloadConfig() {
	_, changed, err := config.Reload()
	if err != nil {
		log.Error().Err(err).Msg("Config reload failed — keeping current config")
		return
	}

	if len(changed) == 0 {
		log.Info().Msg("Config file changed but no values differ")
		return
	}

	log.Info().Strs("changed", changed).Msg("Config hot-reloaded")

	// Broadcast to dashboard
	s.sseBroadcaster.Broadcast(map[string]any{
		"type":    "config_reloaded",
		"message": "Configuration reloaded",
		"changed": changed,
	})
}

// setInitError records an initialization error.
func (s *Service) setInitError(err error) {
	s.initMu.Lock()
	s.initError = err
	s.initMu.Unlock()
	log.Error().Err(err).Msg("Async initialization failed")
}

// GetInitError returns any initialization error.
func (s *Service) GetInitError() error {
	s.initMu.RLock()
	defer s.initMu.RUnlock()
	return s.initError
}

// queueStaleVerification queues a stale observation for background verification.
// This is non-blocking - if the queue is full, the request is dropped.
func (s *Service) queueStaleVerification(observationID int64, cwd string) {
	// Initialize queue on first use
	s.staleQueueOnce.Do(func() {
		s.staleQueue = make(chan staleVerifyRequest, StaleQueueSize)
		s.wg.Add(1)
		go s.processStaleQueue()
	})

	// Non-blocking send - drop if queue is full
	select {
	case s.staleQueue <- staleVerifyRequest{observationID: observationID, cwd: cwd}:
		// Queued
	default:
		// Queue full, drop
		log.Debug().Int64("id", observationID).Msg("Stale verification queue full, dropping")
	}
}

// processStaleQueue processes stale observations in the background.
func (s *Service) processStaleQueue() {
	defer s.wg.Done()

	for {
		select {
		case <-s.ctx.Done():
			return
		case req := <-s.staleQueue:
			s.verifyStaleObservation(req)
		}
	}
}

// verifyStaleObservation verifies a single stale observation in the background.
func (s *Service) verifyStaleObservation(req staleVerifyRequest) {
	// Wait for service to be ready
	if !s.ready.Load() {
		return
	}

	// Get observation from DB
	s.initMu.RLock()
	store := s.observationStore
	processor := s.processor
	s.initMu.RUnlock()

	if store == nil || processor == nil {
		return
	}

	obs, err := store.GetObservationByID(s.ctx, req.observationID)
	if err != nil || obs == nil {
		return
	}

	// Verify with Claude CLI (this is slow but we're in background)
	if !processor.VerifyObservation(s.ctx, obs, req.cwd) {
		// Invalid - delete it
		deleted, err := store.DeleteObservations(s.ctx, []int64{obs.ID})
		if err == nil && deleted > 0 {
			log.Info().
				Int64("id", obs.ID).
				Str("title", obs.Title.String).
				Msg("Background verification: deleted invalid observation")
		}
	} else {
		log.Debug().
			Int64("id", obs.ID).
			Msg("Background verification: observation still valid")
	}
}

// mcpHandlerAdapter wraps mcp.Server to implement grpcserver.MCPHandler.
// It translates gRPC tool-call requests into MCP JSON-RPC requests and back.
type mcpHandlerAdapter struct {
	mcpServer *mcp.Server
}

// HandleToolCall implements grpcserver.MCPHandler.
func (a *mcpHandlerAdapter) HandleToolCall(ctx context.Context, toolName string, argsJSON []byte) ([]byte, bool, error) {
	params := map[string]any{
		"name":      toolName,
		"arguments": json.RawMessage(argsJSON),
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, false, fmt.Errorf("failed to marshal tool call params: %w", err)
	}

	req := &mcp.Request{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/call",
		Params:  json.RawMessage(paramsJSON),
	}

	resp := a.mcpServer.HandleRequest(ctx, req)
	if resp == nil {
		return nil, false, fmt.Errorf("no response from MCP server")
	}
	if resp.Error != nil {
		errJSON, _ := json.Marshal(resp.Error)
		return errJSON, true, nil
	}

	resultJSON, err := json.Marshal(resp.Result)
	if err != nil {
		return nil, false, fmt.Errorf("failed to marshal MCP result: %w", err)
	}
	return resultJSON, false, nil
}

// ToolDefinitions implements grpcserver.MCPHandler.
func (a *mcpHandlerAdapter) ToolDefinitions() []grpcserver.ToolDef {
	tools := a.mcpServer.ListTools()
	defs := make([]grpcserver.ToolDef, len(tools))
	for i, t := range tools {
		schemaJSON, _ := json.Marshal(t.InputSchema)
		defs[i] = grpcserver.ToolDef{
			Name:            t.Name,
			Description:     t.Description,
			InputSchemaJSON: schemaJSON,
		}
	}
	return defs
}

// ServerInfo implements grpcserver.MCPHandler.
func (a *mcpHandlerAdapter) ServerInfo() (string, string) {
	return "engram", a.mcpServer.Version()
}

// setupMiddleware configures HTTP middleware.
func (s *Service) setupMiddleware() {
	// Add request ID first so all subsequent logs can include it
	s.router.Use(RequestID)

	s.router.Use(debugRequestLogger)
	s.router.Use(middleware.Recoverer)
	s.router.Use(middleware.RealIP)

	// Add security headers (X-Frame-Options, X-Content-Type-Options, CSP, etc.)
	s.router.Use(SecurityHeaders)

	// Add request body size limit (10MB) to prevent DoS via large payloads
	s.router.Use(MaxBodySize(10 * 1024 * 1024))

	// Require JSON Content-Type for POST/PUT/PATCH requests
	s.router.Use(RequireJSONContentType)

	// Add gzip compression for responses >1KB (reduces bandwidth ~70% for JSON)
	s.router.Use(middleware.Compress(5)) // Level 5 = good balance of speed vs compression

	// Apply per-client rate limiting (after RealIP so we get the real client IP)
	if s.rateLimiter != nil {
		s.router.Use(PerClientRateLimitMiddleware(s.rateLimiter))
	}
	if s.tokenAuth != nil {
		s.router.Use(s.tokenAuth.Middleware)
	}

	// Note: Timeout middleware is applied per-route, not globally,
	// to avoid killing SSE connections which need to stay open indefinitely
}

// setupRoutes configures HTTP routes.
func (s *Service) setupRoutes() {
	// Serve Vue dashboard from embedded static files
	s.router.Get("/", serveIndex)
	s.router.Get("/assets/*", serveAssets)

	// Auth routes (public — login/logout do not require auth)
	s.router.Post("/api/auth/login", s.handleAuthLogin)
	s.router.Post("/api/auth/logout", s.handleAuthLogout)

	// Email/password auth routes (no token auth required).
	// Handlers delegate to s.authHandlers which is initialised async.
	s.router.Get("/api/auth/setup-needed", s.handleUserSetupNeeded)
	s.router.Post("/api/auth/setup", s.handleUserSetup)
	s.router.Post("/api/auth/user-login", s.handleUserLogin)
	s.router.Post("/api/auth/user-logout", s.handleUserLogout)

	// Registration (public, requires valid invitation code)
	s.router.Post("/api/auth/register", s.handleUserRegister)

	// Admin management (requires authenticated admin session)
	s.router.Route("/api/admin", func(r chi.Router) {
		r.Post("/invitations", s.handleAdminCreateInvitation)
		r.Get("/invitations", s.handleAdminListInvitations)
		r.Get("/users", s.handleAdminListUsers)
		r.Put("/users/{id}", s.handleAdminUpdateUser)
	})

	// Health check (both root and API-prefixed for compatibility)
	// Returns 200 immediately so hooks can connect quickly during init
	// Also returns version for stale worker detection
	s.router.Get("/health", s.handleHealth)
	s.router.Get("/api/health", s.handleHealth)

	// Version endpoint for hooks to check if worker needs restart
	s.router.Get("/api/version", s.handleVersion)

	// Readiness check - returns 200 only when fully initialized
	s.router.Get("/api/ready", s.handleReady)

	// MCP health counters (public — no auth required, lightweight)
	s.router.Get("/api/mcp/health", s.mcpHealth.HandleHealth)

	// OpenAPI docs (read-only spec; protected by global auth middleware if ENGRAM_API_TOKEN is set)
	s.router.Get("/api/docs", http.RedirectHandler("/api/docs/index.html", http.StatusMovedPermanently).ServeHTTP)
	s.router.Get("/api/docs/*", httpSwagger.WrapHandler)

	// Admin/management routes — authentication applied globally via setupMiddleware.
	// Grouped for logical organization; no additional middleware needed.
	s.router.Group(func(r chi.Router) {
		// Vector metrics/health endpoints (return disabled status since vectors removed in v5)
		r.Get("/api/vectors/health", s.handleVectorHealth)
		r.Get("/api/vector/metrics", s.handleVectorMetrics)
		r.Get("/api/graph/stats", s.handleGraphStats)

		// Update endpoints (work before DB is ready)
		r.Get("/api/update/check", s.handleUpdateCheck)
		r.Post("/api/update/apply", s.handleUpdateApply)
		r.Get("/api/update/status", s.handleUpdateStatus)
		r.Post("/api/update/restart", s.handleUpdateRestart)

		// General restart endpoint (works before DB is ready)
		r.Post("/api/restart", s.handleRestart)

		// Selfcheck endpoint (works before DB is ready - checks all components)
		r.Get("/api/selfcheck", s.handleSelfCheck)

		// Dashboard SSE endpoint (works before DB is ready)
		r.Get("/api/events", s.sseBroadcaster.HandleSSE)

		// Log viewer endpoint (works before DB is ready, supports SSE follow mode)
		r.Get("/api/logs", s.handleGetLogs)

		// Instinct import endpoint
		r.Post("/api/instincts/import", s.handleInstinctsImport)

		// Backfill endpoints
		r.Post("/api/backfill", s.handleBackfillIngest)
		r.Post("/api/backfill/session", s.handleBackfillSession)
		r.Get("/api/backfill/status", s.handleBackfillStatus)
		r.Post("/api/import/feedback", s.handleImportFeedback)
	})

	// OpenAI-compatible model list endpoint. Intentionally outside requireReady group:
	// the model registry is populated at init() time (before DB is ready), so this
	// endpoint is always available and useful for LiteLLM proxy configuration.
	s.router.Get("/v1/models", s.handleListModels)

	// Routes that require DB to be ready
	s.router.Group(func(r chi.Router) {
		r.Use(s.requireReady)
		r.Use(middleware.Timeout(DefaultHTTPTimeout))

		// Session routes
		r.Post("/api/sessions/init", s.handleSessionInit)
		r.Get("/api/sessions/list", s.handleListSessions)
		r.Get("/api/sessions", s.handleGetSessionByClaudeID)
		r.Post("/api/sessions/{id}/init", s.handleSessionStart)
		r.Post("/api/sessions/observations", s.handleObservation)
		r.Post("/api/sessions/subagent-complete", s.handleSubagentComplete)
		r.Post("/api/sessions/{id}/summarize", s.handleSummarize)
		r.Post("/api/sessions/{id}/extract-learnings", s.handleExtractLearnings)
		r.Post("/api/sessions/{sessionId}/mark-injected", s.handleSessionMarkInjected)
		r.Post("/api/sessions/{sessionId}/outcome", s.handleSetSessionOutcome)
		r.Post("/api/sessions/{sessionId}/propagate-outcome", s.handlePropagateOutcome)
		r.Get("/api/learning/effectiveness-distribution", s.handleGetEffectivenessDistribution)
		r.Get("/api/learning/strategies", s.handleGetStrategies)
		r.Get("/api/learning/curve", s.handleGetLearningCurve)
		r.Get("/api/learning/hit-rate", s.handleGetHitRateAnalytics)
		r.Get("/api/sessions/{sessionId}/injections", s.handleGetSessionInjections)

		// Session transcript indexing (client pushes JSONL for FTS)
		r.Post("/api/sessions/index", s.handleIndexSession)
		r.Post("/api/sessions/check", s.handleCheckSessions)

		// Event ingest (Level 0 deterministic pipeline)
		r.Post("/api/events/ingest", s.handleIngestEvent)

		// Data routes
		r.Get("/api/observations", s.handleGetObservations)
		r.Get("/api/observations/{id}", s.handleGetObservationByID)
		r.Put("/api/observations/{id}", s.handleUpdateObservation)
		r.Get("/api/summaries", s.handleGetSummaries)
		r.Get("/api/prompts", s.handleGetPrompts)
		r.Get("/api/projects", s.handleGetProjects)
		r.Delete("/api/projects/{id}", s.handleDeleteProject)
		r.Get("/api/stats", s.handleGetStats)
		r.Get("/api/stats/retrieval", s.handleGetRetrievalStats)
		r.Post("/api/graph/sync", s.handleGraphSync)
		r.Get("/api/types", s.handleGetTypes)
		r.Get("/api/models", s.handleGetModels)

		// Observation scoring and feedback routes
		r.Post("/api/observations/{id}/feedback", s.handleObservationFeedback)
		r.Post("/api/observations/{id}/utility", s.handleObservationUtility)
		r.Get("/api/observations/{id}/score", s.handleExplainScore)
		r.Get("/api/observations/{id}/effectiveness", s.handleGetEffectiveness)
		r.Post("/api/observations/mark-injected", s.handleMarkInjected)
		r.Get("/api/observations/top", s.handleGetTopObservations)
		r.Get("/api/observations/most-retrieved", s.handleGetMostRetrieved)
		r.Get("/api/observations/recently-injected", s.handleGetRecentlyInjected)

		// Scoring configuration routes
		r.Get("/api/scoring/stats", s.handleGetScoringStats)
		r.Get("/api/scoring/concepts", s.handleGetConceptWeights)
		r.Put("/api/scoring/concepts/{concept}", s.handleUpdateConceptWeight)
		r.Post("/api/scoring/recalculate", s.handleTriggerRecalculation)

		// Context injection
		r.Get("/api/context/count", s.handleContextCount)
		r.Post("/api/context/inject", s.handleContextInject)
		r.Get("/api/context/inject", s.handleContextInject) // deprecated — use POST
		r.Get("/api/context/search", s.handleSearchByPrompt)
		r.Post("/api/context/search", s.handleSearchByPrompt)
		r.Get("/api/context/files", s.handleFileContext)
		r.Get("/api/context/by-file", s.handleContextByFile)
		r.Post("/api/memory/triggers", s.handleMemoryTriggers)
		r.Post("/api/decisions/search", s.handleSearchDecisions)

		// Issue tracking routes (agent-issues feature)
		r.Get("/api/issues", s.handleListIssues)
		r.Post("/api/issues", s.handleCreateIssue)
		// Static routes must come BEFORE /{id} to avoid chi matching them as IDs.
		r.Get("/api/issues/tracked-projects", s.handleTrackedProjects)
		r.Post("/api/issues/acknowledge", s.handleAcknowledgeIssues)
		r.Get("/api/issues/{id}", s.handleGetIssue)
		r.Patch("/api/issues/{id}", s.handleUpdateIssue)
		r.Delete("/api/issues/{id}", s.handleDeleteIssue)

		// Pattern routes
		r.Get("/api/patterns", s.handleGetPatterns)
		r.Get("/api/patterns/stats", s.handleGetPatternStats)
		r.Get("/api/patterns/search", s.handleSearchPatterns)
		r.Get("/api/patterns/by-name", s.handleGetPatternByName)
		r.Get("/api/patterns/{id}", s.handleGetPatternByID)
		r.Get("/api/patterns/{id}/insight", s.handleGetPatternInsight)
		r.Get("/api/patterns/{id}/observations", s.handleGetPatternObservations)
		r.Post("/api/patterns/{id}/insight", s.handlePostPatternInsight)
		r.Delete("/api/patterns/{id}", s.handleDeletePattern)
		r.Post("/api/patterns/{id}/deprecate", s.handleDeprecatePattern)
		r.Post("/api/patterns/merge", s.handleMergePatterns)

		// Relation routes (knowledge graph)
		r.Get("/api/relations/stats", s.handleGetRelationStats)
		r.Get("/api/relations/type/{type}", s.handleGetRelationsByType)
		r.Get("/api/observations/{id}/relations", s.handleGetRelations)
		r.Get("/api/observations/{id}/graph", s.handleGetRelationGraph)
		r.Get("/api/observations/{id}/related", s.handleGetRelatedObservations)

		// Bulk import, export, and archival routes
		r.Post("/api/observations/bulk-import", s.handleBulkImport)
		r.Get("/api/observations/export", s.handleExportObservations)
		r.Post("/api/observations/archive", s.handleArchiveObservations)
		r.Post("/api/observations/{id}/unarchive", s.handleUnarchiveObservation)
		r.Get("/api/observations/archived", s.handleGetArchivedObservations)
		r.Get("/api/observations/archival-stats", s.handleGetArchivalStats)

		// Search analytics
		r.Get("/api/search/recent", s.handleGetRecentQueries)
		r.Get("/api/search/analytics", s.handleGetSearchAnalytics)
		r.Post("/api/analytics/search-misses", s.handleSearchMissAnalytics)

		// Duplicate detection
		r.Get("/api/observations/duplicates", s.handleFindDuplicates)

		// Bulk status operations
		r.Post("/api/observations/bulk-status", s.handleBulkStatusUpdate)

		// Telemetry
		r.Get("/api/telemetry/similarity", s.handleGetSimilarityTelemetry)

		// Auth routes (require auth — admin only)
		r.Get("/api/auth/me", s.handleAuthMe)
		r.Get("/api/auth/tokens", s.handleListTokens)
		r.Post("/api/auth/tokens", s.handleCreateToken)
		r.Delete("/api/auth/tokens/{id}", s.handleRevokeToken)

		// Vault routes
		r.Get("/api/vault/credentials", s.handleListCredentials)
		r.Get("/api/vault/credentials/{name}", s.handleGetCredential)
		r.Post("/api/vault/credentials", s.handleStoreCredential)
		r.Delete("/api/vault/credentials/{name}", s.handleDeleteCredential)
		r.Get("/api/vault/status", s.handleVaultStatus)
		r.Delete("/api/vault/orphaned-credentials", s.handleDeleteOrphanedCredentials)

		// Memory routes (US3 Commit E — explicit user memories stored in memories table)
		r.Post("/api/memories", s.handleStoreMemoryExplicit)
		r.Get("/api/memories", s.handleListMemories)
		r.Delete("/api/memories/{id}", s.handleDeleteMemoryByID)

		// Tag routes
		r.Post("/api/observations/{id}/tags", s.handleTagObservation)
		r.Get("/api/observations/by-tag/{tag}", s.handleGetObservationsByTag)
		r.Post("/api/observations/batch-tag", s.handleBatchTagObservations)
		r.Get("/api/observations/tag-cloud", s.handleTagCloud)

		// Bulk observation operations
		r.Delete("/api/observations/bulk", s.handleBulkDeleteREST)
		r.Patch("/api/observations/bulk-scope", s.handleBulkScopeChange)

		// Token stats
		r.Get("/api/auth/tokens/{id}/stats", s.handleGetTokenStats)

		// Indexed session routes (separate from live session management)
		r.Get("/api/sessions-index", s.handleListIndexedSessions)
		r.Get("/api/sessions-index/search", s.handleSearchIndexedSessions)

		// Maintenance routes
		r.Post("/api/maintenance/consolidation", s.handleTriggerConsolidation)
		r.Post("/api/maintenance/run", s.handleRunMaintenance)
		r.Get("/api/maintenance/stats", s.handleGetMaintenanceStats)
		r.Get("/api/maintenance/status", s.handleMaintenanceStatus)
		r.Get("/api/maintenance/logs", s.handleMaintenanceLogs)
		r.Get("/api/maintenance/consistency", s.handleConsistencyCheck)
		r.Post("/api/maintenance/purge-patterns", s.handlePurgePatterns)
		r.Post("/api/maintenance/pattern-cleanup", s.handlePatternCleanup)
		r.Post("/api/maintenance/purge-rebuild", s.handlePurgeRebuild)
		r.Post("/api/maintenance/patterns/cleanup", s.handlePatternCleanupAdvanced)
		r.Post("/api/maintenance/apo/rewrite", s.handleAPORewrite)

		// Analytics routes
		r.Get("/api/analytics/trends", s.handleGetTrends)

		// Config
		r.Get("/api/config", s.handleGetConfig)
	})
}

// recordRetrievalStatsExtended records retrieval stats including staleness metrics.
func (s *Service) recordRetrievalStatsExtended(project string, served, verified, deleted, staleExcluded, freshCount, duplicatesRemoved int64, isSearch bool) {
	now := time.Now().Unix()

	s.retrievalStatsMu.Lock()
	stats := s.retrievalStats[project]
	if stats == nil {
		// Cleanup old entries if we're at capacity
		if len(s.retrievalStats) >= maxRetrievalStatsProjects {
			s.cleanupRetrievalStatsLocked()
		}
		stats = &RetrievalStats{}
		s.retrievalStats[project] = stats
	}
	s.retrievalStatsMu.Unlock()

	atomic.AddInt64(&stats.TotalRequests, 1)
	atomic.AddInt64(&stats.ObservationsServed, served)
	atomic.AddInt64(&stats.VerifiedStale, verified)
	atomic.AddInt64(&stats.DeletedInvalid, deleted)
	atomic.AddInt64(&stats.StaleExcluded, staleExcluded)
	atomic.AddInt64(&stats.FreshCount, freshCount)
	atomic.AddInt64(&stats.DuplicatesRemoved, duplicatesRemoved)
	atomic.StoreInt64(&stats.LastUpdated, now)
	if isSearch {
		atomic.AddInt64(&stats.SearchRequests, 1)
	} else {
		atomic.AddInt64(&stats.ContextInjections, 1)
	}

	// Persist to DB via batched flusher (non-blocking).
	s.initMu.RLock()
	logStore := s.retrievalStatsLogStore
	s.initMu.RUnlock()
	if logStore != nil {
		if isSearch {
			logStore.LogEvent(project, "search_request", 1)
		} else {
			logStore.LogEvent(project, "context_injection", 1)
		}
		if served > 0 {
			logStore.LogEvent(project, "observations_served", int(served))
		}
		if staleExcluded > 0 {
			logStore.LogEvent(project, "stale_excluded", int(staleExcluded))
		}
		if freshCount > 0 {
			logStore.LogEvent(project, "fresh_count", int(freshCount))
		}
		if duplicatesRemoved > 0 {
			logStore.LogEvent(project, "duplicates_removed", int(duplicatesRemoved))
		}
	}
}

// cleanupRetrievalStatsLocked removes stale entries from retrievalStats.
// Must be called with retrievalStatsMu held.
func (s *Service) cleanupRetrievalStatsLocked() {
	cutoff := time.Now().Add(-retrievalStatsMaxAge).Unix()
	for project, stats := range s.retrievalStats {
		if atomic.LoadInt64(&stats.LastUpdated) < cutoff {
			delete(s.retrievalStats, project)
		}
	}
}

// GetRetrievalStats returns a copy of the retrieval stats for a project.
// If project is empty, returns aggregate stats across all projects.
func (s *Service) GetRetrievalStats(project string) RetrievalStats {
	s.retrievalStatsMu.RLock()
	defer s.retrievalStatsMu.RUnlock()

	if project != "" {
		// Return stats for specific project
		stats := s.retrievalStats[project]
		if stats == nil {
			return RetrievalStats{}
		}
		return RetrievalStats{
			TotalRequests:      atomic.LoadInt64(&stats.TotalRequests),
			ObservationsServed: atomic.LoadInt64(&stats.ObservationsServed),
			VerifiedStale:      atomic.LoadInt64(&stats.VerifiedStale),
			DeletedInvalid:     atomic.LoadInt64(&stats.DeletedInvalid),
			SearchRequests:     atomic.LoadInt64(&stats.SearchRequests),
			ContextInjections:  atomic.LoadInt64(&stats.ContextInjections),
			StaleExcluded:      atomic.LoadInt64(&stats.StaleExcluded),
			FreshCount:         atomic.LoadInt64(&stats.FreshCount),
			DuplicatesRemoved:  atomic.LoadInt64(&stats.DuplicatesRemoved),
		}
	}

	// Aggregate stats across all projects
	var result RetrievalStats
	for _, stats := range s.retrievalStats {
		result.TotalRequests += atomic.LoadInt64(&stats.TotalRequests)
		result.ObservationsServed += atomic.LoadInt64(&stats.ObservationsServed)
		result.VerifiedStale += atomic.LoadInt64(&stats.VerifiedStale)
		result.DeletedInvalid += atomic.LoadInt64(&stats.DeletedInvalid)
		result.SearchRequests += atomic.LoadInt64(&stats.SearchRequests)
		result.ContextInjections += atomic.LoadInt64(&stats.ContextInjections)
		result.StaleExcluded += atomic.LoadInt64(&stats.StaleExcluded)
		result.FreshCount += atomic.LoadInt64(&stats.FreshCount)
		result.DuplicatesRemoved += atomic.LoadInt64(&stats.DuplicatesRemoved)
	}
	return result
}

// trackSearchQuery records a search query for analytics.
// Writes to the persistent DB store (fire-and-forget) and the in-memory ring buffer.
// O(1) insertion for the ring buffer - no memory allocation or copying on each insert.
func (s *Service) trackSearchQuery(query, project, queryType string, results int, latencyMs float32) {
	// Persist to DB asynchronously (fire-and-forget, never blocks caller).
	s.initMu.RLock()
	sqlStore := s.searchQueryLogStore
	s.initMu.RUnlock()
	if sqlStore != nil {
		sqlStore.LogQuery(project, query, queryType, results, latencyMs)
	}

	// Also maintain the in-memory ring buffer for low-latency in-process access.
	s.recentQueriesMu.Lock()
	defer s.recentQueriesMu.Unlock()

	// Move head back (wrapping around) and insert at new head position
	// This puts the newest item at the head
	s.recentQueriesHead = (s.recentQueriesHead - 1 + maxRecentQueries) % maxRecentQueries

	s.recentQueriesBuf[s.recentQueriesHead] = RecentSearchQuery{
		Query:     query,
		Project:   project,
		Type:      queryType,
		Results:   results,
		Timestamp: time.Now(),
	}

	// Increase length up to max
	if s.recentQueriesLen < maxRecentQueries {
		s.recentQueriesLen++
	}
}

// getCachedObservationCount returns observation count for a project, using cache if available.
// Falls back to database query if cache is expired or missing.
func (s *Service) getCachedObservationCount(ctx context.Context, project string) (int, error) {
	// Check cache first
	s.cachedObsCountsMu.RLock()
	if cached, ok := s.cachedObsCounts[project]; ok {
		if time.Since(cached.timestamp) < s.statsCacheTTL {
			s.cachedObsCountsMu.RUnlock()
			return cached.count, nil
		}
	}
	s.cachedObsCountsMu.RUnlock()

	// Cache miss or expired - query database
	count, err := s.observationStore.GetObservationCount(ctx, project)
	if err != nil {
		return 0, err
	}

	// Update cache
	s.cachedObsCountsMu.Lock()
	s.cachedObsCounts[project] = cachedCount{
		count:     count,
		timestamp: time.Now(),
	}
	s.cachedObsCountsMu.Unlock()

	return count, nil
}

// invalidateObsCountCache invalidates the observation count cache for a project.
// Call this when observations are added, archived, or deleted.
func (s *Service) invalidateObsCountCache(project string) {
	s.cachedObsCountsMu.Lock()
	delete(s.cachedObsCounts, project)
	s.cachedObsCountsMu.Unlock()
}

// invalidateAllObsCountCache clears all observation count caches.
func (s *Service) invalidateAllObsCountCache() {
	s.cachedObsCountsMu.Lock()
	s.cachedObsCounts = make(map[string]cachedCount)
	s.cachedObsCountsMu.Unlock()
}

// Start starts the worker service.
// The HTTP server starts immediately; database initialization happens async.
func (s *Service) Start() error {
	port := config.GetWorkerPort()

	// Auth startup gate (ADR-0001): refuse to start without token unless explicitly disabled
	token := config.GetWorkerToken()
	authDisabled := strings.EqualFold(strings.TrimSpace(os.Getenv("ENGRAM_AUTH_DISABLED")), "true")

	if token == "" && !authDisabled {
		log.Fatal().Msg("ENGRAM_AUTH_ADMIN_TOKEN (or ENGRAM_API_TOKEN) is not set. Set it to secure your engram instance, or set ENGRAM_AUTH_DISABLED=true to explicitly run without authentication (NOT recommended for production).")
	}

	// Deprecation warning for old env var name
	if os.Getenv("ENGRAM_API_TOKEN") != "" && os.Getenv("ENGRAM_AUTH_ADMIN_TOKEN") == "" {
		log.Warn().Msg("auth: ENGRAM_API_TOKEN is deprecated — rename to ENGRAM_AUTH_ADMIN_TOKEN (old name will be removed in v1.3)")
	}

	if authDisabled {
		log.Warn().Msg("auth: authentication is explicitly disabled via ENGRAM_AUTH_DISABLED=true — all endpoints are unauthenticated")
		// Start periodic warning goroutine tracked by WaitGroup for graceful shutdown.
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			ticker := time.NewTicker(60 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-s.ctx.Done():
					return
				case <-ticker.C:
					log.Warn().Msg("auth: reminder — authentication is disabled, all endpoints are unauthenticated")
				}
			}
		}()
	}

	host := config.GetWorkerHost()
	addr := fmt.Sprintf("%s:%d", host, port)

	s.server = &http.Server{
		Addr:              addr,
		Handler:           s.router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0, // Disabled for SSE (long-lived connections)
		IdleTimeout:       120 * time.Second,
	}

	// Check if we're in restart mode (after update)
	isRestart := os.Getenv("ENGRAM_RESTART") == "1"

	// startWithListener binds a TCP listener and launches HTTP + optional gRPC via cmux.
	// Extracted so the retry loop can re-bind on a new listener each attempt.
	startWithListener := func() error {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return err
		}

		// Optional TLS: wrap listener if cert + key are provided.
		tlsCert := os.Getenv("ENGRAM_TLS_CERT")
		tlsKey := os.Getenv("ENGRAM_TLS_KEY")
		if tlsCert != "" && tlsKey != "" {
			cert, tlsErr := tls.LoadX509KeyPair(tlsCert, tlsKey)
			if tlsErr != nil {
				_ = ln.Close()
				return fmt.Errorf("failed to load TLS keypair: %w", tlsErr)
			}
			ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}})
			log.Info().Str("cert", tlsCert).Msg("TLS enabled")
		} else {
			log.Warn().Msg("TLS not configured (ENGRAM_TLS_CERT / ENGRAM_TLS_KEY unset) — serving plaintext")
		}

		m := cmux.New(ln)

		// gRPC connections carry HTTP/2 with the application/grpc content-type header.
		// For h2c (plaintext HTTP/2), we must use MatchWithWriters + SendSettings
		// so cmux sends the server SETTINGS frame before the client sends HEADERS.
		// Without this, the gRPC client blocks waiting for the server preface and
		// the connection times out. For TLS, ALPN handles HTTP/2 negotiation, but
		// SendSettings is harmless there — it works correctly for both modes.
		// The gRPC server may not be ready yet (initializeAsync sets s.grpcServer),
		// so we start Serve() in a goroutine that waits for the server to appear.
		grpcL := m.MatchWithWriters(cmux.HTTP2MatchHeaderFieldPrefixSendSettings("content-type", "application/grpc"))
		go func() {
			// Wait for initializeAsync to create the gRPC server.
			s.initMu.RLock()
			for s.grpcServer == nil {
				s.initMu.RUnlock()
				time.Sleep(100 * time.Millisecond)
				s.initMu.RLock()
			}
			grpcSrv := s.grpcServer
			s.initMu.RUnlock()

			log.Info().Msg("gRPC server ready, serving on cmux")
			if err := grpcSrv.Serve(grpcL); err != nil {
				log.Error().Err(err).Msg("gRPC server error")
			}
		}()

		// All other connections (HTTP/1.1, HTTP/2 non-gRPC) go to the HTTP server.
		httpL := m.Match(cmux.Any())
		go func() {
			if err := s.server.Serve(httpL); err != nil && err != http.ErrServerClosed {
				log.Error().Err(err).Msg("HTTP server error")
			}
		}()

		// m.Serve() blocks until the listener is closed (i.e. on shutdown).
		if err := m.Serve(); err != nil {
			// cmux returns an error when the underlying listener is closed during shutdown.
			// Treat that the same as http.ErrServerClosed — not a real error.
			log.Debug().Err(err).Msg("cmux serve returned (expected on shutdown)")
		}
		return nil
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		maxRetries := 1
		if isRestart {
			maxRetries = 10 // Retry up to 10 times during restart
		}

		for i := 0; i < maxRetries; i++ {
			if err := startWithListener(); err != nil {
				if i < maxRetries-1 && isRestart {
					log.Warn().Err(err).Int("retry", i+1).Msg("Port not ready, retrying...")
					time.Sleep(500 * time.Millisecond)
					continue
				}
				log.Error().Err(err).Msg("Failed to start listener")
			}
			return
		}
	}()

	// Note: Queue processor is started in initializeAsync() after DB is ready

	log.Info().
		Int("port", port).
		Int("pid", getPID()).
		Bool("restart_mode", isRestart).
		Msg("Worker HTTP server started (initialization in progress)")

	return nil
}

// processQueue processes the observation queue in the background.
// Processes immediately when notified, or every QueueProcessInterval as fallback.
func (s *Service) processQueue() {
	defer s.wg.Done()

	ticker := time.NewTicker(QueueProcessInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.sessionManager.ProcessNotify:
			// Immediate processing when observation is queued
			s.processAllSessions()
		case <-ticker.C:
			// Fallback periodic processing
			s.processAllSessions()
		}
	}
}

// processAllSessions processes pending messages for all active sessions.
// Messages are processed in parallel using goroutines, with concurrency
// limited by the processor's semaphore.
func (s *Service) processAllSessions() {
	// Get all sessions with pending messages
	sessions := s.sessionManager.GetAllSessions()

	var wg sync.WaitGroup

	for _, sess := range sessions {
		// Get pending messages
		messages := s.sessionManager.DrainMessages(sess.SessionDBID)
		if len(messages) == 0 {
			continue
		}

		// Process each message in a goroutine
		for _, msg := range messages {
			wg.Add(1)
			go func(sess *session.ActiveSession, msg session.PendingMessage) {
				defer wg.Done()

				switch msg.Type {
				case session.MessageTypeObservation:
					if msg.Observation != nil {
						err := s.processor.ProcessObservation(
							s.ctx,
							sess.SDKSessionID,
							sess.Project,
							msg.Observation.ToolName,
							msg.Observation.ToolInput,
							msg.Observation.ToolResponse,
							msg.Observation.PromptNumber,
							msg.Observation.CWD,
							msg.Observation.UserPrompt,
						)
						if err != nil {
							log.Error().Err(err).
								Str("tool", msg.Observation.ToolName).
								Msg("Failed to process observation")
						}
					}

				case session.MessageTypeSummarize:
					if msg.Summarize != nil {
						err := s.processor.ProcessSummary(
							s.ctx,
							sess.SessionDBID,
							sess.SDKSessionID,
							sess.Project,
							sess.UserPrompt,
							msg.Summarize.LastUserMessage,
							msg.Summarize.LastAssistantMessage,
						)
						if err != nil {
							log.Error().Err(err).
								Int64("sessionId", sess.SessionDBID).
								Msg("Failed to process summary")
						}
						// Delete session after summary
						s.sessionManager.DeleteSession(sess.SessionDBID)
					}
				}
			}(sess, msg)
		}
	}

	// Wait for all goroutines to complete
	wg.Wait()

	// Broadcast status after processing
	s.broadcastProcessingStatus()
}

// syncGraphFromRelations loads all relations from PostgreSQL and syncs them to FalkorDB.
func (s *Service) syncGraphFromRelations() {
	if s.graphStore == nil || s.relationStore == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	log.Info().Msg("Starting graph sync from PostgreSQL relations...")

	// Load all relations (minConfidence=0 fetches everything).
	const maxRelations = 100000
	relations, err := s.relationStore.GetHighConfidenceRelations(ctx, 0, maxRelations)
	if err != nil {
		log.Error().Err(err).Msg("Graph sync: failed to load relations from PostgreSQL")
		return
	}

	if len(relations) == 0 {
		log.Info().Msg("Graph sync: no relations to sync")
		return
	}

	if err := s.graphStore.SyncFromRelations(ctx, relations); err != nil {
		log.Error().Err(err).Msg("Graph sync: SyncFromRelations failed")
		return
	}

	log.Info().Int("total_synced", len(relations)).Msg("Graph sync from PostgreSQL complete")
}

// Shutdown gracefully shuts down the service.
func (s *Service) Shutdown(ctx context.Context) error {
	log.Info().Msg("Starting graceful shutdown...")
	start := time.Now()

	// Cancel context to signal all background goroutines
	s.cancel()

	// Create error collector
	var shutdownErrors []error
	var mu sync.Mutex
	collectError := func(name string, err error) {
		if err != nil {
			mu.Lock()
			shutdownErrors = append(shutdownErrors, fmt.Errorf("%s: %w", name, err))
			mu.Unlock()
			log.Error().Err(err).Str("component", name).Msg("Shutdown error")
		}
	}

	// Phase 1: Stop accepting new work (HTTP server and gRPC server shutdown first)
	log.Debug().Msg("Phase 1: Stopping HTTP and gRPC servers...")
	if s.server != nil {
		if err := s.server.Shutdown(ctx); err != nil {
			collectError("http_server", err)
		}
	}
	if s.grpcServer != nil {
		s.grpcServer.GracefulStop()
	}

	// Phase 2: Stop file watchers (prevent new DB recreation)
	log.Debug().Msg("Phase 2: Stopping watchers...")
	if s.configWatcher != nil {
		_ = s.configWatcher.Stop()
	}

	// Phase 3: Stop background workers (drain queues)
	log.Debug().Msg("Phase 3: Stopping background workers...")
	if s.graphWriter != nil {
		s.graphWriter.Close()
		log.Debug().Msg("Graph writer stopped")
	}
	if s.graphStore != nil {
		collectError("graph_store", s.graphStore.Close())
	}
	if s.recalculator != nil {
		s.recalculator.Stop()
	}
	if s.consolidationScheduler != nil {
		s.consolidationScheduler.Stop()
	}
	if s.patternDetector != nil {
		s.patternDetector.Stop()
	}

	// Phase 4: Shutdown sessions (flush pending work)
	log.Debug().Msg("Phase 4: Shutting down sessions...")
	if s.sessionManager != nil {
		s.sessionManager.ShutdownAll(ctx)
	}

	// Phase 5: Wait for goroutines with timeout
	log.Debug().Msg("Phase 5: Waiting for goroutines...")
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		log.Debug().Msg("All goroutines finished")
	case <-ctx.Done():
		log.Warn().Msg("Timeout waiting for goroutines - forcing shutdown")
	}

	// Phase 6: Close AI/ML services (close models)
	log.Debug().Msg("Phase 6: Closing AI/ML services...")
	if s.reranker != nil {
		collectError("reranker", s.reranker.Close())
	}

	// Phase 7: Close database last (other components may need it)
	log.Debug().Msg("Phase 8: Closing database...")
	if s.store != nil {
		collectError("database", s.store.Close())
	}

	elapsed := time.Since(start)
	if len(shutdownErrors) > 0 {
		log.Warn().
			Int("errors", len(shutdownErrors)).
			Dur("elapsed", elapsed).
			Msg("Worker shutdown completed with errors")
		return shutdownErrors[0]
	}

	log.Info().
		Dur("elapsed", elapsed).
		Msg("Worker service shutdown complete")
	return nil
}

// broadcastProcessingStatus broadcasts the current processing status.
func (s *Service) broadcastProcessingStatus() {
	isProcessing := s.sessionManager.IsAnySessionProcessing()
	queueDepth := s.sessionManager.GetTotalQueueDepth()

	s.sseBroadcaster.Broadcast(map[string]any{
		"type":         "processing_status",
		"isProcessing": isProcessing,
		"queueDepth":   queueDepth,
	})
}

func getPID() int {
	return os.Getpid()
}
