package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/auth"
	cognitivecore "github.com/thebtf/engram/internal/cognitive/core"
	"github.com/thebtf/engram/internal/config"
)

func TestHandleGetFlagsReturnsReadOnlyRuntimeSnapshot(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")
	t.Setenv("ENGRAM_GRAPH_ENABLED", "")
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")
	t.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")
	t.Setenv("ENGRAM_V7_S2_METAMEM", "true")

	cfg := config.Default()
	cfg.TelemetryEnabled = false
	cfg.EnforceSourceProject = true
	cfg.RuleGovernanceEnabled = true

	svc := &Service{config: cfg, flagConfig: cognitivecore.LoadFlagConfigFromEnv()}
	req := httptest.NewRequest(http.MethodGet, "/api/flags", nil)
	w := httptest.NewRecorder()

	svc.handleGetFlags(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var response runtimeFlagsResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	assert.True(t, response.ReadOnly)
	assert.True(t, response.Apply["supported"].(bool))
	assert.Equal(t, "PATCH /api/config", response.Apply["endpoint"])
	assert.Contains(t, response.Apply["fields"], "features.enforce_source_project")
	assert.Contains(t, response.Apply["fields"], "memory.inject_unified")
	assert.True(t, response.Flags["ENGRAM_VNEXT_ENABLED"])
	assert.False(t, response.Flags["ENGRAM_GRAPH_ENABLED"])
	assert.True(t, response.Flags["ENGRAM_CODE_INTEL_ENABLED"])
	assert.True(t, response.Flags["ENGRAM_V7_PLUG_ENABLED"])
	assert.False(t, response.Flags["ENGRAM_V7_S1_STATE"])
	assert.True(t, response.Flags["ENGRAM_V7_S2_METAMEM"])
	assert.NotContains(t, response.Flags, "ENGRAM_AUTH_SKIP_LOCAL")
	assert.NotContains(t, response.Flags, "ENGRAM_TELEMETRY_ENABLED")
	assert.True(t, response.Flags["ENGRAM_ENFORCE_SOURCE_PROJECT"])
	assert.True(t, response.Flags["ENGRAM_RULE_GOVERNANCE_ENABLED"])
	assert.Equal(t, len(response.Items), response.Summary["total"])
	assert.Positive(t, response.Summary["enabled"])
	assert.Positive(t, response.Summary["disabled"])

	var vnextItem runtimeFlagItem
	for _, item := range response.Items {
		if item.Name == "ENGRAM_VNEXT_ENABLED" {
			vnextItem = item
			break
		}
	}
	assert.Equal(t, "env", vnextItem.Source)
	assert.Equal(t, "vnext", vnextItem.Category)
	assert.True(t, vnextItem.RestartRequiredToChange)
}

func TestHandleGetFlags_ConfigUnavailable(t *testing.T) {
	svc := &Service{}
	req := httptest.NewRequest(http.MethodGet, "/api/flags", nil)
	w := httptest.NewRecorder()

	svc.handleGetFlags(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), "config not available")
}

func TestHandlePatchConfig_AdminAppliesAllowlistedSettings(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	t.Setenv("USERPROFILE", tempDir)
	require.NoError(t, config.SaveSettings(map[string]any{
		"ENGRAM_ENFORCE_SOURCE_PROJECT": true,
		"ENGRAM_INJECT_UNIFIED":         true,
	}))
	cfg, _, err := config.Reload()
	require.NoError(t, err)

	svc := &Service{config: cfg}
	body := bytes.NewBufferString(`{
		"features": {"enforce_source_project": false},
		"memory": {"inject_unified": false}
	}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/config", body).
		WithContext(auth.WithIdentity(context.Background(), auth.Admin()))
	w := httptest.NewRecorder()

	svc.handlePatchConfig(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var response map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	assert.Equal(t, true, response["success"])
	assert.Equal(t, true, response["applied"])
	assert.Equal(t, true, response["restart_required"])
	assert.Contains(t, response["changed"], "enforce_source_project")
	assert.Contains(t, response["changed"], "inject_unified (requires restart)")
	assert.Contains(t, response["restart_required_fields"], "memory.inject_unified")

	svc.initMu.RLock()
	require.NotNil(t, svc.config)
	assert.False(t, svc.config.EnforceSourceProject)
	assert.False(t, svc.config.InjectUnified)
	svc.initMu.RUnlock()
}

func TestHandlePatchConfig_RejectsEnvControlledSetting(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	t.Setenv("USERPROFILE", tempDir)
	t.Setenv("ENGRAM_ENFORCE_SOURCE_PROJECT", "true")
	cfg := config.Default()
	svc := &Service{config: cfg}
	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"features": {"enforce_source_project": false}
	}`)).WithContext(auth.WithIdentity(context.Background(), auth.Admin()))
	w := httptest.NewRecorder()

	svc.handlePatchConfig(w, req)

	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "features.enforce_source_project")
	assert.Contains(t, w.Body.String(), "ENGRAM_ENFORCE_SOURCE_PROJECT")
}

func TestHandlePatchConfig_AdminRequired(t *testing.T) {
	svc := &Service{config: config.Default()}
	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"features": {"enforce_source_project": false}
	}`))
	w := httptest.NewRecorder()

	svc.handlePatchConfig(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
}
