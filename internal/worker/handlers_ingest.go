// Package worker provides the main worker service for engram.
package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/pipeline"
	"github.com/thebtf/engram/internal/privacy"
	"github.com/thebtf/engram/pkg/models"
)

// IngestRequest is the request body for the event ingest endpoint.
type IngestRequest struct {
	ToolInput     any    `json:"tool_input"`
	ToolResult    any    `json:"tool_result"`
	SessionID     string `json:"session_id"`
	Project       string `json:"project"`
	ToolName      string `json:"tool_name"`
	WorkstationID string `json:"workstation_id"`
}

// deduplicationCache provides simple TTL-based deduplication for ingest requests.
type deduplicationCache struct {
	entries map[string]time.Time
	ttl     time.Duration
	mu      sync.Mutex
}

// newDeduplicationCache creates a new cache with automatic cleanup.
func newDeduplicationCache(ttl time.Duration) *deduplicationCache {
	c := &deduplicationCache{
		entries: make(map[string]time.Time),
		ttl:     ttl,
	}
	// Background cleanup at half the TTL interval
	go func() {
		ticker := time.NewTicker(ttl / 2)
		defer ticker.Stop()
		for range ticker.C {
			c.cleanup()
		}
	}()
	return c
}

// isDuplicate returns true if the key was seen within the TTL window.
// Records the key if it is new or expired.
func (c *deduplicationCache) isDuplicate(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t, ok := c.entries[key]; ok && time.Since(t) < c.ttl {
		return true
	}
	c.entries[key] = time.Now()
	return false
}

// cleanup removes expired entries from the cache.
func (c *deduplicationCache) cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for k, t := range c.entries {
		if now.Sub(t) >= c.ttl {
			delete(c.entries, k)
		}
	}
}

