// Package config provides configuration management for engram.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

const (
	// DefaultWorkerPort is the default HTTP port for the worker service.
	DefaultWorkerPort = 37777

	// DefaultModel for SDK agent (use "haiku" for cost-efficient processing).
	// Claude Code CLI accepts aliases: haiku, sonnet, opus (always latest versions)
	DefaultModel = "haiku"
)

// DefaultObservationTypes are the observation types to include in context.
var DefaultObservationTypes = []string{
	"bugfix", "feature", "refactor", "change", "discovery", "decision",
}

// DefaultObservationConcepts are the concept tags to include in context.
var DefaultObservationConcepts = []string{
	"how-it-works", "why-it-exists", "what-changed",
	"problem-solution", "gotcha", "pattern", "trade-off",
}

// CriticalConcepts are concepts that indicate "must know" information.
// Observations with these concepts are prioritized in context injection.
var CriticalConcepts = []string{
	"gotcha", "pattern", "problem-solution", "trade-off",
}

// Config holds the application configuration.
// Field order optimized for memory alignment (fieldalignment).
type Config struct {
	ContextFullField          string `json:"context_full_field"`
	DBPath                    string `json:"db_path"`
	Model                     string `json:"model"`
	ClaudeCodePath            string `json:"claude_code_path"`
	EmbeddingModel            string `json:"embedding_model"`
	VectorStorageStrategy     string `json:"vector_storage_strategy"`
	EmbeddingProvider         string `json:"embedding_provider"`
	EmbeddingBaseURL          string `json:"embedding_base_url"`
	EmbeddingModelName        string `json:"embedding_model_name"`
	EmbeddingDimensions       int    `json:"embedding_dimensions"`
	EmbeddingAPIKey           string
	DatabaseDSN               string   `json:"-"`                  // env-only: DATABASE_DSN (contains password, never JSON)
	DatabaseMaxConns          int      `json:"database_max_conns"` // PostgreSQL pool size (default: 10)
	ContextObsConcepts        []string `json:"context_obs_concepts"`
	ContextObsTypes           []string `json:"context_obs_types"`
	ContextFullCount          int      `json:"context_full_count"`
	GraphBranchFactor         int      `json:"graph_branch_factor"`
	GraphEdgeWeight           float64  `json:"graph_edge_weight"`
	ContextRelevanceThreshold float64  `json:"context_relevance_threshold"`
	RerankingCandidates       int      `json:"reranking_candidates"`
	WorkerPort                int      `json:"worker_port"`
	RerankingMinImprovement   float64  `json:"reranking_min_improvement"`
	ContextObservations       int      `json:"context_observations"`
	ContextMaxPromptResults   int      `json:"context_max_prompt_results"`
	ContextSessionCount       int      `json:"context_session_count"`
	MaxConns                  int      `json:"max_conns"`
	RerankingAlpha            float64  `json:"reranking_alpha"`
	GraphMaxHops              int      `json:"graph_max_hops"`
	RerankingResults          int      `json:"reranking_results"`
	GraphRebuildIntervalMin   int      `json:"graph_rebuild_interval_min"`
	HubThreshold              int      `json:"hub_threshold"`
	ObservationRetentionDays  int      `json:"observation_retention_days"`
	MaintenanceIntervalHours  int      `json:"maintenance_interval_hours"`
	ContextShowWorkTokens     bool     `json:"context_show_work_tokens"`
	ContextShowReadTokens     bool     `json:"context_show_read_tokens"`
	RerankingPureMode         bool     `json:"reranking_pure_mode"`
	GraphEnabled              bool     `json:"graph_enabled"`
	MaintenanceEnabled        bool     `json:"maintenance_enabled"`
	RerankingEnabled          bool     `json:"reranking_enabled"`
	ContextShowLastSummary    bool     `json:"context_show_last_summary"`
	CleanupStaleObservations  bool     `json:"cleanup_stale_observations"`
	WorkerHost                string   // env-only
	WorkerToken               string   // env-only
	CollectionConfigPath      string   // env-only
	SessionsDir               string   // env-only: SESSIONS_DIR
	WorkstationID             string   // env-only: WORKSTATION_ID
}

