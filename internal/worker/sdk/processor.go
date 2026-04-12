// Package sdk provides SDK agent integration for engram.
package sdk

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	json "github.com/goccy/go-json"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/learning"
	"github.com/thebtf/engram/internal/privacy"
	"github.com/thebtf/engram/internal/vector"
	"github.com/thebtf/engram/pkg/models"
	"github.com/thebtf/engram/pkg/similarity"
)

// CircuitBreaker implements a simple circuit breaker pattern for CLI calls.
type CircuitBreaker struct {
	failures     int64 // Current failure count
	lastFailure  int64 // Unix timestamp of last failure
	threshold    int64 // Number of failures before opening
	resetTimeout int64 // Seconds to wait before trying again
	state        int32 // 0=closed, 1=open, 2=half-open
}

const (
	circuitClosed   int32 = 0
	circuitOpen     int32 = 1
	circuitHalfOpen int32 = 2
)

// NewCircuitBreaker creates a new circuit breaker.
func NewCircuitBreaker(threshold int64, resetTimeout int64) *CircuitBreaker {
	return &CircuitBreaker{
		threshold:    threshold,
		resetTimeout: resetTimeout,
	}
}

// Allow checks if a request should be allowed through.
func (cb *CircuitBreaker) Allow() bool {
	state := atomic.LoadInt32(&cb.state)
	if state == circuitClosed {
		return true
	}

	if state == circuitOpen {
		// Check if reset timeout has passed
		lastFail := atomic.LoadInt64(&cb.lastFailure)
		if time.Now().Unix()-lastFail > cb.resetTimeout {
			// Transition to half-open
			atomic.CompareAndSwapInt32(&cb.state, circuitOpen, circuitHalfOpen)
			log.Info().Msg("Circuit breaker entering half-open state — testing with next request")
			return true
		}
		return false
	}

	// Half-open: allow one request through
	return true
}

// RecordSuccess records a successful call.
func (cb *CircuitBreaker) RecordSuccess() {
	prevState := atomic.LoadInt32(&cb.state)
	atomic.StoreInt64(&cb.failures, 0)
	atomic.StoreInt32(&cb.state, circuitClosed)
	if prevState != circuitClosed {
		log.Info().Msg("Circuit breaker recovered — LLM calls re-enabled")
	}
}

// RecordFailure records a failed call.
func (cb *CircuitBreaker) RecordFailure() {
	failures := atomic.AddInt64(&cb.failures, 1)
	atomic.StoreInt64(&cb.lastFailure, time.Now().Unix())

	if failures >= cb.threshold {
		atomic.StoreInt32(&cb.state, circuitOpen)
		log.Warn().Int64("failures", failures).Msg("Circuit breaker opened - LLM calls temporarily disabled")
	}
}

// State returns the current state as a string.
func (cb *CircuitBreaker) State() string {
	switch atomic.LoadInt32(&cb.state) {
	case circuitOpen:
		return "open"
	case circuitHalfOpen:
		return "half-open"
	default:
		return "closed"
	}
}

// CircuitBreakerMetrics contains metrics about the circuit breaker state.
type CircuitBreakerMetrics struct {
	State             string `json:"state"`
	Failures          int64  `json:"failures"`
	Threshold         int64  `json:"threshold"`
	ResetTimeoutSecs  int64  `json:"reset_timeout_secs"`
	LastFailureUnix   int64  `json:"last_failure_unix,omitempty"`
	SecondsUntilReset int64  `json:"seconds_until_reset,omitempty"`
}

// Metrics returns the current metrics of the circuit breaker.
func (cb *CircuitBreaker) Metrics() CircuitBreakerMetrics {
	failures := atomic.LoadInt64(&cb.failures)
	lastFail := atomic.LoadInt64(&cb.lastFailure)
	state := cb.State()

	metrics := CircuitBreakerMetrics{
		State:            state,
		Failures:         failures,
		Threshold:        cb.threshold,
		ResetTimeoutSecs: cb.resetTimeout,
	}

	if lastFail > 0 {
		metrics.LastFailureUnix = lastFail
		if state == "open" {
			remaining := cb.resetTimeout - (time.Now().Unix() - lastFail)
			if remaining > 0 {
				metrics.SecondsUntilReset = remaining
			}
		}
	}

	return metrics
}

// RequestDeduplicator tracks recent requests to prevent duplicates.
type RequestDeduplicator struct {
	seen    map[string]int64 // hash -> timestamp
	mu      sync.RWMutex
	ttlSecs int64
	maxSize int
}

// NewRequestDeduplicator creates a new deduplicator.
func NewRequestDeduplicator(ttlSecs int64, maxSize int) *RequestDeduplicator {
	return &RequestDeduplicator{
		seen:    make(map[string]int64),
		ttlSecs: ttlSecs,
		maxSize: maxSize,
	}
}

// IsDuplicate checks if a request hash was seen recently.
func (d *RequestDeduplicator) IsDuplicate(hash string) bool {
	now := time.Now().Unix()

	d.mu.RLock()
	ts, exists := d.seen[hash]
	d.mu.RUnlock()

	if exists && now-ts < d.ttlSecs {
		return true
	}
	return false
}

// Record marks a request hash as seen.
func (d *RequestDeduplicator) Record(hash string) {
	now := time.Now().Unix()

	d.mu.Lock()
	defer d.mu.Unlock()

	// Evict old entries if at capacity
	if len(d.seen) >= d.maxSize {
		threshold := now - d.ttlSecs
		for k, ts := range d.seen {
			if ts < threshold {
				delete(d.seen, k)
			}
		}
	}

	d.seen[hash] = now
}

// hashRequest creates a hash of a request for deduplication.
func hashRequest(toolName, input, output string) string {
	h := sha256.New()
	h.Write([]byte(toolName))
	h.Write([]byte(input))
	h.Write([]byte(output[:min(len(output), 1000)])) // Only hash first 1000 chars of output
	return hex.EncodeToString(h.Sum(nil))[:16]       // Short hash is sufficient
}

// BroadcastFunc is a callback for broadcasting events to SSE clients.
type BroadcastFunc func(event map[string]any)

// SyncObservationFunc is a callback for syncing observations to vector DB.
type SyncObservationFunc func(obs *models.Observation)

// SyncSummaryFunc is a callback for syncing summaries to vector DB.
type SyncSummaryFunc func(summary *models.SessionSummary)

// MaxVectorSyncWorkers is the maximum number of concurrent vector sync operations.
// This prevents unbounded goroutine spawning during high-volume observation ingestion.
const MaxVectorSyncWorkers = 8

