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

// TestEmbed_ClampsIncompatibleDimensionsOverride asserts the safety contract
// (Codex P1): a non-zero override that is not EmbeddingDim — e.g. a stale
// ENGRAM_EMBEDDING_DIMENSIONS=4096 in a deploy template — must NOT be requested,
// because the vector(EmbeddingDim) columns cannot store it (every INSERT would
// fail and recall would silently degrade). It is clamped to EmbeddingDim.
func TestEmbed_ClampsIncompatibleDimensionsOverride(t *testing.T) {
	bodyChan := make(chan map[string]any, 1)
	srv := bodyCaptureServer(t, bodyChan)
	defer srv.Close()

	t.Setenv("ENGRAM_EMBEDDING_URL", srv.URL)
	t.Setenv("ENGRAM_EMBEDDING_DIMENSIONS", "4096") // stale/incompatible value

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
		t.Fatalf("dimensions absent; want clamped to %d", EmbeddingDim)
	}
	if int(got.(float64)) != EmbeddingDim {
		t.Fatalf("dimensions = %v, want clamped to %d (not the incompatible override)", got, EmbeddingDim)
	}
}

// fakeResolver is an in-memory SettingsResolver for precedence tests (no DB).
type fakeResolver map[string]string

func (f fakeResolver) Get(_ context.Context, key string) (string, bool) {
	v, ok := f[key]
	return v, ok
}

// TestNewClientWithSettings_EnvFirstPrecedence pins the CR-2 (#259) read-path contract for
// the embedder: env wins over the settings-store; the store fills in when env is empty; the
// built-in default model applies only when both are empty; URL absence everywhere disables.
func TestNewClientWithSettings_EnvFirstPrecedence(t *testing.T) {
	t.Run("env wins over settings", func(t *testing.T) {
		t.Setenv("ENGRAM_EMBEDDING_URL", "https://env.example.test/v1")
		t.Setenv("ENGRAM_EMBEDDING_MODEL", "env-model")
		res := fakeResolver{
			SettingKeyEmbedURL:   "https://store.example.test/v1",
			SettingKeyEmbedModel: "store-model",
		}
		c, err := NewClientWithSettings(context.Background(), res)
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if c.baseURL != "https://env.example.test" {
			t.Errorf("baseURL = %q, want env value to win", c.baseURL)
		}
		if c.model != "env-model" {
			t.Errorf("model = %q, want env value to win", c.model)
		}
	})

	t.Run("settings fill in when env empty", func(t *testing.T) {
		t.Setenv("ENGRAM_EMBEDDING_URL", "")
		t.Setenv("ENGRAM_EMBEDDING_MODEL", "")
		res := fakeResolver{
			SettingKeyEmbedURL:   "https://store.example.test/v1",
			SettingKeyEmbedModel: "store-model",
		}
		c, err := NewClientWithSettings(context.Background(), res)
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if c.baseURL != "https://store.example.test" {
			t.Errorf("baseURL = %q, want store value when env unset", c.baseURL)
		}
		if c.model != "store-model" {
			t.Errorf("model = %q, want store value when env unset", c.model)
		}
	})

	t.Run("model default when both empty", func(t *testing.T) {
		t.Setenv("ENGRAM_EMBEDDING_URL", "")
		t.Setenv("ENGRAM_EMBEDDING_MODEL", "")
		res := fakeResolver{SettingKeyEmbedURL: "https://store.example.test"}
		c, err := NewClientWithSettings(context.Background(), res)
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if c.model != "text-embedding" {
			t.Errorf("model = %q, want built-in default when env+store empty", c.model)
		}
	})

	t.Run("disabled when URL absent everywhere", func(t *testing.T) {
		t.Setenv("ENGRAM_EMBEDDING_URL", "")
		_, err := NewClientWithSettings(context.Background(), fakeResolver{})
		if err != ErrEmbeddingDisabled {
			t.Fatalf("err = %v, want ErrEmbeddingDisabled when URL absent in env+store", err)
		}
	})

	t.Run("nil resolver is env-only", func(t *testing.T) {
		t.Setenv("ENGRAM_EMBEDDING_URL", "")
		_, err := NewClientWithSettings(context.Background(), nil)
		if err != ErrEmbeddingDisabled {
			t.Fatalf("err = %v, want ErrEmbeddingDisabled with nil resolver and no env", err)
		}
	})
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
