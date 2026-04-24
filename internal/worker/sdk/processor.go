// Package sdk provides SDK agent integration for engram.
package sdk

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	json "github.com/goccy/go-json"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/pkg/models"
)

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
// Field order optimized for memory alignment (fieldalignment).
type Processor struct {
	broadcastFunc       BroadcastFunc
	syncObservationFunc SyncObservationFunc
	syncSummaryFunc     SyncSummaryFunc
	deduplicator        *RequestDeduplicator
	vectorSyncChan      chan *models.Observation
	vectorSyncDone      chan struct{}
	model               string
	vectorSyncWg        sync.WaitGroup
}

// SetBroadcastFunc sets the broadcast callback for SSE events.
func (p *Processor) SetBroadcastFunc(fn BroadcastFunc) {
	p.broadcastFunc = fn
}

// SetSyncObservationFunc sets the callback for syncing observations to vector DB.
func (p *Processor) SetSyncObservationFunc(fn SyncObservationFunc) {
	p.syncObservationFunc = fn
}

// SetSyncSummaryFunc sets the callback for syncing summaries to vector DB.
func (p *Processor) SetSyncSummaryFunc(fn SyncSummaryFunc) {
	p.syncSummaryFunc = fn
}

// SetDedupConfig is retained for compatibility but is a no-op in v5.
func (p *Processor) SetDedupConfig(_ float64, _ int) {}

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

// NewProcessor creates a new SDK processor.
func NewProcessor() *Processor {
	cfg := config.Get()
	return &Processor{
		model:          cfg.Model,
		deduplicator:   NewRequestDeduplicator(300, 1000),                      // 5-minute TTL, 1000 max entries
		vectorSyncChan: make(chan *models.Observation, MaxVectorSyncWorkers*2), // Buffered channel
		vectorSyncDone: make(chan struct{}),
	}
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

// IsAvailable always returns true — LLM backend removed in v5.
func (p *Processor) IsAvailable() bool {
	return true
}

// ProcessObservation no longer persists observations in v5/PR-B.
// The observation and summary subsystem is being retired; this method now performs
// only lightweight filtering/dedup bookkeeping and exits explicitly.
func (p *Processor) ProcessObservation(_ context.Context, sdkSessionID, project string, toolName string, toolInput, toolResponse any, _ int, _ string, _ ...string) error {
	if shouldSkipTool(toolName) {
		log.Debug().Str("tool", toolName).Msg("SDK observation extraction skipped for uninteresting tool in v5")
		return nil
	}

	inputStr := toJSONString(toolInput)
	outputStr := toJSONString(toolResponse)
	if shouldSkipTrivialOperation(toolName, inputStr, outputStr) {
		log.Debug().Str("tool", toolName).Msg("SDK observation extraction skipped for trivial operation in v5")
		return nil
	}

	reqHash := hashRequest(toolName, inputStr, outputStr)
	if p.deduplicator.IsDuplicate(reqHash) {
		log.Debug().Str("tool", toolName).Msg("SDK observation extraction duplicate skipped in v5")
		return nil
	}
	p.deduplicator.Record(reqHash)

	log.Info().
		Str("sdk_session_id", sdkSessionID).
		Str("project", project).
		Str("tool", toolName).
		Msg("SDK observation extraction removed in v5 cleanup; skipping persistence and LLM extraction")
	return nil
}


// ProcessSummary processes a session summary request.
func (p *Processor) ProcessSummary(_ context.Context, sessionDBID int64, sdkSessionID, project, userPrompt, lastUserMsg, lastAssistantMsg string) error {
	log.Info().
		Int64("sessionId", sessionDBID).
		Str("sdkSessionID", sdkSessionID).
		Str("project", project).
		Int("userPromptLen", len(userPrompt)).
		Int("lastUserMsgLen", len(lastUserMsg)).
		Int("lastAssistantMsgLen", len(lastAssistantMsg)).
		Msg("Skipping ProcessSummary: summaries removed in v5 cleanup")
	return nil
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

