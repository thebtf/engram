package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/config"
	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/llm"
	"github.com/thebtf/engram/pkg/models"
)

type fakeModelClient struct {
	model string
}

func (f fakeModelClient) Model() string { return f.model }

func TestBuildModelHealthResponse_UsesSettingsAndRedactsSecrets(t *testing.T) {
	t.Setenv("ENGRAM_EMBEDDING_URL", "")
	t.Setenv("ENGRAM_EMBEDDING_MODEL", "")
	t.Setenv("ENGRAM_EMBEDDING_API_KEY", "")
	t.Setenv("ENGRAM_RERANK_URL", "")
	t.Setenv("ENGRAM_RERANK_MODEL", "")
	t.Setenv("ENGRAM_RERANK_API_KEY", "")
	t.Setenv("ENGRAM_LLM_URL", "")
	t.Setenv("ENGRAM_LLM_MODEL", "")
	t.Setenv("ENGRAM_LLM_API_KEY", "")

	response := buildModelHealthResponse([]*models.ModelSetting{
		{Key: "embedder.url", Value: "https://embedder.example"},
		{Key: "embedder.model", Value: "text-embedding-test"},
		{Key: "embedder.api_key", Encrypted: true, EncryptedValue: []byte{1, 2, 3}},
	}, fakeModelClient{model: "runtime-embedding"}, nil, nil)

	require.Len(t, response.Rows, 3)
	assert.Equal(t, 0, response.Summary.OK)
	assert.Equal(t, 3, response.Summary.Standby)
	assert.Equal(t, 1, response.Summary.Configured)

	embedding := response.Rows[0]
	assert.Equal(t, "recall/embedder", embedding.ID)
	assert.Equal(t, "standby", embedding.Health)
	assert.Equal(t, "settings", embedding.Source)
	assert.Equal(t, "runtime-embedding", embedding.Model)
	assert.True(t, embedding.Configured)
	assert.True(t, embedding.SecretSet)

	payload, err := json.Marshal(response)
	require.NoError(t, err)
	assert.NotContains(t, string(payload), "https://embedder.example")
	assert.NotContains(t, string(payload), "1,2,3")
	assert.Contains(t, string(payload), "secret_set")
}

func TestBuildModelHealthResponse_EnvConfiguredButClientMissingIsDegraded(t *testing.T) {
	t.Setenv("ENGRAM_EMBEDDING_URL", "https://env-embedder.example")
	t.Setenv("ENGRAM_EMBEDDING_MODEL", "env-embedding")
	t.Setenv("ENGRAM_RERANK_URL", "")
	t.Setenv("ENGRAM_LLM_URL", "")

	response := buildModelHealthResponse(nil, nil, nil, nil)

	require.Len(t, response.Rows, 3)
	embedding := response.Rows[0]
	assert.Equal(t, "degraded", embedding.Health)
	assert.Equal(t, "env", embedding.Source)
	assert.Equal(t, "env-embedding", embedding.Model)
	assert.True(t, embedding.Configured)
	assert.Equal(t, 1, response.Summary.Degraded)
}

func TestBuildModelHealthResponse_TypedNilClientIsNotActive(t *testing.T) {
	t.Setenv("ENGRAM_EMBEDDING_URL", "https://env-embedder.example")
	t.Setenv("ENGRAM_EMBEDDING_MODEL", "")
	t.Setenv("ENGRAM_RERANK_URL", "")
	t.Setenv("ENGRAM_LLM_URL", "")

	var embeddingClient *fakeModelClient
	response := buildModelHealthResponse(nil, embeddingClient, nil, nil)

	require.Len(t, response.Rows, 3)
	embedding := response.Rows[0]
	assert.Equal(t, "degraded", embedding.Health)
	assert.Equal(t, "Embedding URL is configured, but the client is not initialized.", embedding.Message)
	assert.Equal(t, 0, response.Summary.OK)
	assert.Equal(t, 1, response.Summary.Degraded)
}

