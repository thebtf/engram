package worker

import (
	"net/http"
	"os"
	"reflect"
	"strings"
	"time"

	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

type modelHealthResponse struct {
	GeneratedAt string             `json:"generated_at"`
	Rows        []modelHealthRow   `json:"rows"`
	Summary     modelHealthSummary `json:"summary"`
}

type modelHealthSummary struct {
	Total      int `json:"total"`
	OK         int `json:"ok"`
	Standby    int `json:"standby"`
	Degraded   int `json:"degraded"`
	Configured int `json:"configured"`
}

type modelHealthRow struct {
	ID         string   `json:"id"`
	Role       string   `json:"role"`
	Provider   string   `json:"provider"`
	Model      string   `json:"model"`
	Health     string   `json:"health"`
	Source     string   `json:"source"`
	Endpoint   string   `json:"endpoint"`
	Message    string   `json:"message"`
	Evidence   []string `json:"evidence"`
	Configured bool     `json:"configured"`
	SecretSet  bool     `json:"secret_set"`
}

type modelNameProvider interface {
	Model() string
}

type modelConfigSignal struct {
	Source   string
	Evidence []string
	Set      bool
}

func (s *Service) handleModelHealth(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		http.Error(w, "model health unavailable: store not initialized", http.StatusServiceUnavailable)
		return
	}

	settingsStore := dbgorm.NewSettingsStore(s.store)
	settings, err := settingsStore.List(r.Context())
	if err != nil {
		http.Error(w, "model health unavailable: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.initMu.RLock()
	embeddingClient := s.embeddingClient
	rerankClient := s.rerankClient
	s.initMu.RUnlock()

	writeJSON(w, buildModelHealthResponse(settings, embeddingClient, rerankClient))
}

func buildModelHealthResponse(settings []*models.ModelSetting, embeddingClient, rerankClient modelNameProvider) modelHealthResponse {
	rowsByKey := modelSettingsByKey(settings)

	rows := []modelHealthRow{
		buildConfiguredModelRow(modelRowInput{
			ID:            "recall/embedder",
			Role:          "embedding",
			Provider:      "OpenAI-compatible embeddings",
			Endpoint:      "/v1/embeddings",
			URLenv:        "ENGRAM_EMBEDDING_URL",
			URLsetting:    "embedder.url",
			ModelEnv:      "ENGRAM_EMBEDDING_MODEL",
			ModelSetting:  "embedder.model",
			SecretEnv:     "ENGRAM_EMBEDDING_API_KEY",
			SecretSetting: "embedder.api_key",
			DefaultModel:  "text-embedding",
			ActiveClient:  embeddingClient,
			ActiveMsg:     "Embedding client is initialized; vector recall can use the configured endpoint.",
			DisabledMsg:   "Embedding URL is not configured; recall falls back to non-vector paths.",
			DegradedMsg:   "Embedding URL is configured, but the client is not initialized.",
		}, rowsByKey),
		buildConfiguredModelRow(modelRowInput{
			ID:            "recall/reranker",
			Role:          "reranker",
			Provider:      "Cohere-compatible rerank",
			Endpoint:      "/v1/rerank",
			URLenv:        "ENGRAM_RERANK_URL",
			URLsetting:    "reranker.url",
			ModelEnv:      "ENGRAM_RERANK_MODEL",
			ModelSetting:  "reranker.model",
			SecretEnv:     "ENGRAM_RERANK_API_KEY",
			SecretSetting: "reranker.api_key",
			DefaultModel:  "bge-reranker",
			ActiveClient:  rerankClient,
			ActiveMsg:     "Reranker client is initialized; recall can reorder fused candidates.",
			DisabledMsg:   "Reranker URL is not configured; recall keeps fusion order.",
			DegradedMsg:   "Reranker URL is configured, but the client is not initialized.",
		}, rowsByKey),
		buildOnDemandLLMRow(),
	}

	return modelHealthResponse{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Rows:        rows,
		Summary:     summarizeModelHealth(rows),
	}
}

type modelRowInput struct {
	ID            string
	Role          string
	Provider      string
	Endpoint      string
	URLenv        string
	URLsetting    string
	ModelEnv      string
	ModelSetting  string
	SecretEnv     string
	SecretSetting string
	DefaultModel  string
	ActiveMsg     string
	DisabledMsg   string
	DegradedMsg   string
	ActiveClient  modelNameProvider
}

func buildConfiguredModelRow(input modelRowInput, rowsByKey map[string]*models.ModelSetting) modelHealthRow {
	url := resolveModelConfigSignal(input.URLenv, input.URLsetting, rowsByKey)
	secret := resolveModelConfigSignal(input.SecretEnv, input.SecretSetting, rowsByKey)
	model, modelSource := resolveModelName(input.ModelEnv, input.ModelSetting, input.DefaultModel, rowsByKey)

	health := "standby"
	message := input.DisabledMsg
	if activeModel, active := activeModelName(input.ActiveClient); active {
		health = "ok"
		message = input.ActiveMsg
		if activeModel != "" {
			model = activeModel
		}
	} else if url.Set {
		health = "degraded"
		message = input.DegradedMsg
	}

	evidence := append([]string{}, url.Evidence...)
	evidence = append(evidence, modelSource.Evidence...)
	evidence = append(evidence, secret.Evidence...)

	return modelHealthRow{
		ID:         input.ID,
		Role:       input.Role,
		Provider:   input.Provider,
		Model:      model,
		Health:     health,
		Source:     url.Source,
		Endpoint:   input.Endpoint,
		Message:    message,
		Evidence:   compactEvidence(evidence),
		Configured: url.Set,
		SecretSet:  secret.Set,
	}
}

func activeModelName(client modelNameProvider) (string, bool) {
	if client == nil {
		return "", false
	}

	value := reflect.ValueOf(client)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		if value.IsNil() {
			return "", false
		}
	}

	return strings.TrimSpace(client.Model()), true
}

func buildOnDemandLLMRow() modelHealthRow {
	url := resolveEnvOnlySignal("ENGRAM_LLM_URL")
	secret := resolveEnvOnlySignal("ENGRAM_LLM_API_KEY")
	model := strings.TrimSpace(os.Getenv("ENGRAM_LLM_MODEL"))
	if model == "" {
		model = "chat-default"
	}

	health := "standby"
	message := "LLM URL is not configured; crystallization and on-demand LLM flows stay disabled."
	if url.Set {
		message = "LLM URL is configured; chat-completion client is created on demand and is not probed by this endpoint."
	}

	return modelHealthRow{
		ID:         "ops/llm",
		Role:       "llm",
		Provider:   "OpenAI-compatible chat",
		Model:      model,
		Health:     health,
		Source:     url.Source,
		Endpoint:   "/v1/chat/completions",
		Message:    message,
		Evidence:   compactEvidence(append(url.Evidence, secret.Evidence...)),
		Configured: url.Set,
		SecretSet:  secret.Set,
	}
}

func modelSettingsByKey(rows []*models.ModelSetting) map[string]*models.ModelSetting {
	out := make(map[string]*models.ModelSetting, len(rows))
	for _, row := range rows {
		if row == nil {
			continue
		}
		key := strings.TrimSpace(row.Key)
		if key != "" {
			out[key] = row
		}
	}
	return out
}

func resolveModelConfigSignal(envKey, settingKey string, rowsByKey map[string]*models.ModelSetting) modelConfigSignal {
	if strings.TrimSpace(os.Getenv(envKey)) != "" {
		return modelConfigSignal{Source: "env", Evidence: []string{envKey}, Set: true}
	}

	if row := rowsByKey[settingKey]; row != nil {
		if row.Encrypted {
			return modelConfigSignal{
				Source:   "settings",
				Evidence: []string{"model_settings." + settingKey + " (secret_set)"},
				Set:      len(row.EncryptedValue) > 0,
			}
		}
		return modelConfigSignal{
			Source:   "settings",
			Evidence: []string{"model_settings." + settingKey},
			Set:      strings.TrimSpace(row.Value) != "",
		}
	}

	return modelConfigSignal{Source: "absent", Evidence: []string{envKey, "model_settings." + settingKey}, Set: false}
}

func resolveEnvOnlySignal(envKey string) modelConfigSignal {
	if strings.TrimSpace(os.Getenv(envKey)) != "" {
		return modelConfigSignal{Source: "env", Evidence: []string{envKey}, Set: true}
	}
	return modelConfigSignal{Source: "absent", Evidence: []string{envKey}, Set: false}
}

func resolveModelName(envKey, settingKey, fallback string, rowsByKey map[string]*models.ModelSetting) (string, modelConfigSignal) {
	if value := strings.TrimSpace(os.Getenv(envKey)); value != "" {
		return value, modelConfigSignal{Source: "env", Evidence: []string{envKey}, Set: true}
	}

	if row := rowsByKey[settingKey]; row != nil && !row.Encrypted && strings.TrimSpace(row.Value) != "" {
		return strings.TrimSpace(row.Value), modelConfigSignal{Source: "settings", Evidence: []string{"model_settings." + settingKey}, Set: true}
	}

	return fallback, modelConfigSignal{Source: "default", Evidence: []string{envKey, "model_settings." + settingKey, "default:" + fallback}, Set: false}
}

func compactEvidence(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, value := range in {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func summarizeModelHealth(rows []modelHealthRow) modelHealthSummary {
	summary := modelHealthSummary{Total: len(rows)}
	for _, row := range rows {
		if row.Configured {
			summary.Configured++
		}
		switch row.Health {
		case "ok":
			summary.OK++
		case "degraded":
			summary.Degraded++
		default:
			summary.Standby++
		}
	}
	return summary
}
