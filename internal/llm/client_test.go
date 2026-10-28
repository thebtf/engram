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

type llmSettingsSnapshot struct {
	values map[string]string
	reads  int
}

func (settings *llmSettingsSnapshot) Get(_ context.Context, key string) (string, bool) {
	settings.reads++
	value, ok := settings.values[key]
	return value, ok
}

func TestNewClientWithSettings_PrecedenceAndStartupSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name, envModel, envKey, storedModel, wantModel, wantAuth, wantPath string
		envURL, absentURL                                                  bool
	}{
		{name: "stored provider", storedModel: "stored-model", wantModel: "stored-model", wantAuth: "Bearer " + "synthetic-stored-key", wantPath: "/stored/v1/chat/completions"},
		{name: "environment overrides stored provider", envURL: true, envModel: "env-model", envKey: "synthetic-env-key", storedModel: "stored-model", wantModel: "env-model", wantAuth: "Bearer synthetic-env-key", wantPath: "/env/v1/chat/completions"},
		{name: "existing default model", wantModel: "chat-default", wantAuth: "Bearer " + "synthetic-stored-key", wantPath: "/stored/v1/chat/completions"},
		{name: "absent provider stays disabled", absentURL: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			type observedRequest struct{ path, model, auth string }
			requests := make(chan observedRequest, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body chatRequest
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					http.Error(w, "invalid synthetic request", http.StatusBadRequest)
					return
				}
				requests <- observedRequest{r.URL.Path, body.Model, r.Header.Get("Authorization")}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"synthetic completion"}}]}`))
			}))
			defer server.Close()
			t.Setenv("ENGRAM_LLM_URL", "")
			if tc.envURL {
				t.Setenv("ENGRAM_LLM_URL", server.URL+"/env/v1")
			}
			t.Setenv("ENGRAM_LLM_MODEL", tc.envModel)
			t.Setenv("ENGRAM_LLM_API_KEY", tc.envKey)
			settings := &llmSettingsSnapshot{values: map[string]string{"llm.model": tc.storedModel, "llm.api_key": "synthetic-stored-key"}}
			if !tc.absentURL {
				settings.values["llm.url"] = server.URL + "/stored/v1"
			}
			client, err := NewClientWithSettings(context.Background(), settings)
			if tc.absentURL {
				if err != ErrLLMDisabled || client != nil || len(requests) != 0 {
					t.Fatal("absent provider must remain disabled without HTTP requests")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			reads := settings.reads
			settings.values["llm.url"], settings.values["llm.model"], settings.values["llm.api_key"] = "changed-after-startup", "changed-model", "changed-key"
			result, err := client.Complete(context.Background(), "synthetic system", "synthetic user")
			if err != nil || result != "synthetic completion" {
				t.Fatalf("completion failed: %v", err)
			}
			observed := <-requests
			if observed.path != tc.wantPath || observed.model != tc.wantModel || observed.auth != tc.wantAuth || settings.reads != reads {
				t.Fatal("provider request did not preserve env/store/default precedence and startup snapshot")
			}
		})
	}
}