// Processor handles SDK agent processing of observations and summaries.
// Uses LLM API (OpenAI-compatible) as primary backend, with Claude CLI as optional fallback.
// Field order optimized for memory alignment (fieldalignment).
type Processor struct {
	observationStore         *gorm.ObservationStore
	summaryStore             *gorm.SummaryStore
	reasoningStore           *gorm.ReasoningTraceStore
	llmClient                learning.LLMClient
	vectorClient             vector.Client
	broadcastFunc            BroadcastFunc
	syncObservationFunc      SyncObservationFunc
	syncSummaryFunc          SyncSummaryFunc
	circuitBreaker           *CircuitBreaker
	deduplicator             *RequestDeduplicator
	vectorSyncChan           chan *models.Observation
	vectorSyncDone           chan struct{}
	sem                      chan struct{}
	model                    string
	dedupSimilarityThreshold float64
	dedupWindowSize          int
	vectorSyncWg             sync.WaitGroup
}

// SetBroadcastFunc sets the broadcast callback for SSE events.
func (p *Processor) SetBroadcastFunc(fn BroadcastFunc) {
	p.broadcastFunc = fn
}

// SetSyncObservationFunc sets the callback for syncing observations to vector DB.
func (p *Processor) SetSyncObservationFunc(fn SyncObservationFunc) {
	p.syncObservationFunc = fn
}

// SetReasoningStore sets the reasoning trace store for System 2 memory extraction.
func (p *Processor) SetReasoningStore(store *gorm.ReasoningTraceStore) {
	p.reasoningStore = store
}

// SetSyncSummaryFunc sets the callback for syncing summaries to vector DB.
func (p *Processor) SetSyncSummaryFunc(fn SyncSummaryFunc) {
	p.syncSummaryFunc = fn
}

// SetDedupConfig sets deduplication parameters.
func (p *Processor) SetDedupConfig(threshold float64, windowSize int) {
	if threshold > 0 && threshold <= 1.0 {
		p.dedupSimilarityThreshold = threshold
	}
	if windowSize > 0 {
		p.dedupWindowSize = windowSize
	}
}

// broadcast sends an event via the broadcast callback if set.
func (p *Processor) broadcast(event map[string]any) {
	if p.broadcastFunc != nil {
		p.broadcastFunc(event)
	}
}

func (p *Processor) enqueueObservationSync(obs *models.Observation) {
	if p.syncObservationFunc == nil || obs == nil {
		return
	}
	if p.vectorSyncChan != nil {
		select {
		case p.vectorSyncChan <- obs:
			return
		default:
			log.Debug().Int64("obs_id", obs.ID).Msg("Vector sync channel full, using fallback goroutine")
		}
	}
	go p.syncObservationFunc(obs)
}

// DefaultConcurrentLLMCalls is the default number of concurrent LLM calls.
// Override with ENGRAM_LLM_CONCURRENCY env var.
const DefaultConcurrentLLMCalls = 4

// NewProcessor creates a new SDK processor.
// It requires at least one LLM backend: either an OpenAI-compatible API (ENGRAM_LLM_URL)
// or a local Claude CLI binary. If neither is available, it returns an error.
func NewProcessor(observationStore *gorm.ObservationStore, summaryStore *gorm.SummaryStore, vectorClient vector.Client) (*Processor, error) {
	cfg := config.Get()

	// Initialize LLM client (OpenAI-compatible API — works in Docker)
	llmCfg := learning.DefaultOpenAIConfig()
	var llmClient learning.LLMClient
	openaiClient := learning.NewOpenAIClient(llmCfg)
	if openaiClient.IsConfigured() {
		llmClient = openaiClient
		log.Info().Str("url", llmCfg.BaseURL).Str("model", llmCfg.Model).Msg("SDK processor using LLM API")
	}

	log.Info().
		Bool("llm_configured", llmClient != nil).
		Str("llm_url", llmCfg.BaseURL).
		Str("llm_model", llmCfg.Model).
		Msg("SDK processor backend summary")

	// Require LLM backend
	if llmClient == nil {
		return nil, fmt.Errorf("no LLM backend available: set ENGRAM_LLM_URL for API access")
	}

	// Configurable concurrency
	concurrency := DefaultConcurrentLLMCalls
	if v := os.Getenv("ENGRAM_LLM_CONCURRENCY"); v != "" {
		if n, err := fmt.Sscanf(v, "%d", &concurrency); n == 1 && err == nil && concurrency > 0 {
			// valid
		} else {
			concurrency = DefaultConcurrentLLMCalls
		}
	}

	return &Processor{
		model:                    cfg.Model,
		llmClient:                llmClient,
		observationStore:         observationStore,
		summaryStore:             summaryStore,
		vectorClient:             vectorClient,
		sem:                      make(chan struct{}, concurrency),
		circuitBreaker:           NewCircuitBreaker(5, 60),                               // Open after 5 failures, reset after 60s
		deduplicator:             NewRequestDeduplicator(300, 1000),                      // 5-minute TTL, 1000 max entries
		vectorSyncChan:           make(chan *models.Observation, MaxVectorSyncWorkers*2), // Buffered channel
		vectorSyncDone:           make(chan struct{}),
		dedupSimilarityThreshold: 0.4, // Will be overridden by config
		dedupWindowSize:          50,  // Will be overridden by config
	}, nil
}

// StartVectorSyncWorkers starts the bounded worker pool for vector sync operations.
// Call this after setting the sync function via SetSyncObservationFunc.
func (p *Processor) StartVectorSyncWorkers() {
	for i := 0; i < MaxVectorSyncWorkers; i++ {
		p.vectorSyncWg.Add(1)
		go p.vectorSyncWorker()
	}
	log.Info().Int("workers", MaxVectorSyncWorkers).Msg("Vector sync worker pool started")
}

// StopVectorSyncWorkers gracefully stops the worker pool.
func (p *Processor) StopVectorSyncWorkers() {
	close(p.vectorSyncDone)
	p.vectorSyncWg.Wait()
	log.Info().Msg("Vector sync worker pool stopped")
}

// vectorSyncWorker is a worker goroutine that processes vector sync requests.
func (p *Processor) vectorSyncWorker() {
	defer p.vectorSyncWg.Done()
	for {
		select {
		case <-p.vectorSyncDone:
			// Drain remaining items before exiting
			for {
				select {
				case obs := <-p.vectorSyncChan:
					if p.syncObservationFunc != nil {
						p.syncObservationFunc(obs)
					}
				default:
					return
				}
			}
		case obs := <-p.vectorSyncChan:
			if p.syncObservationFunc != nil {
				p.syncObservationFunc(obs)
			}
		}
	}
}

// CircuitBreakerMetrics returns detailed metrics about the circuit breaker.
func (p *Processor) CircuitBreakerMetrics() CircuitBreakerMetrics {
	return p.circuitBreaker.Metrics()
}

