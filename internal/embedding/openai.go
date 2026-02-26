package embedding

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/lukaszraczylo/claude-mnemonic/internal/config"
)

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

type openAIEmbedRequest struct {
	Input          interface{} `json:"input"`
	Model          string      `json:"model"`
	EncodingFormat string      `json:"encoding_format"`
}

type openAIEmbedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Model string `json:"model"`
}

func init() {
	RegisterModel(ModelMetadata{
		Name:        "OpenAI Compatible",
		Version:     OpenAIModelVersion,
		Dimensions:  OpenAIDefaultDimension,
		Description: "OpenAI-compatible embedding via REST API (supports LiteLLM proxy)",
	}, newOpenAIModel)
}

func newOpenAIModel() (EmbeddingModel, error) {
	apiKey := config.GetEmbeddingAPIKey()
	if apiKey == "" {
		return nil, fmt.Errorf("EMBEDDING_API_KEY is required for openai provider")
	}

	baseURL := config.GetEmbeddingBaseURL()
	if baseURL == "" {
		baseURL = OpenAIDefaultBaseURL
	}
	modelName := config.GetEmbeddingModelName()
	if modelName == "" {
		modelName = OpenAIDefaultModel
	}
	dimensions := config.GetEmbeddingDimensions()
	if dimensions <= 0 {
		dimensions = OpenAIDefaultDimension
	}

	return &openAIModel{
		client:     &http.Client{Timeout: openAIHTTPTimeout},
		baseURL:    baseURL,
		apiKey:     apiKey,
		modelName:  modelName,
		dimensions: dimensions,
	}, nil
}

func (m *openAIModel) Name() string { return "OpenAI Compatible" }
func (m *openAIModel) Version() string {
	return OpenAIModelVersion
}
func (m *openAIModel) Dimensions() int { return m.dimensions }
func (m *openAIModel) Close() error    { return nil }

func (m *openAIModel) Embed(text string) ([]float32, error) {
	if text == "" {
		return make([]float32, m.dimensions), nil
	}
	results, err := m.embedRequest(text)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("embedding API returned no results for model %s", m.modelName)
	}
	return results[0], nil
}

func (m *openAIModel) EmbedBatch(texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	results, err := m.embedRequest(texts)
	if err != nil {
		return nil, err
	}
	if len(results) != len(texts) {
		return nil, fmt.Errorf("embedding API returned %d results for %d inputs (model=%s)",
			len(results), len(texts), m.modelName)
	}
	return results, nil
}

func (m *openAIModel) embedRequest(input interface{}) ([][]float32, error) {
	reqBody := openAIEmbedRequest{
		Input:          input,
		Model:          m.modelName,
		EncodingFormat: "float",
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal embedding request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, m.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create embedding request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.apiKey)

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send embedding request to %s: %w", m.baseURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodySnippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("embedding API error (model=%s, status=%d): %s",
			m.modelName, resp.StatusCode, strings.TrimSpace(string(bodySnippet)))
	}

	var embedResp openAIEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&embedResp); err != nil {
		return nil, fmt.Errorf("decode embedding response from %s: %w", m.baseURL, err)
	}

	// Sort by index to preserve order
	sort.Slice(embedResp.Data, func(i, j int) bool {
		return embedResp.Data[i].Index < embedResp.Data[j].Index
	})

	results := make([][]float32, len(embedResp.Data))
	for i, d := range embedResp.Data {
		results[i] = d.Embedding
	}
	return results, nil
}
