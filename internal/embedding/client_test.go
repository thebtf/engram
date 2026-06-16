package embedding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// captureServer returns a test server that publishes the Authorization header
// of each request onto authChan and replies with a valid single-vector
// embedding response. A buffered channel (not a shared pointer) carries the
// value across the goroutine boundary so the test is race-free under -race;
// loopback socket I/O is not a happens-before edge for the race detector.
func captureServer(t *testing.T, authChan chan<- string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case authChan <- r.Header.Get("Authorization"):
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"embedding": []float32{0.1, 0.2, 0.3}, "index": 0},
			},
		})
	}))
}

// bodyCaptureServer publishes the decoded request body of each call onto
// bodyChan and replies with a valid single-vector response. Buffered channel
// keeps it race-free under -race (loopback I/O is not a happens-before edge).
func bodyCaptureServer(t *testing.T, bodyChan chan<- map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		select {
		case bodyChan <- body:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"embedding": []float32{0.1, 0.2, 0.3}, "index": 0},
			},
		})
	}))
}

// TestEmbed_SendsDimensionsByDefault asserts the unify-embedding-dimension
// contract: with no ENGRAM_EMBEDDING_DIMENSIONS override, the request carries
// dimensions == EmbeddingDim so an MRL model returns the unified dimension.
func TestEmbed_SendsDimensionsByDefault(t *testing.T) {
	bodyChan := make(chan map[string]any, 1)
	srv := bodyCaptureServer(t, bodyChan)
	defer srv.Close()

	t.Setenv("ENGRAM_EMBEDDING_URL", srv.URL)
	t.Setenv("ENGRAM_EMBEDDING_DIMENSIONS", "")

	c, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := c.Embed(context.Background(), []string{"hello"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	body := <-bodyChan
	got, ok := body["dimensions"]
	if !ok {
		t.Fatalf("dimensions absent from request body; want %d", EmbeddingDim)
	}
	// JSON numbers decode to float64.
	if int(got.(float64)) != EmbeddingDim {
		t.Fatalf("dimensions = %v, want %d", got, EmbeddingDim)
	}
}

// TestEmbed_OmitsDimensionsWhenZero asserts the back-compat escape hatch:
// ENGRAM_EMBEDDING_DIMENSIONS=0 omits the param entirely, for endpoints that
// reject it (non-MRL model, or a proxy that 400s on the unknown field).
func TestEmbed_OmitsDimensionsWhenZero(t *testing.T) {
	bodyChan := make(chan map[string]any, 1)
	srv := bodyCaptureServer(t, bodyChan)
	defer srv.Close()

	t.Setenv("ENGRAM_EMBEDDING_URL", srv.URL)
	t.Setenv("ENGRAM_EMBEDDING_DIMENSIONS", "0")

	c, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := c.Embed(context.Background(), []string{"hello"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	body := <-bodyChan
	if _, ok := body["dimensions"]; ok {
		t.Fatalf("dimensions present in request body, want omitted (got %v)", body["dimensions"])
	}
}

// TestEmbed_HonorsDimensionsOverride asserts an explicit override is sent verbatim.
func TestEmbed_HonorsDimensionsOverride(t *testing.T) {
	bodyChan := make(chan map[string]any, 1)
	srv := bodyCaptureServer(t, bodyChan)
	defer srv.Close()

	t.Setenv("ENGRAM_EMBEDDING_URL", srv.URL)
	t.Setenv("ENGRAM_EMBEDDING_DIMENSIONS", "768")

	c, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := c.Embed(context.Background(), []string{"hello"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	body := <-bodyChan
	got, ok := body["dimensions"]
	if !ok || int(got.(float64)) != 768 {
		t.Fatalf("dimensions = %v (present=%v), want 768", got, ok)
	}
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
	authChan := make(chan string, 1)
	srv := captureServer(t, authChan)
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
	gotAuth := <-authChan
	if want := "Bearer secret-key-123"; gotAuth != want {
		t.Fatalf("Authorization header = %q, want %q", gotAuth, want)
	}
}

// TestEmbed_NoAuthHeaderWhenKeyEmpty asserts the key-less path: with no
// ENGRAM_EMBEDDING_API_KEY, no Authorization header is sent (LAN proxy case).
func TestEmbed_NoAuthHeaderWhenKeyEmpty(t *testing.T) {
	authChan := make(chan string, 1)
	srv := captureServer(t, authChan)
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
	gotAuth := <-authChan
	if gotAuth != "" {
		t.Fatalf("Authorization header = %q, want empty", gotAuth)
	}
}