// IsAvailable checks if an LLM backend (API or CLI) is available for processing.
func (p *Processor) IsAvailable() bool {
	return p.llmClient != nil
}

const writeMergeSimilarityThreshold = 0.75

func unionStrings(parts ...[]string) []string {
	seen := make(map[string]struct{})
	merged := make([]string, 0)
	for _, part := range parts {
		for _, item := range part {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			if _, ok := seen[item]; ok {
				continue
			}
			seen[item] = struct{}{}
			merged = append(merged, item)
		}
	}
	return merged
}

func mergeNarrative(existing, incoming string) string {
	existing = strings.TrimSpace(existing)
	incoming = strings.TrimSpace(incoming)
	switch {
	case existing == "":
		return incoming
	case incoming == "":
		return existing
	case existing == incoming:
		return existing
	case strings.Contains(existing, incoming):
		return existing
	case strings.Contains(incoming, existing):
		return incoming
	default:
		return existing + "\n\n" + incoming
	}
}

func mergeFileMtimes(existing models.JSONInt64Map, incoming map[string]int64) map[string]int64 {
	if len(existing) == 0 && len(incoming) == 0 {
		return nil
	}
	merged := make(map[string]int64, len(existing)+len(incoming))
	for path, mtime := range existing {
		merged[path] = mtime
	}
	for path, mtime := range incoming {
		merged[path] = mtime
	}
	return merged
}

func (p *Processor) queryWriteMergeCandidateIDs(ctx context.Context, project string, obs *models.ParsedObservation) []int64 {
	if p.vectorClient == nil || !p.vectorClient.IsConnected() || obs == nil {
		return nil
	}
	queryText := strings.TrimSpace(strings.Join([]string{obs.Title, obs.Narrative, strings.Join(obs.Facts, " ")}, " "))
	if queryText == "" {
		return nil
	}
	results, err := p.vectorClient.Query(ctx, queryText, 5, vector.BuildWhereFilter(vector.DocTypeObservation, project, false, nil))
	if err != nil {
		log.Warn().Err(err).Str("project", project).Msg("write-merge: vector candidate lookup failed")
		return nil
	}
	candidateIDs := make([]int64, 0, len(results))
	for _, result := range results {
		if result.Similarity < writeMergeSimilarityThreshold {
			continue
		}
		candidateIDs = append(candidateIDs, vector.ExtractObservationIDs([]vector.QueryResult{result}, project)...)
	}
	return candidateIDs
}

func (p *Processor) applyWriteMergeDecision(ctx context.Context, sdkSessionID, project string, obs *models.ParsedObservation, promptNumber int) (*models.Observation, bool, string, error) {
	if obs == nil || !config.Get().WriteMergeEnabled || p.llmClient == nil {
		return nil, false, "", nil
	}
	candidateIDs := p.queryWriteMergeCandidateIDs(ctx, project, obs)
	if len(candidateIDs) == 0 {
		return nil, false, "", nil
	}
	fetchedCandidates, err := p.observationStore.GetObservationsByIDs(ctx, candidateIDs, "default", len(candidateIDs))
	if err != nil {
		return nil, false, "", err
	}
	candidates := make([]*models.Observation, 0, len(fetchedCandidates))
	for _, candidate := range fetchedCandidates {
		if candidate == nil || candidate.ID <= 0 || candidate.Type != obs.Type || candidate.IsSuperseded || candidate.Project != project {
			continue
		}
		candidates = append(candidates, candidate)
	}
	if len(candidates) == 0 {
		return nil, false, "", nil
	}
	newObs := models.NewObservation(sdkSessionID, project, obs, promptNumber, 0)
	decision, err := learning.DecideMerge(ctx, p.llmClient, newObs, candidates)
	if err != nil {
		return nil, false, "", nil
	}
	if decision.Action == learning.MergeActionCreateNew {
		return nil, false, "", nil
	}
	if decision.TargetID <= 0 {
		log.Warn().Str("action", decision.Action).Msg("write-merge: missing target_id, falling back to create")
		return nil, false, "", nil
	}
	var target *models.Observation
	for _, candidate := range candidates {
		if candidate != nil && candidate.ID == decision.TargetID {
			target = candidate
			break
		}
	}
	if target == nil {
		log.Warn().Int64("target_id", decision.TargetID).Msg("write-merge: target_id not in candidate set, falling back to create")
		return nil, false, "", nil
	}

	switch decision.Action {
	case learning.MergeActionSkip:
		log.Info().Str("project", project).Int64("target_id", target.ID).Msg("write-merge: skipped new observation")
		return nil, true, learning.MergeActionSkip, nil
	case learning.MergeActionSupersede:
		if !config.Get().ContradictionDetectionEnabled {
			log.Info().Str("project", project).Int64("target_id", target.ID).Msg("write-merge: contradiction detection disabled; falling back to create path")
			return nil, false, "", nil
		}
		// Keep old observation active until the caller successfully creates the replacement row.
		// This avoids the lossy state where the old row is superseded but the new insert fails.
		return target, false, learning.MergeActionSupersede, nil
	case learning.MergeActionUpdate:
		title := target.Title.String
		if title == "" {
			title = obs.Title
		}
		subtitle := target.Subtitle.String
		if subtitle == "" {
			subtitle = obs.Subtitle
		}
		narrative := mergeNarrative(target.Narrative.String, obs.Narrative)
		facts := unionStrings([]string(target.Facts), obs.Facts)
		concepts := unionStrings([]string(target.Concepts), obs.Concepts)
		filesRead := unionStrings([]string(target.FilesRead), obs.FilesRead)
		filesModified := unionStrings([]string(target.FilesModified), obs.FilesModified)
		commandsRun := unionStrings([]string(target.CommandsRun), obs.CommandsRun)
		fileMtimes := mergeFileMtimes(target.FileMtimes, obs.FileMtimes)
		updated, err := p.observationStore.UpdateObservation(ctx, target.ID, &gorm.ObservationUpdate{
			Title:         &title,
			Subtitle:      &subtitle,
			Narrative:     &narrative,
			Facts:         &facts,
			Concepts:      &concepts,
			FilesRead:     &filesRead,
			FilesModified: &filesModified,
			CommandsRun:   &commandsRun,
			FileMtimes:    &fileMtimes,
		})
		if err != nil {
			return nil, false, "", err
		}
		p.enqueueObservationSync(updated)
		p.broadcast(map[string]any{
			"type":    "observation",
			"action":  "updated",
			"id":      target.ID,
			"project": project,
		})
		log.Info().Str("project", project).Int64("target_id", target.ID).Msg("write-merge: updated existing observation")
		return updated, false, learning.MergeActionUpdate, nil
	default:
		return nil, false, "", nil
	}
}

