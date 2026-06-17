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

// rerankCallBudget caps the wall-clock time the whole Rank() sequence (all attempts
// plus backoff) may consume, so a slow/degraded endpoint cannot stall the synchronous
// recall path. On expiry the caller falls back to fusion order.
const rerankCallBudget = 9 * time.Second

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
	// Clear RawPath/RawQuery/Fragment: url.String() prefers a non-empty RawPath over
	// Path (so an escaped-char input would ignore the suffix strip above), and any
	// query/fragment would corrupt the canonical "/v1/rerank" we re-append. (gemini review)
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
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

// SettingsResolver is the minimal read surface the reranker needs from the settings-store
// (#259). It is defined here, not imported from the db layer, so this low-level package stays
// storage-agnostic (no import cycle): service.go wires a concrete resolver backed by
// SettingsStore. Get returns (value, true) when a non-secret setting exists, ("", false)
// otherwise. Secret values (API keys) are NOT served through this interface in CR-2 — they
// need vault decryption and are handled in CR-3; the API key still comes from env here.
type SettingsResolver interface {
	Get(ctx context.Context, key string) (string, bool)
}

// Settings keys read by the reranker. URL and model are plaintext config; api_key is a
// secret resolved through the same env-first path but backed by an encrypted settings row
// that the resolver decrypts in-process (CR-3).
const (
	SettingKeyRerankURL    = "reranker.url"
	SettingKeyRerankModel  = "reranker.model"
	SettingKeyRerankAPIKey = "reranker.api_key"
)

// NewClient creates a rerank Client from environment variables only (no settings-store).
// Equivalent to NewClientWithSettings(ctx, nil). Retained for callers/tests that have no
// settings backing.
func NewClient() (*Client, error) {
	return NewClientWithSettings(context.Background(), nil)
}

// NewClientWithSettings creates a rerank Client, reading config with ENV-FIRST precedence:
// an environment variable, when set, wins over the settings-store (so existing deployments are
// unchanged and an operator can always override via env). When the env var is empty, the value
// is read from the settings-store (resolver) if one is provided. Returns ErrRerankDisabled when
// neither source yields a URL (the no-reranker default — recall keeps fusion order).
//
// Config sources (NEW env names — phantom ENGRAM_RERANKING_* vars are deliberately NOT reused):
//   - URL:     ENGRAM_RERANK_URL     → else settings "reranker.url"     (required to enable)
//   - model:   ENGRAM_RERANK_MODEL   → else settings "reranker.model"   → else "bge-reranker"
//   - API key: ENGRAM_RERANK_API_KEY → else settings "reranker.api_key" (secret; the resolver
//     decrypts the encrypted settings row in-process — CR-3). Empty → no Authorization header.
func NewClientWithSettings(ctx context.Context, resolver SettingsResolver) (*Client, error) {
	rawURL := resolveSetting(ctx, resolver, "ENGRAM_RERANK_URL", SettingKeyRerankURL)
	if rawURL == "" {
		return nil, ErrRerankDisabled
	}
	model := resolveSetting(ctx, resolver, "ENGRAM_RERANK_MODEL", SettingKeyRerankModel)
	if model == "" {
		model = "bge-reranker"
	}
	return &Client{
		baseURL: normalizeRerankBaseURL(rawURL),
		model:   model,
		apiKey:  resolveSetting(ctx, resolver, "ENGRAM_RERANK_API_KEY", SettingKeyRerankAPIKey),
		httpClient: &http.Client{
			// Recall is latency-sensitive and synchronous (the agent blocks on it). The
			// reranker is a SOFT enhancement whose fallback (fusion order) is already good,
			// so the call must fail FAST, not retry hard. Per-attempt 4s; the whole Rank()
			// sequence is additionally bounded by rerankCallBudget below so the worst case
			// is single-digit seconds, never the ~36s a 3×10s+backoff loop would cost.
			Timeout: 4 * time.Second,
		},
	}, nil
}

// resolveSetting applies env-first precedence: a non-empty env var wins; otherwise the
// settings-store value (if a resolver is wired and the key exists); otherwise "".
func resolveSetting(ctx context.Context, resolver SettingsResolver, envKey, settingKey string) string {
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	if resolver != nil {
		if v, ok := resolver.Get(ctx, settingKey); ok {
			return v
		}
	}
	return ""
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

	// Bound the ENTIRE Rank() sequence (all attempts + backoff) so a degraded endpoint
	// can never block the synchronous recall path beyond a few seconds — the caller
	// falls back to fusion order on any error. Worst case: 2 attempts × 4s + 0.5s
	// backoff ≈ 8.5s, hard-capped by this budget. (A 3×10s+exp-backoff loop would have
	// cost ~36s on the recall path — rejected in code review.)
	ctx, cancel := context.WithTimeout(ctx, rerankCallBudget)
	defer cancel()

	// Retry only transient failures (connection errors, 5xx). A 4xx is a config error
	// (wrong model/auth) — fail fast so the caller falls back without burning retries.
	// 2 attempts total (1 retry): the reranker is a soft enhancement, not worth a long tail.
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			// time.NewTimer + Stop (not time.After) so the backoff timer is released
			// immediately on context cancellation rather than lingering until it fires. (gemini review)
			timer := time.NewTimer(500 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
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
			// The endpoint response is untrusted: defend against out-of-range AND
			// duplicate indices (a repeated index would otherwise silently consume a
			// rank slot and demote a legitimately-ranked passage downstream). Keep the
			// first occurrence, which preserves the endpoint's most-relevant-first order.
			scores := make([]PassageScore, 0, len(result.Results))
			seen := make(map[int]struct{}, len(result.Results))
			for _, r := range result.Results {
				if r.Index < 0 || r.Index >= len(passages) {
					continue // out-of-range index from the endpoint
				}
				if _, dup := seen[r.Index]; dup {
					continue // duplicate index from the endpoint
				}
				seen[r.Index] = struct{}{}
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
	return nil, fmt.Errorf("reranking: after 2 attempts: %w", lastErr)
}
