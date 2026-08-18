// Package worker provides the main worker service for engram.
package worker

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog/log"
	"github.com/soheilhy/cmux"
	httpSwagger "github.com/swaggo/http-swagger"

	"github.com/thebtf/engram/internal/auth"
	booksdomain "github.com/thebtf/engram/internal/books"
	"github.com/thebtf/engram/internal/bulkops"
	"github.com/thebtf/engram/internal/chunking"

	gochunking "github.com/thebtf/engram/internal/chunking/golang"
	mdchunking "github.com/thebtf/engram/internal/chunking/markdown"
	cognitivecore "github.com/thebtf/engram/internal/cognitive/core"
	"github.com/thebtf/engram/internal/cognitive/s1state"
	"github.com/thebtf/engram/internal/cognitive/s2meta"
	"github.com/thebtf/engram/internal/cognitive/s3ambient"
	"github.com/thebtf/engram/internal/cognitive/s4bsurfacing"
	"github.com/thebtf/engram/internal/cognitive/s4directives"
	"github.com/thebtf/engram/internal/cognitive/s5"
	"github.com/thebtf/engram/internal/cognitive/s6"
	"github.com/thebtf/engram/internal/collections"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/crypto"
	"github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/embedding"
	"github.com/thebtf/engram/internal/feedback"
	"github.com/thebtf/engram/internal/graph"
	"github.com/thebtf/engram/internal/grpcserver"
	"github.com/thebtf/engram/internal/injection"
	"github.com/thebtf/engram/internal/logbuf"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/principalmemory"
	"github.com/thebtf/engram/internal/redaction"
	"github.com/thebtf/engram/internal/reranking"
	"github.com/thebtf/engram/internal/sessions"
	"github.com/thebtf/engram/internal/stateplane"
	"github.com/thebtf/engram/internal/telemetry"
	"github.com/thebtf/engram/internal/update"
	"github.com/thebtf/engram/internal/watcher"
	"github.com/thebtf/engram/internal/worker/projectevents"
	"github.com/thebtf/engram/internal/worker/reaper"
	"github.com/thebtf/engram/internal/worker/sdk"
	"github.com/thebtf/engram/internal/worker/session"
	"github.com/thebtf/engram/internal/worker/sse"
	"github.com/thebtf/engram/internal/writelint"
	"github.com/thebtf/engram/pkg/cognitive"
	"github.com/thebtf/engram/pkg/models"
	googlegrpc "google.golang.org/grpc"
)

// Timing and capacity constants for the worker service.
// Adjust via env-gated config rather than changing defaults here.
const (
	// DefaultHTTPTimeout caps handler execution on routes that use Timeout middleware.
	// SSE routes opt out explicitly — they need unbounded connection lifetime.
	DefaultHTTPTimeout = 30 * time.Second

	// ReadyPollInterval is the sleep between readiness checks in WaitReady.
	ReadyPollInterval = 50 * time.Millisecond

	// StaleQueueSize is the channel depth for background stale-check requests.
	// Excess requests are silently dropped to avoid blocking callers.
	StaleQueueSize = 100

	// QueueProcessInterval is the fallback tick rate for the observation queue processor.
	// The processor also fires immediately on each new-observation notification.
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

// maxRetrievalStatsProjects caps the per-project stats map. Projects beyond this
// limit evict oldest entries to keep memory bounded.
const maxRetrievalStatsProjects = 500

// retrievalStatsMaxAge is the expiry window for idle-project stats entries.
const retrievalStatsMaxAge = 24 * time.Hour

// maxRecentQueries is the ring-buffer size for in-process recent-query analytics.
const maxRecentQueries = 100

// Service is the main worker service orchestrator.
type Service struct {
	startTime                        time.Time
	ctx                              context.Context
	initError                        error
	server                           *http.Server
	sessionManager                   *session.Manager
	sseBroadcaster                   *sse.Broadcaster
	processor                        *sdk.Processor
	mcpHealth                        *mcp.MCPHealth
	collectionRegistry               *collections.Registry
	sessionIdxStore                  *sessions.Store
	router                           *chi.Mux
	store                            *gorm.Store
	retrievalStats                   map[string]*RetrievalStats
	sessionStore                     *gorm.SessionStore
	tokenStore                       *gorm.TokenStore
	cancel                           context.CancelFunc
	cachedObsCounts                  map[string]cachedCount
	config                           *config.Config
	staleQueue                       chan staleVerifyRequest
	configWatcher                    *watcher.Watcher
	updater                          *update.Updater
	similarityTelemetry              *telemetry.SimilarityTelemetry
	rateLimiter                      *PerClientRateLimiter
	tokenAuth                        *TokenAuth
	expensiveOpLimiter               *ExpensiveOperationLimiter
	logBuffer                        *logbuf.RingBuffer
	backfillTracker                  *backfillTracker
	grpcServer                       *googlegrpc.Server
	grpcInternalServer               sessionStartContextProvider
	searchQueryLogStore              *gorm.SearchQueryLogStore
	retrievalStatsLogStore           *gorm.RetrievalStatsLogStore
	citationLogStore                 *gorm.CitationLogStore
	injectionTracker                 *injection.Tracker
	injectionLogStore                *gorm.InjectionLogStore
	candidateStore                   *gorm.CandidateStore         // Milestone-F TG4: non-nil when ENGRAM_VNEXT_F_ENABLED=true
	candidateQueueEnabled            bool                         // cached at startup; handlers must not read env per request
	graphEnabled                     bool                         // cached at startup; graph REST handlers must not read env per request
	temporalTruthEnabled             bool                         // cached at startup; temporal truth REST handlers must not read env per request
	candidateReviewStoreSeam         candidateReviewStore         // test seam for REST candidate queue handlers
	candidateReviewSnapshotStoreSeam candidateReviewSnapshotStore // test seam for candidate pre-action snapshots
	graphEdgeStoreSeam               graphEdgeStore               // test seam for graph REST handlers
	graphNodeStoreSeam               graphNodeStore               // test seam for graph REST handlers
	snapshotStore                    *gorm.SnapshotStore          // Milestone-F TG6: non-nil when ENGRAM_VNEXT_F_ENABLED=true
	writelintTokenStore              writelint.TokenStore         // Milestone-F TG5: non-nil when ENGRAM_VNEXT_F_ENABLED=true
	redactionRules                   []redaction.CompiledRule     // Milestone-F TG5: compiled at startup from ENGRAM_REDACTION_RULES_PATH
	transcriptStore                  *gorm.TranscriptStore        // T003: session transcript persistence (flag-gated via ENGRAM_CRYSTALLIZATION_ENABLED)
	// transcriptCreatorOverride is a test seam: when non-nil it replaces
	// transcriptStore in the handleSessionEnd persistence goroutine, letting unit
	// tests assert the real handler path (redact → Create) without a live DB.
	// Production code never sets this field.
	transcriptCreatorOverride   transcriptCreator
	retrievalHooks              *retrievalHooks
	authHandlers                *AuthHandlers
	version                     string
	recentQueriesBuf            [maxRecentQueries]RecentSearchQuery
	wg                          sync.WaitGroup
	initWG                      sync.WaitGroup
	shutdownOnce                sync.Once
	shutdownDone                chan struct{}
	shutdownErr                 error
	recentQueriesLen            int
	recentQueriesHead           int
	statsCacheTTL               time.Duration
	initMu                      sync.RWMutex
	retrievalStatsMu            sync.RWMutex
	recentQueriesMu             sync.RWMutex
	cachedObsCountsMu           sync.RWMutex
	staleQueueOnce              sync.Once
	ready                       atomic.Bool
	vault                       *crypto.Vault
	issueStore                  *gorm.IssueStore
	credentialStore             *gorm.CredentialStore
	memoryStore                 *gorm.MemoryStore
	documentStore               versionedDocumentStore
	booksStore                  booksStore
	booksPipeline               booksPipelineRunner
	memoryStoreSeam             memoryListStore // test-only: when non-nil, overrides memoryStore in List-only paths
	memoryGetStoreSeam          memoryGetStore  // test-only: when non-nil, overrides memoryStore for exact-ID reads
	stateStore                  statePlane
	experienceProvider          experienceHistoryProvider
	temporalTruthProvider       temporalTruthProvider
	principalMemoryQueryService principalMemoryQueryService
	domainOwnerStore            domainOwnerStore
	domainRegistryService       domainRegistryService
	behavioralRulesStore        *gorm.BehavioralRulesStore
	auditStore                  *gorm.AuditStore
	purgeStore                  *gorm.PurgeStore
	testAuditRetainer           auditRetainer // test-only override for retention unit tests
	feedbackUpdater             *feedback.Updater
	segmentStore                *gorm.SegmentStore
	embeddingClient             *embedding.Client
	embeddingStore              *embedding.Store
	embeddingRecorder           *embedding.BackfillRecorder
	rerankClient                *reranking.Client
	promotionStore              *gorm.PromotionStore
	graphStore                  *graph.Store
	graphNodeStore              *graph.NodesStore
	vaultOnce                   sync.Once
	vaultErr                    error
	promptCache                 sync.Map // map[int64]promptCacheEntry — last user prompt per session
	eventBus                    *projectevents.Bus
	projectReaper               projectReaperLifecycle
	projectReaperFactory        projectReaperFactory
	// lastRequestAt tracks the Unix nanosecond timestamp of the most recent
	// MCP/REST request handled by this server. Updated atomically in
	// requestActivityMiddleware on every request.
	// The sleep cycle uses this to implement the ">=4h since last active session"
	// idle gate (T014 AC). Resets to zero on server restart — a fresh process
	// with no requests yet is treated as "idle since epoch", which means the
	// count gate alone determines the first cycle.
	lastRequestAt atomic.Int64
	// sleepCycleWatermarkID stores the maximum memory ID seen at the end of the last
	// successful sleep cycle. CountActiveSince queries only rows with id > this value,
	// ensuring new-memory counting is accurate regardless of total database size.
	// In-process only — resets to 0 on server restart (documented behaviour).
	sleepCycleWatermarkID atomic.Int64

	// dreamWatermark stores the Unix nanosecond timestamp of the max created_at
	// seen at the end of the last successful dream-cycle run. In-process only —
	// resets to 0 on server restart (zero = time.Unix(0,0) = epoch, so all
	// unprocessed transcripts are visible on the first tick after restart).
	dreamWatermark atomic.Int64

	// dreamExtractorFunc is a test seam: when non-nil it replaces the real LLM
	// extractor in runDreamCrystallization. Production code never sets this field.
	// Mirrors the crystallizeFunc seam used by handlers_hooks.go.
	dreamExtractorFunc dreamExtractFunc

	// dreamTranscriptStoreOverride is a test seam: when non-nil it replaces the
	// real *gorm.TranscriptStore in runDreamCrystallization. Satisfies the
	// dreamTranscriptStore interface. Production code never sets this field.
	dreamTranscriptStoreOverride dreamTranscriptStore

	// dreamCandidateStoreOverride is a test seam: when non-nil it replaces the
	// real *gorm.CandidateStore in runDreamCrystallization. Satisfies the
	// dreamCandidateWriter interface (worker-local mirror of crystallization.CandidateWriter).
	// Production code never sets this field.
	dreamCandidateStoreOverride dreamCandidateWriter

	// Cognitive v7 platform substrate (FR-7). The four cross-subsystem
	// primitives plus the resolved feature-flag snapshot. cognitiveQueueLifecycle
	// holds the same value as cognitiveQueue but typed as the local
	// lifecycleQueue interface so the worker can invoke Start/Stop on the
	// hint queue without depending on the concrete CORE-internal type.
	cognitiveRegistry       cognitivecore.SubsystemRegistry
	cognitiveMeter          cognitivecore.SubsystemMeter
	cognitiveQueue          cognitivecore.HintQueue
	cognitiveBus            cognitivecore.AttentionEventBus
	cognitiveQueueLifecycle lifecycleQueue
	flagConfig              cognitivecore.FlagConfig
}

// lifecycleQueue combines the CORE HintQueue contract with the worker-side
// Start/Stop lifecycle methods present on the concrete CORE hint-queue
// implementation. The concrete type is unexported by design (per T008 AC);
// the worker package satisfies the interface here via Go duck typing so it
// can drive Start/Stop without leaking the implementation type.
type lifecycleQueue interface {
	cognitivecore.HintQueue
	Start(ctx context.Context) error
	Stop() error
}

type projectReaperLifecycle interface {
	Start(context.Context)
	Stop()
}

type projectReaperFactory func(*gorm.Store) (projectReaperLifecycle, error)

func defaultProjectReaperFactory(store *gorm.Store) (projectReaperLifecycle, error) {
	return reaper.New(store.DB)
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

// SetCandidateStore injects the crystallization candidate store (Milestone-F TG4).
// Called during initializeAsync when ENGRAM_VNEXT_F_ENABLED=true.
func (s *Service) SetCandidateStore(cs *gorm.CandidateStore) {
	s.initMu.Lock()
	s.candidateStore = cs
	s.initMu.Unlock()
}

func wireStateStore(s *Service, stateStore statePlane) {
	s.initMu.Lock()
	s.stateStore = stateStore
	s.initMu.Unlock()
}

func shouldRegisterRealS1StateWriter(flagCfg cognitivecore.FlagConfig) bool {
	return flagCfg.IsPlugEnabled() && flagCfg.IsSubsystemEnabled("s1")
}

func hasEffectiveStateWriter(writer cognitive.StateWriter) bool {
	if writer == nil {
		return false
	}
	value := reflect.ValueOf(writer)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return !value.IsNil()
	default:
		return true
	}
}

func registerS1StateWriterSubsystem(registry cognitivecore.SubsystemRegistry, writer cognitive.StateWriter) error {
	if !hasEffectiveStateWriter(writer) {
		return s1state.ErrNoWriter
	}
	subsystem := s1state.NewSubsystem(writer)
	if err := registry.Register(subsystem); err != nil {
		return err
	}
	if err := registry.Enable(subsystem.Name()); err != nil {
		return err
	}
	return nil
}

func shouldRegisterRealS2CandidateProposer(flagCfg cognitivecore.FlagConfig) bool {
	return flagCfg.IsPlugEnabled() && flagCfg.IsSubsystemEnabled("s2")
}

func registerS2CandidateProposerSubsystem(registry cognitivecore.SubsystemRegistry, index s2meta.MetaIndex) error {
	if index == nil {
		return s2meta.ErrNoMetaIndex
	}
	subsystem := s2meta.NewSubsystem(index)
	if err := registry.Register(subsystem); err != nil {
		return err
	}
	if err := registry.Enable(subsystem.Name()); err != nil {
		return err
	}
	if err := registry.Disable("core.noop.candidate_proposer"); err != nil {
		return err
	}
	return nil
}

func shouldRegisterRealS3Ambient(flagCfg cognitivecore.FlagConfig) bool {
	return flagCfg.IsPlugEnabled() && flagCfg.IsSubsystemEnabled("s3")
}

func hasEffectiveHintEmitter(emitter cognitive.HintEmitter) bool {
	if emitter == nil {
		return false
	}
	value := reflect.ValueOf(emitter)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return !value.IsNil()
	default:
		return true
	}
}

