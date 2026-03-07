package reranking

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestServer(handler http.HandlerFunc) *httptest.Server {
	return httptest.NewServer(handler)
}

func TestAPIService_Rerank_Success(t *testing.T) {
	server := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/v1/rerank", r.URL.Path)
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		var req rerankRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "test-model", req.Model)
		assert.Equal(t, "test query", req.Query)
		assert.Len(t, req.Documents, 3)

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(rerankResponse{
			Results: []rerankResponseResult{
				{Index: 2, RelevanceScore: 0.95},
				{Index: 0, RelevanceScore: 0.80},
				{Index: 1, RelevanceScore: 0.50},
			},
		})
	})
	defer server.Close()

	svc, err := NewAPIService(APIConfig{
		BaseURL: server.URL + "/v1/rerank",
		APIKey:  "test-key",
		Model:   "test-model",
		Alpha:   0.7,
		Timeout: 5 * time.Second,
	})
	require.NoError(t, err)

	candidates := []Candidate{
		{ID: "a", Content: "doc a", Score: 0.9},
		{ID: "b", Content: "doc b", Score: 0.8},
		{ID: "c", Content: "doc c", Score: 0.7},
	}

	results, err := svc.Rerank("test query", candidates, 10)
	require.NoError(t, err)
	require.Len(t, results, 3)

	// First result should be doc c (index 2) with highest combined score
	// combined = 0.7*0.95 + 0.3*0.7 = 0.665 + 0.21 = 0.875
	assert.Equal(t, "c", results[0].ID)
	assert.InDelta(t, 0.95, results[0].RerankScore, 0.01)

	// Second should be doc a (index 0)
	// combined = 0.7*0.80 + 0.3*0.9 = 0.56 + 0.27 = 0.83
	assert.Equal(t, "a", results[1].ID)
}

func TestAPIService_Rerank_EmptyCandidates(t *testing.T) {
	svc, err := NewAPIService(APIConfig{
		BaseURL: "http://unused",
		APIKey:  "test-key",
		Timeout: time.Second,
	})
	require.NoError(t, err)

	results, err := svc.Rerank("query", nil, 10)
	assert.NoError(t, err)
	assert.Nil(t, results)
}

func TestAPIService_Rerank_429FallbackToOriginalOrder(t *testing.T) {
	server := newTestServer(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	defer server.Close()

	svc, err := NewAPIService(APIConfig{
		BaseURL: server.URL + "/v1/rerank",
		APIKey:  "test-key",
		Timeout: 5 * time.Second,
	})
	require.NoError(t, err)

	candidates := []Candidate{
		{ID: "a", Content: "doc a", Score: 0.9},
		{ID: "b", Content: "doc b", Score: 0.8},
	}

	results, err := svc.Rerank("query", candidates, 10)
	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.Equal(t, "a", results[0].ID, "should preserve original order on 429")
	assert.Equal(t, "b", results[1].ID)
}

func TestAPIService_Rerank_500FallbackToOriginalOrder(t *testing.T) {
	server := newTestServer(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer server.Close()

	svc, err := NewAPIService(APIConfig{
		BaseURL: server.URL + "/v1/rerank",
		APIKey:  "test-key",
		Timeout: 5 * time.Second,
	})
	require.NoError(t, err)

	candidates := []Candidate{
		{ID: "a", Content: "doc a", Score: 0.9},
	}

	results, err := svc.Rerank("query", candidates, 10)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "a", results[0].ID)
}

func TestAPIService_Rerank_TimeoutFallback(t *testing.T) {
	server := newTestServer(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(600 * time.Millisecond) // Exceeds 200ms timeout
		w.WriteHeader(http.StatusOK)
	})
	defer server.Close()

	svc, err := NewAPIService(APIConfig{
		BaseURL: server.URL + "/v1/rerank",
		APIKey:  "test-key",
		Timeout: 200 * time.Millisecond,
	})
	require.NoError(t, err)

	candidates := []Candidate{
		{ID: "a", Content: "doc a", Score: 0.9},
		{ID: "b", Content: "doc b", Score: 0.8},
	}

	start := time.Now()
	results, err := svc.Rerank("query", candidates, 10)
	elapsed := time.Since(start)

	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.Equal(t, "a", results[0].ID, "should preserve original order on timeout")
	assert.Less(t, elapsed, 500*time.Millisecond, "should return within timeout")
}

