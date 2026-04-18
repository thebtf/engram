// Package llmclient provides a minimal OpenAI-compatible LLM HTTP client.
// It replaces the removed internal/learning package for callers that only
// need to call Complete() against an OpenAI-compatible API.
package llmclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// LLMClient is the minimal interface for calling an LLM completion API.
type LLMClient interface {
	Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error)
	IsConfigured() bool
}

// Config holds configuration for the OpenAI-compatible client.
type Config struct {
	BaseURL   string
	APIKey    string
	Model     string
	MaxTokens int
	Timeout   time.Duration
}

// DefaultConfig returns a Config populated from environment variables:
//
//	ENGRAM_LLM_URL    — base URL of the OpenAI-compatible endpoint
//	ENGRAM_LLM_API_KEY — API key (optional for local endpoints)
//	ENGRAM_LLM_MODEL  — model name (default: "gpt-4o-mini")
func DefaultConfig() Config {
	model := os.Getenv("ENGRAM_LLM_MODEL")
	if model == "" {
		model = "gpt-4o-mini"
	}
	return Config{
		BaseURL:   os.Getenv("ENGRAM_LLM_URL"),
		APIKey:    os.Getenv("ENGRAM_LLM_API_KEY"),
		Model:     model,
		MaxTokens: 2000,
		Timeout:   120 * time.Second,
	}
}

// openAIClient implements LLMClient against an OpenAI-compatible HTTP endpoint.
type openAIClient struct {
	cfg        Config
	httpClient *http.Client
}

// New creates a new LLM client with the given configuration.
func New(cfg Config) LLMClient {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return &openAIClient{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: timeout},
	}
}

// IsConfigured returns true when the base URL is set (minimum requirement).
func (c *openAIClient) IsConfigured() bool {
	return c.cfg.BaseURL != ""
}

// Complete sends a chat completion request to the configured endpoint.
func (c *openAIClient) Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	if c.cfg.BaseURL == "" {
		return "", fmt.Errorf("LLM not configured: ENGRAM_LLM_URL is not set")
	}

	payload := map[string]any{
		"model": c.cfg.Model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
		"max_tokens": c.cfg.MaxTokens,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	url := c.cfg.BaseURL + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("LLM HTTP call: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		limitedBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("LLM API returned status %d: %s", resp.StatusCode, string(limitedBody))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("LLM returned no choices")
	}
	return result.Choices[0].Message.Content, nil
}