func registerS3AmbientSubsystem(registry cognitivecore.SubsystemRegistry, emitter cognitive.HintEmitter) error {
	if !hasEffectiveHintEmitter(emitter) {
		return s3ambient.ErrNoEmitter
	}
	subsystem := s3ambient.NewSubsystem(emitter)
	if err := registry.Register(subsystem); err != nil {
		return err
	}
	if err := registry.Enable(subsystem.Name()); err != nil {
		return err
	}
	if err := registry.Disable("core.noop.hint_emitter"); err != nil {
		return err
	}
	return nil
}

func shouldRegisterRealS4ADirectives(flagCfg cognitivecore.FlagConfig) bool {
	return flagCfg.IsPlugEnabled() && flagCfg.IsSubsystemEnabled("s4a")
}

func registerS4ADirectivesSubsystem(registry cognitivecore.SubsystemRegistry, service *s4directives.Service) error {
	if service == nil {
		return s4directives.ErrNoService
	}
	subsystem := s4directives.NewSubsystem(service)
	if err := registry.Register(subsystem); err != nil {
		return err
	}
	if err := registry.Enable(subsystem.Name()); err != nil {
		return err
	}
	if err := registry.Disable("core.noop.attention_event_writer"); err != nil {
		return err
	}
	if err := registry.Disable("core.noop.directive_distiller"); err != nil {
		return err
	}
	return nil
}

func shouldRegisterRealS4BSurfacing(flagCfg cognitivecore.FlagConfig) bool {
	return flagCfg.IsPlugEnabled() && flagCfg.IsSubsystemEnabled("s4b")
}

func hasEffectiveS4BSource(source s4bsurfacing.Source) bool {
	if source == nil {
		return false
	}
	value := reflect.ValueOf(source)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return !value.IsNil()
	default:
		return true
	}
}

func registerS4BSurfacingSubsystem(registry cognitivecore.SubsystemRegistry, source s4bsurfacing.Source) error {
	if !hasEffectiveS4BSource(source) {
		return s4bsurfacing.ErrNoSource
	}
	subsystem := s4bsurfacing.NewSubsystem(source)
	if err := registry.Register(subsystem); err != nil {
		return err
	}
	if err := registry.Enable(subsystem.Name()); err != nil {
		return err
	}
	if err := registry.Disable("core.noop.candidate_proposer"); err != nil {
		return err
	}
	return nil
}

func shouldRegisterRealS5ProductMetrics(flagCfg cognitivecore.FlagConfig) bool {
	return flagCfg.IsPlugEnabled() && flagCfg.IsSubsystemEnabled("s5")
}

func hasEffectiveProductMetricsProvider(provider cognitivecore.ProductMetricsProvider) bool {
	if provider == nil {
		return false
	}
	value := reflect.ValueOf(provider)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return !value.IsNil()
	default:
		return true
	}
}

func registerS5ProductMetricsSubsystem(registry cognitivecore.SubsystemRegistry, provider cognitivecore.ProductMetricsProvider) error {
	if !hasEffectiveProductMetricsProvider(provider) {
		return fmt.Errorf("s5 product metrics provider not configured")
	}
	subsystem, ok := provider.(cognitivecore.Subsystem)
	if !ok {
		return fmt.Errorf("registered ProductMetricsProvider does not satisfy core.Subsystem")
	}
	if err := registry.Register(subsystem); err != nil {
		return err
	}
	if err := registry.Enable(subsystem.Name()); err != nil {
		return err
	}
	return nil
}

func shouldRegisterRealS6OutcomeProposer(flagCfg cognitivecore.FlagConfig) bool {
	return flagCfg.IsPlugEnabled() && flagCfg.IsSubsystemEnabled("s6")
}

func registerS6OutcomeProposerSubsystem(registry cognitivecore.SubsystemRegistry, store s6.OutcomeStore) error {
	if store == nil {
		return fmt.Errorf("s6 outcome store is not configured")
	}
	subsystem := s6.NewSubsystem(store)
	if err := registry.Register(subsystem); err != nil {
		return err
	}
	if err := registry.Enable(subsystem.Name()); err != nil {
		return err
	}
	if err := registry.Disable("core.noop.candidate_proposer"); err != nil {
		return err
	}
	return nil
}

func noopNamesBySubsystem() map[string][]string {
	return map[string][]string{
		"s1":  {"core.noop.state_writer"},
		"s3":  {"core.noop.hint_emitter"},
		"s4a": {"core.noop.attention_event_writer", "core.noop.directive_distiller"},
		"s4b": {},
		"s5":  {},
		"s6":  {},
	}
}

