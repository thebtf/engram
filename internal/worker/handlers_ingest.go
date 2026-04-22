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

// handleIngestEvent godoc
// @Summary Ingest tool event
// @Description Receives raw tool events from Claude Code hooks. Stores in raw_events, runs deterministic Level 0 pipeline, creates an observation, and triggers async embedding.
// @Tags Events
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body IngestRequest true "Tool event data"
// @Success 202 {object} map[string]interface{}
// @Failure 400 {string} string "bad request"
// @Failure 500 {string} string "internal error"
// @Router /api/events/ingest [post]
func (s *Service) handleIngestEvent(w http.ResponseWriter, r *http.Request) {
	log.Info().Msg("/api/events/ingest removed in v5; rejecting ingest request without reading payload")

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotImplemented)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":         "removed_in_v5",
		"error":          "event ingest endpoint was removed in v5",
		"tool_name":      "",
		"session_id":     "",
		"project":        "",
		"workstation_id": "",
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
