package embedding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// captureServer returns a test server that records the Authorization header of
// the last request and replies with a valid single-vector embedding response.
func captureServer(t *testing.T, gotAuth *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"embedding": []float32{0.1, 0.2, 0.3}, "index": 0},
			},
		})
	}))
}

// TestNewClient_DisabledWithoutURL asserts the documented contract: an empty
// ENGRAM_EMBEDDING_URL yields ErrEmbeddingDisabled.
func TestNewClient_DisabledWithoutURL(t *testing.T) {
	t.Setenv("ENGRAM_EMBEDDING_URL", "")
	_, err := NewClient()
	if err != ErrEmbeddingDisabled {
		t.Fatalf("expected ErrEmbeddingDisabled, got %v", err)
	}
}

// TestEmbed_SendsBearerWhenKeySet asserts that ENGRAM_EMBEDDING_API_KEY produces
// an "Authorization: Bearer <key>" header on the outgoing request.
func TestEmbed_SendsBearerWhenKeySet(t *testing.T) {
	var gotAuth string
	srv := captureServer(t, &gotAuth)
	defer srv.Close()

	t.Setenv("ENGRAM_EMBEDDING_URL", srv.URL)
	t.Setenv("ENGRAM_EMBEDDING_API_KEY", "secret-key-123")

	c, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := c.Embed(context.Background(), []string{"hello"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if want := "Bearer secret-key-123"; gotAuth != want {
		t.Fatalf("Authorization header = %q, want %q", gotAuth, want)
	}
}

// TestEmbed_NoAuthHeaderWhenKeyEmpty asserts the key-less path: with no
// ENGRAM_EMBEDDING_API_KEY, no Authorization header is sent (LAN proxy case).
func TestEmbed_NoAuthHeaderWhenKeyEmpty(t *testing.T) {
	var gotAuth string
	srv := captureServer(t, &gotAuth)
	defer srv.Close()

	t.Setenv("ENGRAM_EMBEDDING_URL", srv.URL)
	t.Setenv("ENGRAM_EMBEDDING_API_KEY", "")

	c, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := c.Embed(context.Background(), []string{"hello"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if gotAuth != "" {
		t.Fatalf("Authorization header = %q, want empty", gotAuth)
	}
}
