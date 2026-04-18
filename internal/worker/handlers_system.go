package worker

import (
	"net/http"
	"os"
)

// handleGetConfig returns the current runtime configuration, grouped by category.
// Secrets (API keys, DSN, encryption keys) are redacted.
func (s *Service) handleGetConfig(w http.ResponseWriter, _ *http.Request) {
	s.initMu.RLock()
	cfg := s.config
	s.initMu.RUnlock()

	if cfg == nil {
		http.Error(w, "config not available", http.StatusServiceUnavailable)
		return
	}

	response := map[string]any{
		"llm": map[string]any{
			"url":            os.Getenv("ENGRAM_LLM_URL"),
			"model":          os.Getenv("ENGRAM_LLM_MODEL"),
			"max_tokens":     cfg.LLMMaxTokens,
			"filter_enabled": cfg.LLMFilterEnabled,
			"filter_model":   cfg.LLMFilterModel,
		},
		"embedding": map[string]any{
			"provider":   cfg.EmbeddingProvider,
			"base_url":   cfg.EmbeddingBaseURL,
			"model":      cfg.EmbeddingModelName,
			"dimensions": cfg.EmbeddingDimensions,
		},
		"reranking": map[string]any{
			"enabled":    cfg.RerankingEnabled,
			"provider":   cfg.RerankingProvider,
			"api_url":    cfg.RerankingAPIBaseURL,
			"model":      cfg.RerankingAPIModel,
			"pure_mode":  cfg.RerankingPureMode,
			"timeout_ms": cfg.RerankingTimeoutMS,
			"batch_size": cfg.RerankingBatchSize,
			"candidates": cfg.RerankingCandidates,
			"results":    cfg.RerankingResults,
			"alpha":      cfg.RerankingAlpha,
		},
		"context": map[string]any{
			"observations":        cfg.ContextObservations,
			"max_tokens":          cfg.ContextMaxTokens,
			"session_count":       cfg.ContextSessionCount,
			"relevance_threshold": cfg.ContextRelevanceThreshold,
			"obs_types":           cfg.ContextObsTypes,
			"obs_concepts":        cfg.ContextObsConcepts,
			"show_work_tokens":    cfg.ContextShowWorkTokens,
			"show_read_tokens":    cfg.ContextShowReadTokens,
			"show_last_summary":   cfg.ContextShowLastSummary,
		},
		"search": map[string]any{
			"hyde_enabled":               cfg.HyDEEnabled,
			"hyde_url":                   cfg.HyDEAPIURL,
			"hyde_model":                 cfg.HyDEModel,
			"type_lanes_enabled":         cfg.TypeLanesEnabled,
			"query_expansion_timeout_ms": cfg.QueryExpansionTimeoutMS,
			"dedup_threshold":            cfg.DedupSimilarityThreshold,
			"clustering_threshold":       cfg.ClusteringThreshold,
		},
		"maintenance": map[string]any{
			"enabled":                 cfg.MaintenanceEnabled,
			"interval_hours":          cfg.MaintenanceIntervalHours,
			"retention_days":          cfg.ObservationRetentionDays,
			"cleanup_stale":           cfg.CleanupStaleObservations,
			"smart_gc_enabled":        cfg.SmartGCEnabled,
			"smart_gc_threshold":      cfg.SmartGCThreshold,
			"consolidation_enabled":   cfg.ConsolidationEnabled,
			"consolidation_threshold": cfg.ConsolidationThreshold,
		},
		"memory": map[string]any{
			"supersession_enabled":    cfg.SupersessionEnabled,
			"supersession_threshold":  cfg.SupersessionThreshold,
			"store_path_supersession": cfg.StorePathSupersessionEnabled,
			"write_merge_enabled":     cfg.WriteMergeEnabled,
			"contradiction_detection": cfg.ContradictionDetectionEnabled,
			"injection_floor":         cfg.InjectionFloor,
			"inject_unified":          cfg.InjectUnified,
			"always_inject_limit":     cfg.AlwaysInjectLimit,
			"project_inject_limit":    cfg.ProjectInjectLimit,
			"session_boost":           cfg.SessionBoost,
		},
		"storage": map[string]any{
			"vector_strategy":    cfg.VectorStorageStrategy,
			"database_max_conns": cfg.DatabaseMaxConns,
			"log_buffer_size":    cfg.LogBufferSize,
		},
		"features": map[string]any{
			"telemetry_enabled":      cfg.TelemetryEnabled,
			"enforce_source_project": cfg.EnforceSourceProject,
			"project_briefing":       cfg.ProjectBriefingEnabled,
			"entity_extraction":      cfg.EntityExtractionEnabled,
		},
	}

	writeJSON(w, response)
}
