// Package learning provides LLM-based extraction of behavioral patterns from session transcripts.
package learning

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// LLMClient defines the interface for LLM completion calls.
type LLMClient interface {
	Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

// OpenAIClient implements LLMClient using an OpenAI-compatible API.
type OpenAIClient struct {
	baseURL   string
	apiKey    string
	model     string
	maxTokens int
	client    *http.Client
}

// OpenAIConfig holds configuration for the OpenAI-compatible client.
type OpenAIConfig struct {
	BaseURL   string        // ENGRAM_LLM_URL (default: reuse ENGRAM_EMBEDDING_URL base)
	APIKey    string        // ENGRAM_LLM_API_KEY
	Model     string        // ENGRAM_LLM_MODEL (default: gpt-4o-mini)
	MaxTokens int           // ENGRAM_LLM_MAX_TOKENS (default: 4096)
	Timeout   time.Duration // HTTP client timeout (default: 120s)
}

// DefaultOpenAIConfig returns config from environment variables.
func DefaultOpenAIConfig() OpenAIConfig {
	cfg := OpenAIConfig{
		BaseURL: os.Getenv("ENGRAM_LLM_URL"),
		APIKey:  os.Getenv("ENGRAM_LLM_API_KEY"),
		Model:   os.Getenv("ENGRAM_LLM_MODEL"),
	}

	if cfg.BaseURL == "" {
		// Fall back to embedding base URL if set
		if embURL := os.Getenv("ENGRAM_EMBEDDING_BASE_URL"); embURL != "" {
			cfg.BaseURL = embURL
		}
	}

	if cfg.APIKey == "" {
		// Fall back to embedding API key if LLM-specific key not set
		if embKey := os.Getenv("ENGRAM_EMBEDDING_API_KEY"); embKey != "" {
			cfg.APIKey = embKey
		}
	}

	if cfg.Model == "" {
		cfg.Model = "gpt-4o-mini"
	}

	if v := os.Getenv("ENGRAM_LLM_MAX_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MaxTokens = n
		}
	}

	return cfg
}

// NewOpenAIClient creates a new OpenAI-compatible LLM client.
func NewOpenAIClient(cfg OpenAIConfig) *OpenAIClient {
	timeout := 120 * time.Second
	if cfg.Timeout > 0 {
		timeout = cfg.Timeout
	}
	maxTokens := cfg.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096 // Sensible default for thinking models
	}
	return &OpenAIClient{
		baseURL:   cfg.BaseURL,
		apiKey:    cfg.APIKey,
		model:     cfg.Model,
		maxTokens: maxTokens,
		client:    &http.Client{Timeout: timeout},
	}
}

// chatRequest is the OpenAI chat completion request format.
type chatRequest struct {
	Model     string        `json:"model"`
	Messages  []chatMessage `json:"messages"`
	MaxTokens int           `json:"max_tokens,omitempty"` // Max output tokens (reasoning + content). Default 4096 for thinking models.
	Timeout   int           `json:"timeout,omitempty"`    // LiteLLM: override proxy→backend timeout (seconds)
}

// chatMessage is a single message in the chat completion request.
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatResponse is the OpenAI chat completion response format.
type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Complete sends a chat completion request and returns the response text.
func (c *OpenAIClient) Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	if c.baseURL == "" {
		return "", fmt.Errorf("LLM URL not configured (set ENGRAM_LLM_URL)")
	}

	reqBody := chatRequest{
		Model: c.model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		MaxTokens: c.maxTokens,
		Timeout:   300,  // Override LiteLLM proxy→backend timeout for slow local models
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	base := strings.TrimSuffix(c.baseURL, "/")
	// Support both "http://host:port" and "http://host:port/v1" formats
	if strings.HasSuffix(base, "/v1") {
		base = strings.TrimSuffix(base, "/v1")
	}
	url := base + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("LLM request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("LLM API error %d: %s", resp.StatusCode, string(respBody))
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return "", fmt.Errorf("unmarshal response: %w", err)
	}

	if chatResp.Error != nil {
		return "", fmt.Errorf("LLM error: %s", chatResp.Error.Message)
	}

	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("LLM returned no choices")
	}

	return chatResp.Choices[0].Message.Content, nil
}

// IsConfigured returns true if the LLM client has a URL configured.
func (c *OpenAIClient) IsConfigured() bool {
	return c.baseURL != ""
}