// ProcessObservation processes a single tool observation and extracts insights.
func (p *Processor) ProcessObservation(ctx context.Context, sdkSessionID, project string, toolName string, toolInput, toolResponse any, promptNumber int, cwd string, userPrompt ...string) error {
	// Skip certain tools that aren't worth processing
	if shouldSkipTool(toolName) {
		log.Info().Str("tool", toolName).Msg("Skipping tool (not interesting for memory)")
		return nil
	}

	// Convert tool data to strings for pre-filtering
	inputStr := toJSONString(toolInput)
	outputStr := toJSONString(toolResponse)

	// Pre-filter trivial operations without calling Haiku
	if shouldSkipTrivialOperation(toolName, inputStr, outputStr) {
		log.Debug().Str("tool", toolName).Msg("Skipping trivial operation (pre-filter)")
		return nil
	}

	// Check for duplicate request within TTL window
	reqHash := hashRequest(toolName, inputStr, outputStr)
	if p.deduplicator.IsDuplicate(reqHash) {
		log.Debug().Str("tool", toolName).Msg("Skipping duplicate request (dedup)")
		return nil
	}

	// Check circuit breaker before making LLM call
	if !p.circuitBreaker.Allow() {
		log.Warn().Str("tool", toolName).Msg("Circuit breaker open - skipping LLM call")
		return fmt.Errorf("circuit breaker open")
	}

	log.Info().Str("tool", toolName).Msg("Processing tool execution via LLM")

	// Record this request to prevent duplicates
	p.deduplicator.Record(reqHash)

	// Build the prompt with optional user intent context (Learning Memory v3 FR-4)
	var userIntent string
	if len(userPrompt) > 0 && userPrompt[0] != "" {
		userIntent = userPrompt[0]
	}
	exec := ToolExecution{
		ToolName:   toolName,
		ToolInput:  inputStr,
		ToolOutput: outputStr,
		CWD:        cwd,
		UserIntent: userIntent,
	}
	prompt := BuildObservationPrompt(exec)

	// Acquire semaphore slot (limits concurrent LLM calls)
	select {
	case p.sem <- struct{}{}:
		defer func() { <-p.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}

	// Call LLM backend (API or CLI fallback)
	response, err := p.callLLM(ctx, prompt)
	if err != nil {
		p.circuitBreaker.RecordFailure()
		log.Error().Err(err).Str("tool", toolName).Msg("Failed to call LLM for observation extraction")
		return err
	}
	p.circuitBreaker.RecordSuccess()

	// Parse observations from response
	observations := ParseObservations(response, sdkSessionID)
	if len(observations) == 0 {
		log.Info().Str("tool", toolName).Msg("No observations extracted (Claude deemed not significant)")
		return nil
	}

	// Get existing observations for deduplication
	existingObs, err := p.observationStore.GetRecentObservations(ctx, project, p.dedupWindowSize)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to get existing observations for dedup check")
		existingObs = nil // Continue without dedup
	}

	// Store each observation (with deduplication check)
	var storedCount, skippedCount, mergeEvaluatedCount, mergeSkipCount int

	for _, obs := range observations {
		// Capture file modification times for staleness detection
		obs.FileMtimes = captureFileMtimes(obs.FilesRead, obs.FilesModified, cwd)

		// Convert to stored observation for similarity check
		storedObs := obs.ToStoredObservation()

		// Check if this observation is too similar to existing ones
		if existingObs != nil && similarity.IsSimilarToAny(storedObs, existingObs, p.dedupSimilarityThreshold) {
			log.Debug().
				Str("type", string(obs.Type)).
				Str("title", obs.Title).
				Msg("Skipping observation - too similar to existing")
			skippedCount++
			continue
		}

		mergeEvaluatedCount++
		mergedObs, mergeSkipped, mergeAction, mergeErr := p.applyWriteMergeDecision(ctx, sdkSessionID, project, obs, promptNumber)
		if mergeErr != nil {
			log.Warn().Err(mergeErr).Msg("write-merge: failed to apply merge decision, falling back to create path")
		}
		if mergeSkipped {
			mergeSkipCount++
			skippedCount++
			continue
		}
		if mergedObs != nil && mergedObs.ID > 0 {
			if existingObs != nil {
				replaced := false
				for i, existing := range existingObs {
					if existing != nil && existing.ID == mergedObs.ID {
						existingObs[i] = mergedObs
						replaced = true
						break
					}
				}
				if !replaced {
					existingObs = append(existingObs, mergedObs)
				}
			}
			if mergeAction == learning.MergeActionUpdate {
				storedCount++
				continue
			}
		}

		id, createdAtEpoch, err := p.observationStore.StoreObservation(ctx, sdkSessionID, project, obs, promptNumber, 0)
		if err != nil {
			log.Error().Err(err).Msg("Failed to store observation")
			continue
		}
		if mergedObs != nil && mergedObs.ID > 0 && !mergedObs.IsSuperseded {
			if err := p.observationStore.MarkAsSuperseded(ctx, mergedObs.ID); err != nil {
				rollbackErr := p.observationStore.DeleteObservation(ctx, id)
				if rollbackErr != nil {
					log.Error().Err(rollbackErr).Int64("new_id", id).Msg("write-merge: failed to rollback replacement observation after supersede failure")
					return fmt.Errorf("write-merge: failed to supersede target %d after replacement insert: %w (rollback failed: %v)", mergedObs.ID, err, rollbackErr)
				}
				return fmt.Errorf("write-merge: failed to supersede target %d after replacement insert: %w", mergedObs.ID, err)
			}
		}
		storedCount++
		log.Info().
			Int64("id", id).
			Str("type", string(obs.Type)).
			Str("title", obs.Title).
			Int("trackedFiles", len(obs.FileMtimes)).
			Msg("Observation stored")

		// Sync to vector DB via bounded worker pool (non-blocking to reduce latency)
		if p.syncObservationFunc != nil {
			fullObs := models.NewObservation(sdkSessionID, project, obs, promptNumber, 0)
			fullObs.ID = id
			fullObs.CreatedAtEpoch = createdAtEpoch
			p.enqueueObservationSync(fullObs)
		}

		// Broadcast new observation event for dashboard refresh
		p.broadcast(map[string]any{
			"type":    "observation",
			"action":  "created",
			"id":      id,
			"project": project,
		})

		// Add to existing for subsequent dedup checks within same batch
		if existingObs != nil {
			existingObs = append(existingObs, storedObs)
		}
	}

	if skippedCount > 0 {
		log.Info().
			Int("stored", storedCount).
			Int("skipped", skippedCount).
			Msg("Observation processing complete (duplicates skipped)")
	}
	if mergeEvaluatedCount > 0 {
		log.Info().
			Str("project", project).
			Int("merge_evaluated", mergeEvaluatedCount).
			Int("merge_skipped", mergeSkipCount).
			Float64("merge_skip_rate", float64(mergeSkipCount)/float64(mergeEvaluatedCount)).
			Msg("write-merge telemetry")
	}

	// Asynchronously extract reasoning traces from the LLM response (System 2 memory).
	// Only runs when a reasoning store is configured and the response contains
	// enough multi-step reasoning indicators.
	if p.reasoningStore != nil && storedCount > 0 && DetectReasoning(response) {
		go p.extractAndStoreReasoning(context.Background(), sdkSessionID, project, response)
	}

	return nil
}

