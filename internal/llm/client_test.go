package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// completionServer returns a test server that publishes the Authorization header
// of each request onto authChan and replies with a valid chat completion
// response containing the given content string. A buffered channel (not a
// shared pointer) carries the value across the goroutine boundary so the test
// is race-free under -race; loopback socket I/O is not a happens-before edge
// for the race detector.
func completionServer(t *testing.T, authChan chan<- string, content string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case authChan <- r.Header.Get("Authorization"):
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"content": content,
					},
				},
			},
		})
	}))
}

// TestNewClient_DisabledWithoutURL asserts the documented contract: an empty
// ENGRAM_LLM_URL yields ErrLLMDisabled.
func TestNewClient_DisabledWithoutURL(t *testing.T) {
	t.Setenv("ENGRAM_LLM_URL", "")
	_, err := NewClient()
	if err != ErrLLMDisabled {
		t.Fatalf("expected ErrLLMDisabled, got %v", err)
	}
}

// TestComplete_SendsBearerWhenKeySet asserts that ENGRAM_LLM_API_KEY produces
// an "Authorization: Bearer <key>" header on the outgoing request.
func TestComplete_SendsBearerWhenKeySet(t *testing.T) {
	authChan := make(chan string, 1)
	srv := completionServer(t, authChan, "hello")
	defer srv.Close()

	t.Setenv("ENGRAM_LLM_URL", srv.URL)
	t.Setenv("ENGRAM_LLM_API_KEY", "secret-key-456")
	t.Setenv("ENGRAM_LLM_MODEL", "gpt-test")

	c, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := c.Complete(context.Background(), "sys", "usr"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	gotAuth := <-authChan
	if want := "Bearer secret-key-456"; gotAuth != want {
		t.Fatalf("Authorization header = %q, want %q", gotAuth, want)
	}
}

// TestComplete_NoAuthHeaderWhenKeyEmpty asserts the key-less path: with no
// ENGRAM_LLM_API_KEY, no Authorization header is sent (LAN proxy case).
func TestComplete_NoAuthHeaderWhenKeyEmpty(t *testing.T) {
	authChan := make(chan string, 1)
	srv := completionServer(t, authChan, "hello")
	defer srv.Close()

	t.Setenv("ENGRAM_LLM_URL", srv.URL)
	t.Setenv("ENGRAM_LLM_API_KEY", "")
	t.Setenv("ENGRAM_LLM_MODEL", "gpt-test")

	c, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := c.Complete(context.Background(), "sys", "usr"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	gotAuth := <-authChan
	if gotAuth != "" {
		t.Fatalf("Authorization header = %q, want empty", gotAuth)
	}
}

// TestComplete_HappyPath asserts that Complete returns the content string from
// the first choice in a valid server response.
func TestComplete_HappyPath(t *testing.T) {
	authChan := make(chan string, 1)
	const want = "crystallization complete"
	srv := completionServer(t, authChan, want)
	defer srv.Close()

	t.Setenv("ENGRAM_LLM_URL", srv.URL)
	t.Setenv("ENGRAM_LLM_API_KEY", "")
	t.Setenv("ENGRAM_LLM_MODEL", "gpt-test")

	c, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	got, err := c.Complete(context.Background(), "you are helpful", "summarize")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got != want {
		t.Fatalf("Complete() = %q, want %q", got, want)
	}
}

// TestComplete_RetrySucceeds asserts that transient server failures are
// retried: the server fails twice then succeeds, and Complete returns the
// eventual success content without error.
func TestComplete_RetrySucceeds(t *testing.T) {
	attempts := 0
	const want = "retry worked"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			http.Error(w, "temporary failure", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": want}},
			},
		})
	}))
	defer srv.Close()

	t.Setenv("ENGRAM_LLM_URL", srv.URL)
	t.Setenv("ENGRAM_LLM_API_KEY", "")
	t.Setenv("ENGRAM_LLM_MODEL", "gpt-test")

	c, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	// Use a background context; the backoff sleeps (6s total: 2s + 4s for 2 retries)
	// are real but acceptable in a test environment. The race detector is unaffected
	// because attempts is only written in the handler goroutine.
	got, err := c.Complete(context.Background(), "sys", "usr")
	if err != nil {
		t.Fatalf("Complete after retries: %v", err)
	}
	if got != want {
		t.Fatalf("Complete() = %q, want %q", got, want)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 server hits, got %d", attempts)
	}
}
