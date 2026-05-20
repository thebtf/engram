package embedding

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

// ErrEmbeddingDisabled is returned when no embedding URL is configured.
var ErrEmbeddingDisabled = fmt.Errorf("embedding: disabled (ENGRAM_EMBEDDING_URL not set)")

// Client communicates with a LiteLLM-compatible /v1/embeddings endpoint.
type Client struct {
	baseURL    string
	model      string
	httpClient *http.Client
}

// NewClient creates an embedding Client from environment variables.
// Returns ErrEmbeddingDisabled if ENGRAM_EMBEDDING_URL is empty.
func NewClient() (*Client, error) {
	baseURL := os.Getenv("ENGRAM_EMBEDDING_URL")
	if baseURL == "" {
		return nil, ErrEmbeddingDisabled
	}
	model := os.Getenv("ENGRAM_EMBEDDING_MODEL")
	if model == "" {
		model = "text-embedding"
	}
	return &Client{
		baseURL: baseURL,
		model:   model,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

// embeddingRequest is the OpenAI-compatible request body.
type embeddingRequest struct {
	Input []string `json:"input"`
	Model string   `json:"model"`
}

// embeddingResponse is the OpenAI-compatible response.
type embeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
}

// Model returns the embedding model name configured for this client.
func (c *Client) Model() string {
	return c.model
}

// Embed generates embeddings for the given texts.
// Returns one vector per input text, in the same order.
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	body, err := json.Marshal(embeddingRequest{
		Input: texts,
		Model: c.model,
	})
	if err != nil {
		return nil, fmt.Errorf("embedding: marshal request: %w", err)
	}

	url := c.baseURL + "/v1/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embedding: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// Simple retry: 3 attempts with exponential backoff
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(1<<attempt) * time.Second):
			}
			req.Body = io.NopCloser(bytes.NewReader(body))
		}

		resp, doErr := c.httpClient.Do(req)
		if doErr != nil {
			lastErr = doErr
			continue
		}

		respBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("embedding: HTTP %d: %s", resp.StatusCode, string(respBody))
			continue
		}

		var result embeddingResponse
		if err := json.Unmarshal(respBody, &result); err != nil {
			return nil, fmt.Errorf("embedding: decode response: %w", err)
		}

		vectors := make([][]float32, len(texts))
		for _, d := range result.Data {
			if d.Index < len(vectors) {
				vectors[d.Index] = d.Embedding
			}
		}
		return vectors, nil
	}
	return nil, fmt.Errorf("embedding: after 3 attempts: %w", lastErr)
}
