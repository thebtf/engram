// Package expansion provides context-aware query expansion for improved search recall.
package expansion

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// HyDEConfig holds configuration for HyDE generation.
type HyDEConfig struct {
	// APIURL is the OpenAI-compatible chat completions endpoint.
	APIURL string
	// APIKey is the API key for authentication.
	APIKey string
	// Model is the LLM model to use (default: "gpt-4o-mini").
	Model string
	// MaxTokens is the max tokens for the LLM response (default: 150).
	MaxTokens int
	// Timeout is the deadline for LLM calls (default: 800ms).
	Timeout time.Duration
	// CacheTTL is the TTL for cached hypothetical documents (default: 5 minutes).
	CacheTTL time.Duration
}

// DefaultHyDEConfig returns sensible defaults for HyDE generation.
func DefaultHyDEConfig() HyDEConfig {
	return HyDEConfig{
		Model:     "gpt-4o-mini",
		MaxTokens: 150,
		Timeout:   800 * time.Millisecond,
		CacheTTL:  5 * time.Minute,
	}
}

// HyDEGenerator produces hypothetical documents for query expansion.
// Uses template-based generation for common intents (zero latency) and
// falls back to LLM generation for complex queries.
type HyDEGenerator struct {
	client *http.Client
	config HyDEConfig
	cache  map[string]*cacheEntry
	mu     sync.RWMutex
}

type cacheEntry struct {
	value     string
	expiresAt time.Time
}

// NewHyDEGenerator creates a new HyDE generator.
func NewHyDEGenerator(cfg HyDEConfig) *HyDEGenerator {
	return &HyDEGenerator{
		client: &http.Client{Timeout: cfg.Timeout},
		config: cfg,
		cache:  make(map[string]*cacheEntry),
	}
}

// Generate produces a hypothetical document for the given query and intent.
// For error/implementation intents, uses zero-latency templates.
// For question/architecture/general intents, calls the LLM.
// Returns empty string on failure (graceful degradation).
func (g *HyDEGenerator) Generate(ctx context.Context, query string, intent QueryIntent) string {
	query = strings.TrimSpace(query)
	if query == "" {
		return ""
	}

	// Try template-based generation first (zero latency)
	if hypothesis := g.generateFromTemplate(query, intent); hypothesis != "" {
		return hypothesis
	}

	// Fall back to LLM generation
	return g.generateFromLLM(ctx, query)
}

// generateFromTemplate uses intent-specific templates for common query patterns.
// Returns empty string if no template matches (caller falls back to LLM).
func (g *HyDEGenerator) generateFromTemplate(query string, intent QueryIntent) string {
	keyTerms := extractKeyTerms(query)
	if len(keyTerms) == 0 {
		return ""
	}

	terms := strings.Join(keyTerms, " ")

	switch intent {
	case IntentError:
		return fmt.Sprintf(
			"An observation about fixing %s: the error was caused by a misconfiguration "+
				"and was resolved by modifying the relevant code to handle the edge case correctly.",
			terms,
		)
	case IntentImplementation:
		return fmt.Sprintf(
			"Code implementation of %s: the feature was added using the existing patterns "+
				"in the codebase, following the established conventions for handlers and services.",
			terms,
		)
	default:
		return ""
	}
}

// generateFromLLM calls an OpenAI-compatible chat completions API.
// Returns empty string on any failure (timeout, error, short response).
func (g *HyDEGenerator) generateFromLLM(ctx context.Context, query string) string {
	if g.config.APIURL == "" || g.config.APIKey == "" {
		return ""
	}

	// Check cache
	cacheKey := g.cacheKey(query)
	if cached := g.getFromCache(cacheKey); cached != "" {
		return cached
	}

	reqBody := map[string]any{
		"model": g.config.Model,
		"messages": []map[string]string{
			{
				"role":    "system",
				"content": "Write a short technical document (2-3 sentences) that would answer this question. Be specific and factual. Do not include any preamble.",
			},
			{
				"role":    "user",
				"content": query,
			},
		},
		"max_tokens":  g.config.MaxTokens,
		"temperature": 0.3,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return ""
	}

	reqCtx, cancel := context.WithTimeout(ctx, g.config.Timeout)
	defer cancel()

	url := strings.TrimSuffix(g.config.APIURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+g.config.APIKey)

	resp, err := g.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return ""
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return ""
	}

	if len(result.Choices) == 0 {
		return ""
	}

	hypothesis := strings.TrimSpace(result.Choices[0].Message.Content)

	// Guard: skip if too short
	if len(hypothesis) < 20 {
		return ""
	}

	// Cache the result
	g.putInCache(cacheKey, hypothesis)

	return hypothesis
}

func (g *HyDEGenerator) cacheKey(query string) string {
	h := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(query))))
	return hex.EncodeToString(h[:8])
}

func (g *HyDEGenerator) getFromCache(key string) string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if entry, ok := g.cache[key]; ok {
		if time.Now().Before(entry.expiresAt) {
			return entry.value
		}
	}
	return ""
}

func (g *HyDEGenerator) putInCache(key, value string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Simple eviction: clear all if too large
	if len(g.cache) > 500 {
		g.cache = make(map[string]*cacheEntry)
	}

	g.cache[key] = &cacheEntry{
		value:     value,
		expiresAt: time.Now().Add(g.config.CacheTTL),
	}
}
