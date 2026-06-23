package worker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	assert.False(t, response.Apply["supported"].(bool))
	assert.Equal(t, "PATCH /api/config", response.Apply["endpoint"])
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
