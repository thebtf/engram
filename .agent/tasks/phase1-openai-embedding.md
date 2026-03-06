# Phase 1: OpenAI-Compatible Embedding

## Goal
Add an OpenAI-compatible HTTP embedding model alongside the existing ONNX/BGE model.
Selection is config-driven. No breaking changes to existing behavior.

## Codebase Root
`D:\Dev\forks\engram`

## Module
`github.com/thebtf/engram`

## LANGUAGE
All file content (code, comments) MUST be English. No exceptions.

## Key Interfaces (already exist — do NOT change)

```go
// internal/embedding/model.go

type EmbeddingModel interface {
    Name() string
    Version() string
    Dimensions() int
    Embed(text string) ([]float32, error)
    EmbedBatch(texts []string) ([][]float32, error)
    Close() error
}

type ModelMetadata struct {
    Name        string `json:"name"`
    Version     string `json:"version"`
    Description string `json:"description"`
    Dimensions  int    `json:"dimensions"`
    Default     bool   `json:"default"`
}

// Register model with global registry:
func RegisterModel(meta ModelMetadata, factory ModelFactory)

// Get model by version string:
func GetModel(version string) (EmbeddingModel, error)
```

```go
// internal/embedding/service.go (already exists)
// NewService() uses DefaultModelVersion
// NewServiceWithModel(version string) takes explicit version
```

## Files to Create/Modify

### 1. internal/embedding/openai.go (NEW)

Implement `openAIModel` struct that satisfies the `EmbeddingModel` interface.
Register it in `init()` with version `"openai"`.

```go
package embedding

import (
    "bytes"
    "encoding/json"
    "fmt"
    "net/http"
    "time"
)

// OpenAI embedding model constants
const (
    OpenAIModelVersion     = "openai"
    OpenAIDefaultBaseURL   = "https://api.openai.com/v1"
    OpenAIDefaultModel     = "text-embedding-3-small"
    OpenAIDefaultDimension = 1536
    openAIHTTPTimeout      = 30 * time.Second
)

type openAIModel struct {
    client     *http.Client
    baseURL    string
    apiKey     string
    modelName  string
    dimensions int
}

// openAIEmbedRequest is the request body for POST /v1/embeddings.
type openAIEmbedRequest struct {
    Input          interface{} `json:"input"`            // string or []string
    Model          string      `json:"model"`
    EncodingFormat string      `json:"encoding_format"`  // "float"
}

// openAIEmbedResponse is the response from POST /v1/embeddings.
type openAIEmbedResponse struct {
    Data []struct {
        Embedding []float32 `json:"embedding"`
        Index     int       `json:"index"`
    } `json:"data"`
    Model string `json:"model"`
}
```

Implement all interface methods. `Close()` is a no-op (no resources to release).

For `EmbedBatch`, send all texts as a single request (OpenAI supports `[]string` input).
Sort the returned embeddings by `index` field to ensure correct ordering.

Error handling: HTTP non-2xx → return descriptive error with status code.
Include response body snippet in errors (first 200 chars).

Register in `init()`:
```go
func init() {
    // OpenAI model is registered but only used when EMBEDDING_PROVIDER=openai config is set.
    // The factory reads config at creation time.
    RegisterModel(ModelMetadata{
        Name:        "OpenAI Compatible",
        Version:     OpenAIModelVersion,
        Dimensions:  OpenAIDefaultDimension,
        Description: "OpenAI-compatible embedding via REST API (supports LiteLLM proxy)",
    }, newOpenAIModel)
}
```

The factory `newOpenAIModel()` reads config from `config.Get()`:
- BaseURL: `config.GetEmbeddingBaseURL()` (default: `OpenAIDefaultBaseURL`)
- APIKey: `config.GetEmbeddingAPIKey()`
- ModelName: `config.GetEmbeddingModelName()` (default: `OpenAIDefaultModel`)
- Dimensions: `config.GetEmbeddingDimensions()` (default: `OpenAIDefaultDimension`)

If APIKey is empty, return error: "EMBEDDING_API_KEY is required for openai provider".

### 2. internal/config/config.go (MODIFY)

Add fields to Config struct in the string group (after VectorStorageStrategy):
```go
EmbeddingProvider  string // env: EMBEDDING_PROVIDER ("builtin" or "openai")
EmbeddingBaseURL   string // env: EMBEDDING_BASE_URL
EmbeddingAPIKey    string // env: EMBEDDING_API_KEY (never in JSON)
EmbeddingModelName string // env: EMBEDDING_MODEL_NAME
EmbeddingDimensions int   // env: EMBEDDING_DIMENSIONS
```

Note: Fields are named without json tags — EmbeddingAPIKey is env-only (security).
EmbeddingProvider, BaseURL, ModelName CAN have json tags for settings.json support.

Add to Default():
```go
EmbeddingProvider:   "builtin",
EmbeddingModelName:  OpenAIDefaultModel,  // only used when provider=openai
EmbeddingDimensions: OpenAIDefaultDimension,
```

Wait — we can't import embedding from config (circular). Use hardcoded defaults instead:
```go
EmbeddingProvider:   "builtin",
EmbeddingModelName:  "text-embedding-3-small",
EmbeddingDimensions: 1536,
```

Add to Load() JSON parsing:
```go
if v, ok := settings["EMBEDDING_PROVIDER"].(string); ok && v != "" {
    cfg.EmbeddingProvider = v
}
if v, ok := settings["EMBEDDING_BASE_URL"].(string); ok && v != "" {
    cfg.EmbeddingBaseURL = v
}
// EMBEDDING_API_KEY: env-only, NOT in JSON settings
if v, ok := settings["EMBEDDING_MODEL_NAME"].(string); ok && v != "" {
    cfg.EmbeddingModelName = v
}
if v, ok := settings["EMBEDDING_DIMENSIONS"].(float64); ok && v > 0 {
    cfg.EmbeddingDimensions = int(v)
}
```

