package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gormdb "github.com/thebtf/engram/internal/db/gorm"

	"gorm.io/gorm"

	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/crypto"
	"github.com/thebtf/engram/pkg/models"
)

// testHexKey is a deterministic 32-byte AES key (hex) for vault-backed decrypt tests.
const testHexKey = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

// newTestVault builds a real crypto.Vault from a fixed key so encrypt/decrypt round-trips
// are exercised end-to-end (no mock crypto).
func newTestVault(t *testing.T) *crypto.Vault {
	t.Helper()
	v, err := crypto.NewVault(&config.Config{EncryptionKey: testHexKey})
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	return v
}

// fakeSettingsReader is an in-memory settingsReader for adapter unit tests (no DB).
// A key mapped to a nil row + non-nil err simulates a store error; a missing key
// returns gorm.ErrRecordNotFound to mirror SettingsStore.Get's not-found behavior.
type fakeSettingsReader struct {
	rows map[string]*models.ModelSetting
	err  error // when non-nil, every Get returns this error
}

func (f fakeSettingsReader) Get(_ context.Context, key string) (*models.ModelSetting, error) {
	if f.err != nil {
		return nil, f.err
	}
	row, ok := f.rows[key]
	if !ok {
		return nil, fmt.Errorf("get setting %q: %w", key, gorm.ErrRecordNotFound)
	}
	return row, nil
}

// TestSettingsResolver_Get pins the CR-2 (#259) worker-adapter contract: a plain row
// resolves to (value,true); an encrypted row, a missing key, and a store error all
// resolve to ("",false) so the client falls through to env/default.
func TestSettingsResolver_Get(t *testing.T) {
	ctx := context.Background()

	t.Run("plain row resolves to value", func(t *testing.T) {
		r := &settingsResolver{reader: fakeSettingsReader{rows: map[string]*models.ModelSetting{
			"reranker.url": {Key: "reranker.url", Value: "https://store.example.test", Encrypted: false},
		}}}
		got, ok := r.Get(ctx, "reranker.url")
		if !ok || got != "https://store.example.test" {
			t.Fatalf("Get = (%q, %v), want (%q, true)", got, ok, "https://store.example.test")
		}
	})

	t.Run("encrypted row with no vault wired reports absent", func(t *testing.T) {
		// vaultProvider nil → cannot decrypt → fall through to env/default.
		r := &settingsResolver{reader: fakeSettingsReader{rows: map[string]*models.ModelSetting{
			"reranker.api_key": {Key: "reranker.api_key", Encrypted: true, EncryptedValue: []byte{0x01, 0x02}},
		}}}
		got, ok := r.Get(ctx, "reranker.api_key")
		if ok || got != "" {
			t.Fatalf("Get(encrypted, no vault) = (%q, %v), want (\"\", false)", got, ok)
		}
	})

	t.Run("missing key reports absent", func(t *testing.T) {
		r := &settingsResolver{reader: fakeSettingsReader{rows: map[string]*models.ModelSetting{}}}
		got, ok := r.Get(ctx, "absent.key")
		if ok || got != "" {
			t.Fatalf("Get(missing) = (%q, %v), want (\"\", false)", got, ok)
		}
	})

	t.Run("store error reports absent (never breaks client init)", func(t *testing.T) {
		r := &settingsResolver{reader: fakeSettingsReader{err: fmt.Errorf("connection refused")}}
		got, ok := r.Get(ctx, "reranker.url")
		if ok || got != "" {
			t.Fatalf("Get(error) = (%q, %v), want (\"\", false)", got, ok)
		}
	})

	t.Run("nil reader reports absent", func(t *testing.T) {
		r := newSettingsResolver(nil, nil)
		got, ok := r.Get(ctx, "reranker.url")
		if ok || got != "" {
			t.Fatalf("Get(nil reader) = (%q, %v), want (\"\", false)", got, ok)
		}
	})

	t.Run("nil row with nil error does not panic (gemini #304)", func(t *testing.T) {
		// A settingsReader returning (nil, nil) — possible with a custom or future
		// implementation — must resolve to absent, never panic on row.Encrypted.
		r := &settingsResolver{reader: fakeSettingsReader{rows: map[string]*models.ModelSetting{
			"reranker.url": nil, // present key, nil row, nil error
		}}}
		got, ok := r.Get(ctx, "reranker.url")
		if ok || got != "" {
			t.Fatalf("Get(nil row) = (%q, %v), want (\"\", false)", got, ok)
		}
	})
}