var (
	globalConfig *Config
	configOnce   sync.Once
	configMu     sync.RWMutex
)

// DataDir returns the data directory path (~/.engram).
func DataDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".engram")
}

// DBPath returns the database file path.
func DBPath() string {
	if v := strings.TrimSpace(os.Getenv("ENGRAM_DB_PATH")); v != "" {
		return v
	}
	return filepath.Join(DataDir(), "engram.db")
}

// SettingsPath returns the settings file path.
func SettingsPath() string {
	return filepath.Join(DataDir(), "settings.json")
}

// EnsureDataDir creates the data directory if it doesn't exist.
// Uses 0700 permissions (owner-only) for security.
func EnsureDataDir() error {
	return os.MkdirAll(DataDir(), 0700)
}

// EnsureSettings creates a default settings file if it doesn't exist.
func EnsureSettings() error {
	path := SettingsPath()

	// Check if file exists
	if _, err := os.Stat(path); err == nil {
		return nil // File exists
	}

	// Create default settings file with comments
	defaultSettings := `{
  "ENGRAM_WORKER_PORT": 37777,
  "ENGRAM_MODEL": "haiku",
  "ENGRAM_CONTEXT_OBSERVATIONS": 100,
  "ENGRAM_CONTEXT_FULL_COUNT": 25,
  "ENGRAM_CONTEXT_SESSION_COUNT": 10
}
`
	return os.WriteFile(path, []byte(defaultSettings), 0600)
}

// EnsureAll ensures all required directories and files exist.
func EnsureAll() error {
	if err := EnsureDataDir(); err != nil {
		return err
	}
	if err := EnsureSettings(); err != nil {
		return err
	}
	return nil
}

// DefaultEmbeddingModel is the default embedding model to use.
const DefaultEmbeddingModel = "bge-v1.5"

// Default returns a Config with default values.
func Default() *Config {
	return &Config{
		WorkerPort:                DefaultWorkerPort,
		DBPath:                    DBPath(),
		MaxConns:                  4,
		Model:                     DefaultModel,
		EmbeddingModel:            DefaultEmbeddingModel,
		RerankingEnabled:          true,  // Enable by default for improved relevance
		RerankingCandidates:       100,   // Retrieve top 100 candidates
		RerankingResults:          10,    // Return top 10 after reranking
		RerankingAlpha:            0.7,   // Favor cross-encoder score
		RerankingMinImprovement:   0,     // Always apply reranking
		GraphEnabled:              true,  // Enable graph-aware search by default
		GraphMaxHops:              2,     // Two-hop traversal
		GraphBranchFactor:         5,     // Expand top 5 neighbors per node
		GraphEdgeWeight:           0.3,   // Minimum edge weight to follow
		GraphRebuildIntervalMin:   60,    // Rebuild graph every 60 minutes
		VectorStorageStrategy:     "hub", // Hub storage strategy (LEANN-inspired)
		EmbeddingProvider:         "builtin",
		EmbeddingBaseURL:          "https://api.openai.com/v1",
		EmbeddingModelName:        "text-embedding-3-small",
		EmbeddingDimensions:       1536,
		HubThreshold:              5, // Require 5+ accesses to store embedding
		ContextObservations:       100,
		ContextFullCount:          25,
		ContextSessionCount:       10,
		ContextShowReadTokens:     true,
		ContextShowWorkTokens:     true,
		ContextFullField:          "narrative",
		ContextShowLastSummary:    true,
		ContextObsTypes:           DefaultObservationTypes,
		ContextObsConcepts:        DefaultObservationConcepts,
		ContextRelevanceThreshold: 0.3,   // Minimum 30% similarity to include
		ContextMaxPromptResults:   10,    // Cap at 10 results max (0 = no cap, threshold only)
		MaintenanceEnabled:        true,  // Enable scheduled maintenance
		MaintenanceIntervalHours:  6,     // Run every 6 hours
		ObservationRetentionDays:  0,     // 0 = no age-based deletion (keep all)
		CleanupStaleObservations:  false, // Don't auto-cleanup stale observations
		WorkerHost:                "127.0.0.1",
		DatabaseMaxConns:          10,
	}
}

