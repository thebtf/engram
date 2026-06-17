// Package reranking provides a cross-encoder rerank client for the recall path.
//
// This package is a NEW build (rank-4, 2026-06-17). It is NOT a restoration of the
// v5-demolished internal/reranking ONNX cross-encoder (removed in PR #185). It targets
// a LiteLLM /rerank Cohere-compatible endpoint (e.g. bge-reranker served via vLLM) over
// HTTP — no local inference, no ONNX, no onnxruntime dependency. See AGENTS.md
// "V5 DEMOLITION GUARD": the old package is pre-demolition-stale and must never be revived.
package reranking

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

	"github.com/rs/zerolog/log"
)

// ErrRerankDisabled is returned when no rerank URL is configured. The recall path
// treats this as "reranker absent" and silently keeps the fusion order — a missing
// reranker MUST NEVER block or fail recall.
var ErrRerankDisabled = fmt.Errorf("reranking: disabled (ENGRAM_RERANK_URL not set)")

// PassageScore is one reranked passage: the original index into the input slice plus
// the relevance score the cross-encoder assigned. Cohere/LiteLLM normalizes
// relevance_score to [0,1] server-side, so no client-side sigmoid is applied.
type PassageScore struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

// normalizeRerankBaseURL strips a trailing "/v1", "/rerank", or "/v1/rerank" path
// segment so operators may supply any of:
//
//	https://host                 → https://host
//	https://host/v1              → https://host
//	https://host/rerank          → https://host
//	https://host/v1/rerank       → https://host
//
// The client always re-appends the canonical "/v1/rerank" path. A "v1" inside the
// host (https://v1.example.com) is left intact.
func normalizeRerankBaseURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	path := strings.TrimRight(parsed.Path, "/")
	for _, suffix := range []string{"/v1/rerank", "/rerank", "/v1"} {
		if strings.HasSuffix(path, suffix) {
			path = path[:len(path)-len(suffix)]
			break
		}
	}
	parsed.Path = path
	return strings.TrimRight(parsed.String(), "/")
}

// Client communicates with a LiteLLM-compatible /v1/rerank (Cohere) endpoint.
// Mirrors the embedding.Client wiring pattern: env-gated constructor, optional Bearer
// auth, bounded HTTP client.
type Client struct {
	baseURL    string
	model      string
	apiKey     string
	httpClient *http.Client
}

// NewClient creates a rerank Client from environment variables.
// Returns ErrRerankDisabled if ENGRAM_RERANK_URL is empty (the no-reranker default).
//
// Env (all NEW names — the phantom ENGRAM_RERANKING_* vars from the v5 era have zero
// code reads and are deliberately NOT reused):
//   - ENGRAM_RERANK_URL    — base URL of the LiteLLM rerank endpoint (required to enable)
//   - ENGRAM_RERANK_MODEL  — model/deployment alias (default "bge-reranker")
//   - ENGRAM_RERANK_API_KEY — optional Bearer token; omitted when empty (key-less LAN proxy)
func NewClient() (*Client, error) {
	rawURL := os.Getenv("ENGRAM_RERANK_URL")
	if rawURL == "" {
		return nil, ErrRerankDisabled
	}
	model := os.Getenv("ENGRAM_RERANK_MODEL")
	if model == "" {
		model = "bge-reranker"
	}
	return &Client{
		baseURL: normalizeRerankBaseURL(rawURL),
		model:   model,
		apiKey:  os.Getenv("ENGRAM_RERANK_API_KEY"),
		httpClient: &http.Client{
			// Recall is latency-sensitive; keep the rerank call bounded. On timeout the
			// caller falls back to fusion order, so this is a soft ceiling not a hard dep.
			Timeout: 10 * time.Second,
		},
	}, nil
}

// Model returns the configured rerank model name.
func (c *Client) Model() string { return c.model }

// rerankRequest is the Cohere-compatible request body LiteLLM expects.
type rerankRequest struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopN      int      `json:"top_n"`
}

// rerankResponse is the Cohere-compatible response shape.
type rerankResponse struct {
	Results []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
	} `json:"results"`
}

// Rank scores each passage against the query and returns the passages in the
// reranker's order (most relevant first), as PassageScore{Index, RelevanceScore}
// where Index points back into the input passages slice.
//
// Contract notes:
//   - top_n is set to len(passages) — this is REORDER, never a top-k drop. The caller
//     owns truncation to its limit (the recall handler), so the reranker returns all.
//   - Returns an error only on a genuine call failure (bad config, timeout, non-200,
//     malformed body). The caller MUST treat any error as "keep fusion order" and never
//     propagate it to the recall response.
//   - Empty or single-element passages short-circuit (nothing to reorder).
func (c *Client) Rank(ctx context.Context, query string, passages []string) ([]PassageScore, error) {
	if len(passages) <= 1 {
		out := make([]PassageScore, len(passages))
		for i := range passages {
			out[i] = PassageScore{Index: i, RelevanceScore: 0}
		}
		return out, nil
	}

	reqBody := rerankRequest{
		Model:     c.model,
		Query:     query,
		Documents: passages,
		TopN:      len(passages),
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("reranking: marshal request: %w", err)
	}

	endpoint := c.baseURL + "/v1/rerank"

	// Retry only transient failures (connection errors, 5xx). A 4xx is a config error
	// (wrong model/auth) — fail fast so the caller falls back without burning retries.
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(1<<attempt) * time.Second):
			}
		}

		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if reqErr != nil {
			return nil, fmt.Errorf("reranking: create request: %w", reqErr)
		}
		req.Header.Set("Content-Type", "application/json")
		if c.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.apiKey)
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

		if resp.StatusCode == http.StatusOK {
			var result rerankResponse
			if uErr := json.Unmarshal(respBody, &result); uErr != nil {
				return nil, fmt.Errorf("reranking: decode response: %w", uErr)
			}
			scores := make([]PassageScore, 0, len(result.Results))
			for _, r := range result.Results {
				if r.Index < 0 || r.Index >= len(passages) {
					continue // defend against an out-of-range index from the endpoint
				}
				scores = append(scores, PassageScore{Index: r.Index, RelevanceScore: r.RelevanceScore})
			}
			if len(scores) == 0 {
				return nil, fmt.Errorf("reranking: endpoint returned no usable results")
			}
			return scores, nil
		}

		// 4xx → config error, do not retry.
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return nil, fmt.Errorf("reranking: HTTP %d (config error, not retrying): %s", resp.StatusCode, string(respBody))
		}
		// 5xx → transient, retry.
		lastErr = fmt.Errorf("reranking: HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	log.Debug().Err(lastErr).Msg("reranking: all attempts failed; caller will fall back to fusion order")
	return nil, fmt.Errorf("reranking: after 3 attempts: %w", lastErr)
}