// TestSettingsResolver_DecryptPath pins the CR-3 (#259) secret read path: an encrypted row
// is decrypted in-process when a matching vault is wired, and EVERY decrypt failure mode
// (fingerprint mismatch, corrupt ciphertext, vault-provider error) is fail-SOFT — it resolves
// to absent so the reranker/embedder falls through to env/default and init never breaks.
func TestSettingsResolver_DecryptPath(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	// Encrypt a known secret with the test vault to seed the rows.
	ciphertext, err := v.Encrypt("sk-secret-123")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	t.Run("encrypted row decrypts to plaintext when vault matches", func(t *testing.T) {
		r := &settingsResolver{
			reader: fakeSettingsReader{rows: map[string]*models.ModelSetting{
				"reranker.api_key": {Key: "reranker.api_key", Encrypted: true, EncryptedValue: ciphertext, EncryptionKeyFingerprint: v.Fingerprint()},
			}},
			vaultProvider: func() (*crypto.Vault, error) { return v, nil },
		}
		got, ok := r.Get(ctx, "reranker.api_key")
		if !ok || got != "sk-secret-123" {
			t.Fatalf("Get(encrypted, vault) = (%q, %v), want (%q, true)", got, ok, "sk-secret-123")
		}
	})

	t.Run("fingerprint mismatch is fail-soft (absent)", func(t *testing.T) {
		r := &settingsResolver{
			reader: fakeSettingsReader{rows: map[string]*models.ModelSetting{
				"reranker.api_key": {Key: "reranker.api_key", Encrypted: true, EncryptedValue: ciphertext, EncryptionKeyFingerprint: "deadbeefdeadbeef"},
			}},
			vaultProvider: func() (*crypto.Vault, error) { return v, nil },
		}
		got, ok := r.Get(ctx, "reranker.api_key")
		if ok || got != "" {
			t.Fatalf("Get(fingerprint mismatch) = (%q, %v), want (\"\", false) — must not break init", got, ok)
		}
	})

	t.Run("corrupt ciphertext is fail-soft (absent)", func(t *testing.T) {
		r := &settingsResolver{
			reader: fakeSettingsReader{rows: map[string]*models.ModelSetting{
				"reranker.api_key": {Key: "reranker.api_key", Encrypted: true, EncryptedValue: []byte{0x00, 0x01, 0x02}, EncryptionKeyFingerprint: v.Fingerprint()},
			}},
			vaultProvider: func() (*crypto.Vault, error) { return v, nil },
		}
		got, ok := r.Get(ctx, "reranker.api_key")
		if ok || got != "" {
			t.Fatalf("Get(corrupt ciphertext) = (%q, %v), want (\"\", false)", got, ok)
		}
	})

	t.Run("vault-provider error is fail-soft (absent)", func(t *testing.T) {
		r := &settingsResolver{
			reader: fakeSettingsReader{rows: map[string]*models.ModelSetting{
				"reranker.api_key": {Key: "reranker.api_key", Encrypted: true, EncryptedValue: ciphertext, EncryptionKeyFingerprint: v.Fingerprint()},
			}},
			vaultProvider: func() (*crypto.Vault, error) { return nil, fmt.Errorf("vault init failed") },
		}
		got, ok := r.Get(ctx, "reranker.api_key")
		if ok || got != "" {
			t.Fatalf("Get(vault error) = (%q, %v), want (\"\", false)", got, ok)
		}
	})

	t.Run("empty stored fingerprint still decrypts (back-compat)", func(t *testing.T) {
		// A row with no fingerprint stamped skips the match check and attempts decrypt directly.
		r := &settingsResolver{
			reader: fakeSettingsReader{rows: map[string]*models.ModelSetting{
				"reranker.api_key": {Key: "reranker.api_key", Encrypted: true, EncryptedValue: ciphertext},
			}},
			vaultProvider: func() (*crypto.Vault, error) { return v, nil },
		}
		got, ok := r.Get(ctx, "reranker.api_key")
		if !ok || got != "sk-secret-123" {
			t.Fatalf("Get(no fingerprint) = (%q, %v), want (%q, true)", got, ok, "sk-secret-123")
		}
	})
}

