package worker

import (
	"net/http"
	"os"
	"strings"

	cognitivecore "github.com/thebtf/engram/internal/cognitive/core"
	"github.com/thebtf/engram/internal/config"
)

type runtimeFlagItem struct {
	Name                    string `json:"name"`
	Enabled                 bool   `json:"enabled"`
	Source                  string `json:"source"`
	Category                string `json:"category"`
	RestartRequiredToChange bool   `json:"restart_required_to_change"`
	Description             string `json:"description"`
}

type runtimeFlagsResponse struct {
	Flags    map[string]bool   `json:"flags"`
	Items    []runtimeFlagItem `json:"items"`
	Summary  map[string]int    `json:"summary"`
	ReadOnly bool              `json:"read_only"`
	Apply    map[string]any    `json:"apply"`
	Config   map[string]any    `json:"config,omitempty"`
}

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
		"context": map[string]any{
			"observations":        cfg.ContextObservations,
			"max_tokens":          cfg.ContextMaxTokens,
			"session_count":       cfg.ContextSessionCount,
			"relevance_threshold": cfg.ContextRelevanceThreshold,
			"obs_types":           cfg.ContextObsTypes,
			"obs_concepts":        cfg.ContextObsConcepts,
		},
		"memory": map[string]any{
			"inject_unified":       cfg.InjectUnified,
			"always_inject_limit":  cfg.AlwaysInjectLimit,
			"project_inject_limit": cfg.ProjectInjectLimit,
		},
		"storage": map[string]any{
			"vector_strategy":    cfg.VectorStorageStrategy,
			"database_max_conns": cfg.DatabaseMaxConns,
			"log_buffer_size":    cfg.LogBufferSize,
		},
		"features": map[string]any{
			"telemetry_enabled":      cfg.TelemetryEnabled,
			"enforce_source_project": cfg.EnforceSourceProject,
		},
	}

	writeJSON(w, response)
}

// handleGetFlags returns a read-only snapshot of runtime feature flags.
// The snapshot is intentionally not an apply surface: most subsystem gates are
// process-start env flags, so changing them without an explicit restart receipt
// would produce fake-operable UI.
func (s *Service) handleGetFlags(w http.ResponseWriter, _ *http.Request) {
	s.initMu.RLock()
	cfg := s.config
	s.initMu.RUnlock()

	if cfg == nil {
		http.Error(w, "config not available", http.StatusServiceUnavailable)
		return
	}

	writeJSON(w, buildRuntimeFlagsResponse(cfg, s.flagConfig))
}

func buildRuntimeFlagsResponse(cfg *config.Config, flagCfg cognitivecore.FlagConfig) runtimeFlagsResponse {
	items := []runtimeFlagItem{
		envRuntimeFlag("ENGRAM_VNEXT_ENABLED", "vnext", "Master vNext gate for hybrid retrieval, purge, audit, and retention paths."),
		envRuntimeFlag("ENGRAM_LIFECYCLE_ENABLED", "vnext", "Enables lifecycle tier promotion/demotion and tier_filter runtime behavior."),
		envRuntimeFlag("ENGRAM_VNEXT_F_ENABLED", "vnext", "Milestone F gate for privacy scope, taxonomy, candidates, governance, and redaction paths."),
		envRuntimeFlag("ENGRAM_GRAPH_ENABLED", "vnext", "Enables the PostgreSQL-backed knowledge graph subsystem."),
		envRuntimeFlag("ENGRAM_ADAPTIVE_ENABLED", "vnext", "Enables adaptive memory segmentation and adaptive brief retrieval."),
		envRuntimeFlag("ENGRAM_CRYSTALLIZATION_ENABLED", "vnext", "Enables the LLM-backed crystallization dream cycle."),
		envRuntimeFlag("ENGRAM_CODE_INTEL_ENABLED", "code-intel", "Enables codebase_index, codebase_status, and codebase_search tools."),
	}
	items = append(items, v7RuntimeFlags(flagCfg)...)
	items = append(items,
		configRuntimeFlag("ENGRAM_AUTHENTIK_ENABLED", "auth", cfg.AuthentikEnabled, true, "Enables Authentik forward-auth header integration."),
		configRuntimeFlag("ENGRAM_AUTHENTIK_AUTO_PROVISION", "auth", cfg.AuthentikAutoProvision, true, "Auto-provisions users from trusted Authentik headers."),
		configRuntimeFlag("ENGRAM_ENFORCE_SOURCE_PROJECT", "operations", cfg.EnforceSourceProject, false, "Enforces source/project scoping on store and recall paths."),
		configRuntimeFlag("ENGRAM_INJECT_UNIFIED", "memory", cfg.InjectUnified, true, "Routes session injection through the unified retrieval path."),
		configRuntimeFlag("ENGRAM_RULE_GOVERNANCE_ENABLED", "rules", cfg.RuleGovernanceEnabled, false, "Routes explicit active-rule intents into rule candidates."),
		configRuntimeFlag("ENGRAM_RULE_ARBITER_ENABLED", "rules", cfg.RuleArbiterEnabled, false, "Starts the bounded proposal-only rule arbiter when governance is enabled."),
		configRuntimeFlag("ENGRAM_RULE_ROUTER_ENABLED", "rules", cfg.RuleRouterEnabled, false, "Switches session-start/context reads to the bounded rule injection router."),
	)

	flags := make(map[string]bool, len(items))
	enabled := 0
	for _, item := range items {
		flags[item.Name] = item.Enabled
		if item.Enabled {
			enabled++
		}
	}

	return runtimeFlagsResponse{
		Flags: flags,
		Items: items,
		Summary: map[string]int{
			"total":    len(items),
			"enabled":  enabled,
			"disabled": len(items) - enabled,
		},
		ReadOnly: true,
		Apply: map[string]any{
			"supported": false,
			"endpoint":  "PATCH /api/config",
			"reason":    "runtime flag mutation needs a settings save endpoint plus restart-required receipt",
		},
		Config: map[string]any{
			"source": "current process snapshot",
		},
	}
}

