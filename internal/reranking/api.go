package reranking

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/rs/zerolog/log"
)

// APIConfig holds configuration for the API-based reranker.
type APIConfig struct {
	BaseURL  string
	APIKey   string
	Model    string
	Alpha    float64
	Timeout  time.Duration
}

// DefaultAPIConfig returns sensible defaults for the API reranker.
func DefaultAPIConfig() APIConfig {
	return APIConfig{
		Model:   "rerank-english-v3.0",
		Alpha:   0.7,
		Timeout: 500 * time.Millisecond,
	}
}

// APIService provides cross-encoder reranking via an external API (Cohere-compatible).
type APIService struct {
	client  *http.Client
	baseURL string
	apiKey  string
	model   string
	alpha   float64
}

// NewAPIService creates a new API-based reranker.
func NewAPIService(cfg APIConfig) (*APIService, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("reranking API base URL is required")
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("reranking API key is required")
	}

	alpha := cfg.Alpha
	if alpha <= 0 || alpha > 1 {
		alpha = 0.7
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 500 * time.Millisecond
	}

	model := cfg.Model
	if model == "" {
		model = "rerank-english-v3.0"
	}

	return &APIService{
		client: &http.Client{
			Timeout: timeout,
		},
		baseURL: cfg.BaseURL,
		apiKey:  cfg.APIKey,
		model:   model,
		alpha:   alpha,
	}, nil
}

// rerankRequest is the Cohere-compatible rerank API request body.
type rerankRequest struct {
	Model           string   `json:"model"`
	Query           string   `json:"query"`
	Documents       []string `json:"documents"`
	TopN            int      `json:"top_n,omitempty"`
	ReturnDocuments bool     `json:"return_documents,omitempty"`
}

// rerankResponse is the Cohere-compatible rerank API response.
type rerankResponse struct {
	Results []rerankResponseResult `json:"results"`
}

type rerankResponseResult struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

// callAPI sends a rerank request to the API and returns scored results.
// On any error (timeout, 429, 5xx), returns nil to signal graceful degradation.
func (s *APIService) callAPI(query string, documents []string, topN int) ([]rerankResponseResult, error) {
	reqBody := rerankRequest{
		Model:     s.model,
		Query:     query,
		Documents: documents,
		TopN:      topN,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal rerank request: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.client.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create rerank request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.apiKey)

	resp, err := s.client.Do(req)
	if err != nil {
		// Timeout or network error — graceful degradation
		log.Debug().Err(err).Msg("Rerank API call failed, falling back to original order")
		return nil, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		log.Debug().Int("status", resp.StatusCode).Msg("Rerank API returned error, falling back to original order")
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, nil
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("rerank API returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result rerankResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode rerank response: %w", err)
	}

	return result.Results, nil
}

// Rerank reranks candidates using combined bi-encoder + cross-encoder scores.
func (s *APIService) Rerank(query string, candidates []Candidate, limit int) ([]RerankResult, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = DefaultResultLimit
	}

	documents := candidateDocuments(candidates)
	apiResults, err := s.callAPI(query, documents, 0) // Request all, we merge scores
	if err != nil {
		return nil, err
	}

	// Graceful degradation: API unavailable, return original order
	if apiResults == nil {
		return originalOrderResults(candidates, limit), nil
	}

	// Build score map: index -> relevance_score
	scoreMap := make(map[int]float64, len(apiResults))
	for _, r := range apiResults {
		scoreMap[r.Index] = r.RelevanceScore
	}

	results := make([]RerankResult, len(candidates))
	for i, c := range candidates {
		rerankScore := scoreMap[i] // 0.0 if not in API results
		results[i] = RerankResult{
			ID:            c.ID,
			Content:       c.Content,
			OriginalScore: c.Score,
			RerankScore:   rerankScore,
			CombinedScore: s.alpha*rerankScore + (1-s.alpha)*c.Score,
			Metadata:      c.Metadata,
			OriginalRank:  i + 1,
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].CombinedScore > results[j].CombinedScore
	})

	for i := range results {
		results[i].RerankRank = i + 1
		results[i].RankImprovement = results[i].OriginalRank - results[i].RerankRank
	}

	if len(results) > limit {
		results = results[:limit]
	}

	log.Debug().
		Int("candidates", len(candidates)).
		Int("returned", len(results)).
		Float64("alpha", s.alpha).
		Msg("API reranking completed")

	return results, nil
}

// RerankByScore reranks candidates sorted by pure cross-encoder score only.
func (s *APIService) RerankByScore(query string, candidates []Candidate, limit int) ([]RerankResult, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = DefaultResultLimit
	}

	documents := candidateDocuments(candidates)
	apiResults, err := s.callAPI(query, documents, 0)
	if err != nil {
		return nil, err
	}

	if apiResults == nil {
		return originalOrderResults(candidates, limit), nil
	}

	scoreMap := make(map[int]float64, len(apiResults))
	for _, r := range apiResults {
		scoreMap[r.Index] = r.RelevanceScore
	}

	results := make([]RerankResult, len(candidates))
	for i, c := range candidates {
		rerankScore := scoreMap[i]
		results[i] = RerankResult{
			ID:            c.ID,
			Content:       c.Content,
			OriginalScore: c.Score,
			RerankScore:   rerankScore,
			CombinedScore: rerankScore, // Pure rerank score
			Metadata:      c.Metadata,
			OriginalRank:  i + 1,
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].RerankScore > results[j].RerankScore
	})

	for i := range results {
		results[i].RerankRank = i + 1
		results[i].RankImprovement = results[i].OriginalRank - results[i].RerankRank
	}

	if len(results) > limit {
		results = results[:limit]
	}

	return results, nil
}

// Score scores a single query-document pair.
// API rerankers return a single normalized score, so rawScore == normalizedScore.
func (s *APIService) Score(query, document string) (rawScore, normalizedScore float64, err error) {
	apiResults, err := s.callAPI(query, []string{document}, 1)
	if err != nil {
		return 0, 0, err
	}
	if apiResults == nil || len(apiResults) == 0 {
		return 0, 0, fmt.Errorf("rerank API returned no results")
	}

	score := apiResults[0].RelevanceScore
	return score, score, nil
}

// Close releases resources. API service has no persistent resources to release.
func (s *APIService) Close() error {
	return nil
}

// candidateDocuments extracts document strings from candidates.
func candidateDocuments(candidates []Candidate) []string {
	docs := make([]string, len(candidates))
	for i, c := range candidates {
		docs[i] = c.Content
	}
	return docs
}

// originalOrderResults returns candidates in their original order as RerankResults.
// Used as fallback when the API is unavailable.
func originalOrderResults(candidates []Candidate, limit int) []RerankResult {
	results := make([]RerankResult, len(candidates))
	for i, c := range candidates {
		results[i] = RerankResult{
			ID:            c.ID,
			Content:       c.Content,
			OriginalScore: c.Score,
			RerankScore:   c.Score,
			CombinedScore: c.Score,
			Metadata:      c.Metadata,
			OriginalRank:  i + 1,
			RerankRank:    i + 1,
		}
	}
	if len(results) > limit {
		results = results[:limit]
	}
	return results
}