// extractAndStoreReasoning extracts a reasoning trace from LLM output and
// stores it if quality is sufficient. Runs asynchronously — errors are logged,
// never propagated to the caller.
func (p *Processor) extractAndStoreReasoning(ctx context.Context, sdkSessionID, project, response string) {
	traceJSON, err := p.callLLM(ctx, reasoningExtractionPrompt+response)
	if err != nil {
		log.Debug().Err(err).Msg("Reasoning extraction LLM call failed")
		return
	}

	var trace ReasoningTrace
	if err := json.Unmarshal([]byte(traceJSON), &trace); err != nil || len(trace.Steps) == 0 {
		log.Debug().Msg("No reasoning steps extracted")
		return
	}

	// Evaluate quality via LLM
	qualityStr, err := p.callLLM(ctx, reasoningQualityPrompt+traceJSON)
	if err == nil {
		qualityStr = strings.TrimSpace(qualityStr)
		if q, parseErr := strconv.ParseFloat(qualityStr, 64); parseErr == nil {
			trace.QualityScore = q
		}
	}

	// Only persist traces with quality >= 0.5
	if trace.QualityScore < 0.5 {
		log.Debug().
			Float64("quality", trace.QualityScore).
			Int("steps", len(trace.Steps)).
			Msg("Reasoning trace below quality threshold, discarding")
		return
	}

	// Marshal steps and task_context back to JSON strings for DB storage
	stepsJSON, err := json.Marshal(trace.Steps)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to marshal reasoning steps")
		return
	}
	taskCtxJSON, err := json.Marshal(trace.TaskContext)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to marshal reasoning task context")
		return
	}

	dbTrace := &gorm.ReasoningTrace{
		SDKSessionID: sdkSessionID,
		Project:      project,
		Steps:        string(stepsJSON),
		QualityScore: trace.QualityScore,
		TaskContext:  string(taskCtxJSON),
	}

	id, err := p.reasoningStore.Create(ctx, dbTrace)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to store reasoning trace")
		return
	}

	log.Info().
		Int64("id", id).
		Float64("quality", trace.QualityScore).
		Int("steps", len(trace.Steps)).
		Msg("Reasoning trace extracted and stored")
}