func TestAPIService_Rerank_LimitApplied(t *testing.T) {
	server := newTestServer(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(rerankResponse{
			Results: []rerankResponseResult{
				{Index: 0, RelevanceScore: 0.9},
				{Index: 1, RelevanceScore: 0.8},
				{Index: 2, RelevanceScore: 0.7},
			},
		})
	})
	defer server.Close()

	svc, err := NewAPIService(APIConfig{
		BaseURL: server.URL + "/v1/rerank",
		APIKey:  "test-key",
		Timeout: 5 * time.Second,
	})
	require.NoError(t, err)

	candidates := []Candidate{
		{ID: "a", Content: "doc a", Score: 0.5},
		{ID: "b", Content: "doc b", Score: 0.4},
		{ID: "c", Content: "doc c", Score: 0.3},
	}

	results, err := svc.Rerank("query", candidates, 2)
	require.NoError(t, err)
	assert.Len(t, results, 2)
}

func TestAPIService_RerankByScore_Success(t *testing.T) {
	server := newTestServer(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(rerankResponse{
			Results: []rerankResponseResult{
				{Index: 0, RelevanceScore: 0.3},
				{Index: 1, RelevanceScore: 0.9},
			},
		})
	})
	defer server.Close()

	svc, err := NewAPIService(APIConfig{
		BaseURL: server.URL + "/v1/rerank",
		APIKey:  "test-key",
		Alpha:   0.7,
		Timeout: 5 * time.Second,
	})
	require.NoError(t, err)

	candidates := []Candidate{
		{ID: "a", Content: "doc a", Score: 0.95}, // Higher bi-encoder
		{ID: "b", Content: "doc b", Score: 0.50}, // Lower bi-encoder
	}

	results, err := svc.RerankByScore("query", candidates, 10)
	require.NoError(t, err)
	require.Len(t, results, 2)

	// RerankByScore ignores original score — pure rerank score
	assert.Equal(t, "b", results[0].ID, "doc b should be first (highest rerank score)")
	assert.InDelta(t, 0.9, results[0].RerankScore, 0.01)
}

func TestAPIService_Score_Success(t *testing.T) {
	server := newTestServer(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(rerankResponse{
			Results: []rerankResponseResult{
				{Index: 0, RelevanceScore: 0.87},
			},
		})
	})
	defer server.Close()

	svc, err := NewAPIService(APIConfig{
		BaseURL: server.URL + "/v1/rerank",
		APIKey:  "test-key",
		Timeout: 5 * time.Second,
	})
	require.NoError(t, err)

	raw, normalized, err := svc.Score("test query", "test document")
	require.NoError(t, err)
	assert.InDelta(t, 0.87, raw, 0.01)
	assert.InDelta(t, 0.87, normalized, 0.01) // API returns same for both
}

func TestAPIService_Score_APIUnavailable(t *testing.T) {
	server := newTestServer(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	defer server.Close()

	svc, err := NewAPIService(APIConfig{
		BaseURL: server.URL + "/v1/rerank",
		APIKey:  "test-key",
		Timeout: 5 * time.Second,
	})
	require.NoError(t, err)

	_, _, err = svc.Score("query", "document")
	assert.Error(t, err, "Score should error when API is unavailable (no fallback for single-score)")
}

func TestAPIService_Rerank_ResponseCountMismatch(t *testing.T) {
	// API returns fewer results than candidates
	server := newTestServer(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(rerankResponse{
			Results: []rerankResponseResult{
				{Index: 0, RelevanceScore: 0.9},
				// Missing index 1 and 2
			},
		})
	})
	defer server.Close()

	svc, err := NewAPIService(APIConfig{
		BaseURL: server.URL + "/v1/rerank",
		APIKey:  "test-key",
		Alpha:   0.5,
		Timeout: 5 * time.Second,
	})
	require.NoError(t, err)

	candidates := []Candidate{
		{ID: "a", Content: "doc a", Score: 0.8},
		{ID: "b", Content: "doc b", Score: 0.7},
		{ID: "c", Content: "doc c", Score: 0.6},
	}

	results, err := svc.Rerank("query", candidates, 10)
	require.NoError(t, err)
	require.Len(t, results, 3)

	// Doc a should be first (has API score 0.9), docs b and c have rerank score 0.0
	assert.Equal(t, "a", results[0].ID)
}

