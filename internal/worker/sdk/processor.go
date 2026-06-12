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

// ---------------------------------------------------------------------------
// Request deduplication
// ---------------------------------------------------------------------------

// RequestDeduplicator prevents the same tool execution from being processed
// twice within a short window. This matters because hooks can fire multiple
// times for the same event (e.g., retries, UI refresh) and duplicate
// observations pollute the knowledge base with redundant entries.
type RequestDeduplicator struct {
	seen    map[string]int64 // hash -> Unix timestamp of first sighting
	mu      sync.RWMutex
	ttlSecs int64
	maxSize int
}

// NewRequestDeduplicator creates a deduplicator with the given TTL (seconds)
// and capacity. Once the map reaches maxSize, expired entries are evicted
// before adding new ones — this bounds memory without a background goroutine.
func NewRequestDeduplicator(ttlSecs int64, maxSize int) *RequestDeduplicator {
	return &RequestDeduplicator{
		seen:    make(map[string]int64),
		ttlSecs: ttlSecs,
		maxSize: maxSize,
	}
}

// IsDuplicate returns true if the given hash was recorded within the TTL window.
func (d *RequestDeduplicator) IsDuplicate(hash string) bool {
	now := time.Now().Unix()

	d.mu.RLock()
	ts, exists := d.seen[hash]
	d.mu.RUnlock()

	return exists && now-ts < d.ttlSecs
}

