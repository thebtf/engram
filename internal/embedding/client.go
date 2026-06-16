package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// ErrEmbeddingDisabled is returned when no embedding URL is configured.
var ErrEmbeddingDisabled = fmt.Errorf("embedding: disabled (ENGRAM_EMBEDDING_URL not set)")

// normalizeEmbeddingBaseURL strips a trailing "/v1" path segment (and any
// surrounding slashes) so that operators may supply either:
//
//	https://host        → https://host
//	https://host/       → https://host
//	https://host/v1     → https://host
//	https://host/v1/    → https://host
//
// Only a trailing PATH segment named "v1" is removed. A "v1" that forms part
// of the host name (e.g. https://v1.example.com) is left intact.
func normalizeEmbeddingBaseURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		// Unparseable input: return as-is rather than corrupt it.
		return raw
	}
	// Strip exactly one trailing "/v1" path segment if present.
	// Operating on parsed.Path avoids the false-positive where the host
	// itself ends with "v1" (e.g. "https://v1" would corrupt to "https:"
	// under a pure string-suffix match on the full URL string).
	path := strings.TrimRight(parsed.Path, "/")
	if strings.HasSuffix(path, "/v1") {
		parsed.Path = path[:len(path)-3]
	} else {
		parsed.Path = path
	}
	return strings.TrimRight(parsed.String(), "/")
}

// Client communicates with a LiteLLM-compatible /v1/embeddings endpoint.
type Client struct {
	baseURL    string
	model      string
	apiKey     string
	dimensions int
	httpClient *http.Client
}

// NewClient creates an embedding Client from environment variables.
// Returns ErrEmbeddingDisabled if ENGRAM_EMBEDDING_URL is empty.
//
// When ENGRAM_EMBEDDING_API_KEY is set, requests carry an
// "Authorization: Bearer <key>" header. When it is empty, no Authorization
// header is sent — supporting key-less endpoints such as a LAN LiteLLM or
// Ollama proxy on a trusted network.
func NewClient() (*Client, error) {
	rawURL := os.Getenv("ENGRAM_EMBEDDING_URL")
	if rawURL == "" {
		return nil, ErrEmbeddingDisabled
	}
	baseURL := normalizeEmbeddingBaseURL(rawURL)
	model := os.Getenv("ENGRAM_EMBEDDING_MODEL")
	if model == "" {
		model = "text-embedding"
	}
	// dimensions sent as the OpenAI-compatible `dimensions` request param so an
	// MRL-capable model (Qwen3-Embedding) returns a vector at the unified EmbeddingDim.
	//
	// Only two values are coherent with the fixed vector(EmbeddingDim) columns:
	//   - EmbeddingDim (default): request the unified dimension.
	//   - 0: OMIT the param entirely — for endpoints that reject `dimensions`
	//        (a non-MRL model, or a proxy that 400s on the unknown field). The
	//        model must then natively return EmbeddingDim, or the startup assert /
	//        backfill dim guard will catch the mismatch.
	// Any OTHER value (e.g. a stale ENGRAM_EMBEDDING_DIMENSIONS=4096 left in a deploy
	// template) would make the client request a size the columns cannot store — every
	// INSERT would fail and recall would silently degrade to FTS forever. So a
	// non-zero, non-EmbeddingDim override is rejected: clamp to EmbeddingDim and warn
	// loudly rather than honor a column-incompatible dimension.
	dimensions := EmbeddingDim
	if raw := os.Getenv("ENGRAM_EMBEDDING_DIMENSIONS"); raw != "" {
		if d, err := strconv.Atoi(raw); err != nil {
			log.Warn().Str("value", raw).Int("using", EmbeddingDim).
				Msg("embedding: ENGRAM_EMBEDDING_DIMENSIONS is not an integer, using EmbeddingDim")
		} else if d == 0 || d == EmbeddingDim {
			dimensions = d
		} else {
			log.Warn().Int("requested", d).Int("using", EmbeddingDim).
				Msg("embedding: ENGRAM_EMBEDDING_DIMENSIONS must be 0 (omit) or EmbeddingDim; " +
					"a different value cannot be stored in the vector(EmbeddingDim) columns — clamping to EmbeddingDim")
		}
	}
	return &Client{
		baseURL:    baseURL,
		model:      model,
		apiKey:     os.Getenv("ENGRAM_EMBEDDING_API_KEY"),
		dimensions: dimensions,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

// embeddingRequest is the OpenAI-compatible request body.
// Dimensions is omitted (omitempty) when zero so endpoints that do not support
// the MRL `dimensions` param receive a byte-identical request to the pre-1536 era.
type embeddingRequest struct {
	Input      []string `json:"input"`
	Model      string   `json:"model"`
	Dimensions int      `json:"dimensions,omitempty"`
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

	reqBody := embeddingRequest{
		Input: texts,
		Model: c.model,
	}
	if c.dimensions > 0 {
		reqBody.Dimensions = c.dimensions
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("embedding: marshal request: %w", err)
	}

	url := c.baseURL + "/v1/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embedding: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

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
			if d.Index >= 0 && d.Index < len(vectors) {
				vectors[d.Index] = d.Embedding
			}
		}
		return vectors, nil
	}
	return nil, fmt.Errorf("embedding: after 3 attempts: %w", lastErr)
}
