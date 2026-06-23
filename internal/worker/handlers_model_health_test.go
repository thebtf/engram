package worker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	}, fakeModelClient{model: "runtime-embedding"}, nil)

	require.Len(t, response.Rows, 3)
	assert.Equal(t, 1, response.Summary.OK)
	assert.Equal(t, 2, response.Summary.Standby)
	assert.Equal(t, 1, response.Summary.Configured)

	embedding := response.Rows[0]
	assert.Equal(t, "recall/embedder", embedding.ID)
	assert.Equal(t, "ok", embedding.Health)
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

	response := buildModelHealthResponse(nil, nil, nil)

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
	response := buildModelHealthResponse(nil, embeddingClient, nil)

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