// Load loads configuration from the settings file, merging with defaults.
func Load() (*Config, error) {
	cfg := Default()

	data, err := os.ReadFile(SettingsPath())
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	// Load settings from JSON file (skip if file doesn't exist)
	if err == nil {
		var settings map[string]interface{}
		if err := json.Unmarshal(data, &settings); err == nil {
			if v, ok := settings["ENGRAM_WORKER_PORT"].(float64); ok {
				cfg.WorkerPort = int(v)
			}
			// WorkerHost and WorkerToken are env-only (not settable via JSON file).
			if v, ok := settings["ENGRAM_DB_PATH"].(string); ok && v != "" {
				cfg.DBPath = v
			}
			if v, ok := settings["ENGRAM_MODEL"].(string); ok {
				cfg.Model = v
			}
			if v, ok := settings["CLAUDE_CODE_PATH"].(string); ok {
				cfg.ClaudeCodePath = v
			}
			if v, ok := settings["ENGRAM_EMBEDDING_MODEL"].(string); ok && v != "" {
				cfg.EmbeddingModel = v
			}
			if v, ok := settings["ENGRAM_RERANKING_ENABLED"].(bool); ok {
				cfg.RerankingEnabled = v
			}
			if v, ok := settings["ENGRAM_RERANKING_CANDIDATES"].(float64); ok && v > 0 {
				cfg.RerankingCandidates = int(v)
			}
			if v, ok := settings["ENGRAM_RERANKING_RESULTS"].(float64); ok && v > 0 {
				cfg.RerankingResults = int(v)
			}
			if v, ok := settings["ENGRAM_RERANKING_ALPHA"].(float64); ok && v >= 0 && v <= 1 {
				cfg.RerankingAlpha = v
			}
			if v, ok := settings["ENGRAM_RERANKING_MIN_IMPROVEMENT"].(float64); ok && v >= 0 {
				cfg.RerankingMinImprovement = v
			}
			if v, ok := settings["ENGRAM_RERANKING_PURE_MODE"].(bool); ok {
				cfg.RerankingPureMode = v
			}
			if v, ok := settings["ENGRAM_CONTEXT_OBSERVATIONS"].(float64); ok {
				cfg.ContextObservations = int(v)
			}
			if v, ok := settings["ENGRAM_CONTEXT_FULL_COUNT"].(float64); ok {
				cfg.ContextFullCount = int(v)
			}
			if v, ok := settings["ENGRAM_CONTEXT_SESSION_COUNT"].(float64); ok {
				cfg.ContextSessionCount = int(v)
			}
			if v, ok := settings["ENGRAM_CONTEXT_OBS_TYPES"].(string); ok && v != "" {
				cfg.ContextObsTypes = splitTrim(v)
			}
			if v, ok := settings["ENGRAM_CONTEXT_OBS_CONCEPTS"].(string); ok && v != "" {
				cfg.ContextObsConcepts = splitTrim(v)
			}
			if v, ok := settings["ENGRAM_CONTEXT_RELEVANCE_THRESHOLD"].(float64); ok && v >= 0 && v <= 1 {
				cfg.ContextRelevanceThreshold = v
			}
			if v, ok := settings["ENGRAM_CONTEXT_MAX_PROMPT_RESULTS"].(float64); ok && v >= 0 {
				cfg.ContextMaxPromptResults = int(v)
			}
			if v, ok := settings["ENGRAM_GRAPH_ENABLED"].(bool); ok {
				cfg.GraphEnabled = v
			}
			if v, ok := settings["ENGRAM_GRAPH_MAX_HOPS"].(float64); ok && v > 0 {
				cfg.GraphMaxHops = int(v)
			}
			if v, ok := settings["ENGRAM_GRAPH_BRANCH_FACTOR"].(float64); ok && v > 0 {
				cfg.GraphBranchFactor = int(v)
			}
			if v, ok := settings["ENGRAM_GRAPH_EDGE_WEIGHT"].(float64); ok && v >= 0 && v <= 1 {
				cfg.GraphEdgeWeight = v
			}
			if v, ok := settings["ENGRAM_GRAPH_REBUILD_INTERVAL_MIN"].(float64); ok && v > 0 {
				cfg.GraphRebuildIntervalMin = int(v)
			}
			if v, ok := settings["ENGRAM_VECTOR_STORAGE_STRATEGY"].(string); ok && v != "" {
				cfg.VectorStorageStrategy = v
			}
			if v, ok := settings["EMBEDDING_PROVIDER"].(string); ok && v != "" {
				cfg.EmbeddingProvider = v
			}
			if v, ok := settings["EMBEDDING_BASE_URL"].(string); ok && v != "" {
				cfg.EmbeddingBaseURL = v
			}
			// EMBEDDING_API_KEY is env-only, NOT loaded from JSON file.
			if v, ok := settings["EMBEDDING_MODEL_NAME"].(string); ok && v != "" {
				cfg.EmbeddingModelName = v
			}
			if v, ok := settings["EMBEDDING_DIMENSIONS"].(float64); ok && v > 0 {
				cfg.EmbeddingDimensions = int(v)
			}
			if v, ok := settings["ENGRAM_HUB_THRESHOLD"].(float64); ok && v > 0 {
				cfg.HubThreshold = int(v)
			}
		}
	}

	// Environment variable overrides (take precedence over JSON settings)
	if v := strings.TrimSpace(os.Getenv("ENGRAM_DB_PATH")); v != "" {
		cfg.DBPath = v
	}
	if v := strings.TrimSpace(os.Getenv("ENGRAM_WORKER_HOST")); v != "" {
		cfg.WorkerHost = v
	}
	if v := strings.TrimSpace(os.Getenv("ENGRAM_API_TOKEN")); v != "" {
		cfg.WorkerToken = v
	}
	if v := envFirstOf("ENGRAM_EMBEDDING_PROVIDER", "EMBEDDING_PROVIDER"); v != "" {
		cfg.EmbeddingProvider = v
	}
	if v := envFirstOf("ENGRAM_EMBEDDING_BASE_URL", "EMBEDDING_BASE_URL"); v != "" {
		cfg.EmbeddingBaseURL = v
	}
	if v := envFirstOf("ENGRAM_EMBEDDING_API_KEY", "EMBEDDING_API_KEY"); v != "" {
		cfg.EmbeddingAPIKey = v
	}
	if v := envFirstOf("ENGRAM_EMBEDDING_MODEL_NAME", "EMBEDDING_MODEL_NAME"); v != "" {
		cfg.EmbeddingModelName = v
	}
	if v := envFirstOf("ENGRAM_EMBEDDING_DIMENSIONS", "EMBEDDING_DIMENSIONS"); v != "" {
		if d, err := strconv.Atoi(v); err == nil && d > 0 {
			cfg.EmbeddingDimensions = d
		}
	}
	if v := strings.TrimSpace(os.Getenv("DATABASE_DSN")); v != "" {
		cfg.DatabaseDSN = v
	}
	if v := strings.TrimSpace(os.Getenv("DATABASE_MAX_CONNS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.DatabaseMaxConns = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("COLLECTION_CONFIG")); v != "" {
		cfg.CollectionConfigPath = v
	}
	if v := strings.TrimSpace(os.Getenv("SESSIONS_DIR")); v != "" {
		cfg.SessionsDir = v
	}
	if v := strings.TrimSpace(os.Getenv("WORKSTATION_ID")); v != "" {
		cfg.WorkstationID = v
	}

	return cfg, nil
}

// envFirstOf returns the first non-empty env var value from the given keys.
// Allows ENGRAM_-prefixed vars to take priority over legacy unprefixed vars.
func envFirstOf(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// splitTrim splits a comma-separated string and trims whitespace.
func splitTrim(s string) []string {
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// Get returns the global configuration, loading it if necessary.
func Get() *Config {
	configOnce.Do(func() {
		var err error
		globalConfig, err = Load()
		if err != nil {
			globalConfig = Default()
		}
	})

	configMu.RLock()
	defer configMu.RUnlock()
	return globalConfig
}

// GetWorkerPort returns the worker port from environment or config.
func GetWorkerPort() int {
	if port := os.Getenv("ENGRAM_WORKER_PORT"); port != "" {
		var p int
		if err := json.Unmarshal([]byte(port), &p); err == nil && p > 0 {
			return p
		}
	}
	return Get().WorkerPort
}

// GetWorkerHost returns the worker host from environment, config, or fallback.
func GetWorkerHost() string {
	host := strings.TrimSpace(os.Getenv("ENGRAM_WORKER_HOST"))
	if host != "" {
		return host
	}
	if cfgHost := strings.TrimSpace(Get().WorkerHost); cfgHost != "" {
		return cfgHost
	}
	return "127.0.0.1"
}

// GetWorkerToken returns the worker authentication token.
func GetWorkerToken() string {
	return strings.TrimSpace(os.Getenv("ENGRAM_API_TOKEN"))
}

// GetEmbeddingProvider returns the embedding provider ("builtin" or "openai").
func GetEmbeddingProvider() string {
	if v := envFirstOf("ENGRAM_EMBEDDING_PROVIDER", "EMBEDDING_PROVIDER"); v != "" {
		return v
	}
	return Get().EmbeddingProvider
}

// GetEmbeddingBaseURL returns the OpenAI-compatible API base URL.
func GetEmbeddingBaseURL() string {
	if v := envFirstOf("ENGRAM_EMBEDDING_BASE_URL", "EMBEDDING_BASE_URL"); v != "" {
		return v
	}
	return Get().EmbeddingBaseURL
}

// GetEmbeddingAPIKey returns the embedding API key (env-only, never from config file).
func GetEmbeddingAPIKey() string {
	return envFirstOf("ENGRAM_EMBEDDING_API_KEY", "EMBEDDING_API_KEY")
}

// GetDatabaseDSN returns the PostgreSQL DSN.
// env DATABASE_DSN takes priority (contains password, never stored in config file).
// Returns empty string if not configured.
func GetDatabaseDSN() string {
	if v := strings.TrimSpace(os.Getenv("DATABASE_DSN")); v != "" {
		return v
	}
	return Get().DatabaseDSN
}

// GetEmbeddingModelName returns the embedding model name for external providers.
func GetEmbeddingModelName() string {
	if v := envFirstOf("ENGRAM_EMBEDDING_MODEL_NAME", "EMBEDDING_MODEL_NAME"); v != "" {
		return v
	}
	if cfg := Get(); cfg.EmbeddingModelName != "" {
		return cfg.EmbeddingModelName
	}
	return "text-embedding-3-small"
}

// GetCollectionConfigPath returns the path to the collections YAML config.
// Falls back to ~/.config/engram/collections.yml if env is unset.
func GetCollectionConfigPath() string {
	if v := strings.TrimSpace(os.Getenv("COLLECTION_CONFIG")); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "engram", "collections.yml")
}

// GetSessionsDir returns the sessions directory.
// Falls back to ~/.claude/projects/ if not set.
func GetSessionsDir() string {
	if v := os.Getenv("SESSIONS_DIR"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "projects")
}

// GetWorkstationID returns the workstation identifier from environment.
// Returns empty string if not set; caller should fall back to sessions.WorkstationID().
func GetWorkstationID() string {
	return strings.TrimSpace(os.Getenv("WORKSTATION_ID"))
}

// GetEmbeddingDimensions returns the embedding vector dimensions for external providers.
func GetEmbeddingDimensions() int {
	if v := envFirstOf("ENGRAM_EMBEDDING_DIMENSIONS", "EMBEDDING_DIMENSIONS"); v != "" {
		if d, err := strconv.Atoi(v); err == nil && d > 0 {
			return d
		}
	}
	if cfg := Get(); cfg.EmbeddingDimensions > 0 {
		return cfg.EmbeddingDimensions
	}
	return 1536
}