// Record marks the hash as seen at the current time.
// If the map is at capacity, entries older than TTL are evicted first.
func (d *RequestDeduplicator) Record(hash string) {
	now := time.Now().Unix()

	d.mu.Lock()
	defer d.mu.Unlock()

	// Evict expired entries when at capacity to keep memory bounded.
	// We evict lazily here rather than in a background goroutine to avoid
	// the complexity of goroutine ownership and shutdown ordering.
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

// hashRequest produces a short (16-char) SHA-256 prefix over tool name, input,
// and the first 1000 chars of output. Truncating the output is intentional:
// large outputs that differ only in trailing whitespace should not create
// distinct dedup keys.
func hashRequest(toolName, input, output string) string {
	h := sha256.New()
	h.Write([]byte(toolName))
	h.Write([]byte(input))
	h.Write([]byte(output[:min(len(output), 1000)]))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// ---------------------------------------------------------------------------
// Processor — type and callbacks
// ---------------------------------------------------------------------------

// BroadcastFunc is a callback for broadcasting events to SSE clients.
type BroadcastFunc func(event map[string]any)

// SyncObservationFunc is a callback for syncing observations to vector DB.
type SyncObservationFunc func(obs *models.Observation)

// SyncSummaryFunc is a callback for syncing summaries to vector DB.
type SyncSummaryFunc func(summary *models.SessionSummary)

// MaxVectorSyncWorkers caps the worker pool that drains the vector sync channel.
// Without this bound, a burst of observations would spawn an unbounded number
// of goroutines and exhaust resources in the embedding service.
const MaxVectorSyncWorkers = 8

// Processor handles SDK agent processing of observations and summaries.
// Fields are ordered for cache-line alignment (fieldalignment).
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

// SetBroadcastFunc sets the SSE broadcast callback.
func (p *Processor) SetBroadcastFunc(fn BroadcastFunc) {
	p.broadcastFunc = fn
}

// SetSyncObservationFunc sets the observation vector-sync callback.
func (p *Processor) SetSyncObservationFunc(fn SyncObservationFunc) {
	p.syncObservationFunc = fn
}

// SetSyncSummaryFunc sets the summary vector-sync callback.
func (p *Processor) SetSyncSummaryFunc(fn SyncSummaryFunc) {
	p.syncSummaryFunc = fn
}

// SetDedupConfig is retained for API compatibility but is a no-op in v5.
// The deduplicator is now configured at construction time via NewProcessor.
func (p *Processor) SetDedupConfig(_ float64, _ int) {}

// broadcast delivers an event to SSE clients via the registered callback.
// A nil callback is a valid no-op state (before wiring, or in tests).
func (p *Processor) broadcast(event map[string]any) {
	if p.broadcastFunc != nil {
		p.broadcastFunc(event)
	}
}

// enqueueObservationSync submits an observation to the bounded worker pool.
// If the channel is full, it falls back to a direct goroutine so that the
// calling request is not blocked — the tradeoff is an unbounded goroutine
// spike under extreme load, which is acceptable because the primary bound
// (the worker pool channel) absorbs the normal-load case.
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

// ---------------------------------------------------------------------------
// Constructor
// ---------------------------------------------------------------------------

// NewProcessor creates a fully configured Processor. The vector sync worker
// pool is NOT started here; call StartVectorSyncWorkers after wiring
// SetSyncObservationFunc to avoid workers draining a nil callback.
func NewProcessor() *Processor {
	cfg := config.Get()
	return &Processor{
		model:          cfg.Model,
		deduplicator:   NewRequestDeduplicator(300, 1000),                      // 5-min TTL, 1 000 max entries
		vectorSyncChan: make(chan *models.Observation, MaxVectorSyncWorkers*2), // buffered to absorb short bursts
		vectorSyncDone: make(chan struct{}),
	}
}

// ---------------------------------------------------------------------------
// Worker pool lifecycle
// ---------------------------------------------------------------------------

// StartVectorSyncWorkers launches the bounded worker pool. Must be called
// after SetSyncObservationFunc so workers have a valid callback.
func (p *Processor) StartVectorSyncWorkers() {
	for i := 0; i < MaxVectorSyncWorkers; i++ {
		p.vectorSyncWg.Add(1)
		go p.vectorSyncWorker()
	}
	log.Info().Int("workers", MaxVectorSyncWorkers).Msg("Vector sync worker pool started")
}

// StopVectorSyncWorkers signals workers to stop and waits for them to finish
// draining the channel. This is a synchronous shutdown — the caller blocks
// until all in-flight observations are processed.
func (p *Processor) StopVectorSyncWorkers() {
	close(p.vectorSyncDone)
	p.vectorSyncWg.Wait()
	log.Info().Msg("Vector sync worker pool stopped")
}

// vectorSyncWorker is a long-running goroutine in the sync pool.
// On shutdown signal (vectorSyncDone closed) it drains any remaining items
// from the channel before returning, so observations queued before StopVectorSyncWorkers
// are not silently dropped.
func (p *Processor) vectorSyncWorker() {
	defer p.vectorSyncWg.Done()
	for {
		select {
		case <-p.vectorSyncDone:
			// Drain: process all queued observations before exiting.
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

// ---------------------------------------------------------------------------
// Core processing
// ---------------------------------------------------------------------------

// IsAvailable always returns true — the LLM backend was removed in v5.
// This method exists to satisfy callers that gate on model availability.
func (p *Processor) IsAvailable() bool {
	return true
}

// ProcessObservation performs lightweight pre-filtering and dedup bookkeeping.
// In v5/PR-B the observation persistence and LLM extraction subsystem was
// retired; this method preserves the filtering contract so downstream
// code that checks return values continues to work without change.
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

// ProcessSummary is a stub that logs and returns nil. In v5 the session
// summary subsystem was retired — this signature is retained so callers
// compile without modification.
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

// ---------------------------------------------------------------------------
// Tool filtering
// ---------------------------------------------------------------------------

// shouldSkipTool returns true for tools that produce no meaningful knowledge.
// The skip list is intentionally conservative — unknown tools pass through
// so that new tools are not silently ignored.
func shouldSkipTool(toolName string) bool {
	switch toolName {
	// Internal task-tracking tools — not code operations.
	case "TodoWrite", "Task", "TaskOutput":
		return true
	// File discovery tools — listings with no semantic content.
	case "Glob", "ListDir", "LS", "KillShell":
		return true
	// Interactive tools — not executable artifacts.
	case "AskUserQuestion":
		return true
	// Plan-mode meta-tools — control flow, not work.
	case "EnterPlanMode", "ExitPlanMode":
		return true
	// Skill/command dispatch — meta-operations on the agent itself.
	case "Skill", "SlashCommand":
		return true
	// High-volume, low-value tools that flood the dedup table without insight.
	// Valuable knowledge should be saved via store_memory instead.
	case "Read", "Grep", "WebSearch":
		return true
	}
	// All other tools (Edit, Write, Bash, WebFetch, NotebookEdit, …) pass through.
	return false
}

// shouldSkipTrivialOperation applies a whitelist filter before making any API
// call. Only tool outputs that are likely to contain actionable knowledge reach
// the next stage. The whitelist inverts the default: skip everything except the
// explicitly interesting cases.
func shouldSkipTrivialOperation(toolName, inputStr, outputStr string) bool {
	// Too-short outputs carry no useful signal regardless of tool.
	if len(outputStr) < 50 {
		return true
	}

	lowerInput := strings.ToLower(inputStr)

	switch toolName {
	case "Edit", "Write":
		// Code modifications always carry architectural signal.
		return false

	case "Bash":
		// Only build/test invocations reveal project state worth recording.
		for _, interesting := range interestingBashCommands {
			if strings.Contains(lowerInput, interesting) {
				return false
			}
		}
		// All other Bash outputs (git, ls, curl, echo, etc.) are noise.
		return true

	default:
		// Everything else (Read, Grep, Agent, WebFetch, …) is high-volume, low-insight.
		return true
	}
}

// interestingBashCommands lists the command prefixes that indicate a
// build or test invocation worth recording as an observation.
var interestingBashCommands = []string{
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

// ---------------------------------------------------------------------------
// Filter helpers: self-referential and meaningful-content checks
// ---------------------------------------------------------------------------

// isSelfReferentialSummary returns true when the summary describes the memory
// agent's own initialization or waiting state rather than actual user work.
// Two or more matching phrases are required to avoid false positives — any
// single phrase might appear legitimately in real work summaries.
func isSelfReferentialSummary(summary *models.ParsedSummary) bool {
	content := strings.ToLower(
		summary.Request + " " + summary.Completed + " " + summary.Learned +
			" " + summary.NextSteps + " " + summary.Investigated + " " + summary.Notes,
	)

	matchCount := 0
	for _, phrase := range selfReferentialPhrases {
		if strings.Contains(content, phrase) {
			matchCount++
			if matchCount >= 2 {
				return true
			}
		}
	}
	return false
}

// selfReferentialPhrases is the vocabulary that indicates a summary about the
// memory agent itself rather than the user's work. Each phrase is lowercase
// to match the lowercased content string.
var selfReferentialPhrases = []string{
	// Agent role references
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

// hasMeaningfulContent returns true when the assistant message contains
// evidence of real technical work. This guards against generating summaries
// for sessions that were only system-message exchanges.
func hasMeaningfulContent(assistantMsg string) bool {
	// Short messages never contain enough substance for a useful summary.
	if len(strings.TrimSpace(assistantMsg)) < 200 {
		return false
	}

	lowerMsg := strings.ToLower(assistantMsg)

	// Bail early if multiple skip indicators are present — the message is
	// almost certainly a system-only or hook-status response.
	skipCount := 0
	for _, skip := range metaSkipIndicators {
		if strings.Contains(lowerMsg, skip) {
			skipCount++
			if skipCount >= 2 {
				return false
			}
		}
	}

	// Require at least two work indicators so incidental mentions of file
	// extensions don't pass the filter.
	matchCount := 0
	for _, indicator := range workIndicators {
		if strings.Contains(lowerMsg, strings.ToLower(indicator)) {
			matchCount++
			if matchCount >= 2 {
				return true
			}
		}
	}
	return false
}

// metaSkipIndicators are strings that appear in system-only or hook-status
// messages but rarely in messages about real user work.
var metaSkipIndicators = []string{
	"hook success",
	"callback hook",
	"session start",
	"sessionstart",
	"system-reminder",
	"memory extraction agent",
	"memory agent",
	"extraction agent",
	"no technical learnings",
	"waiting for",
	"waiting to",
	"no code or project work",
	"no substantive",
	"no work has been completed",
	"no work done",
	"awaiting tool",
	"awaiting user",
	"role definition",
	"operational guidelines",
	"analyze tool executions",
	"extract meaningful observations",
}

// workIndicators are strings that appear when Claude is doing real technical
// work — file extensions, modification verbs, code keywords.
var workIndicators = []string{
	// File extensions signal concrete artifacts were touched.
	".go", ".ts", ".js", ".py", ".md", ".json", ".yaml", ".yml",
	// Modification verbs signal actions were taken.
	"edited", "modified", "created", "deleted", "updated", "changed",
	"added", "removed", "fixed", "implemented", "refactored",
	// Code constructs confirm code was discussed or written.
	"```", "lines ", "function ", "const ", "var ", "let ",
	"type ", "struct ", "class ", "def ", "func ",
}

// ---------------------------------------------------------------------------
// JSON and file utilities
// ---------------------------------------------------------------------------

// toJSONString serialises v to a JSON string. String values pass through
// unchanged so that pre-serialised tool inputs are not double-encoded.
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

// safeResolvePath resolves path relative to cwd and verifies the result does
// not escape cwd. This is the security boundary for all file-system access in
// the processor — path traversal attacks via crafted tool inputs are rejected
// here before any os.Stat or os.ReadFile call.
func safeResolvePath(path, cwd string) (string, bool) {
	cleanPath := filepath.Clean(path)

	// A path that still contains ".." after cleaning has escaped the base.
	if strings.Contains(cleanPath, "..") {
		return "", false
	}

	if filepath.IsAbs(cleanPath) {
		if cwd == "" {
			return cleanPath, true
		}
		// For absolute paths, use filepath.Rel for cross-platform correctness.
		// On Windows, strings.HasPrefix would fail on case-insensitive comparisons.
		cleanCwd := filepath.Clean(cwd)
		rel, err := filepath.Rel(cleanCwd, cleanPath)
		if err != nil || strings.HasPrefix(rel, "..") {
			return "", false
		}
		return cleanPath, true
	}

	if cwd == "" {
		return cleanPath, true
	}

	cleanCwd := filepath.Clean(cwd)
	absPath := filepath.Join(cleanCwd, cleanPath)

	// filepath.Rel is the canonical escape check — it returns ".." prefix when
	// the joined path leaves the base directory.
	rel, err := filepath.Rel(cleanCwd, absPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", false
	}

	return absPath, true
}

// captureFileMtimes returns a map of path → mtime (Unix milliseconds) for all
// unique paths in filesRead and filesModified. Paths that fail validation or
// do not exist are silently omitted — remote/Docker setups where the server
// cannot stat client paths are the normal case, not an error.
func captureFileMtimes(filesRead, filesModified []string, cwd string) map[string]int64 {
	// Deduplicate: a path that appears in both read and modified lists should
	// produce only one entry.
	allPaths := make(map[string]struct{}, len(filesRead)+len(filesModified))
	for _, p := range filesRead {
		allPaths[p] = struct{}{}
	}
	for _, p := range filesModified {
		allPaths[p] = struct{}{}
	}

	// Goroutine overhead outweighs the benefit for small path lists.
	if len(allPaths) <= 10 {
		return captureFileMtimesSequential(allPaths, cwd)
	}
	return captureFileMtimesParallel(allPaths, cwd)
}

// captureFileMtimesSequential stats paths one at a time. Used for short lists
// where spawning goroutines would cost more than the parallelism saves.
func captureFileMtimesSequential(paths map[string]struct{}, cwd string) map[string]int64 {
	mtimes := make(map[string]int64, len(paths))
	for path := range paths {
		absPath, ok := safeResolvePath(path, cwd)
		if !ok {
			continue
		}
		if info, err := os.Stat(absPath); err == nil {
			mtimes[path] = info.ModTime().UnixMilli()
		}
	}
	return mtimes
}

// captureFileMtimesParallel stats paths using a bounded goroutine pool.
// A semaphore of 8 slots prevents the kernel from being flooded with concurrent
// stat syscalls, which can cause measurable slowdown on network-mounted
// filesystems.
func captureFileMtimesParallel(paths map[string]struct{}, cwd string) map[string]int64 {
	type mtimeResult struct {
		path  string
		mtime int64
	}

	results := make(chan mtimeResult, len(paths))
	sem := make(chan struct{}, 8) // limit concurrent stat calls
	var wg sync.WaitGroup

	for path := range paths {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			absPath, ok := safeResolvePath(p, cwd)
			if !ok {
				return
			}
			if info, err := os.Stat(absPath); err == nil {
				results <- mtimeResult{path: p, mtime: info.ModTime().UnixMilli()}
			}
		}(path)
	}

	// Close results after all goroutines finish so the range loop below
	// terminates cleanly. This goroutine owns the results channel's close.
	go func() {
		wg.Wait()
		close(results)
	}()

	mtimes := make(map[string]int64, len(paths))
	for res := range results {
		mtimes[res.path] = res.mtime
	}
	return mtimes
}

// GetFileMtimes returns current modification times for a flat list of paths.
// Used by the context injection layer to detect stale files before injecting
// their content. In Docker/remote mode, os.Stat on client paths returns an
// error and the result is an empty map — this is the intended no-op behaviour.
func GetFileMtimes(paths []string, cwd string) map[string]int64 {
	return captureFileMtimes(paths, nil, cwd)
}

// GetFileContent reads a file for verification purposes.
// Returns (content, true) on success. Returns ("", false) when the path fails
// validation, the file does not exist, or the read fails — callers should treat
// false as "content unavailable, skip verification", not as an error.
// Content is truncated to 2 000 bytes: enough context for staleness checking
// without paying the cost of reading large generated files.
func GetFileContent(path, cwd string) (string, bool) {
	absPath, ok := safeResolvePath(path, cwd)
	if !ok {
		return "", false
	}

	content, err := os.ReadFile(absPath) // #nosec G304 -- path validated by safeResolvePath
	if err != nil {
		return "", false
	}

	if len(content) > 2000 {
		return string(content[:2000]) + "\n...[truncated]", true
	}
	return string(content), true
}