// ProcessSummary processes a session summary request.
func (p *Processor) ProcessSummary(ctx context.Context, sessionDBID int64, sdkSessionID, project, userPrompt, lastUserMsg, lastAssistantMsg string) error {
	// Debug: log what we received
	log.Debug().
		Int64("sessionId", sessionDBID).
		Int("lastAssistantMsgLen", len(lastAssistantMsg)).
		Str("lastAssistantMsgPreview", truncate(lastAssistantMsg, 200)).
		Msg("ProcessSummary called")

	// If no assistant message provided, build content from stored observations for this session
	if !hasMeaningfulContent(lastAssistantMsg) && p.observationStore != nil {
		type obsRow struct {
			Type      string `gorm:"column:type"`
			Title     string `gorm:"column:title"`
			Narrative string `gorm:"column:narrative"`
		}
		var rows []obsRow
		if err := p.observationStore.GetDB().WithContext(ctx).
			Raw(`SELECT type, COALESCE(title, '') as title, COALESCE(narrative, '') as narrative
				FROM observations WHERE sdk_session_id = ? ORDER BY created_at_epoch DESC LIMIT 10`, sdkSessionID).
			Scan(&rows).Error; err == nil && len(rows) > 0 {
			var sb strings.Builder
			sb.WriteString("Session observations:\n")
			for _, o := range rows {
				sb.WriteString("- [")
				sb.WriteString(o.Type)
				sb.WriteString("] ")
				sb.WriteString(o.Title)
				if o.Narrative != "" {
					sb.WriteString(": ")
					sb.WriteString(o.Narrative)
				}
				sb.WriteString("\n")
			}
			lastAssistantMsg = sb.String()
			log.Debug().
				Int64("sessionId", sessionDBID).
				Int("observations", len(rows)).
				Msg("Built summary content from session observations")
		}
	}

	// Third fallback: use the session's initial user prompt
	if !hasMeaningfulContent(lastAssistantMsg) && userPrompt != "" && len(strings.TrimSpace(userPrompt)) >= 10 {
		lastAssistantMsg = "Session started with user request: " + userPrompt
		log.Debug().
			Int64("sessionId", sessionDBID).
			Msg("Using userPrompt as summary fallback")
	}

	// Skip summary generation if there's still no meaningful content
	if !hasMeaningfulContent(lastAssistantMsg) {
		log.Info().
			Int64("sessionId", sessionDBID).
			Int("msgLen", len(lastAssistantMsg)).
			Msg("Skipping summary - no meaningful content available")
		return nil
	}

	// Build the summary prompt
	req := SummaryRequest{
		SessionDBID:          sessionDBID,
		SDKSessionID:         sdkSessionID,
		Project:              project,
		UserPrompt:           userPrompt,
		LastUserMessage:      lastUserMsg,
		LastAssistantMessage: lastAssistantMsg,
	}
	prompt := BuildSummaryPrompt(req)

	// Acquire semaphore slot (limits concurrent LLM calls)
	select {
	case p.sem <- struct{}{}:
		defer func() { <-p.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}

	// Call LLM backend (API or CLI fallback)
	response, err := p.callLLM(ctx, prompt)
	if err != nil {
		log.Error().Err(err).Int64("sessionId", sessionDBID).Msg("Failed to call LLM for summary")
		return err
	}

	// Parse summary from response
	summary := ParseSummary(response, sessionDBID)
	if summary == nil {
		log.Info().Int64("sessionId", sessionDBID).Msg("No summary generated (skipped or empty)")
		return nil
	}

	// Filter out summaries that describe the memory agent itself
	if isSelfReferentialSummary(summary) {
		log.Info().Int64("sessionId", sessionDBID).Msg("Skipping self-referential summary (describes agent, not user work)")
		return nil
	}

	// Store the summary (promptNumber=0, discoveryTokens=0 for summaries)
	id, createdAtEpoch, err := p.summaryStore.StoreSummary(ctx, sdkSessionID, project, summary, 0, 0)
	if err != nil {
		log.Error().Err(err).Msg("Failed to store summary")
		return err
	}

	log.Info().
		Int64("id", id).
		Int64("sessionId", sessionDBID).
		Msg("Summary stored")

	// Sync to vector DB if callback is set
	if p.syncSummaryFunc != nil {
		fullSummary := models.NewSessionSummary(sdkSessionID, project, summary, 0, 0)
		fullSummary.ID = id
		fullSummary.CreatedAtEpoch = createdAtEpoch
		p.syncSummaryFunc(fullSummary)
	}

	// Broadcast new summary event for dashboard refresh
	p.broadcast(map[string]any{
		"type":    "summary",
		"action":  "created",
		"id":      id,
		"project": project,
	})

	return nil
}

// MaxPromptSize is the maximum size of a prompt that can be passed to the LLM.
// This prevents resource exhaustion from extremely large prompts.
const MaxPromptSize = 100 * 1024 // 100KB

// sanitizePrompt removes null bytes and control characters from a prompt.
// Keeps newlines, tabs, and carriage returns as they're valid in prompts.
func sanitizePrompt(s string) string {
	return strings.Map(func(r rune) rune {
		// Keep printable ASCII, extended Unicode, and common whitespace
		if r >= 32 || r == '\n' || r == '\t' || r == '\r' {
			return r
		}
		// Remove null bytes and other control characters
		return -1
	}, s)
}

// callLLM calls the LLM backend with the given prompt.
// Tries LLM API first (works in Docker), falls back to Claude CLI if available.
func (p *Processor) callLLM(ctx context.Context, prompt string) (string, error) {
	if len(prompt) > MaxPromptSize {
		return "", fmt.Errorf("prompt exceeds maximum size of %d bytes", MaxPromptSize)
	}
	prompt = sanitizePrompt(prompt)

	// Try LLM API first (OpenAI-compatible — works in Docker without Claude CLI)
	// Retry with backoff for transient errors (EOF, connection reset, 429, 503)
	var lastErr error
	if p.llmClient != nil {
		for attempt := 0; attempt < 3; attempt++ {
			if attempt > 0 {
				backoff := time.Duration(attempt*2) * time.Second
				time.Sleep(backoff)
			}
			llmCtx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			response, err := p.llmClient.Complete(llmCtx, systemPrompt, prompt)
			cancel()
			if err == nil {
				// Constitution P9: RedactSecrets on LLM output before returning
				return privacy.RedactSecrets(response), nil
			}
			lastErr = err
			errStr := err.Error()
			// Retry only on transient errors
			if strings.Contains(errStr, "EOF") ||
				strings.Contains(errStr, "connection reset") ||
				strings.Contains(errStr, "connection refused") ||
				strings.Contains(errStr, "no such host") ||
				strings.Contains(errStr, "429") ||
				strings.Contains(errStr, "500") ||
				strings.Contains(errStr, "502") ||
				strings.Contains(errStr, "503") ||
				strings.Contains(errStr, "504") {
				log.Warn().Err(err).Int("attempt", attempt+1).Msg("LLM API transient error, retrying")
				continue
			}
			// Non-transient error — don't retry
			log.Warn().Err(err).Msg("LLM API call failed, trying CLI fallback")
			break
		}
		if lastErr != nil {
			log.Warn().Err(lastErr).Msg("LLM API call failed after retries")
		}
	}

	if lastErr != nil {
		return "", fmt.Errorf("LLM call failed after retries: %w", lastErr)
	}
	return "", fmt.Errorf("no LLM backend available (llmClient=%v)", p.llmClient != nil)
}

// shouldSkipTool returns true for tools that aren't worth processing.
func shouldSkipTool(toolName string) bool {
	// Skip tools that rarely produce meaningful observations
	skipTools := map[string]bool{
		// Internal tracking tools
		"TodoWrite":  true,
		"Task":       true,
		"TaskOutput": true,

		// File discovery tools (just listings, no insights)
		"Glob":      true,
		"ListDir":   true,
		"LS":        true,
		"KillShell": true,

		// Question/interaction tools (no code insights)
		"AskUserQuestion": true,

		// Plan mode tools (planning, not execution)
		"EnterPlanMode": true,
		"ExitPlanMode":  true,

		// Skill/command execution (meta-operations)
		"Skill":        true,
		"SlashCommand": true,

		// High-volume, low-value tools — create noise without meaningful observations
		"Read":      true,
		"Grep":      true,
		"WebSearch": true,
	}

	skip, found := skipTools[toolName]
	if found {
		return skip
	}
	return false // Process remaining tools: Edit, Write, Bash, WebFetch, NotebookEdit
}

// shouldSkipTrivialOperation performs local pre-filtering to skip trivial operations
// without making a Haiku API call. Returns true if the operation is too trivial to process.
func shouldSkipTrivialOperation(toolName, inputStr, outputStr string) bool {
	// Skip if output is too small to be meaningful
	if len(outputStr) < 50 {
		return true
	}

	// WHITELIST approach: only process tool outputs that are likely to contain
	// meaningful insights. Skip everything else. This inverted logic prevents
	// garbage observations (PowerShell errors, auth failures, etc.) from
	// polluting the knowledge base and degrading agent performance.
	lowerInput := strings.ToLower(inputStr)

	switch toolName {
	case "Edit", "Write":
		// Code modifications — always interesting (architecture, decisions)
		return false

	case "Bash":
		// Only process build/test results — they reveal project state
		interestingCommands := []string{
			// Go
			"go build", "go test", "go vet",
			// Node/JS
			"npm run build", "npm test", "npx tsc",
			// Rust
			"cargo build", "cargo test", "cargo clippy",
			// .NET
			"dotnet build", "dotnet test", "dotnet publish",
			// Make/Docker
			"make ", "docker build", "docker compose",
			// Python
			"pytest", "python -m pytest",
			// JS test runners
			"jest", "vitest",
		}
		for _, cmd := range interestingCommands {
			if strings.Contains(lowerInput, cmd) {
				return false
			}
		}
		// All other Bash outputs: skip (git, ls, curl, echo, etc.)
		return true

	default:
		// All other tools (Read, Grep, Agent, WebFetch, etc.): skip
		// These produce high-volume, low-insight observations.
		// Valuable knowledge should be saved explicitly via store_memory.
		return true
	}
}

// toJSONString converts an interface to a JSON string.
func toJSONString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// safeResolvePath resolves a path relative to cwd and validates it doesn't escape the cwd directory.
// Returns the resolved absolute path and true if valid, or empty string and false if path traversal detected.
// This function is a security sanitizer for path traversal attacks.
func safeResolvePath(path, cwd string) (string, bool) {
	// Clean the input path to normalize any .. or . components
	cleanPath := filepath.Clean(path)

	// Reject paths that explicitly contain parent directory traversal after cleaning
	if strings.Contains(cleanPath, "..") {
		return "", false
	}

	if filepath.IsAbs(cleanPath) {
		// For absolute paths, verify they're within cwd if cwd is specified
		if cwd != "" {
			cleanCwd := filepath.Clean(cwd)
			// Use filepath.Rel for cross-platform safety (handles case-insensitive
			// Windows paths correctly, unlike strings.HasPrefix)
			rel, err := filepath.Rel(cleanCwd, cleanPath)
			if err != nil || strings.HasPrefix(rel, "..") {
				return "", false
			}
		}
		return cleanPath, true
	}

	if cwd == "" {
		return cleanPath, true
	}

	// Clean the cwd first
	cleanCwd := filepath.Clean(cwd)

	// Join and clean the path
	absPath := filepath.Join(cleanCwd, cleanPath)

	// Use filepath.Rel to verify the path is actually within cwd
	// If Rel returns a path starting with "..", it escapes the base
	rel, err := filepath.Rel(cleanCwd, absPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", false
	}

	return absPath, true
}

// captureFileMtimes captures current modification times for tracked files.
// Returns a map of absolute file paths to their mtime in epoch milliseconds.
// For large file lists (>10 files), uses parallel stat calls for better performance.
func captureFileMtimes(filesRead, filesModified []string, cwd string) map[string]int64 {
	// Combine all unique file paths
	allPaths := make(map[string]struct{}, len(filesRead)+len(filesModified))
	for _, path := range filesRead {
		allPaths[path] = struct{}{}
	}
	for _, path := range filesModified {
		allPaths[path] = struct{}{}
	}

	// For small lists, use sequential processing (goroutine overhead not worth it)
	if len(allPaths) <= 10 {
		return captureFileMtimesSequential(allPaths, cwd)
	}

	// For larger lists, parallelize with bounded concurrency
	return captureFileMtimesParallel(allPaths, cwd)
}

// captureFileMtimesSequential captures mtimes sequentially (efficient for small lists).
func captureFileMtimesSequential(paths map[string]struct{}, cwd string) map[string]int64 {
	mtimes := make(map[string]int64, len(paths))

	for path := range paths {
		absPath, ok := safeResolvePath(path, cwd)
		if !ok {
			// Skip paths that attempt directory traversal
			continue
		}

		info, err := os.Stat(absPath)
		if err == nil {
			mtimes[path] = info.ModTime().UnixMilli()
		}
	}

	return mtimes
}

// captureFileMtimesParallel captures mtimes in parallel with bounded concurrency.
func captureFileMtimesParallel(paths map[string]struct{}, cwd string) map[string]int64 {
	type mtimeResult struct {
		path  string
		mtime int64
	}

	results := make(chan mtimeResult, len(paths))
	sem := make(chan struct{}, 8) // Limit to 8 concurrent stat calls
	var wg sync.WaitGroup

	for path := range paths {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			sem <- struct{}{}        // Acquire
			defer func() { <-sem }() // Release

			absPath, ok := safeResolvePath(p, cwd)
			if !ok {
				// Skip paths that attempt directory traversal
				return
			}

			info, err := os.Stat(absPath)
			if err == nil {
				results <- mtimeResult{path: p, mtime: info.ModTime().UnixMilli()}
			}
		}(path)
	}

	// Close results channel when all goroutines complete
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results
	mtimes := make(map[string]int64, len(paths))
	for res := range results {
		mtimes[res.path] = res.mtime
	}

	return mtimes
}