func TestSettingsProviders_FourInputStartupAndRestart(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost") || !strings.Contains(parsed.Path, "_test") {
		t.Skip("requires an explicit loopback synthetic test database")
	}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "ENGRAM_") {
			t.Setenv(name, "")
		}
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("TZ", "UTC")
	t.Setenv("DATABASE_DSN", dsn)
	t.Setenv("ENGRAM_VAULT_KEY", testHexKey)
	t.Setenv("ENGRAM_AUTH_DISABLED", "true")
	store := openIntegrationStore(t)
	settings := gormdb.NewSettingsStore(store)
	vault := newTestVault(t)
	type providerRequest struct {
		path, model, auth string
		dimensions        *int
	}
	requests := make(chan providerRequest, 8)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model      string   `json:"model"`
			Dimensions *int     `json:"dimensions"`
			Input      []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad synthetic request", http.StatusBadRequest)
			return
		}
		if r.URL.Path == "/v1/chat/completions" || (len(body.Input) == 1 && body.Input[0] == "synthetic memory") {
			requests <- providerRequest{r.URL.Path, body.Model, r.Header.Get("Authorization"), body.Dimensions}
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/embeddings" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"embedding": make([]float32, 1536), "index": 0}}})
		} else if r.URL.Path == "/v1/chat/completions" {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"synthetic completion"}}]}`))
		} else {
			http.Error(w, "unexpected synthetic endpoint", http.StatusNotFound)
		}
	}))
	defer provider.Close()
	for key, value := range map[string]string{
		"embedder.url": provider.URL, "embedder.model": "stored-embedding", "embedder.api_key": "synthetic-embedding-key", "embedder.dimensions": "0",
		"llm.url": provider.URL, "llm.model": "stored-llm", "llm.api_key": "synthetic-llm-key",
	} {
		row := &models.ModelSetting{Key: key, Value: value}
		if strings.HasSuffix(key, ".api_key") {
			row.Value, row.Encrypted, row.EncryptionKeyFingerprint = "", true, vault.Fingerprint()
			row.EncryptedValue, err = vault.Encrypt(value)
			require.NoError(t, err)
		}
		_, err := settings.Set(context.Background(), row)
		require.NoError(t, err)
	}
	project := uuid.NewString()
	require.NoError(t, store.DB.Create(&gormdb.Project{ID: project}).Error)
	t.Cleanup(func() {
		require.NoError(t, store.DB.Exec("DELETE FROM audit_log WHERE memory_id IN (SELECT id FROM memories WHERE project = ?)", project).Error)
		require.NoError(t, store.DB.Exec("DELETE FROM content_chunks WHERE memory_id IN (SELECT id FROM memories WHERE project = ?)", project).Error)
		require.NoError(t, store.DB.Exec("DELETE FROM memories WHERE project = ?", project).Error)
		require.NoError(t, store.DB.Where("id = ?", project).Delete(&gormdb.Project{}).Error)
	})
	for run := range 2 {
		service, err := NewService("four-input-provider-fixture", nil)
		require.NoError(t, err)
		closed := false
		stop := func() {
			if !closed {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				require.NoError(t, service.Shutdown(ctx))
				closed = true
			}
		}
		t.Cleanup(stop)
		service.initWG.Wait()
		require.NoError(t, service.GetInitError())
		require.True(t, service.ready.Load())
		service.initMu.RLock()
		llmClient, embedder := service.llmClient, service.embeddingClient
		service.initMu.RUnlock()
		require.NotNil(t, llmClient)
		require.NotNil(t, embedder)
		wantModel := "stored-llm"
		if run == 1 {
			wantModel = "after-restart-llm"
		}
		checkRequests := func() {
			completion, err := llmClient.Complete(context.Background(), "synthetic system", "synthetic user")
			require.NoError(t, err)
			require.Equal(t, "synthetic completion", completion)
			chat := <-requests
			require.Equal(t, "/v1/chat/completions", chat.path)
			require.Equal(t, wantModel, chat.model)
			require.Equal(t, "Bearer "+"synthetic-llm-key", chat.auth)
			_, err = embedder.Embed(context.Background(), []string{"synthetic memory"})
			require.NoError(t, err)
			embedded := <-requests
			require.Equal(t, "/v1/embeddings", embedded.path)
			require.Equal(t, "stored-embedding", embedded.model)
			require.Equal(t, "Bearer "+"synthetic-embedding-key", embedded.auth)
			if run == 0 {
				require.Nil(t, embedded.dimensions, "stored zero must omit the provider parameter")
			} else {
				require.NotNil(t, embedded.dimensions)
				require.Equal(t, 1536, *embedded.dimensions)
			}
		}
		checkRequests()
		if run == 0 {
			_, err = settings.Set(context.Background(), &models.ModelSetting{Key: "llm.model", Value: "after-restart-llm"})
			require.NoError(t, err)
			_, err = settings.Set(context.Background(), &models.ModelSetting{Key: "embedder.dimensions", Value: "1536"})
			require.NoError(t, err)
			checkRequests()
		}
		if run == 0 {
			body, err := json.Marshal(storeMemoryRequest{Project: project, Content: "Four-input startup preserves durable memory", SourceAgent: "synthetic-fixture"})
			require.NoError(t, err)
			created := httptest.NewRecorder()
			service.router.ServeHTTP(created, httptest.NewRequest(http.MethodPost, "/api/memories", bytes.NewReader(body)))
			require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
		}
		listed := httptest.NewRecorder()
		service.router.ServeHTTP(listed, httptest.NewRequest(http.MethodGet, "/api/memories?project="+project+"&limit=50", nil))
		require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
		memories := decodeKnowledgeItems[models.Memory](t, listed)
		require.Len(t, memories, 1)
		require.Equal(t, "Four-input startup preserves durable memory", memories[0].Content)
		require.Equal(t, project, memories[0].Project)
		health := httptest.NewRecorder()
		service.router.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/api/model-health", nil))
		require.Equal(t, http.StatusOK, health.Code, health.Body.String())
		var snapshot modelHealthResponse
		require.NoError(t, json.Unmarshal(health.Body.Bytes(), &snapshot))
		require.Equal(t, wantModel, snapshot.Rows[2].Model, "health must report the actual startup-resolved client")
		require.Equal(t, "settings", snapshot.Rows[2].Source)
		require.NotContains(t, health.Body.String(), provider.URL)
		require.NotContains(t, health.Body.String(), "synthetic-llm-key")
		require.NotContains(t, health.Body.String(), "synthetic-embedding-key")
		stop()
	}
}