Add to env-var overrides block (after the existing block at end of Load()):
```go
if v := strings.TrimSpace(os.Getenv("EMBEDDING_PROVIDER")); v != "" {
    cfg.EmbeddingProvider = v
}
if v := strings.TrimSpace(os.Getenv("EMBEDDING_BASE_URL")); v != "" {
    cfg.EmbeddingBaseURL = v
}
if v := strings.TrimSpace(os.Getenv("EMBEDDING_API_KEY")); v != "" {
    cfg.EmbeddingAPIKey = v
}
if v := strings.TrimSpace(os.Getenv("EMBEDDING_MODEL_NAME")); v != "" {
    cfg.EmbeddingModelName = v
}
// EMBEDDING_DIMENSIONS handled as integer in env
```

Add getter functions after GetWorkerToken():
```go
// GetEmbeddingProvider returns the embedding provider ("builtin" or "openai").
func GetEmbeddingProvider() string {
    if v := strings.TrimSpace(os.Getenv("EMBEDDING_PROVIDER")); v != "" {
        return v
    }
    return Get().EmbeddingProvider
}

// GetEmbeddingBaseURL returns the OpenAI-compatible API base URL.
func GetEmbeddingBaseURL() string {
    if v := strings.TrimSpace(os.Getenv("EMBEDDING_BASE_URL")); v != "" {
        return v
    }
    if cfg := Get(); cfg.EmbeddingBaseURL != "" {
        return cfg.EmbeddingBaseURL
    }
    return "https://api.openai.com/v1"
}

// GetEmbeddingAPIKey returns the embedding API key (env-only).
func GetEmbeddingAPIKey() string {
    return strings.TrimSpace(os.Getenv("EMBEDDING_API_KEY"))
}

// GetEmbeddingModelName returns the embedding model name for external providers.
func GetEmbeddingModelName() string {
    if v := strings.TrimSpace(os.Getenv("EMBEDDING_MODEL_NAME")); v != "" {
        return v
    }
    if cfg := Get(); cfg.EmbeddingModelName != "" {
        return cfg.EmbeddingModelName
    }
    return "text-embedding-3-small"
}

// GetEmbeddingDimensions returns the embedding vector dimensions for external providers.
func GetEmbeddingDimensions() int {
    if cfg := Get(); cfg.EmbeddingDimensions > 0 {
        return cfg.EmbeddingDimensions
    }
    return 1536
}
```

### 3. internal/embedding/service.go (MODIFY — small change)

Add `NewServiceFromConfig()` function that reads `config.GetEmbeddingProvider()` and creates the appropriate service:

```go
// NewServiceFromConfig creates an embedding service based on EMBEDDING_PROVIDER config.
// Uses "openai" provider when EMBEDDING_PROVIDER=openai, builtin ONNX otherwise.
func NewServiceFromConfig() (*Service, error) {
    provider := config.GetEmbeddingProvider()
    switch provider {
    case "openai":
        return NewServiceWithModel(OpenAIModelVersion)
    default:
        return NewService() // builtin BGE
    }
}
```

Add import: `"github.com/thebtf/engram/internal/config"`

Check for circular import: embedding imports config — config does NOT import embedding → safe.

### 4. internal/worker/service.go (MODIFY — tiny change)

In `initializeAsync()`, find where embedding.NewService() is called and change to embedding.NewServiceFromConfig().

Find the call (grep for `embedding.New`) and replace with `embedding.NewServiceFromConfig()`.

### 5. internal/worker/handlers.go (MODIFY — add /v1/models endpoint)

Add handler `handleListModels`:

```go
// handleListModels returns available embedding models in OpenAI-compatible format.
// Compatible with LiteLLM proxy model listing.
func (s *Service) handleListModels(w http.ResponseWriter, r *http.Request) {
    models := embedding.ListModels()

    type modelData struct {
        ID      string `json:"id"`
        Object  string `json:"object"`
        Created int64  `json:"created"`
        OwnedBy string `json:"owned_by"`
    }

    type response struct {
        Object string      `json:"object"`
        Data   []modelData `json:"data"`
    }

    data := make([]modelData, 0, len(models))
    for _, m := range models {
        data = append(data, modelData{
            ID:      m.Version,
            Object:  "model",
            Created: 0,
            OwnedBy: "engram",
        })
    }

    writeJSON(w, http.StatusOK, response{
        Object: "list",
        Data:   data,
    })
}
```

Find how `writeJSON` is called in existing handlers and use the same pattern.

### 6. internal/worker/service.go — register /v1/models route (MODIFY)

In `setupRoutes()`, add:
```go
s.router.Get("/v1/models", s.handleListModels)
```

## Constraints

- MUST NOT break existing behavior: if EMBEDDING_PROVIDER is not set, uses builtin BGE exactly as before
- MUST NOT require EMBEDDING_API_KEY if provider is "builtin"
- EMBEDDING_API_KEY MUST NOT be readable from settings.json (env-only, security)
- EmbedBatch MUST preserve order (sort by index from OpenAI response)
- HTTP client timeout: 30s
- All errors must include enough context to debug (model name, endpoint, status code)
- No new external dependencies needed (uses standard net/http)

## Done When

- `go vet ./...` passes (no new vet errors beyond existing CGO warnings)
- Setting EMBEDDING_PROVIDER=builtin: old behavior unchanged
- Setting EMBEDDING_PROVIDER=openai with EMBEDDING_BASE_URL=http://localhost:4000: service initializes with openai model
- /v1/models endpoint returns JSON list of registered models
- Report all modified/created files