// GetFileMtimes returns current modification times for a list of file paths.
// This is used for staleness checking when injecting context.
// In Docker/remote mode, os.Stat on client paths returns error → empty map (no-op).
func GetFileMtimes(paths []string, cwd string) map[string]int64 {
	return captureFileMtimes(paths, nil, cwd)
}

// GetFileContent reads file content for verification purposes.
// Returns content and ok status. In Docker/remote mode, files don't exist → ("", false).
func GetFileContent(path, cwd string) (string, bool) {

	absPath, ok := safeResolvePath(path, cwd)
	if !ok {
		// Reject paths that attempt directory traversal
		return "", false
	}

	content, err := os.ReadFile(absPath) // #nosec G304 -- path validated by safeResolvePath
	if err != nil {
		return "", false
	}

	// Limit to first 2000 chars for verification (enough context, not too expensive)
	if len(content) > 2000 {
		return string(content[:2000]) + "\n...[truncated]", true
	}
	return string(content), true
}

// VerifyObservation checks if an observation is still valid given the current file contents.
// Returns true if the observation is still accurate, false if it should be deleted.
func (p *Processor) VerifyObservation(ctx context.Context, obs *models.Observation, cwd string) bool {
	// Build file content context
	var fileContents []string
	var paths []string

	// Combine files_read and files_modified
	for _, path := range obs.FilesRead {
		paths = append(paths, path)
	}
	for _, path := range obs.FilesModified {
		paths = append(paths, path)
	}

	// Get current content of tracked files
	for _, path := range paths {
		if content, ok := GetFileContent(path, cwd); ok {
			fileContents = append(fileContents, fmt.Sprintf("=== %s ===\n%s", path, content))
		}
	}

	if len(fileContents) == 0 {
		// No files available to verify against - keep the observation
		return true
	}

	// Build verification prompt
	prompt := fmt.Sprintf(`You are verifying if a previously recorded observation is still accurate.

OBSERVATION:
- Type: %s
- Title: %s
- Subtitle: %s
- Narrative: %s
- Facts: %v

CURRENT FILE CONTENTS:
%s

TASK: Check if the observation is still accurate given the current file contents.
Reply with ONLY one of:
- VALID - if the observation is still accurate
- INVALID - if the observation is no longer accurate (the code/behavior changed)
- UNCERTAIN - if you can't determine validity (files might be incomplete)

Your response:`,
		obs.Type,
		obs.Title.String,
		obs.Subtitle.String,
		obs.Narrative.String,
		obs.Facts,
		strings.Join(fileContents, "\n\n"),
	)

	// Call LLM backend for quick verification
	response, err := p.callLLM(ctx, prompt)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to verify observation, keeping it")
		return true // On error, keep the observation
	}

	response = strings.TrimSpace(strings.ToUpper(response))

	// Parse response
	if strings.Contains(response, "INVALID") {
		log.Info().
			Int64("id", obs.ID).
			Str("title", obs.Title.String).
			Msg("Observation verified as INVALID - will delete")
		return false
	}

	// VALID or UNCERTAIN - keep the observation
	log.Debug().
		Int64("id", obs.ID).
		Str("title", obs.Title.String).
		Str("result", response).
		Msg("Observation verified")
	return true
}