func TestNewAPIService_Validation(t *testing.T) {
	_, err := NewAPIService(APIConfig{APIKey: "key"})
	assert.Error(t, err, "should require BaseURL")

	_, err = NewAPIService(APIConfig{BaseURL: "http://example.com"})
	assert.Error(t, err, "should require APIKey")

	svc, err := NewAPIService(APIConfig{BaseURL: "http://example.com", APIKey: "key"})
	require.NoError(t, err)
	assert.NotNil(t, svc)
}

func TestAPIService_Close(t *testing.T) {
	svc, err := NewAPIService(APIConfig{
		BaseURL: "http://example.com",
		APIKey:  "key",
	})
	require.NoError(t, err)
	assert.NoError(t, svc.Close())
}

func TestAPIService_RankImprovement(t *testing.T) {
	server := newTestServer(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(rerankResponse{
			Results: []rerankResponseResult{
				{Index: 0, RelevanceScore: 0.3},
				{Index: 1, RelevanceScore: 0.9},
			},
		})
	})
	defer server.Close()

	svc, err := NewAPIService(APIConfig{
		BaseURL: server.URL + "/v1/rerank",
		APIKey:  "key",
		Alpha:   1.0, // Pure rerank score for predictable ordering
		Timeout: 5 * time.Second,
	})
	require.NoError(t, err)

	candidates := []Candidate{
		{ID: "a", Content: "doc a", Score: 0.9},
		{ID: "b", Content: "doc b", Score: 0.8},
	}

	results, err := svc.Rerank("query", candidates, 10)
	require.NoError(t, err)
	require.Len(t, results, 2)

	// Doc b moves from rank 2 to rank 1: improvement = 2 - 1 = 1
	assert.Equal(t, "b", results[0].ID)
	assert.Equal(t, 1, results[0].RankImprovement)

	// Doc a moves from rank 1 to rank 2: improvement = 1 - 2 = -1
	assert.Equal(t, "a", results[1].ID)
	assert.Equal(t, -1, results[1].RankImprovement)
}

func TestAPIService_Rerank_4xxError(t *testing.T) {
	server := newTestServer(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": "bad request"}`))
	})
	defer server.Close()

	svc, err := NewAPIService(APIConfig{
		BaseURL: server.URL + "/v1/rerank",
		APIKey:  "key",
		Timeout: 5 * time.Second,
	})
	require.NoError(t, err)

	candidates := []Candidate{{ID: "a", Content: "doc", Score: 0.5}}
	_, err = svc.Rerank("query", candidates, 10)
	assert.Error(t, err, "4xx (non-429) should return error, not degrade gracefully")
}

func TestAPIService_Reranker_InterfaceCompliance(t *testing.T) {
	// Compile-time check that APIService implements Reranker
	var _ Reranker = (*APIService)(nil)
}

func TestAPIService_DefaultAlpha(t *testing.T) {
	svc, err := NewAPIService(APIConfig{
		BaseURL: "http://example.com",
		APIKey:  "key",
		Alpha:   0, // Invalid
	})
	require.NoError(t, err)
	assert.InDelta(t, 0.7, svc.alpha, 0.01, "should default to 0.7")
}

func TestAPIService_DefaultModel(t *testing.T) {
	svc, err := NewAPIService(APIConfig{
		BaseURL: "http://example.com",
		APIKey:  "key",
	})
	require.NoError(t, err)
	assert.Equal(t, "rerank-english-v3.0", svc.model)
}

func TestAPIService_ConcurrentRequests(t *testing.T) {
	var requestCount atomic.Int32
	server := newTestServer(func(w http.ResponseWriter, _ *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(rerankResponse{
			Results: []rerankResponseResult{
				{Index: 0, RelevanceScore: 0.9},
			},
		})
	})
	defer server.Close()

	svc, err := NewAPIService(APIConfig{
		BaseURL: server.URL + "/v1/rerank",
		APIKey:  "key",
		Timeout: 5 * time.Second,
	})
	require.NoError(t, err)

	candidates := []Candidate{{ID: "a", Content: "doc", Score: 0.5}}

	// Fire 5 concurrent requests
	done := make(chan error, 5)
	for i := 0; i < 5; i++ {
		go func() {
			_, err := svc.Rerank("query", candidates, 10)
			done <- err
		}()
	}

	for i := 0; i < 5; i++ {
		assert.NoError(t, <-done)
	}
	assert.Equal(t, int32(5), requestCount.Load())
}