func enableFlaggedCoreNoOps(registry cognitivecore.SubsystemRegistry, flagCfg cognitivecore.FlagConfig) error {
	if !flagCfg.IsPlugEnabled() {
		return nil
	}
	if err := registry.Enable("core.noop.candidate_proposer"); err != nil {
		return fmt.Errorf("enable core.noop.candidate_proposer fallback: %w", err)
	}
	noopsBySubsystem := noopNamesBySubsystem()
	subsystemKeys := make([]string, 0, len(noopsBySubsystem))
	for k := range noopsBySubsystem {
		subsystemKeys = append(subsystemKeys, k)
	}
	sort.Strings(subsystemKeys)
	for _, subName := range subsystemKeys {
		if !flagCfg.IsSubsystemEnabled(subName) {
			continue
		}
		for _, noopName := range noopsBySubsystem[subName] {
			if err := registry.Enable(noopName); err != nil {
				return fmt.Errorf("enable %s (for subsystem %s): %w", noopName, subName, err)
			}
		}
	}
	return nil
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

// cachedCount holds a point-in-time observation count with a wall-clock timestamp
// so callers can apply a TTL check without querying the database on every request.
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

// staleVerifyRequest carries the parameters for a background staleness check.
// It is sent over the buffered staleQueue channel from injection handlers.
type staleVerifyRequest struct {
	cwd           string
	observationID int64
}

// RecentSearchQuery is one entry in the in-process ring buffer of recent semantic
// searches. Exported for the /api/search/recent handler JSON response.
type RecentSearchQuery struct {
	Timestamp time.Time `json:"timestamp"`
	Query     string    `json:"query"`
	Project   string    `json:"project,omitempty"`
	Type      string    `json:"type,omitempty"`
	Results   int       `json:"results"`
}

// setupCallbacks wires session-lifecycle hooks so the SSE broadcaster pushes
// real-time create/delete events to dashboard subscribers. Both callbacks also
// refresh the processing-status banner so the UI queue depth stays current.
func (s *Service) setupCallbacks(mgr *session.Manager) {
	if mgr == nil {
		return
	}

	mgr.SetOnSessionCreated(func(id int64) {
		s.broadcastProcessingStatus()
		s.sseBroadcaster.Broadcast(map[string]any{
			"type":   "session",
			"action": "created",
			"id":     id,
		})
	})

	mgr.SetOnSessionDeleted(func(id int64) {
		s.broadcastProcessingStatus()
		s.sseBroadcaster.Broadcast(map[string]any{
			"type":   "session",
			"action": "deleted",
			"id":     id,
		})
	})
}

// NewService constructs the worker service and returns it ready to call Start.
// Lightweight infra (router, SSE broadcaster, rate limiter, cognitive platform)
// is assembled synchronously so that the health endpoint is immediately
// reachable. All database-dependent work is deferred to initializeAsync, which
// runs in a goroutine. Callers should watch WaitReady or poll /api/ready before
// issuing data-plane requests.
func NewService(version string, logBuffer *logbuf.RingBuffer) (*Service, error) {
	cfg := config.Get()

	// Cancellable root context — cancelled in Shutdown to drain all goroutines.
	ctx, cancel := context.WithCancel(context.Background())

	router := chi.NewRouter()
	sseBroadcaster := sse.NewBroadcaster()

	// Resolve the Claude Code plugin directory so the updater can locate the
	// installed package without relying on a hardcoded absolute path.
	homeDir, _ := os.UserHomeDir()
	installDir := fmt.Sprintf("%s/.claude/plugins/marketplaces/engram", homeDir)

	// 100 req/sec with a burst allowance of 200 keeps interactive CLI usage
	// completely unthrottled while protecting against runaway automation.
	rateLimiter := NewPerClientRateLimiter(100.0, 200)

	tokenAuth, err := NewTokenAuth(config.GetWorkerToken())
	if err != nil {
		cancel()
		return nil, fmt.Errorf("init token auth: %w", err)
	}

	// Cognitive v7 platform: assemble the four CORE primitives and register the
	// NoOps subsystem set before the Service struct is materialised so the
	// fields can be assigned in the struct literal below (no nil-window).
	flagCfg := cognitivecore.LoadFlagConfigFromEnv()
	cMeter := cognitivecore.NewLocalMeter()
	cBus := cognitivecore.NewAttentionEventBus(cMeter)
	cQueue := cognitivecore.NewHintQueue()
	cRegistry := cognitivecore.NewRegistry()
	if err := cognitivecore.RegisterNoOps(cRegistry); err != nil {
		cancel()
		return nil, fmt.Errorf("register cognitive NoOps: %w", err)
	}

	// Inject the CORE-wide Dependencies bundle into the registry so subsystems
	// receive real Bus/Queue/Meter handles at Enable time (PM review finding 3).
	// Type-assert to access SetDependencies — the method lives on the concrete
	// *registry rather than the SubsystemRegistry interface.
	type depsSetter interface {
		SetDependencies(deps cognitivecore.Dependencies)
	}
	if setter, ok := cRegistry.(depsSetter); ok {
		setter.SetDependencies(cognitivecore.Dependencies{
			Registry: cRegistry,
			Bus:      cBus,
			Queue:    cQueue,
			Meter:    cMeter,
			// DB and Logger remain opaque any; downstream subsystems unwrap.
		})
	} else {
		cancel()
		return nil, fmt.Errorf("cognitive registry does not implement SetDependencies — wiring broken")
	}

	// Flag-gated activation (PM re-review finding 1 — spec FR-1 / FR-5 / US1).
	//
	// Spec FR-5 requires "subsystem enabled IFF master flag AND per-subsystem
	// flag". RegisterNoOps provides the shared CORE fallback surfaces; with the
	// master flag on, `core.noop.candidate_proposer` is always enabled as the
	// baseline CandidateProposer fallback, while the per-subsystem loop below only
	// enables additional subsystem-scoped NoOps:
	//
	//	s1  (state)               → core.noop.state_writer
	//	s3  (ambient)             → core.noop.hint_emitter
	//	s4a (directives-capture)  → core.noop.attention_event_writer
	//	                          + core.noop.directive_distiller
	//	(s2 / s4b / s5 / s6 have no extra CORE NoOp toggles at this scope.)
	//
	// Each per-subsystem flag gates whether its extra CORE NoOps are enabled.
	// Other subsystems' NoOps stay in the "registered" state and ResolveImpls
	// returns nothing for their interfaces; the shared candidate_proposer fallback
	// remains active under the master flag until a real proposer replaces it.
	if err := enableFlaggedCoreNoOps(cRegistry, flagCfg); err != nil {
		cancel()
		return nil, err
	}
	if shouldRegisterRealS5ProductMetrics(flagCfg) {
		if err := registerS5ProductMetricsSubsystem(cRegistry, s5.NewProvider(s5.Dependencies{})); err != nil {
			cancel()
			return nil, fmt.Errorf("register real s5 product metrics provider: %w", err)
		}
	}
	if shouldRegisterRealS3Ambient(flagCfg) {
		if err := registerS3AmbientSubsystem(cRegistry, s3ambient.NewEmitter(true)); err != nil {
			cancel()
			return nil, fmt.Errorf("register real s3 ambient subsystem: %w", err)
		}
	}

	// cQueue is the concrete *cognitivecore.hintQueue which satisfies the
	// worker-local lifecycleQueue interface; assert that here so a future
	// signature change in CORE produces a build error rather than a panic.
	cQueueLifecycle, ok := any(cQueue).(lifecycleQueue)
	if !ok {
		cancel()
		return nil, fmt.Errorf("cognitive hint queue does not satisfy lifecycleQueue interface")
	}
	if err := cQueueLifecycle.Start(ctx); err != nil {
		cancel()
		return nil, fmt.Errorf("start cognitive hint queue: %w", err)
	}

	// Assemble the service struct. Fields that require database access are left
	// nil here and populated by initializeAsync under initMu before ready is set.
	svc := &Service{
		version:               version,
		config:                cfg,
		sseBroadcaster:        sseBroadcaster,
		router:                router,
		ctx:                   ctx,
		cancel:                cancel,
		startTime:             time.Now(),
		updater:               update.New(version, installDir),
		retrievalStats:        make(map[string]*RetrievalStats),
		rateLimiter:           rateLimiter,
		tokenAuth:             tokenAuth,
		expensiveOpLimiter:    NewExpensiveOperationLimiter(),
		logBuffer:             logBuffer,
		backfillTracker:       newBackfillTracker(),
		cachedObsCounts:       make(map[string]cachedCount),
		candidateQueueEnabled: candidateQueueEnabledFromEnv(),
		graphEnabled:          graphEnabledFromEnv(),
		temporalTruthEnabled:  temporalTruthEnabledFromEnv(),
		statsCacheTTL:         time.Minute,
		mcpHealth:             mcp.NewMCPHealth(),
		eventBus:              &projectevents.Bus{},
		projectReaperFactory:  defaultProjectReaperFactory,

		cognitiveRegistry:       cRegistry,
		cognitiveMeter:          cMeter,
		cognitiveQueue:          cQueue,
		cognitiveBus:            cBus,
		cognitiveQueueLifecycle: cQueueLifecycle,
		flagConfig:              flagCfg,
	}

	// Routes and middleware are registered synchronously so /health responds
	// immediately without waiting for the database to come up.
	svc.setupMiddleware()
	svc.setupRoutes()

	// Auth startup gate (ADR-0001): validate before starting heavy initialization.
	// Fail fast here so initializeAsync never runs without a token unless explicitly disabled.
	{
		token := config.GetWorkerToken()
		authDisabled := authDisabledFromEnv()
		if token == "" && !authDisabled {
			cancel()
			return nil, fmt.Errorf("ENGRAM_AUTH_ADMIN_TOKEN is not set — set it to secure your engram instance, or set ENGRAM_AUTH_DISABLED=true to explicitly run without authentication (NOT recommended for production)")
		}
	}

	// Kick off heavy initialization in the background. The service is already
	// accepting requests at this point; data-plane routes gate on s.ready.
	svc.initWG.Add(1)
	go func() {
		defer svc.initWG.Done()
		svc.initializeAsync()
	}()
	return svc, nil
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

// initializeAsync runs all database-dependent startup work in a background goroutine.
// On completion it sets s.ready so that data-plane HTTP and gRPC handlers unblock.
// On any fatal error it calls setInitError which surfaces through /api/health.
func (s *Service) initializeAsync() {
	log.Info().Msg("background init: starting")
	if s.ctx.Err() != nil {
		return
	}

	// Verify data directory layout and settings file presence before the first DB dial.
	if err := config.EnsureAll(); err != nil {
		s.setInitError(fmt.Errorf("ensure data dir: %w", err))
		return
	}

	// Open the PostgreSQL connection pool and run pending schema migrations.
	store, err := gorm.NewStore(gorm.Config{
		DSN:      s.config.DatabaseDSN,
		MaxConns: s.config.DatabaseMaxConns,
	})
	if err != nil {
		s.setInitError(fmt.Errorf("init database: %w", err))
		return
	}
	if s.ctx.Err() != nil {
		_ = store.Close()
		return
	}

	// Thin store wrappers that scope queries to their respective tables.
	sessionStore := gorm.NewSessionStore(store)

	// Session manager owns active-session state and the queue notification channel.
	sessionManager := session.NewManager(sessionStore)

	// SDK processor pipelines raw tool observations into memories.
	processor := sdk.NewProcessor()
	processor.SetBroadcastFunc(func(event map[string]any) {
		s.sseBroadcaster.Broadcast(event)
	})

	// Create token store and wire into auth middleware
	tokenStore := gorm.NewTokenStore(store)

	// Create auth stores for email/password dashboard authentication (T007-T009).
	userStore := gorm.NewUserStore(store.DB)
	invitationStore := gorm.NewInvitationStore(store.DB)
	authSessionStore := gorm.NewAuthSessionStore(store.DB)

	// Create injection log store for vNext injection tracking (migration 106).
	// CR-1 (provenance-cleanup): the legacy InjectionStore (observation_injections)
	// was removed — injection_log is now the sole injection-record sink.
	injectionLogStore := gorm.NewInjectionLogStore(store)

	// Create citation log store for vNext Phase A citation tracking (migration 107).
	citationLogStore := gorm.NewCitationLogStore(store)

	// Create issue store for cross-project agent issues
	issueStore := gorm.NewIssueStore(store.GetDB())

	// Create memory + behavioral rules + credential stores for US3 observations split.
	// All three stores are wired here (Commit E — T021).
	memoryStore := gorm.NewMemoryStore(store)
	behavioralRulesStore := gorm.NewBehavioralRulesStore(store)
	if shouldRegisterRealS2CandidateProposer(s.flagConfig) {
		if err := registerS2CandidateProposerSubsystem(s.cognitiveRegistry, memoryStore); err != nil {
			s.setInitError(fmt.Errorf("register real s2 candidate proposer: %w", err))
			return
		}
	}
	if shouldRegisterRealS6OutcomeProposer(s.flagConfig) {
		if err := registerS6OutcomeProposerSubsystem(s.cognitiveRegistry, s6.NewMemoryStoreAdapter(memoryStore)); err != nil {
			s.setInitError(fmt.Errorf("register real s6 outcome proposer: %w", err))
			return
		}
	}
	credentialStore := gorm.NewCredentialStore(store)

	// Create feedback updater for vNext Phase A closed-loop learning.
	feedbackUpdater := feedback.NewUpdater(memoryStore)

	// Create audit store for Milestone D audit trail (FR-D2 / NFR-D4).
	auditStore := gorm.NewAuditStore(store.GetDB())
	stateStore := gorm.NewStateStore(store.GetDB(), auditStore)
	statePlaneSvc := stateplane.NewService(stateStore, nil)
	principalMemoryQuerySvc := principalmemory.NewPrincipalMemoryQueryService(memoryStore, auditStore)
	temporalTruthStore := gorm.NewTemporalTruthStore(store.GetDB())
	temporalTruthProvider := newMemoryTemporalTruthProvider(temporalTruthStore, principalMemoryQuerySvc)
	experienceProvider := newMemoryExperienceProvider(principalMemoryQuerySvc)
	domainOwnerStore := gorm.NewDomainOwnerStore(store)
	domainRegistrySvc := principalmemory.NewDomainRegistryService(domainOwnerStore, auditStore)
	attentionEventStore := gorm.NewAttentionEventStore(store.GetDB())
	if shouldRegisterRealS4BSurfacing(s.flagConfig) {
		if err := registerS4BSurfacingSubsystem(s.cognitiveRegistry, attentionEventStore); err != nil {
			s.setInitError(fmt.Errorf("register real s4b directive surfacing subsystem: %w", err))
			return
		}
	}
	directiveCaptureSvc := s4directives.NewService(attentionEventStore)
	if shouldRegisterRealS4ADirectives(s.flagConfig) {
		if err := registerS4ADirectivesSubsystem(s.cognitiveRegistry, directiveCaptureSvc); err != nil {
			s.setInitError(fmt.Errorf("register real s4a directives subsystem: %w", err))
			return
		}
	}
	if shouldRegisterRealS1StateWriter(s.flagConfig) {
		if err := registerS1StateWriterSubsystem(s.cognitiveRegistry, statePlaneSvc); err != nil {
			s.setInitError(fmt.Errorf("register real s1 state writer: %w", err))
			return
		}
	}
	wireStateStore(s, statePlaneSvc)

	// Create purge store for Milestone D project-level hard deletion (T008).
	// Gated behind ENGRAM_VNEXT_ENABLED: purge_project is a vnext action (Milestone D).
	var purgeStore *gorm.PurgeStore
	if os.Getenv("ENGRAM_VNEXT_ENABLED") == "true" {
		purgeStore = gorm.NewPurgeStore(store)
	}

	// Create transcript store for T003 session-end persistence (always created;
	// the handler goroutine is gated by isCrystallizationEnabled() at call time).
	transcriptStore := gorm.NewTranscriptStore(store.GetDB())

	// Publish all store handles under initMu so downstream code that inspects
	// them (e.g., handler middleware) sees a consistent snapshot once ready fires.
	// Dedup config was removed in v5 (US11) — the SDK processor now uses fixed defaults.
	s.initMu.Lock()
	s.store = store
	s.sessionStore = sessionStore
	s.injectionLogStore = injectionLogStore
	s.citationLogStore = citationLogStore
	s.injectionTracker = injection.NewTracker(injectionLogStore)
	s.issueStore = issueStore
	s.credentialStore = credentialStore
	s.memoryStore = memoryStore
	s.experienceProvider = experienceProvider
	s.temporalTruthProvider = temporalTruthProvider
	s.principalMemoryQueryService = principalMemoryQuerySvc
	s.domainOwnerStore = domainOwnerStore
	s.domainRegistryService = domainRegistrySvc
	s.behavioralRulesStore = behavioralRulesStore
	s.feedbackUpdater = feedbackUpdater
	s.auditStore = auditStore
	s.purgeStore = purgeStore
	s.transcriptStore = transcriptStore
	s.tokenStore = tokenStore
	s.sessionManager = sessionManager
	s.processor = processor
	s.initMu.Unlock()

	// Wire crystallization candidate storage only when the candidate flag is enabled.
	// The dream cycle additionally checks both flags and writer availability before
	// it reads transcripts or constructs an extractor.
	if os.Getenv("ENGRAM_VNEXT_F_ENABLED") == "true" {
		candidateStore := gorm.NewCandidateStore(store.GetDB(), auditStore)
		s.SetCandidateStore(candidateStore)

		// TG6 — snapshot store for bulk-op rollback and auto-pruning (T043/T049).
		snapshotStore := gorm.NewSnapshotStore(store.GetDB())
		s.initMu.Lock()
		s.snapshotStore = snapshotStore
		s.initMu.Unlock()

		// TG5 — redaction layer (ADR-F-004, EC-F9: startup-only, no hot-reload).
		// Rules are compiled once here; any rule-change requires a process restart.
		rulesPath := os.Getenv("ENGRAM_REDACTION_RULES_PATH")
		compiledRules, rErr := redaction.LoadRulesFromPath(rulesPath)
		if rErr != nil {
			log.Warn().Err(rErr).Str("path", rulesPath).Msg("redaction: failed to load rules, layer disabled")
		} else if len(compiledRules) > 0 {
			log.Info().Int("rules", len(compiledRules)).Str("path", rulesPath).Msg("redaction: rules loaded")
		}
		s.initMu.Lock()
		s.redactionRules = compiledRules
		s.initMu.Unlock()

		// TG5 — write-lint TokenStore.
		// Parse ENGRAM_WRITE_LINT_TOKEN_TTL_SEC; fall back to default 600s.
		tsCfg := writelint.DefaultTokenStoreConfig()
		if ttlStr := os.Getenv("ENGRAM_WRITE_LINT_TOKEN_TTL_SEC"); ttlStr != "" {
			if ttlSec := parseInt64Env(ttlStr, 600); ttlSec > 0 {
				tsCfg.TTL = time.Duration(ttlSec) * time.Second
			}
		}
		ts := writelint.NewTokenStore(tsCfg)
		s.initMu.Lock()
		s.writelintTokenStore = ts
		s.initMu.Unlock()
		// Orchestrator is wired further below after graphStore is constructed
		// (wireVnextF is called after wireVnextStores sets s.graphStore).
	}

	// Wire token store into auth middleware for client token lookups.
	// Also wire the shared *auth.Validator (constructed below alongside the
	// gRPC server) so HTTP and gRPC share one validation chain (FR-2).
	if s.tokenAuth != nil {
		s.tokenAuth.SetTokenStore(tokenStore)
	}

	// Wire email/password auth stores into TokenAuth middleware and create AuthHandlers.
	authHandlersInstance := NewAuthHandlers(userStore, invitationStore, authSessionStore, domainOwnerStore)
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

	// Setup callbacks on stores and processors
	s.setupCallbacks(sessionManager)

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

	// Initialize MCP server and SSE handler (serves /sse and /message on the worker port)
	// Create document store for collection MCP tools
	documentStore := gorm.NewDocumentStore(store)

	// Create chunking manager for document ingestion
	chunkManager := s.createChunkManager()

	// Create versioned document store for collaborative document MCP tools (migration 051).
	versionedDocumentStore := gorm.NewVersionedDocumentStore(store)
	booksStore := gorm.NewBooksStore(store)
	booksPipeline := booksdomain.NewPipeline(booksStore, versionedDocumentStore)

	mcpServer := mcp.NewServer(mcp.ServerOptions{
		Version:            s.version,
		SessionStore:       sessionStore,
		CollectionRegistry: collectionRegistry,
		DocumentStore:      documentStore,
		ChunkManager:       chunkManager,
	})

	// Wire versioned document store into MCP server for collaborative document tools.
	mcpServer.SetVersionedDocumentStore(versionedDocumentStore)
	s.initMu.Lock()
	s.documentStore = versionedDocumentStore
	s.booksStore = booksStore
	s.booksPipeline = booksPipeline
	s.initMu.Unlock()

	mcpServer.SetIssueStore(issueStore)

	// Wire memory + behavioral rules stores (US3 Commit C).
	// These power the new static-entity MCP tools store_rule / list_rules and
	// will be used by Commit E when handleStoreMemory / handleRecall are
	// switched from observations to memories/behavioral_rules.
	mcpServer.SetMemoryStore(memoryStore)
	mcpServer.SetMetaMemoryIndex(memoryStore)
	mcpServer.SetPrincipalMemoryQueryService(principalMemoryQuerySvc)
	mcpServer.SetDomainRegistryService(domainRegistrySvc)
	mcpServer.SetBehavioralRulesStore(behavioralRulesStore)
	mcpServer.SetStateStore(statePlaneSvc)
	mcpServer.SetExperienceProvider(experienceProvider)
	mcpServer.SetTemporalTruthProvider(temporalTruthProvider)
	mcpServer.SetDirectiveCaptureService(directiveCaptureSvc)
	if shouldRegisterRealS3Ambient(s.flagConfig) {
		mcpServer.SetHintQueue(s.cognitiveQueue)
	}

	// Wire the raw DB handle so handleGetMemoryStats can run injection_log /
	// citation_log / memories-by-status raw SQL queries. Uses the same shared
	// *gorm.DB the service already holds to reuse the connection pool.
	mcpServer.SetStatsDB(store.GetDB())

	// Wire purge store for the purge_project admin action (Milestone D T008).
	// Gated behind ENGRAM_VNEXT_ENABLED: mirrors wireVnextStores pattern.
	if os.Getenv("ENGRAM_VNEXT_ENABLED") == "true" {
		mcpServer.SetPurgeStore(purgeStore)
	}

	// Wire the shared candidateStore into the MCP server (Milestone-F TG4 T026).
	// Re-use the instance already wired above (lines ~584-587) to avoid a second
	// allocation. Gated on ENGRAM_VNEXT_F_ENABLED; the MCP handler gates individual
	// tool calls with the same check so schema + runtime are consistent.
	s.initMu.RLock()
	existingCandidateStore := s.candidateStore
	s.initMu.RUnlock()
	if existingCandidateStore != nil {
		mcpServer.SetCandidateStore(existingCandidateStore)
	}

	// Wire promotion, graph, and audit stores into the MCP server and record them on
	// the Service for the sleep cycle goroutine and audit logging. Extracted into
	// wireVnextStores so the wiring is testable without a full service initialisation.
	promotionStore := gorm.NewPromotionStore(store.GetDB())
	nodesStore := graph.NewNodesStore(store.GetDB())
	graphStore := graph.NewStore(store.GetDB(), nodesStore)
	continuitySlotStore := gorm.NewContinuitySlotStore(store.GetDB())
	wireVnextStores(mcpServer, promotionStore, graphStore, nodesStore, auditStore, continuitySlotStore)
	s.initMu.Lock()
	s.promotionStore = promotionStore
	s.graphStore = graphStore
	s.graphNodeStore = nodesStore
	s.initMu.Unlock()

	// TG5: Wire write-lint orchestrator + redaction rules into MCP server.
	// Must happen after wireVnextStores so graphStore is fully constructed.
	s.initMu.RLock()
	wlTS := s.writelintTokenStore
	wlRedRules := s.redactionRules
	wlCandidateStore := s.candidateStore
	s.initMu.RUnlock()
	wireVnextF(mcpServer, memoryStore, auditStore, wlTS, graphStore, wlCandidateStore, wlRedRules)

	// TG6 — wire snapshot governance tools into the MCP server (T043/T044).
	// snapshotStore was created in initializeAsync when ENGRAM_VNEXT_F_ENABLED=true.
	// Both SetSnapshotStore and SetBulkFacade are required: ListTools gates on
	// snapshotStore != nil, and non-dry-run bulk_* calls route through the facade.
	s.initMu.RLock()
	existingSnapshotStore := s.snapshotStore
	s.initMu.RUnlock()
	if existingSnapshotStore != nil {
		mcpServer.SetSnapshotStore(existingSnapshotStore)
		bulkFacade := bulkops.NewFacade(existingSnapshotStore, existingCandidateStore, memoryStore, auditStore)
		mcpServer.SetBulkFacade(bulkFacade)
	}

	// CR-2/CR-3 (#259): the embedder and reranker resolve their config with env-first
	// precedence backed by the settings-store. Env wins when set; otherwise the store value;
	// otherwise the client's default. Non-secret config (URL, model) is read as plaintext;
	// secret config (API keys) is stored encrypted and decrypted in-process via the vault
	// (CR-3). The vault provider is lazy (s.getVault) so a deployment with no secret settings
	// never forces vault-key initialization. Decryption is fail-soft → env/default.
	//
	// The SettingsStore wraps the already-open *gorm.Store (cheap wrapper, no new pool). The
	// same instance is shared with the MCP settings tool below so neither path opens its own
	// connection pool.
	settingsStore := gorm.NewSettingsStore(store)
	settingsRes := newSettingsResolver(settingsStore, s.getVault)
	mcpServer.SetSettingsStore(settingsStore)

	// CR-4 (#259): one-time idempotent backfill of legacy model-config env vars into the
	// settings-store. Seeds a store key only when the env var is set AND the key is absent —
	// never clobbers an operator-set value. Lets an operator migrate off env vars at leisure.
	// Runs before the clients init below so a freshly-seeded value is available this boot.
	migrateEnvToSettingsStore(s.ctx, settingsStore, s.getVault)

	// Initialize embedding client and store (optional — disabled if ENGRAM_EMBEDDING_URL unset).
	embClient, embErr := embedding.NewClientWithSettings(s.ctx, settingsRes)
	if embErr != nil {
		if errors.Is(embErr, embedding.ErrEmbeddingDisabled) {
			log.Info().Msg("embedding: disabled (ENGRAM_EMBEDDING_URL not set)")
		} else {
			log.Warn().Err(embErr).Msg("embedding: failed to initialize client")
		}
	} else if dimErr := embedding.AssertEmbeddingDimensions(s.ctx, store.GetDB()); dimErr != nil {
		// Synchrony-by-construction: a live vector column whose dimension disagrees
		// with the EmbeddingDim SSOT means schema/constant drift. Persisting vectors
		// would corrupt search or fail at INSERT, so DISABLE the embedding path with a
		// fatal-config log rather than start it on a mismatched schema. The rest of the
		// server still runs (embedding is optional); recall degrades to FTS-only.
		log.Error().Err(dimErr).Msg("embedding: dimension assert failed — embedding path DISABLED (fix schema or EmbeddingDim)")
	} else {
		embStore := embedding.NewStore(store.GetDB())
		embRec := &embedding.BackfillRecorder{}
		mcpServer.SetEmbeddingStores(embClient, embStore)
		go func() {
			if bfErr := embedding.Backfill(s.ctx, store.GetDB(), embClient, embStore, 50, embRec); bfErr != nil {
				log.Warn().Err(bfErr).Msg("embedding backfill: stopped")
			}
		}()
		// CR-004: code chunk embedding backfill — mirrors the memory backfill above.
		// Reuses the same embClient and embRec. Telemetry trade-off: /api/stats/vnext
		// surfaces exactly one BackfillRecorder (s.embeddingRecorder), so the code
		// and memory pipelines DELIBERATELY share it — their success/failure counts
		// and last_error aggregate. A separate code recorder would currently be
		// unsurfaced (invisible counters), which is worse than aggregation; splitting
		// them is a follow-up that must also add a labeled stats field (out of
		// CR-004's embed-pipeline scope).
		// Gated by this else-branch so it is a no-op when ENGRAM_EMBEDDING_URL is unset (flag-dark).
		go func() {
			cbStore := gorm.NewCodeChunkStore(store.GetDB())
			if cbErr := embedding.CodeBackfill(s.ctx, cbStore, embClient, 50, embRec); cbErr != nil {
				log.Warn().Err(cbErr).Msg("code embedding backfill: stopped")
			}
		}()

		s.initMu.Lock()
		s.embeddingClient = embClient
		s.embeddingStore = embStore
		s.embeddingRecorder = embRec
		s.initMu.Unlock()
	}

	// Rank-4: initialize the cross-encoder rerank client (optional — disabled if
	// ENGRAM_RERANK_URL unset). Independent of embedding: the reranker reorders the
	// fused candidate pool on the recall path and works whether or not the vector leg
	// is enabled. When disabled, recall keeps the fusion order (failure-silent).
	rerankClient, rerankErr := reranking.NewClientWithSettings(s.ctx, settingsRes)
	if rerankErr != nil {
		if errors.Is(rerankErr, reranking.ErrRerankDisabled) {
			log.Info().Msg("reranking: disabled (ENGRAM_RERANK_URL not set)")
		} else {
			log.Warn().Err(rerankErr).Msg("reranking: failed to initialize client")
		}
	} else {
		mcpServer.SetRerankClient(rerankClient)
		s.initMu.Lock()
		s.rerankClient = rerankClient
		s.initMu.Unlock()
		log.Info().Str("model", rerankClient.Model()).Msg("reranking: cross-encoder rerank enabled on recall path")
	}

	segmentStore := gorm.NewSegmentStore(store)
	s.initMu.Lock()
	s.segmentStore = segmentStore
	s.initMu.Unlock()

	// Wire code intelligence store into MCP server (CR-006).
	// Gated on ENGRAM_CODE_INTEL_ENABLED=true; flag-off leaves codeChunkStore nil
	// so tools/list is byte-identical to pre-CR-006 when the flag is off.
	if os.Getenv("ENGRAM_CODE_INTEL_ENABLED") == "true" {
		codeChunkStore := gorm.NewCodeChunkStore(store.GetDB())
		mcpServer.SetCodeChunkStore(codeChunkStore)
	}

	// Wire gRPC server: create adapter over mcpServer and register with the server.
	// initMu protects s.grpcServer — the cmux goroutine polls for it.
	//
	// Auth (FR-2 / Plan ADR-002): build the shared *auth.Validator from the
	// operator key (server-host env) and the freshly-constructed tokenStore,
	// then pass it to grpcserver.New. The same validator backs the HTTP
	// middleware in Phase 3; until then HTTP keeps its inline check.
	//
	// Pass nil validator when ENGRAM_AUTH_DISABLED is the deliberate
	// operator choice (mirrors the empty-token branch the previous code took).
	adapter := &mcpHandlerAdapter{mcpServer: mcpServer}
	var grpcValidator *auth.Validator
	if !authDisabledFromEnv() {
		grpcValidator = auth.NewValidator(config.GetWorkerToken(), tokenStore)
	}
	// Share the validator with the HTTP middleware (FR-2: symmetric validation).
	if s.tokenAuth != nil && grpcValidator != nil {
		s.tokenAuth.SetValidator(grpcValidator)
	}
	grpcSrv, grpcInternalSrv := grpcserver.New(adapter, grpcValidator)
	grpcInternalSrv.SetDB(store.DB)
	grpcInternalSrv.SetBus(s.eventBus)
	s.initMu.Lock()
	s.grpcServer = grpcSrv
	s.grpcInternalServer = grpcInternalSrv
	s.initMu.Unlock()

	s.initMu.Lock()
	s.collectionRegistry = collectionRegistry
	s.sessionIdxStore = sessionIdxStore
	s.searchQueryLogStore = searchQueryLogStore
	s.retrievalStatsLogStore = retrievalStatsLogStore
	s.initMu.Unlock()

	// Start project reaper (hourly cleanup of hard-expired soft-deleted projects).
	if err := s.initializeProjectReaper(store); err != nil {
		s.setInitError(err)
		return
	}

	// Start retention cron for injection_log and citation_log cleanup.
	// CR-1 (provenance-cleanup, PR #272 review): injection_log + citation_log are now
	// written on EVERY session regardless of ENGRAM_VNEXT_ENABLED (the flag only selects
	// the response algorithm), so retention must run unconditionally — otherwise flag-off
	// (default) deployments grow these append-only tables without the promised cleanup.
	s.startRetentionCron(s.ctx)

	// Start sleep cycle goroutine when lifecycle is enabled (milestone-B T014).
	// Trigger conditions per T014 AC: >=10 new memories since last cycle (tracked
	// via watermark with CountActiveSince) AND >=4h idle (no HTTP/MCP requests).
	// See sleep_cycle.go for implementation details.
	if os.Getenv("ENGRAM_LIFECYCLE_ENABLED") == "true" {
		s.startSleepCycle(s.ctx)
	}

	// Start the observation queue processor only when the SDK processor is wired.
	// processQueue blocks on ProcessNotify or a fallback ticker — both are
	// non-nil at this point, but guard the allocation explicitly.
	if processor != nil {
		s.wg.Add(1)
		go s.processQueue()
	}

	// Critical initialization has completed, including installation of the
	// project reaper that owns stale-project cleanup. Watchers remain nonfatal
	// and may start after readiness is published.
	if err := s.publishReady(); err != nil {
		s.setInitError(fmt.Errorf("publish readiness: %w", err))
		return
	}
	log.Info().Msg("background init: complete, service ready")

	// Watch config and database files for external changes.
	s.startWatchers()
}

func (s *Service) initializeProjectReaper(store *gorm.Store) error {
	factory := s.projectReaperFactory
	if factory == nil {
		factory = defaultProjectReaperFactory
	}
	projectReaper, err := factory(store)
	if err != nil {
		return fmt.Errorf("init project reaper: %w", err)
	}
	if projectReaper == nil {
		return errors.New("init project reaper: factory returned nil")
	}
	s.initMu.Lock()
	s.projectReaper = projectReaper
	s.initMu.Unlock()
	projectReaper.Start(s.ctx)
	return nil
}

func (s *Service) publishReady() error {
	s.initMu.RLock()
	projectReaperInstalled := s.projectReaper != nil
	s.initMu.RUnlock()
	if !projectReaperInstalled {
		return errors.New("project reaper is not initialized")
	}
	s.ready.Store(true)
	return nil
}

// startWatchers registers filesystem notification handlers for config hot-reload.
// Database-file watching is not applicable for PostgreSQL (server-managed file).
func (s *Service) startWatchers() {
	configPath := config.SettingsPath()
	cw, err := watcher.New(configPath, func() {
		log.Warn().Str("path", configPath).Msg("config file modified — triggering hot-reload")
		s.reloadConfig()
	})
	if err != nil {
		log.Warn().Err(err).Str("path", configPath).Msg("config watcher: init failed, hot-reload disabled")
		return
	}
	if err := cw.Start(); err != nil {
		log.Warn().Err(err).Str("path", configPath).Msg("config watcher: start failed, hot-reload disabled")
		return
	}
	s.configWatcher = cw
	log.Info().Str("path", configPath).Msg("config watcher active")
}

// reloadConfig hot-reloads configuration from disk without process restart.
// Uses config.Reload() to atomically swap the global config. Services that
// call config.Get() per-request will pick up new values automatically.
// Structural changes (port, token) log a warning — manual restart needed.
func (s *Service) reloadConfig() {
	_, changed, err := s.applyConfigReload()
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

func (s *Service) applyConfigReload() (*config.Config, []string, error) {
	newCfg, changed, err := config.Reload()
	if err != nil {
		return nil, nil, err
	}

	s.initMu.Lock()
	s.config = newCfg
	s.initMu.Unlock()

	return newCfg, changed, nil
}

// isCrystallizationEnabled reports whether the crystallization pipeline is
// active for this process. Reads ENGRAM_CRYSTALLIZATION_ENABLED at call time
// so that test code can override it via t.Setenv without restart.
func isCrystallizationEnabled() bool {
	return os.Getenv("ENGRAM_CRYSTALLIZATION_ENABLED") == "true"
}

// setInitError stores a fatal startup error and logs it. Once set, the error
// is visible through GetInitError and surfaced by the /api/health handler.
// Called only from initializeAsync — never after ready is true.
func (s *Service) setInitError(err error) {
	s.initMu.Lock()
	s.initError = err
	s.initMu.Unlock()
	log.Error().Err(err).Msg("background init: fatal error")
}

// GetInitError returns any error recorded during background initialization.
// Returns nil when initialization completed successfully or is still in progress.
func (s *Service) GetInitError() error {
	s.initMu.RLock()
	defer s.initMu.RUnlock()
	return s.initError
}

// queueStaleVerification sends a staleness-check request to the background
// processor. The channel is initialised lazily on the first call. If the
// channel is full the request is silently discarded — callers must not block
// on delivery.
func (s *Service) queueStaleVerification(observationID int64, cwd string) {
	s.staleQueueOnce.Do(func() {
		s.staleQueue = make(chan staleVerifyRequest, StaleQueueSize)
		s.wg.Add(1)
		go s.processStaleQueue()
	})

	select {
	case s.staleQueue <- staleVerifyRequest{observationID: observationID, cwd: cwd}:
		// accepted
	default:
		log.Debug().Int64("id", observationID).Msg("stale-verify queue full, request dropped")
	}
}

// processStaleQueue drains staleQueue, dispatching each request to
// verifyStaleObservation until the service context is cancelled.
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

// verifyStaleObservation handles one background staleness-check request.
// Observation-era stale verification was retired in v5; this is a no-op
// retained so the queue infrastructure remains functional for future use.
func (s *Service) verifyStaleObservation(req staleVerifyRequest) {
	if !s.ready.Load() {
		return
	}
	_ = req // retired in v5
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

// setupMiddleware registers global HTTP middleware on the router.
// Order is intentional — each layer depends on the output of the layers above it:
//
//  1. RequestID     — attach a trace ID before anything else logs
//  2. requestActivity — record last-request timestamp for the sleep-cycle idle gate
//  3. debugRequestLogger — structured log line per request
//  4. Recoverer     — catch panics from all downstream handlers
//  5. RealIP        — unwrap X-Forwarded-For before rate-limit keying
//  6. SecurityHeaders — X-Frame-Options, HSTS, CSP
//  7. MaxBodySize   — 10 MB cap prevents DoS via oversized payloads
//  8. RequireJSONContentType — enforce Content-Type on mutating requests
//  9. Compress(5)   — gzip responses; level 5 balances latency vs ratio
//  10. Rate limiter  — per-client token bucket (after RealIP for accurate keying)
//  11. TokenAuth     — bearer-token or session-cookie validation
//
// Timeout middleware is not applied globally because SSE connections need
// an unbounded write lifetime. Routes that require timeouts apply them individually.
func (s *Service) setupMiddleware() {
	s.router.Use(RequestID)
	s.router.Use(s.requestActivityMiddleware)
	s.router.Use(debugRequestLogger)
	s.router.Use(middleware.Recoverer)
	s.router.Use(middleware.RealIP)
	s.router.Use(SecurityHeaders)
	s.router.Use(MaxBodySize(10 * 1024 * 1024))
	s.router.Use(RequireJSONContentType)
	s.router.Use(middleware.Compress(5))
	if s.rateLimiter != nil {
		s.router.Use(PerClientRateLimitMiddleware(s.rateLimiter))
	}
	if s.tokenAuth != nil {
		s.router.Use(s.tokenAuth.Middleware)
	}
}

// setupRoutes registers all HTTP routes on the router.
//
// Route groups:
//   - Public (no auth):  auth login/logout, setup, registration
//   - Pre-ready:         health, version, readiness, update, restart, SSE, logs
//   - DB-ready group:    all data-plane endpoints — gated by requireReady
//
// The pre-ready group is intentionally ungated so that the health hook,
// update checks, and dashboard SSE stream work before the database is available.
func (s *Service) setupRoutes() {
	// Dashboard static assets served from the embedded filesystem.
	s.router.Get("/", serveIndex)
	s.router.Get("/_nuxt/*", serveAssets)
	s.router.Get("/_fonts/*", serveAssets)
	s.router.Get("/i18n/*", serveAssets)
	s.router.Get("/assets/*", serveAssets)
	s.router.Get("/branding/*", serveAssets)
	s.router.Get("/favicon.svg", serveAssets)

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

	// Health returns 200 as soon as the process starts; hooks rely on this
	// for the initial connection handshake before the database is ready.
	// Both paths exist for backward compatibility with older hook versions.
	// /health predates the operator console and remains a machine JSON probe by
	// default. Browsers explicitly asking for HTML get the SPA route instead.
	s.router.Get("/health", s.handleHealthRoute)
	s.router.Get("/api/health", s.handleHealth)

	// Version lets hooks detect a stale worker process after an update.
	s.router.Get("/api/version", s.handleVersion)

	// Ready returns 200 only once initializeAsync has completed successfully.
	s.router.Get("/api/ready", s.handleReady)

	// MCP health counters (public — no auth required, lightweight)
	s.router.Get("/api/mcp/health", s.mcpHealth.HandleHealth)

	// OpenAPI docs (read-only spec; protected by global auth middleware if ENGRAM_AUTH_ADMIN_TOKEN is set)
	s.router.Get("/api/docs", http.RedirectHandler("/api/docs/index.html", http.StatusMovedPermanently).ServeHTTP)
	s.router.Get("/api/docs/*", httpSwagger.WrapHandler)

	// Admin/management routes — authentication applied globally via setupMiddleware.
	// Grouped for logical organization; no additional middleware needed.
	s.router.Group(func(r chi.Router) {
		// Vector metrics/health endpoints — report live pgvector subsystem status
		r.Get("/api/vectors/health", s.handleVectorHealth)
		r.Get("/api/vector/metrics", s.handleVectorMetrics)

		// Update endpoints (work before DB is ready)
		r.Get("/api/update/check", s.handleUpdateCheck)
		r.Post("/api/update/apply", s.handleUpdateApply)
		r.Get("/api/update/status", s.handleUpdateStatus)
		r.Post("/api/update/restart", s.handleUpdateRestart)

		// General restart endpoint (works before DB is ready)
		r.Post("/api/restart", s.handleRestart)

		// Selfcheck endpoint (works before DB is ready - checks all components)
		r.Get("/api/selfcheck", s.handleSelfCheck)

		// Runtime feature flags (read-only current-process snapshot; works before DB is ready)
		r.Get("/api/flags", s.handleGetFlags)

		// Migration state (read-only DB bookkeeping; returns 503 until DB is ready)
		r.Get("/api/migrations", s.handleGetMigrations)

		// Config management is file-backed, so recovery settings remain available before DB readiness.
		r.Get("/api/config", s.handleGetConfig)
		r.Patch("/api/config", s.handlePatchConfig)

		// Dashboard SSE endpoint (works before DB is ready)
		r.Get("/api/events", s.sseBroadcaster.HandleSSE)

		// Log viewer endpoint (works before DB is ready, supports SSE follow mode)
		r.Get("/api/logs", s.handleGetLogs)

		// Instinct import endpoint
		r.Post("/api/instincts/import", s.handleInstinctsImport)

		// Backfill endpoints
		r.Post("/api/backfill/session", s.handleBackfillSession)
		r.Get("/api/backfill/status", s.handleBackfillStatus)
		r.Post("/api/import/feedback", s.handleImportFeedback)

		// Cognitive v7 stats endpoints (FR-8 / Clarify C1).
		// Routes inherit the auth middleware applied at the Group level.
		// Per-handler enforcement rejects ENGRAM_AUTH_ADMIN_TOKEN callers
		// (operator key) with 403; workstation keycards and session cookies
		// proceed. The handlers themselves do not require DB readiness — they
		// only read in-memory CORE platform state.
		r.Get("/api/stats/v7/subsystems", s.handleStatsV7Subsystems)
		r.Get("/api/stats/v7/substrate", s.handleStatsV7Substrate)
		r.Get("/api/stats/v7/product", s.handleStatsV7Product)
	})

	// OpenAI-compatible model list endpoint. Intentionally outside requireReady group:
	// the model registry is populated at init() time (before DB is ready), so this
	// endpoint is always available and useful for LiteLLM proxy configuration.
	s.router.Get("/v1/models", s.handleListModels)

	// All routes below require database readiness.
	// requireReady returns 503 with a descriptive body until initializeAsync completes.
	s.router.Group(func(r chi.Router) {
		r.Use(s.requireReady)
		r.Use(middleware.Timeout(DefaultHTTPTimeout))

		// Session lifecycle
		r.Post("/api/sessions/init", s.handleSessionInit)
		r.Get("/api/sessions/list", s.handleListSessions)
		r.Get("/api/sessions", s.handleGetSessionByClaudeID)
		r.Post("/api/sessions/{id}/init", s.handleSessionStart)
		r.Post("/api/sessions/subagent-complete", s.handleSubagentComplete)
		r.Post("/api/sessions/{id}/summarize", s.handleSummarize)

		// Session transcript indexing (client pushes JSONL for FTS)
		r.Post("/api/sessions/index", s.handleIndexSession)
		r.Post("/api/sessions/check", s.handleCheckSessions)

		// Hook callbacks from Claude Code stop/session-end hooks
		r.Post("/api/hooks/session-end", s.handleSessionEnd)

		// Adaptive memory endpoints (gated by ENGRAM_ADAPTIVE_ENABLED)
		r.Post("/api/context/reinject", s.handleReinject)
		r.Get("/api/context/reinject", s.handleReinject)
		r.Post("/api/hooks/correction", s.handleCorrection)
		r.Post("/api/hooks/code-extraction", s.handleCodeExtraction)
		r.Post("/api/hooks/segment-check", s.handleSegmentCheck)
		if shouldRegisterRealS3Ambient(s.flagConfig) {
			r.Post("/api/hooks/ambient-candidates", s.handleAmbientCandidates)
		}

		// Event ingest (Level 0 deterministic pipeline)
		r.Post("/api/events/ingest", s.handleIngestEvent)

		// Observation and project data
		r.Get("/api/observations", s.handleGetObservations)
		r.Get("/api/projects", s.handleGetProjects)
		r.Delete("/api/projects/{id}", s.handleDeleteProject)
		r.Get("/api/stats", s.handleGetStats)
		r.Get("/api/stats/retrieval", s.handleGetRetrievalStats)
		r.Get("/api/stats/vnext", s.handleStatsVnext)
		r.Get("/api/types", s.handleGetTypes)
		r.Get("/api/models", s.handleGetModels)
		r.Get("/api/model-health", s.handleModelHealth)
		r.Get("/api/state/session/{sessionID}", s.handleGetStateSession)
		r.Get("/api/state/project/{project}", s.handleGetStateProject)
		r.Get("/api/state/resume", s.handleGetStateResume)

		// Experience/history read surface (CR-009) — read-only, bounded, archive-trigger-gated.
		r.Get("/api/experience-history", s.handleExperienceHistoryRead)
		r.Get("/api/experience-history/{experienceID}", s.handleExperienceHistoryDetail)
		// Temporal truth read surface (CR-011) — bounded, provenance-first, flag-dark.
		r.Post("/api/temporal-truth/refresh", s.handleTemporalTruthRefresh)
		r.Get("/api/temporal-truth", s.handleTemporalTruthRead)

		// Context injection
		r.Get("/api/context/count", s.handleContextCount)
		r.Post("/api/context/inject", s.handleContextInject)
		r.Get("/api/context/inject", s.handleContextInject) // deprecated — use POST
		r.Post("/api/context/session-start", s.handleSessionStartContextStatic)
		r.Get("/api/context/session-start", s.handleSessionStartContextStatic)
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

		// Search usage analytics
		r.Get("/api/search/recent", s.handleGetRecentQueries)
		r.Get("/api/search/analytics", s.handleGetSearchAnalytics)
		r.Post("/api/analytics/search-misses", s.handleSearchMissAnalytics)

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
		r.Get("/api/memories/principal", s.handlePrincipalMemoryQuery)
		r.Get("/api/memory-domains", s.handleListMemoryDomains)
		r.Put("/api/memory-domains/{domain}", s.handleUpsertMemoryDomain)
		r.Delete("/api/memory-domains/{domain}", s.handleDeleteMemoryDomain)
		r.Get("/api/memory/candidates", s.handleListMemoryCandidates)
		r.Get("/api/memory/candidates/{id}", s.handleGetMemoryCandidate)
		r.Post("/api/memory/candidates/{id}/promote", s.handlePromoteMemoryCandidate)
		r.Post("/api/memory/candidates/{id}/reject", s.handleRejectMemoryCandidate)
		r.Post("/api/memory/candidates/{id}/supersede", s.handleSupersedeMemoryCandidate)
		r.Get("/api/memory/review-metrics", s.handleReadMemoryReviewMetrics)
		r.Get("/api/memory/review-queue", s.handleReadMemoryReviewQueue)
		r.Get("/api/memory/review-packets/{packetID}", s.handleGetMemoryReviewPacketDetail)
		r.Post("/api/memory/review-packets/{packetID}/preview", s.handlePreviewMemoryReviewPacketAction)
		r.Post("/api/memory/review-packets/{packetID}/apply", s.handleApplyMemoryReviewPacketAction)
		r.Post("/api/memories/suppress", s.handleSuppressMemories)
		r.Get("/api/memories/{id}/audit", s.handleGetMemoryAudit)
		r.Post("/api/memories/{id}/suppress", s.handleSuppressMemoryByID)
		r.Get("/api/memories/{id}", s.handleGetMemoryByID)
		r.Delete("/api/memories/{id}", s.handleDeleteMemoryByID)

		// Knowledge graph bridge (CR-002 graph lane)
		r.Get("/api/graph/nodes", s.handleGetGraphNodes)
		r.Post("/api/graph/nodes", s.handleCreateGraphNode)
		r.Delete("/api/graph/nodes/{id}", s.handleDeleteGraphNode)
		r.Get("/api/graph/edges", s.handleGetGraphEdges)
		r.Post("/api/graph/edges", s.handleCreateGraphEdge)
		r.Delete("/api/graph/edges/{id}", s.handleDeleteGraphEdge)
		r.Get("/api/graph/traverse", s.handleTraverseGraph)
		r.Get("/api/graph/find-path", s.handleFindGraphPath)

		// Versioned documents bridge (CR-002 documents lane)
		r.Get("/api/documents", s.handleListDocuments)
		r.Post("/api/documents", s.handleCreateDocument)
		r.Get("/api/documents/read", s.handleReadDocument)
		r.Get("/api/documents/history", s.handleDocumentHistory)
		r.Get("/api/documents/comments", s.handleListDocumentComments)
		r.Post("/api/documents/comment", s.handleAddDocumentComment)

		// Books ingestion bridge (CR-002 books lane)
		r.Post("/api/books", s.handleCreateBookJob)
		r.Get("/api/books/{id}/status", s.handleGetBookJobStatus)

		// Access administration bridge (CR-002 access lane)
		r.Get("/api/access/providers", s.handleAccessProviders)
		r.Get("/api/access/invitations", s.handleAccessListInvitations)
		r.Post("/api/access/invitations", s.handleAccessCreateInvitation)
		r.Post("/api/access/invitations/{id}/revoke", s.handleAccessRevokeInvitation)
		r.Get("/api/access/users", s.handleAccessListUsers)
		r.Get("/api/access/users/{id}", s.handleAccessGetUserDrilldown)
		r.Patch("/api/access/users/{id}", s.handleAccessUpdateUser)
		r.Get("/api/access/roles", s.handleAccessListRoles)
		r.Get("/api/access/sessions", s.handleAccessListSessions)
		r.Post("/api/access/sessions/{id}/revoke", s.handleAccessRevokeSession)
		r.Get("/api/access/log", s.handleAccessListAudit)
		// Behavioral rules management
		r.Get("/api/rules", s.handleListBehavioralRules)
		r.Post("/api/rules", s.handleCreateBehavioralRule)
		r.Patch("/api/rules/{id}/enabled", s.handleSetBehavioralRuleEnabled)
		r.Patch("/api/rules/{id}", s.handleUpdateBehavioralRule)
		r.Delete("/api/rules/{id}", s.handleDeleteBehavioralRule)

		// Token stats
		r.Get("/api/auth/tokens/{id}/stats", s.handleGetTokenStats)
	})

	// Catch-all browser routes for the promoted operator-console surface.
	// With the promoted app embedded into static/, this serves the SPA shell for
	// client-side routes such as /memory and /settings. If an explicit upstream
	// proxy is configured, serveIndex will delegate there instead.
	s.router.Get("/*", serveIndex)
}

// recordRetrievalStatsExtended accumulates per-project retrieval metrics atomically.
// The map entry is created under a write lock; all numeric updates then use atomic
// operations so readers never need to hold the lock while scanning counters.
// If the map is at capacity, aged-out entries are evicted before inserting a new key.
func (s *Service) recordRetrievalStatsExtended(project string, served, verified, deleted, staleExcluded, freshCount, duplicatesRemoved int64, isSearch bool) {
	now := time.Now().Unix()

	s.retrievalStatsMu.Lock()
	stats := s.retrievalStats[project]
	if stats == nil {
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

// cleanupRetrievalStatsLocked evicts project entries whose LastUpdated timestamp
// is older than retrievalStatsMaxAge. Caller must hold retrievalStatsMu for write.
func (s *Service) cleanupRetrievalStatsLocked() {
	cutoff := time.Now().Add(-retrievalStatsMaxAge).Unix()
	for proj, stats := range s.retrievalStats {
		if atomic.LoadInt64(&stats.LastUpdated) < cutoff {
			delete(s.retrievalStats, proj)
		}
	}
}

// GetRetrievalStats returns a point-in-time snapshot of retrieval counters.
// When project is non-empty the snapshot is scoped to that project only.
// When project is empty the counters are summed across all tracked projects.
// All counter reads are atomic so callers do not need the stats lock.
func (s *Service) GetRetrievalStats(project string) RetrievalStats {
	s.retrievalStatsMu.RLock()
	defer s.retrievalStatsMu.RUnlock()

	if project != "" {
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

	// Sum across all projects for a cluster-level view.
	var agg RetrievalStats
	for _, stats := range s.retrievalStats {
		agg.TotalRequests += atomic.LoadInt64(&stats.TotalRequests)
		agg.ObservationsServed += atomic.LoadInt64(&stats.ObservationsServed)
		agg.VerifiedStale += atomic.LoadInt64(&stats.VerifiedStale)
		agg.DeletedInvalid += atomic.LoadInt64(&stats.DeletedInvalid)
		agg.SearchRequests += atomic.LoadInt64(&stats.SearchRequests)
		agg.ContextInjections += atomic.LoadInt64(&stats.ContextInjections)
		agg.StaleExcluded += atomic.LoadInt64(&stats.StaleExcluded)
		agg.FreshCount += atomic.LoadInt64(&stats.FreshCount)
		agg.DuplicatesRemoved += atomic.LoadInt64(&stats.DuplicatesRemoved)
	}
	return agg
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

	// Ring buffer write: decrement head with wrap-around so the newest entry
	// is always at index 0 of a virtual ordered view. O(1), no allocation.
	s.recentQueriesMu.Lock()
	defer s.recentQueriesMu.Unlock()

	s.recentQueriesHead = (s.recentQueriesHead - 1 + maxRecentQueries) % maxRecentQueries
	s.recentQueriesBuf[s.recentQueriesHead] = RecentSearchQuery{
		Query:     query,
		Project:   project,
		Type:      queryType,
		Results:   results,
		Timestamp: time.Now(),
	}
	if s.recentQueriesLen < maxRecentQueries {
		s.recentQueriesLen++
	}
}

// getCachedObservationCount returns the total observation count for a project,
// using the in-process cache when the entry is fresher than statsCacheTTL.
// A cache miss triggers a synchronous DB read across the v5 stores.
func (s *Service) getCachedObservationCount(ctx context.Context, project string) (int, error) {
	s.cachedObsCountsMu.RLock()
	if cached, ok := s.cachedObsCounts[project]; ok && time.Since(cached.timestamp) < s.statsCacheTTL {
		s.cachedObsCountsMu.RUnlock()
		return cached.count, nil
	}
	s.cachedObsCountsMu.RUnlock()

	// Cache miss or expired - query v5 stores.
	count := 0
	if s.memoryStore != nil {
		mems, err := s.memoryStore.List(ctx, project, 1000)
		if err != nil {
			return 0, err
		}
		count += len(mems)
	}
	if s.behavioralRulesStore != nil {
		projectPtr := &project
		rules, err := s.behavioralRulesStore.List(ctx, projectPtr, 1000)
		if err != nil {
			return 0, err
		}
		count += len(rules)
	}

	// Refresh the cache entry for this project.
	s.cachedObsCountsMu.Lock()
	s.cachedObsCounts[project] = cachedCount{count: count, timestamp: time.Now()}
	s.cachedObsCountsMu.Unlock()

	return count, nil
}

// Start binds the TCP listener and launches the HTTP and gRPC servers via cmux.
// The HTTP server begins accepting requests immediately; data-plane routes
// return 503 until initializeAsync completes and sets the ready flag.
func (s *Service) Start() error {
	port := config.GetWorkerPort()

	// Auth startup gate (ADR-0001): refuse to start without token unless explicitly disabled
	token := config.GetWorkerToken()
	authDisabled := authDisabledFromEnv()

	if token == "" && !authDisabled {
		log.Fatal().Msg("ENGRAM_AUTH_ADMIN_TOKEN is not set. Set it to secure your engram instance, or set ENGRAM_AUTH_DISABLED=true to explicitly run without authentication (NOT recommended for production).")
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

	// WriteTimeout is deliberately 0: SSE connections are long-lived and must not
	// be cut off by a write deadline. All other routes enforce DefaultHTTPTimeout
	// via per-route middleware instead of a global server timeout.
	s.server = &http.Server{
		Addr:              addr,
		Handler:           s.router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       120 * time.Second,
	}

	// ENGRAM_RESTART=1 is set by the updater before exec-restarting the process.
	// In restart mode we retry the bind up to 10 times to allow the old process
	// to release the port before we claim it.
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

		// Bind attempts: 1 on a fresh start, up to 10 on a post-update restart.
		maxRetries := 1
		if isRestart {
			maxRetries = 10
		}
		for i := 0; i < maxRetries; i++ {
			if err := startWithListener(); err != nil {
				if i < maxRetries-1 && isRestart {
					log.Warn().Err(err).Int("attempt", i+1).Msg("port not yet free, retrying bind")
					time.Sleep(500 * time.Millisecond)
					continue
				}
				log.Error().Err(err).Msg("listener bind failed")
			}
			return
		}
	}()

	// Queue processor is started in initializeAsync after the DB is ready.

	log.Info().
		Int("port", port).
		Int("pid", getPID()).
		Bool("restart_mode", isRestart).
		Msg("HTTP server started — waiting for async init")

	return nil
}

// processQueue runs as a long-lived goroutine tracked by s.wg.
// It calls processAllSessions immediately when the session manager notifies
// of a new pending observation, and also on a periodic fallback tick so
// that observations are never silently abandoned if a notification is missed.
func (s *Service) processQueue() {
	defer s.wg.Done()

	tick := time.NewTicker(QueueProcessInterval)
	defer tick.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.sessionManager.ProcessNotify:
			s.processAllSessions()
		case <-tick.C:
			s.processAllSessions()
		}
	}
}

// processAllSessions drains the pending-message queues for every active session
// and dispatches each message to the SDK processor in a goroutine. Concurrency
// is bounded by the processor's internal semaphore. The function blocks until
// all dispatched goroutines finish, then pushes an updated processing-status
// event to dashboard subscribers.
func (s *Service) processAllSessions() {
	activeSessions := s.sessionManager.GetAllSessions()

	var wg sync.WaitGroup
	for _, sess := range activeSessions {
		msgs := s.sessionManager.DrainMessages(sess.SessionDBID)
		if len(msgs) == 0 {
			continue
		}
		for _, msg := range msgs {
			wg.Add(1)
			go func(sess *session.ActiveSession, msg session.PendingMessage) {
				defer wg.Done()
				switch msg.Type {
				case session.MessageTypeObservation:
					if msg.Observation == nil {
						return
					}
					if err := s.processor.ProcessObservation(
						s.ctx,
						sess.SDKSessionID,
						sess.Project,
						msg.Observation.ToolName,
						msg.Observation.ToolInput,
						msg.Observation.ToolResponse,
						msg.Observation.PromptNumber,
						msg.Observation.CWD,
						msg.Observation.UserPrompt,
					); err != nil {
						log.Error().Err(err).Str("tool", msg.Observation.ToolName).Msg("observation processing failed")
					}

				case session.MessageTypeSummarize:
					if msg.Summarize == nil {
						return
					}
					if err := s.processor.ProcessSummary(
						s.ctx,
						sess.SessionDBID,
						sess.SDKSessionID,
						sess.Project,
						sess.UserPrompt,
						msg.Summarize.LastUserMessage,
						msg.Summarize.LastAssistantMessage,
					); err != nil {
						log.Error().Err(err).Int64("sessionId", sess.SessionDBID).Msg("summary processing failed")
					}
					// Session is complete after a summary — remove it from the active set.
					s.sessionManager.DeleteSession(sess.SessionDBID)
				}
			}(sess, msg)
		}
	}
	wg.Wait()
	s.broadcastProcessingStatus()
}

// Shutdown performs an ordered graceful stop of all service components.
// The phased sequence is:
//
//  1. Cancel root context  — signals all goroutines to stop accepting new work
//  2. HTTP + gRPC servers  — stop accepting new connections (in-flight requests drain)
//  3. Config watcher       — avoid spurious hot-reload during teardown
//  4. Background workers   — cognitive queue, write-lint janitor
//  5. Session manager      — flush pending observation/summary messages
//  6. WaitGroup drain      — wait up to the caller-supplied context deadline
//  7. Database             — closed last because components above may still read it
//
// The caller supplies the deadline via ctx. If the deadline fires before the
// WaitGroup drains, teardown continues and a warning is logged. The first
// component error (if any) is returned; subsequent errors are only logged.
func (s *Service) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.shutdownOnce.Do(func() {
		s.shutdownDone = make(chan struct{})
		go func() {
			s.shutdownErr = s.shutdown(ctx)
			close(s.shutdownDone)
		}()
	})

	select {
	case <-s.shutdownDone:
		return s.shutdownErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) shutdown(ctx context.Context) error {
	log.Info().Msg("graceful shutdown: starting")
	start := time.Now()

	if s.cancel != nil {
		s.cancel()
	}
	s.initWG.Wait()

	var shutdownErrors []error
	var errMu sync.Mutex
	collectError := func(component string, err error) {
		if err == nil {
			return
		}
		errMu.Lock()
		shutdownErrors = append(shutdownErrors, fmt.Errorf("%s: %w", component, err))
		errMu.Unlock()
		log.Error().Err(err).Str("component", component).Msg("shutdown error")
	}

	if s.server != nil {
		collectError("http_server", s.server.Shutdown(ctx))
	}
	if s.grpcServer != nil {
		s.grpcServer.GracefulStop()
	}
	if s.configWatcher != nil {
		_ = s.configWatcher.Stop()
	}
	if s.cognitiveQueueLifecycle != nil {
		collectError("cognitive_hint_queue", s.cognitiveQueueLifecycle.Stop())
	}
	if s.writelintTokenStore != nil {
		s.writelintTokenStore.Close()
	}

	s.initMu.RLock()
	projectReaper := s.projectReaper
	s.initMu.RUnlock()
	if projectReaper != nil {
		projectReaper.Stop()
	}

	if s.sessionManager != nil {
		s.sessionManager.ShutdownAll(ctx)
	}

	drained := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(drained)
	}()
	select {
	case <-drained:
	case <-ctx.Done():
		log.Warn().Msg("shutdown: goroutine drain timed out, forcing")
	}

	s.initMu.RLock()
	store := s.store
	s.initMu.RUnlock()
	if store != nil {
		collectError("database", store.Close())
	}

	if len(shutdownErrors) > 0 {
		return shutdownErrors[0]
	}
	log.Info().Dur("elapsed", time.Since(start)).Msg("graceful shutdown complete")
	return nil
}

// broadcastProcessingStatus pushes the current queue state to all dashboard
// SSE subscribers. Called after every session event and batch-process cycle
// so the UI processing indicator and queue-depth badge stay accurate.
func (s *Service) broadcastProcessingStatus() {
	s.sseBroadcaster.Broadcast(map[string]any{
		"type":         "processing_status",
		"isProcessing": s.sessionManager.IsAnySessionProcessing(),
		"queueDepth":   s.sessionManager.GetTotalQueueDepth(),
	})
}

func getPID() int {
	return os.Getpid()
}

// wireVnextStores injects the promotion, graph, audit, and nodes stores into
// the MCP server. Extracted from initializeAsync so the wiring path is unit-
// testable: a test that calls wireVnextStores and then checks mcpServer tool
// advertise surface will break if any Set* call is removed.
//
// Callers are responsible for storing the same *gorm.PromotionStore /
// *graph.Store values on Service.promotionStore / Service.graphStore for the
// sleep cycle goroutine, and *gorm.AuditStore on Service.auditStore for audit logging.
// nodesStore does NOT need a separate Service field: it is accessed via
// graphStore (graph.Store.nodes, used by Resolve) and mcpServer (Server.nodesStore,
// used by add_node / get_edges). No other Service method references it directly.
func wireVnextStores(mcpServer *mcp.Server, promotionStore *gorm.PromotionStore, graphStore *graph.Store, nodesStore *graph.NodesStore, auditStore *gorm.AuditStore, continuitySlotStores ...*gorm.ContinuitySlotStore) {
	mcpServer.SetPromotionStore(promotionStore)
	mcpServer.SetGraphStore(graphStore)
	mcpServer.SetNodesStore(nodesStore)
	mcpServer.SetAuditStore(auditStore)
	if len(continuitySlotStores) > 0 {
		mcpServer.SetContinuitySlotStore(continuitySlotStores[0])
	}
}

// wireVnextF wires the TG5 write-lint orchestrator into the MCP server.
// Called after wireVnextStores (which sets graphStore on the service) so the
// orchestrator receives a live graphStore for link_contradiction edge creation.
// Also wires redactionRules from service state into the MCP server.
// Extracted for testability — a test that calls wireVnextF and checks
// mcpServer.writeLint will break if wiring is omitted.
func wireVnextF(
	mcpServer *mcp.Server,
	memStore writelint.MemoryStoreInterface,
	auditLogger writelint.AuditLoggerInterface,
	ts writelint.TokenStore,
	gs *graph.Store,
	cs *gorm.CandidateStore,
	redactionRules []redaction.CompiledRule,
) {
	if ts == nil {
		return // ENGRAM_VNEXT_F_ENABLED was false; nothing to wire
	}
	orchCfg := writelint.OrchestratorConfig{
		MemoryStore:    memStore,
		AuditLogger:    auditLogger,
		TokenStore:     ts,
		GraphStore:     newGraphStoreAdapter(gs),
		CandidateStore: newCandidateStoreAdapter(cs),
		// rank-9: opt-in auto-supersede for near-identical writes. Default 0 (disabled);
		// operators set e.g. 0.97 to converge effectively-identical duplicates at write time.
		// Out-of-range values are ignored (NewOrchestrator/Phase1 only honor 0 < t <= 1 above
		// DupThreshold), so a fat-fingered env can't turn on aggressive merging.
		AutoSupersedeThreshold: parseFloatEnv(os.Getenv("ENGRAM_AUTO_SUPERSEDE_THRESHOLD"), 0),
	}
	orch := writelint.NewOrchestrator(orchCfg)
	mcpServer.SetWriteLintOrchestrator(orch)
	mcpServer.SetRedactionRules(redactionRules)
}

// parseInt64Env parses a decimal integer string; returns defaultVal on failure.
func parseInt64Env(s string, defaultVal int64) int64 {
	if s == "" {
		return defaultVal
	}
	var n int64
	_, err := fmt.Sscanf(s, "%d", &n)
	if err != nil || n <= 0 {
		return defaultVal
	}
	return n
}

// parseFloatEnv parses a float in [0,1]; returns defaultVal on parse error or
// out-of-range input. Clamping out-of-range to the default (rather than to 0/1)
// means a malformed ENGRAM_AUTO_SUPERSEDE_THRESHOLD can never silently enable an
// aggressive auto-merge — it falls back to the safe default instead.
func parseFloatEnv(s string, defaultVal float64) float64 {
	if s == "" {
		return defaultVal
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 || v > 1 {
		return defaultVal
	}
	return v
}

// newGraphStoreAdapter wraps *graph.Store to satisfy writelint.GraphStoreInterface.
// Returns nil when gs is nil (nil-safe path: orchestrator falls back to
// description-only for link_contradiction per finding 4).
func newGraphStoreAdapter(gs *graph.Store) writelint.GraphStoreInterface {
	if gs == nil {
		return nil
	}
	return &graphStoreAdapter{gs: gs}
}

type graphStoreAdapter struct {
	gs *graph.Store
}

func (a *graphStoreAdapter) CreateEdge(ctx context.Context, sourceID, targetID int64, edgeType, reasoning string) error {
	unlock := graph.LockWrites()
	defer unlock()
	e := &graph.Edge{
		SourceID:  &sourceID,
		TargetID:  &targetID,
		EdgeType:  edgeType,
		Reasoning: reasoning,
	}
	_, err := a.gs.Create(ctx, e)
	return err
}

// newCandidateStoreAdapter wraps *gorm.CandidateStore to satisfy writelint.CandidateStoreInterface.
// Returns nil when cs is nil (nil-safe: orchestrator stores plain memory fallback).
func newCandidateStoreAdapter(cs *gorm.CandidateStore) writelint.CandidateStoreInterface {
	if cs == nil {
		return nil
	}
	return &candidateStoreAdapter{cs: cs}
}

type candidateStoreAdapter struct {
	cs *gorm.CandidateStore
}

func (a *candidateStoreAdapter) CreatePending(ctx context.Context, content, project, actor string) error {
	// NewCrystallizationCandidate(sourceSessionID, proposedContent, promotionTarget, opts)
	// Use actor as sourceSessionID (closest available analog) and project as the
	// proposed_promotion_target for context; defaults apply for tier/epistemic type.
	c, err := models.NewCrystallizationCandidate(actor, content, project, models.CandidateOptions{})
	if err != nil {
		return fmt.Errorf("mark_candidate: construct: %w", err)
	}
	_, err = a.cs.Create(ctx, c)
	return err
}
