package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// ErrLLMDisabled is returned when no LLM URL is configured.
var ErrLLMDisabled = fmt.Errorf("llm: disabled (ENGRAM_LLM_URL not set)")

// Completer is the interface satisfied by *Client. It allows callers to depend
// on an interface rather than the concrete type, enabling straightforward test
// stubbing without live HTTP endpoints.
type Completer interface {
	Complete(ctx context.Context, system, user string) (string, error)
}

// normalizeBaseURL strips a trailing "/v1" path segment (and any surrounding
// slashes) so that operators may supply either form:
//
//	https://host        → https://host
//	https://host/       → https://host
//	https://host/v1     → https://host
//	https://host/v1/    → https://host
//
// Only a trailing PATH segment named "v1" is removed. A "v1" that forms part
// of the host name (e.g. https://v1.example.com) is left intact because the
// operation is performed on parsed.Path, not on the raw URL string.
func normalizeBaseURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		// Unparseable input: return as-is rather than corrupt it.
		return raw
	}
	path := strings.TrimRight(parsed.Path, "/")
	if strings.HasSuffix(path, "/v1") {
		parsed.Path = path[:len(path)-3]
	} else {
		parsed.Path = path
	}
	return strings.TrimRight(parsed.String(), "/")
}

// Client communicates with an OpenAI-compatible /v1/chat/completions endpoint.
type Client struct {
	baseURL    string
	model      string
	apiKey     string
	httpClient *http.Client
}

// NewClient creates a Client from environment variables.
// Returns ErrLLMDisabled if ENGRAM_LLM_URL is empty.
//
// When ENGRAM_LLM_API_KEY is set, requests carry an
// "Authorization: Bearer <key>" header. When it is empty, no Authorization
// header is sent — supporting key-less endpoints such as a LAN LiteLLM or
// Ollama proxy on a trusted network.
func NewClient() (*Client, error) {
	rawURL := os.Getenv("ENGRAM_LLM_URL")
	if rawURL == "" {
		return nil, ErrLLMDisabled
	}
	baseURL := normalizeBaseURL(rawURL)
	model := os.Getenv("ENGRAM_LLM_MODEL")
	if model == "" {
		model = "chat-default"
	}
	return &Client{
		baseURL: baseURL,
		model:   model,
		apiKey:  os.Getenv("ENGRAM_LLM_API_KEY"),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

// chatRequest is the OpenAI-compatible request body for chat completions.
type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatResponse is the OpenAI-compatible chat completion response.
type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// Complete sends system + user messages to the chat completions endpoint and
// returns the text of choices[0].message.content. It retries up to 3 times
// with exponential backoff, respecting ctx cancellation between attempts.
func (c *Client) Complete(ctx context.Context, system, user string) (string, error) {
	body, err := json.Marshal(chatRequest{
		Model: c.model,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
	})
	if err != nil {
		return "", fmt.Errorf("llm: marshal request: %w", err)
	}

	endpoint := c.baseURL + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("llm: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	// 3-attempt exponential backoff, identical in shape to embedding/client.go.
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
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
			lastErr = fmt.Errorf("llm: HTTP %d: %s", resp.StatusCode, string(respBody))
			continue
		}

		var result chatResponse
		if err := json.Unmarshal(respBody, &result); err != nil {
			return "", fmt.Errorf("llm: decode response: %w", err)
		}
		if len(result.Choices) == 0 {
			return "", fmt.Errorf("llm: response contained no choices")
		}
		return result.Choices[0].Message.Content, nil
	}
	return "", fmt.Errorf("llm: after 3 attempts: %w", lastErr)
}