// handleIngestEvent handles POST /api/events/ingest.
// Receives raw tool events from Claude Code hooks, stores them in raw_events,
// runs the deterministic Level 0 pipeline, creates an observation, and
// triggers asynchronous embedding via vectorSync.
func (s *Service) handleIngestEvent(w http.ResponseWriter, r *http.Request) {
	var req IngestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Validate required fields
	if req.ToolName == "" {
		http.Error(w, "tool_name is required", http.StatusBadRequest)
		return
	}
	if req.SessionID == "" {
		http.Error(w, "session_id is required", http.StatusBadRequest)
		return
	}

	// Stringify tool_input and tool_result for pipeline functions
	toolInputStr := toJSONString(req.ToolInput)
	toolResultStr := toJSONString(req.ToolResult)

	// Redact secrets from tool input/result before any pipeline processing.
	if privacy.ContainsSecrets(toolInputStr) {
		log.Warn().Str("tool", req.ToolName).Msg("ingest: tool_input contains secrets — redacting before pipeline processing")
		toolInputStr = privacy.RedactSecrets(toolInputStr)
	}
	if privacy.ContainsSecrets(toolResultStr) {
		log.Warn().Str("tool", req.ToolName).Msg("ingest: tool_result contains secrets — redacting before pipeline processing")
		toolResultStr = privacy.RedactSecrets(toolResultStr)
	}

	// Filter: skip tools that should never be observed
	if pipeline.ShouldSkipTool(req.ToolName) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "skipped", "reason": "filtered_tool"})
		return
	}

	// Filter: skip trivial operations (e.g. tiny reads, no-op results)
	if pipeline.ShouldSkipTrivial(req.ToolName, toolInputStr, toolResultStr) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "skipped", "reason": "trivial"})
		return
	}

	// Deduplication: skip identical events within the TTL window
	dedupKey := computeDedupKey(req.ToolName, toolInputStr, toolResultStr)
	if s.ingestDedup != nil && s.ingestDedup.isDuplicate(dedupKey) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "skipped", "reason": "duplicate"})
		return
	}

	// Store raw event first (source of truth)
	toolInputJSON, _ := json.Marshal(req.ToolInput)
	toolResultJSON, _ := json.Marshal(req.ToolResult)

	rawEvent := &models.RawEvent{
		SessionID:     req.SessionID,
		ToolName:      req.ToolName,
		ToolInput:     toolInputJSON,
		ToolResult:    toolResultJSON,
		Project:       req.Project,
		WorkstationID: req.WorkstationID,
	}

	eventID, err := s.rawEventStore.InsertRawEvent(r.Context(), rawEvent)
	if err != nil {
		log.Error().Err(err).Str("tool", req.ToolName).Msg("Failed to store raw event")
		http.Error(w, "failed to store event", http.StatusInternalServerError)
		return
	}

	// Run deterministic Level 0 pipeline (no LLM involved)
	obsType := pipeline.ClassifyEvent(req.ToolName, toolInputStr, toolResultStr)
	title := pipeline.GenerateTitle(req.ToolName, toolInputStr)
	concepts := pipeline.ExtractConcepts(req.ToolName, toolInputStr, toolResultStr)
	filePaths := pipeline.ExtractFilePaths(toolInputStr, toolResultStr)
	facts := pipeline.ExtractFacts(req.ToolName, toolInputStr, toolResultStr)

	// Classify file paths into read vs modified based on tool semantics
	filesRead, filesModified := classifyFilesByTool(req.ToolName, filePaths)

	parsed := &models.ParsedObservation{
		Type:          obsType,
		Title:         title,
		Concepts:      concepts,
		Facts:         facts,
		FilesRead:     filesRead,
		FilesModified: filesModified,
	}
	parsed.SourceType = models.ClassifySourceType(req.ToolName)

	// Store observation using the existing store interface
	obsID, _, err := s.observationStore.StoreObservation(r.Context(), req.SessionID, req.Project, parsed, 0, 0)
	if err != nil {
		log.Error().Err(err).Str("tool", req.ToolName).Msg("Failed to store observation")
		http.Error(w, "failed to store observation", http.StatusInternalServerError)
		return
	}

	// Mark raw event as processed
	if markErr := s.rawEventStore.MarkProcessed(r.Context(), eventID); markErr != nil {
		log.Warn().Err(markErr).Int64("eventId", eventID).Msg("Failed to mark raw event as processed")
	}

	// Async: sync observation to vector store for embedding search
	if s.vectorSync != nil {
		obs := models.NewObservation(req.SessionID, req.Project, parsed, 0, 0)
		obs.ID = obsID
		s.asyncVectorSync(func() {
			if syncErr := s.vectorSync.SyncObservation(s.ctx, obs); syncErr != nil {
				log.Error().Err(syncErr).Int64("obsId", obsID).Msg("Failed to sync observation to vector store")
			}
		})
	}

	log.Debug().
		Int64("eventId", eventID).
		Int64("obsId", obsID).
		Str("tool", req.ToolName).
		Str("type", string(obsType)).
		Msg("Ingested event and created Level 0 observation")

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":   "accepted",
		"event_id": eventID,
		"obs_id":   obsID,
		"type":     obsType,
		"title":    title,
	})
}

// classifyFilesByTool splits file paths into read vs modified based on the tool name.
func classifyFilesByTool(toolName string, paths []string) (filesRead, filesModified []string) {
	switch toolName {
	case "Edit", "Write", "NotebookEdit":
		return nil, paths
	case "Read", "Grep", "WebFetch", "WebSearch":
		return paths, nil
	case "Bash":
		// Bash can both read and write; conservatively classify all as modified
		return nil, paths
	default:
		return paths, nil
	}
}

// computeDedupKey creates a short hash for deduplication of ingest requests.
func computeDedupKey(toolName, toolInput, toolResult string) string {
	// Truncate result to first 200 chars to avoid hashing massive outputs
	truncResult := toolResult
	if len(truncResult) > 200 {
		truncResult = truncResult[:200]
	}
	h := sha256.Sum256([]byte(toolName + "|" + toolInput + "|" + truncResult))
	return hex.EncodeToString(h[:16]) // 128-bit is sufficient for dedup
}

// toJSONString converts any value to a string representation for pipeline processing.
// Strings pass through unchanged; other types are JSON-marshalled.
func toJSONString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(data)
}
