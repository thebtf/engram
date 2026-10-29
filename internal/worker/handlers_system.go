package worker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/config"
	dbgorm "github.com/thebtf/engram/internal/db/gorm"
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

type patchConfigRequest struct {
	Features *patchConfigFeatures `json:"features,omitempty"`
}

type patchConfigFeatures struct {
	EnforceSourceProject *bool `json:"enforce_source_project,omitempty"`
	TelemetryEnabled     *bool `json:"telemetry_enabled,omitempty"`
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

	writeJSON(w, buildConfigResponseWithLifecycle(cfg))
}

// handlePatchConfig applies the narrow operator-safe runtime settings allowlist.
// Env-controlled values are refused rather than reported as applied.
func (s *Service) handlePatchConfig(w http.ResponseWriter, r *http.Request) {
	if rejectNonAdmin(w, r) {
		return
	}

	var req patchConfigRequest
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	updates, unsupported, blocked := configSettingsUpdates(req)
	if len(unsupported) > 0 {
		http.Error(w, "unsupported config field: "+strings.Join(unsupported, ", "), http.StatusBadRequest)
		return
	}
	if len(blocked) > 0 {
		http.Error(w, "config field controlled by environment: "+strings.Join(blocked, ", "), http.StatusConflict)
		return
	}
	if len(updates) == 0 {
		http.Error(w, "no supported config changes", http.StatusBadRequest)
		return
	}

	beforeConfig := s.currentConfigResponse()
	if err := config.SaveSettings(updates); err != nil {
		http.Error(w, "save config failed", http.StatusInternalServerError)
		return
	}

	newCfg, changed, err := s.applyConfigReload()
	if err != nil {
		http.Error(w, "reload config failed", http.StatusInternalServerError)
		return
	}

	restartRequiredFields := configRestartRequiredFields(changed)
	afterConfig := buildConfigResponseWithLifecycle(newCfg)
	auditLogged := s.logConfigAudit(r.Context(), beforeConfig, afterConfig, updates, changed, restartRequiredFields)
	writeJSON(w, map[string]any{
		"success":                 true,
		"applied":                 true,
		"audit_logged":            auditLogged,
		"changed":                 changed,
		"restart_required":        len(restartRequiredFields) > 0,
		"restart_required_fields": restartRequiredFields,
		"config":                  afterConfig,
	})
}

func (s *Service) currentConfigResponse() map[string]any {
	s.initMu.RLock()
	cfg := s.config
	s.initMu.RUnlock()
	if cfg == nil {
		cfg = config.Get()
	}
	return buildConfigResponseWithLifecycle(cfg)
}

func (s *Service) logConfigAudit(ctx context.Context, beforeConfig map[string]any, afterConfig map[string]any, updates map[string]any, changed []string, restartRequiredFields []string) bool {
	s.initMu.RLock()
	auditStore := s.auditStore
	s.initMu.RUnlock()
	if auditStore == nil {
		return false
	}
	actor, sourceSessionID := configAuditIdentity(ctx)
	before := configAuditRaw(map[string]any{
		"config": beforeConfig,
	})
	after := configAuditRaw(map[string]any{
		"config":                  afterConfig,
		"requested_updates":       updates,
		"changed":                 changed,
		"restart_required":        len(restartRequiredFields) > 0,
		"restart_required_fields": restartRequiredFields,
	})
	auditCtx := context.WithoutCancel(ctx)
	if err := auditStore.Log(auditCtx, dbgorm.AuditLogEntry{
		Action:          "config.patch",
		Actor:           actor,
		SourceSessionID: sourceSessionID,
		BeforeState:     before,
		AfterState:      after,
		Reason:          "PATCH /api/config",
	}); err != nil {
		log.Error().Err(err).Str("actor", actor).Strs("changed", changed).Msg("config audit log failed")
		return false
	}
	return true
}

func configAuditIdentity(ctx context.Context) (actor string, sourceSessionID string) {
	id, ok := auth.IdentityFrom(ctx)
	if !ok {
		return "unauthenticated", ""
	}
	if principal, kind, ok := id.MemoryOwner(); ok {
		return string(id.Source) + ":" + kind + ":" + principal, id.WorkstationID()
	}
	if id.KeycardID != "" {
		return string(id.Source) + ":" + id.KeycardID, id.WorkstationID()
	}
	if id.Role != "" {
		return string(id.Source) + ":" + string(id.Role), id.WorkstationID()
	}
	if id.Source != "" {
		return string(id.Source), id.WorkstationID()
	}
	return "unknown", ""
}

func configAuditRaw(value any) *json.RawMessage {
	b, err := json.Marshal(value)
	if err != nil {
		log.Error().Err(err).Msg("config audit marshal failed")
		return nil
	}
	raw := json.RawMessage(b)
	return &raw
}