// isSelfReferentialSummary checks if a summary describes the memory agent itself
// rather than actual user work. These summaries should be filtered out.
func isSelfReferentialSummary(summary *models.ParsedSummary) bool {
	// Combine all summary fields for checking
	content := strings.ToLower(summary.Request + " " + summary.Completed + " " + summary.Learned + " " + summary.NextSteps + " " + summary.Investigated + " " + summary.Notes)

	// Indicators that the summary is about the memory agent, not user work
	selfReferentialPhrases := []string{
		// Agent references
		"memory extraction",
		"memory agent",
		"extraction agent",
		"hook execution",
		"hook mechanism",
		// Session meta-state
		"session initialization",
		"session setup",
		"session has just started",
		"session just started",
		"agent initialization",
		"no technical learnings",
		"no code or project work",
		// Waiting states
		"waiting for the user",
		"waiting for user",
		"awaiting actual",
		"awaiting claude code",
		"awaiting tool",
		"awaiting user",
		// Meta checkpoint references
		"progress checkpoint",
		"checkpoint request",
		// Common no-work phrases
		"no work has been completed",
		"no work completed",
		"no work done",
		"no actual work",
		"nothing has been completed",
		"nothing completed",
		// Role/guideline parroting
		"role definition",
		"operational guidelines",
		"providing role",
		"providing guidelines",
		// System prompt echoes
		"extract meaningful observations",
		"meaningful learnings",
		"analyze tool executions",
		"observations for future sessions",
		// Empty session indicators
		"empty session",
		"no substantive work",
		"no meaningful work",
		"just beginning",
		"just begun",
	}

	matchCount := 0
	for _, phrase := range selfReferentialPhrases {
		if strings.Contains(content, phrase) {
			matchCount++
		}
	}

	// If the summary mentions 2+ self-referential phrases, it's about the agent
	return matchCount >= 2
}

// hasMeaningfulContent checks if the assistant response contains meaningful content
// worth generating a summary for. This filters out initial greetings, empty sessions,
// and sessions where only system messages were exchanged.
func hasMeaningfulContent(assistantMsg string) bool {
	// Skip if empty or too short (need substantial content)
	if len(strings.TrimSpace(assistantMsg)) < 200 {
		return false
	}

	lowerMsg := strings.ToLower(assistantMsg)

	// Skip messages that are primarily about system/hook status or meta-instructions
	skipIndicators := []string{
		// System/hook markers
		"hook success",
		"callback hook",
		"session start",
		"sessionstart",
		"system-reminder",
		// Agent self-references
		"memory extraction agent",
		"memory agent",
		"extraction agent",
		// No-work indicators
		"no technical learnings",
		"waiting for",
		"waiting to",
		"no code or project work",
		"no substantive",
		"no work has been completed",
		"no work done",
		"awaiting tool",
		"awaiting user",
		// Meta-instruction echoes
		"role definition",
		"operational guidelines",
		"analyze tool executions",
		"extract meaningful observations",
	}

	skipCount := 0
	for _, skip := range skipIndicators {
		if strings.Contains(lowerMsg, skip) {
			skipCount++
		}
	}
	// If multiple skip indicators found, this is likely a system-only session
	if skipCount >= 2 {
		return false
	}

	// Check for indicators of actual work being done
	workIndicators := []string{
		// Concrete file operations (with paths)
		".go", ".ts", ".js", ".py", ".md", ".json", ".yaml", ".yml",
		// Code modifications
		"edited", "modified", "created", "deleted", "updated", "changed",
		"added", "removed", "fixed", "implemented", "refactored",
		// Tool results
		"```", "lines ", "function ", "const ", "var ", "let ",
		"type ", "struct ", "class ", "def ", "func ",
	}

	matchCount := 0
	for _, indicator := range workIndicators {
		if strings.Contains(lowerMsg, strings.ToLower(indicator)) {
			matchCount++
		}
	}

	// Require at least 2 work indicators to generate a summary
	return matchCount >= 2
}

// systemPrompt is the extraction system prompt for the live SDK processor.
// It uses the same category taxonomy as the backfill extractor, adapted for
// single tool-execution analysis (one exchange rather than multi-exchange chunks).
const systemPrompt = `You are a coding session analyst. Analyze this single tool execution and extract ONLY observations matching these categories. If none match, output <no_observations_found/>.

CATEGORY 1 — DECISION: Agent or user explicitly chose between alternatives.
CATEGORY 2 — CORRECTION: User told the agent it was wrong.
CATEGORY 3 — DEBUGGING ARC: Error appeared, was investigated, and resolved.
CATEGORY 4 — GOTCHA: Something behaved unexpectedly.
CATEGORY 5 — PATTERN: A reusable approach that worked well.
CATEGORY 6 — USER_BEHAVIOR: User corrected agent's approach or revealed a workflow preference. Extract as TRIGGER/RULE/REASON.

DO NOT EXTRACT: File reads without decisions, routine commits, tool invocations without meaningful output, status checks, version bumps, generic descriptions.

RULES:
- Maximum 1 observation per tool execution.
- Maximum 150 words per narrative.
- Do NOT include any text before or after the XML. Output ONLY the XML.

EXAMPLES:

Example 1 (user_behavior):
"USER: you have tavily for this, FYI"
<observation><category>user_behavior</category><type>decision</type><title>Rule: Use Tavily for doc research</title><narrative>TRIGGER: When studying external library docs. RULE: Use Tavily not manual WebFetch. REASON: Manual wastes 10+ calls.</narrative><concepts><concept>workflow</concept></concepts></observation>

Example 2 (no_observations_found):
"ASSISTANT: [tool: Read] Reading config.go."
<no_observations_found/>

OUTPUT FORMAT:
<observation>
<category>decision|correction|debugging|gotcha|pattern|user_behavior</category>
<type>decision|bugfix|feature|refactor|discovery|change</type>
<title>Short descriptive title (max 60 chars)</title>
<subtitle>One-line summary</subtitle>
<narrative>Context → What happened → Why it matters. Max 150 words.</narrative>
<facts>
<fact>Specific fact 1</fact>
</facts>
<concepts>
<concept>tag1</concept>
</concepts>
<files_read>
<file>/path/to/file</file>
</files_read>
<files_modified>
<file>/path/to/file</file>
</files_modified>
</observation>

Valid concepts (use ONLY these values in <concept> tags):
how-it-works, why-it-exists, what-changed, problem-solution, gotcha, pattern,
trade-off, best-practice, anti-pattern, architecture, security, performance,
testing, debugging, workflow, tooling, refactoring, api, database,
configuration, error-handling

Do NOT invent new concept names. If no concept fits, omit the <concepts> section.`