func envRuntimeFlag(name, category, description string) runtimeFlagItem {
	return runtimeFlagItem{
		Name:                    name,
		Enabled:                 strings.TrimSpace(os.Getenv(name)) == "true",
		Source:                  "env",
		Category:                category,
		RestartRequiredToChange: true,
		Description:             description,
	}
}

func v7RuntimeFlags(flagCfg cognitivecore.FlagConfig) []runtimeFlagItem {
	return []runtimeFlagItem{
		resolvedRuntimeFlag("ENGRAM_V7_PLUG_ENABLED", "v7", flagCfg.IsPlugEnabled(), true, "Master v7 cognitive substrate gate resolved during service startup."),
		resolvedRuntimeFlag("ENGRAM_V7_S1_STATE", "v7", flagCfg.IsSubsystemEnabled("s1"), true, "Effective state for the v7 S1 state subsystem."),
		resolvedRuntimeFlag("ENGRAM_V7_S2_METAMEM", "v7", flagCfg.IsSubsystemEnabled("s2"), true, "Effective state for the v7 S2 metamemory subsystem."),
		resolvedRuntimeFlag("ENGRAM_V7_S3_AMBIENT", "v7", flagCfg.IsSubsystemEnabled("s3"), true, "Effective state for the v7 S3 ambient subsystem."),
		resolvedRuntimeFlag("ENGRAM_V7_S4A_DIRECTIVES_CAPTURE", "v7", flagCfg.IsSubsystemEnabled("s4a"), true, "Effective state for the v7 S4a directive capture subsystem."),
		resolvedRuntimeFlag("ENGRAM_V7_S4B_DIRECTIVES_SURFACING", "v7", flagCfg.IsSubsystemEnabled("s4b"), true, "Effective state for the v7 S4b directive surfacing subsystem."),
		resolvedRuntimeFlag("ENGRAM_V7_S5_TELEMETRY", "v7", flagCfg.IsSubsystemEnabled("s5"), true, "Effective state for the v7 S5 product telemetry subsystem."),
		resolvedRuntimeFlag("ENGRAM_V7_S6_OUTCOME", "v7", flagCfg.IsSubsystemEnabled("s6"), true, "Effective state for the v7 S6 outcome subsystem."),
	}
}

func resolvedRuntimeFlag(name, category string, enabled bool, restartRequired bool, description string) runtimeFlagItem {
	return runtimeFlagItem{
		Name:                    name,
		Enabled:                 enabled,
		Source:                  "runtime",
		Category:                category,
		RestartRequiredToChange: restartRequired,
		Description:             description,
	}
}

func configRuntimeFlag(name, category string, enabled bool, restartRequired bool, description string) runtimeFlagItem {
	return runtimeFlagItem{
		Name:                    name,
		Enabled:                 enabled,
		Source:                  "config",
		Category:                category,
		RestartRequiredToChange: restartRequired,
		Description:             description,
	}
}