func TestHandleModelHealth_StoreUnavailable(t *testing.T) {
	svc := &Service{}
	req := httptest.NewRequest(http.MethodGet, "/api/model-health", nil)
	w := httptest.NewRecorder()

	svc.handleModelHealth(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), "store not initialized")
}

func TestBuildModelHealthResponse_LLMUsesStoredConfigurationAndStartupModel(t *testing.T) {
	t.Setenv("ENGRAM_LLM_URL", "")
	t.Setenv("ENGRAM_LLM_MODEL", "actual-startup-model")
	t.Setenv("ENGRAM_LLM_API_KEY", "synthetic-health-key")
	client, err := llm.NewClientWithSettings(context.Background(), &settingsResolver{reader: fakeSettingsReader{rows: map[string]*models.ModelSetting{
		"llm.url": {Key: "llm.url", Value: "https://synthetic-provider.invalid"},
	}}})
	require.NoError(t, err)
	response := buildModelHealthResponse([]*models.ModelSetting{
		{Key: "llm.url", Value: "https://synthetic-provider.invalid"},
		{Key: "llm.model", Value: "changed-for-next-startup"},
		{Key: "llm.api_key", Encrypted: true, EncryptedValue: []byte{1, 2, 3}},
	}, nil, nil, client)
	row := response.Rows[2]
	assert.Equal(t, "settings", row.Source)
	assert.Equal(t, "actual-startup-model", row.Model)
	assert.True(t, row.Configured)
	assert.True(t, row.SecretSet)
	assert.Equal(t, "standby", row.Health, "configuration is not a provider probe")
	payload, err := json.Marshal(response)
	require.NoError(t, err)
	assert.NotContains(t, string(payload), "https://synthetic-provider.invalid")
}

func TestModelHealthHTTPWithSyntheticSettings(t *testing.T) {
	for _, key := range []string{"ENGRAM_EMBEDDING_URL", "ENGRAM_EMBEDDING_MODEL", "ENGRAM_EMBEDDING_API_KEY", "ENGRAM_RERANK_URL", "ENGRAM_RERANK_MODEL", "ENGRAM_RERANK_API_KEY", "ENGRAM_LLM_URL", "ENGRAM_LLM_MODEL", "ENGRAM_LLM_API_KEY"} {
		t.Setenv(key, "")
	}
	t.Setenv("ENGRAM_AUTH_DISABLED", "true")
	db := knowledgeSQLiteDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE model_settings (id INTEGER PRIMARY KEY, key TEXT, value TEXT, encrypted_value BLOB, encrypted BOOLEAN, encryption_key_fingerprint TEXT, description TEXT, edited_by TEXT, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME, version INTEGER)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO model_settings (key, value, encrypted) VALUES (?, ?, false), (?, ?, false)`, "embedder.url", "https://synthetic.invalid", "embedder.model", "synthetic-model").Error)
	svc := &Service{router: chi.NewRouter(), store: &dbgorm.Store{DB: db}, config: &config.Config{}}
	svc.setupMiddleware()
	svc.setupRoutes()
	svc.ready.Store(true)
	server := httptest.NewServer(svc.router)
	defer server.Close()
	response, err := server.Client().Get(server.URL + "/api/model-health")
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Equal(t, "application/json", response.Header.Get("Content-Type"))
	var snapshot modelHealthResponse
	require.NoError(t, json.NewDecoder(response.Body).Decode(&snapshot))
	require.Len(t, snapshot.Rows, 3)
	assert.Equal(t, "recall/embedder", snapshot.Rows[0].ID)
	assert.Equal(t, "settings", snapshot.Rows[0].Source)
	assert.Equal(t, "synthetic-model", snapshot.Rows[0].Model)
	assert.Equal(t, "degraded", snapshot.Rows[0].Health)
	assert.Equal(t, 1, snapshot.Summary.Configured)
}
