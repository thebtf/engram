package mcp

// tools_settings_test.go — unit tests for the settings MCP tool (#259 CR-3).
//
// These cover the validation + authorization layer (admin gate on set/delete, required
// args, secret-key classification) without a live database — every assertion here fires
// before settingsStore() is reached. Full encrypt/store/redact round-trips against Postgres
// are DSN-gated and live in internal/db/gorm/settings_store_test.go.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustMarshalSettings(t *testing.T, m map[string]any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(m)
	require.NoError(t, err)
	return b
}

// noIdentityCtx is a bare context with no auth identity (mirrors auth disabled).
func noIdentityCtx() context.Context { return context.Background() }

// TestSettings_SetRequiresAdmin verifies set is rejected without admin identity:
// no identity (auth disabled → fail closed) and a read-write keycard both fail.
func TestSettings_SetRequiresAdmin(t *testing.T) {
	srv := NewServer(ServerOptions{Version: "test"})

	args := mustMarshalSettings(t, map[string]any{"action": "set", "key": "reranker.url", "value": "https://x"})

	_, err := srv.handleSettingsConsolidated(noIdentityCtx(), args)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "admin authorization required")

	_, err = srv.handleSettingsConsolidated(readWriteCtx(), args)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "admin authorization required")
}

// TestSettings_DeleteRequiresAdmin verifies delete is admin-gated the same way.
func TestSettings_DeleteRequiresAdmin(t *testing.T) {
	srv := NewServer(ServerOptions{Version: "test"})
	args := mustMarshalSettings(t, map[string]any{"action": "delete", "key": "reranker.url"})

	_, err := srv.handleSettingsConsolidated(readWriteCtx(), args)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "admin authorization required")
}

// TestSettings_SetMissingArgs verifies arg validation (after the admin gate passes).
func TestSettings_SetMissingArgs(t *testing.T) {
	srv := NewServer(ServerOptions{Version: "test"})

	// Missing key.
	_, err := srv.handleSettingsConsolidated(adminCtx(), mustMarshalSettings(t, map[string]any{"action": "set", "value": "v"}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "key is required")

	// Missing value.
	_, err = srv.handleSettingsConsolidated(adminCtx(), mustMarshalSettings(t, map[string]any{"action": "set", "key": "reranker.url"}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "value is required")
}

// TestSettings_UnknownAction verifies the router rejects unknown actions, and that a
// missing action is reported.
func TestSettings_UnknownAction(t *testing.T) {
	srv := NewServer(ServerOptions{Version: "test"})

	_, err := srv.handleSettingsConsolidated(adminCtx(), mustMarshalSettings(t, map[string]any{"action": "frobnicate"}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown settings action")

	_, err = srv.handleSettingsConsolidated(adminCtx(), mustMarshalSettings(t, map[string]any{}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "action required")
}

func TestSettings_StrictMutationInputFailsBeforeStore(t *testing.T) {
	for _, tc := range []struct {
		name  string
		raw   string
		field string
	}{
		{name: "wrong action type", raw: `{"action":7}`, field: "action"},
		{name: "null key", raw: `{"action":"set","key":null,"value":"v"}`, field: "key"},
		{name: "wrong key type", raw: `{"action":"set","key":7,"value":"v"}`, field: "key"},
		{name: "null value", raw: `{"action":"set","key":"strict.test","value":null}`, field: "value"},
		{name: "wrong value type", raw: `{"action":"set","key":"strict.test","value":7}`, field: "value"},
		{name: "null encrypt", raw: `{"action":"set","key":"strict.test","value":"v","encrypt":null}`, field: "encrypt"},
		{name: "string encrypt", raw: `{"action":"set","key":"strict.test","value":"v","encrypt":"false"}`, field: "encrypt"},
		{name: "numeric encrypt", raw: `{"action":"set","key":"strict.test","value":"v","encrypt":1}`, field: "encrypt"},
		{name: "wrong description type", raw: `{"action":"set","key":"strict.test","value":"v","description":true}`, field: "description"},
		{name: "wrong delete key type", raw: `{"action":"delete","key":9}`, field: "key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewServer(ServerOptions{Version: "strict-settings"})

			out, err := srv.handleSettingsConsolidated(adminCtx(), json.RawMessage(tc.raw))

			require.Error(t, err)
			assert.Empty(t, out)
			assert.Contains(t, err.Error(), tc.field)
			assert.NotContains(t, err.Error(), "settings store not available",
				"malformed present input must fail before resolving the durable store")
		})
	}
}

// TestIsSecretSettingKey pins the secret-classification convention: only keys ending in
// ".api_key" are secrets. URLs and model names are plaintext config.
func TestIsSecretSettingKey(t *testing.T) {
	secret := []string{"reranker.api_key", "embedder.api_key", "future.thing.api_key"}
	plain := []string{"reranker.url", "reranker.model", "embedder.url", "embedder.model", "api_key_prefix.url", "apikey"}

	for _, k := range secret {
		if !isSecretSettingKey(k) {
			t.Errorf("isSecretSettingKey(%q) = false, want true", k)
		}
	}
	for _, k := range plain {
		if isSecretSettingKey(k) {
			t.Errorf("isSecretSettingKey(%q) = true, want false", k)
		}
	}
}

// TestRequireAdmin verifies the gate: admin passes, read-write fails, no-identity fails closed.
func TestRequireAdmin(t *testing.T) {
	if err := requireAdmin(adminCtx(), "set"); err != nil {
		t.Errorf("requireAdmin(admin) = %v, want nil", err)
	}
	if err := requireAdmin(readWriteCtx(), "set"); err == nil {
		t.Error("requireAdmin(read-write) = nil, want error")
	}
	if err := requireAdmin(noIdentityCtx(), "delete"); err == nil {
		t.Error("requireAdmin(no identity) = nil, want error (fail closed)")
	}
}