func buildConfigResponse(cfg *config.Config) map[string]any {
	return map[string]any{
		"context": map[string]any{
			"observations":        cfg.ContextObservations,
			"max_tokens":          cfg.ContextMaxTokens,
			"session_count":       cfg.ContextSessionCount,
			"relevance_threshold": cfg.ContextRelevanceThreshold,
			"obs_types":           cfg.ContextObsTypes,
			"obs_concepts":        cfg.ContextObsConcepts,
		},
		"memory": map[string]any{
			"inject_unified":       true,
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
}

func buildConfigResponseWithLifecycle(effective *config.Config) map[string]any {
	response := buildConfigResponse(effective)
	lifecycle := map[string]any{
		"restart_required": false,
		"pending_restart":  []map[string]any{},
		"apply": map[string]any{
			"supported": false,
			"reason":    "generic restart/apply endpoint is not available",
		},
	}
	desired, err := config.Load()
	if err != nil {
		lifecycle["error"] = err.Error()
		response["lifecycle"] = lifecycle
		return response
	}
	pending := configPendingRestart(effective, desired)
	lifecycle["restart_required"] = len(pending) > 0
	lifecycle["pending_restart"] = pending
	response["lifecycle"] = lifecycle
	return response
}

func configPendingRestart(effective *config.Config, desired *config.Config) []map[string]any {
	pending := make([]map[string]any, 0, 3)
	if effective.WorkerPort != desired.WorkerPort {
		pending = append(pending, map[string]any{
			"field":     "server.worker_port",
			"effective": effective.WorkerPort,
			"desired":   desired.WorkerPort,
			"reason":    "requires_restart",
		})
	}
	if effective.WorkerToken != desired.WorkerToken {
		pending = append(pending, map[string]any{
			"field":     "server.worker_token",
			"effective": "changed",
			"desired":   "changed",
			"reason":    "requires_restart",
			"sensitive": true,
		})
	}
	return pending
}

func configSettingsUpdates(req patchConfigRequest) (map[string]any, []string, []string) {
	updates := map[string]any{}
	unsupported := []string{}
	blocked := []string{}

	if req.Features != nil {
		addBoolSettingUpdate(updates, &blocked, "features.enforce_source_project", "ENGRAM_ENFORCE_SOURCE_PROJECT", req.Features.EnforceSourceProject)
		if req.Features.TelemetryEnabled != nil {
			unsupported = append(unsupported, "features.telemetry_enabled")
		}
	}

	return updates, unsupported, blocked
}

func addBoolSettingUpdate(updates map[string]any, blocked *[]string, path string, envKey string, value *bool) {
	if value == nil {
		return
	}
	if strings.TrimSpace(os.Getenv(envKey)) != "" {
		*blocked = append(*blocked, path+" via "+envKey)
		return
	}
	updates[envKey] = *value
}

func configRestartRequiredFields(changed []string) []string {
	fields := make([]string, 0, len(changed))
	for _, field := range changed {
		if !strings.Contains(field, "requires restart") {
			continue
		}
		fields = append(fields, field)
	}
	return fields
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

	writeJSON(w, buildRuntimeFlagsResponse(cfg))
}

// handleGetMigrations returns the applied database migration state from the
// actual gormigrate bookkeeping table. It is intentionally read-only.
func (s *Service) handleGetMigrations(w http.ResponseWriter, r *http.Request) {
	s.initMu.RLock()
	store := s.store
	s.initMu.RUnlock()

	if store == nil {
		http.Error(w, "database not available", http.StatusServiceUnavailable)
		return
	}

	state, err := store.GetMigrationState(r.Context())
	if err != nil {
		http.Error(w, "migration state unavailable", http.StatusInternalServerError)
		return
	}

	writeJSON(w, state)
}

func buildRuntimeFlagsResponse(cfg *config.Config) runtimeFlagsResponse {
	items := []runtimeFlagItem{
		resolvedRuntimeFlag("ENGRAM_VNEXT_ENABLED", "memory", true, false, "Built-in hybrid retrieval, purge, audit, and retention."),
		resolvedRuntimeFlag("ENGRAM_LIFECYCLE_ENABLED", "memory", true, false, "Built-in lifecycle promotion, demotion, and tier filtering."),
		resolvedRuntimeFlag("ENGRAM_VNEXT_F_ENABLED", "memory", true, false, "Built-in privacy, candidates, write-lint, governance, and redaction."),
		envRuntimeFlag("ENGRAM_TEMPORAL_TRUTH_ENABLED", "vnext", "Enables the bounded CR-011 temporal truth read surface."),
		resolvedRuntimeFlag("ENGRAM_ADAPTIVE_ENABLED", "memory", true, false, "Built-in adaptive segmentation and brief retrieval."),
		resolvedRuntimeFlag("ENGRAM_CRYSTALLIZATION_ENABLED", "memory", true, false, "Built-in crystallization; inference requires a configured LLM provider."),
		resolvedRuntimeFlag("ENGRAM_CODE_INTEL_ENABLED", "code-intel", config.CodeIntelEnabled(), true, "Codebase tools are enabled by default; explicit false stops them."),
	}
	items = append(items,
		configRuntimeFlag("ENGRAM_ENFORCE_SOURCE_PROJECT", "operations", cfg.EnforceSourceProject, false, "Enforces source/project scoping on store and recall paths."),
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
			"supported": true,
			"endpoint":  "PATCH /api/config",
			"fields":    []string{"features.enforce_source_project"},
			"reason":    "only allowlisted config-backed fields are writable; env-controlled flags remain read-only",
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
