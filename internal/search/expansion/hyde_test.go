package expansion

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHyDEGenerator_TemplateError(t *testing.T) {
	gen := NewHyDEGenerator(DefaultHyDEConfig())
	result := gen.Generate(context.Background(), "how to fix the connection timeout error", IntentError)
	assert.NotEmpty(t, result)
	assert.Contains(t, result, "fix")
	assert.Contains(t, result, "connection")
	assert.Contains(t, result, "timeout")
}

func TestHyDEGenerator_TemplateImplementation(t *testing.T) {
	gen := NewHyDEGenerator(DefaultHyDEConfig())
	result := gen.Generate(context.Background(), "implement user authentication handler", IntentImplementation)
	assert.NotEmpty(t, result)
	assert.Contains(t, result, "user")
	assert.Contains(t, result, "authentication")
}

func TestHyDEGenerator_TemplateNoMatchFallsToLLM(t *testing.T) {
	// Question intent has no template; without API config, returns empty
	gen := NewHyDEGenerator(DefaultHyDEConfig())
	result := gen.Generate(context.Background(), "how does the caching layer work", IntentQuestion)
	assert.Empty(t, result)
}

func TestHyDEGenerator_EmptyQuery(t *testing.T) {
	gen := NewHyDEGenerator(DefaultHyDEConfig())
	result := gen.Generate(context.Background(), "", IntentError)
	assert.Empty(t, result)
}

func TestHyDEGenerator_LLMSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Contains(t, r.URL.Path, "/chat/completions")
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))

		resp := map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]string{
						"content": "The caching layer in engram uses an in-memory LRU cache with configurable TTL. Results are cached by query hash and evicted when capacity is reached.",
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := DefaultHyDEConfig()
	cfg.APIURL = server.URL
	cfg.APIKey = "test-key"

	gen := NewHyDEGenerator(cfg)
	result := gen.Generate(context.Background(), "how does the caching layer work", IntentQuestion)
	assert.NotEmpty(t, result)
	assert.Contains(t, result, "caching")
}

func TestHyDEGenerator_LLMTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := DefaultHyDEConfig()
	cfg.APIURL = server.URL
	cfg.APIKey = "test-key"
	cfg.Timeout = 100 * time.Millisecond

	gen := NewHyDEGenerator(cfg)

	start := time.Now()
	result := gen.Generate(context.Background(), "what is the architecture of the system", IntentArchitecture)
	elapsed := time.Since(start)

	assert.Empty(t, result)
	assert.Less(t, elapsed, 300*time.Millisecond)
}

func TestHyDEGenerator_LLM429Fallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	cfg := DefaultHyDEConfig()
	cfg.APIURL = server.URL
	cfg.APIKey = "test-key"

	gen := NewHyDEGenerator(cfg)
	result := gen.Generate(context.Background(), "what is the architecture", IntentArchitecture)
	assert.Empty(t, result)
}

func TestHyDEGenerator_LLMShortResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": "Short."}},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := DefaultHyDEConfig()
	cfg.APIURL = server.URL
	cfg.APIKey = "test-key"

	gen := NewHyDEGenerator(cfg)
	result := gen.Generate(context.Background(), "what is X", IntentQuestion)
	assert.Empty(t, result) // < 20 chars guard
}

func TestHyDEGenerator_CacheHit(t *testing.T) {
	var callCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callCount, 1)
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": "The system uses a layered architecture with clear separation of concerns between handlers, services, and stores."}},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := DefaultHyDEConfig()
	cfg.APIURL = server.URL
	cfg.APIKey = "test-key"
	cfg.CacheTTL = 5 * time.Minute

	gen := NewHyDEGenerator(cfg)

	// First call — hits API
	result1 := gen.Generate(context.Background(), "what is the architecture", IntentQuestion)
	require.NotEmpty(t, result1)
	assert.Equal(t, int32(1), atomic.LoadInt32(&callCount))

	// Second call — cache hit, no API call
	result2 := gen.Generate(context.Background(), "what is the architecture", IntentQuestion)
	assert.Equal(t, result1, result2)
	assert.Equal(t, int32(1), atomic.LoadInt32(&callCount))
}

func TestHyDEGenerator_NoAPIConfig(t *testing.T) {
	gen := NewHyDEGenerator(DefaultHyDEConfig())
	// No APIURL or APIKey — LLM path should return empty
	result := gen.Generate(context.Background(), "what is the system design", IntentArchitecture)
	assert.Empty(t, result)
}

func TestHyDEGenerator_DefaultConfig(t *testing.T) {
	cfg := DefaultHyDEConfig()
	assert.Equal(t, "gpt-4o-mini", cfg.Model)
	assert.Equal(t, 150, cfg.MaxTokens)
	assert.Equal(t, 800*time.Millisecond, cfg.Timeout)
	assert.Equal(t, 5*time.Minute, cfg.CacheTTL)
}

func TestHyDEGenerator_TemplateOnlyUsesKeyTerms(t *testing.T) {
	gen := NewHyDEGenerator(DefaultHyDEConfig())

	// Stop words should be filtered out
	result := gen.Generate(context.Background(), "how to fix the database connection error in production", IntentError)
	assert.NotEmpty(t, result)
	// Should contain key terms, not stop words
	assert.Contains(t, result, "database")
	assert.Contains(t, result, "production")
}

func TestHyDEGenerator_LLMEmptyChoices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{"choices": []map[string]any{}}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := DefaultHyDEConfig()
	cfg.APIURL = server.URL
	cfg.APIKey = "test-key"

	gen := NewHyDEGenerator(cfg)
	result := gen.Generate(context.Background(), "describe the system", IntentQuestion)
	assert.Empty(t, result)
}
