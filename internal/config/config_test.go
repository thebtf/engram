// Package config provides configuration management for engram.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

// ConfigSuite is a test suite for config operations.
type ConfigSuite struct {
	suite.Suite
	tempDir     string
	origHomeDir string
}

func (s *ConfigSuite) SetupTest() {
	var err error
	s.tempDir, err = os.MkdirTemp("", "config-test-*")
	s.Require().NoError(err)

	// Save and override HOME (+ USERPROFILE for Windows where os.UserHomeDir reads USERPROFILE)
	s.origHomeDir = os.Getenv("HOME")
	os.Setenv("HOME", s.tempDir)
	os.Setenv("USERPROFILE", s.tempDir)
}

func (s *ConfigSuite) TearDownTest() {
	os.Setenv("HOME", s.origHomeDir)
	os.Setenv("USERPROFILE", s.origHomeDir)
	os.RemoveAll(s.tempDir)
}

func TestConfigSuite(t *testing.T) {
	suite.Run(t, new(ConfigSuite))
}

// TestDefault verifies default configuration values.
func (s *ConfigSuite) TestDefault() {
	cfg := Default()

	s.Equal(DefaultWorkerPort, cfg.WorkerPort)
	s.Equal(DefaultModel, cfg.Model)
	s.Equal(4, cfg.MaxConns)
	s.Equal(100, cfg.ContextObservations)
	s.Equal(25, cfg.ContextFullCount)
	s.Equal(10, cfg.ContextSessionCount)
	s.Equal("narrative", cfg.ContextFullField)
	s.Equal(DefaultObservationTypes, cfg.ContextObsTypes)
	s.Equal(DefaultObservationConcepts, cfg.ContextObsConcepts)
}

// TestDefault_HubStorageStrategy verifies hub storage strategy default.
func (s *ConfigSuite) TestDefault_HubStorageStrategy() {
	cfg := Default()
	s.Equal("hub", cfg.VectorStorageStrategy)
	s.Equal(5, cfg.HubThreshold)
}

// TestDefault_MemoryLimits verifies memory limit defaults.
func (s *ConfigSuite) TestDefault_MemoryLimits() {
	cfg := Default()
	s.Equal(10000, cfg.StoreMemoryHardLimit)
	s.Equal(1000, cfg.StoreMemorySoftLimit)
	s.InDelta(0.92, cfg.StoreMemoryDedupThreshold, 1e-9)
}

// TestDefault_SignalWeights verifies that default signal weights are populated.
func (s *ConfigSuite) TestDefault_SignalWeights() {
	cfg := Default()
	s.NotEmpty(cfg.SignalWeights)
	s.InDelta(1.0, cfg.SignalWeights["git_commit"], 1e-9)
	s.InDelta(3.0, cfg.SignalWeights["pr_merged"], 1e-9)
	s.InDelta(-0.5, cfg.SignalWeights["error_streak"], 1e-9)
}

// TestDefault_InjectUnifiedTrue verifies that InjectUnified defaults to true (FR-3).
func (s *ConfigSuite) TestDefault_InjectUnifiedTrue() {
	cfg := Default()
	s.True(cfg.InjectUnified, "InjectUnified must default to true so the unified inject path is active")
}

// TestDefault_EnforceSourceProjectTrue verifies that EnforceSourceProject defaults to true (T010).
func (s *ConfigSuite) TestDefault_EnforceSourceProjectTrue() {
	cfg := Default()
	s.True(cfg.EnforceSourceProject, "EnforceSourceProject must default to true")
}

// TestDefault_InjectLimits verifies inject limit defaults.
func (s *ConfigSuite) TestDefault_InjectLimits() {
	cfg := Default()
	s.Equal(20, cfg.AlwaysInjectLimit)
	s.Equal(15, cfg.ProjectInjectLimit)
}

// TestDefault_OutcomeRecorder verifies outcome recorder interval default.
func (s *ConfigSuite) TestDefault_OutcomeRecorder() {
	cfg := Default()
	s.Equal(15, cfg.OutcomeRecorderIntervalMinutes)
}

// TestDefault_TelemetryEnabled verifies telemetry is on by default.
func (s *ConfigSuite) TestDefault_TelemetryEnabled() {
	cfg := Default()
	s.True(cfg.TelemetryEnabled)
}

// TestDefault_ContextMaxTokens verifies context max tokens default.
func (s *ConfigSuite) TestDefault_ContextMaxTokens() {
	cfg := Default()
	s.Equal(8000, cfg.ContextMaxTokens)
}

// TestInjectUnifiedDefaultTrue verifies that ENGRAM_INJECT_UNIFIED defaults to true (FR-3).
func (s *ConfigSuite) TestInjectUnifiedDefaultTrue() {
	cfg, err := Load()
	s.Require().NoError(err)
	s.True(cfg.InjectUnified, "InjectUnified must default to true so the unified inject path is active")
}

// TestInjectUnifiedEnvOverride verifies that ENGRAM_INJECT_UNIFIED=false enables the emergency rollback path.
func (s *ConfigSuite) TestInjectUnifiedEnvOverride() {
	s.T().Setenv("ENGRAM_INJECT_UNIFIED", "false")
	cfg, err := Load()
	s.Require().NoError(err)
	s.False(cfg.InjectUnified, "ENGRAM_INJECT_UNIFIED=false must activate the legacy inject path")
}

// TestInjectUnifiedEnvOverrideTrue verifies that ENGRAM_INJECT_UNIFIED=true is accepted.
func (s *ConfigSuite) TestInjectUnifiedEnvOverrideTrue() {
	s.T().Setenv("ENGRAM_INJECT_UNIFIED", "true")
	cfg, err := Load()
	s.Require().NoError(err)
	s.True(cfg.InjectUnified)
}

// TestDataDir verifies data directory path contains .engram.
func (s *ConfigSuite) TestDataDir() {
	dir := DataDir()
	s.Contains(dir, ".engram")
}

// TestDBPath verifies database path contains engram.db.
func (s *ConfigSuite) TestDBPath() {
	path := DBPath()
	s.Contains(path, "engram.db")
}

// TestDBPath_EnvOverride verifies ENGRAM_DB_PATH env overrides the default.
func (s *ConfigSuite) TestDBPath_EnvOverride() {
	s.T().Setenv("ENGRAM_DB_PATH", "/custom/path/engram.db")
	path := DBPath()
	s.Equal("/custom/path/engram.db", path)
}

// TestSettingsPath verifies settings file path contains settings.json.
func (s *ConfigSuite) TestSettingsPath() {
	path := SettingsPath()
	s.Contains(path, "settings.json")
}

// TestEnsureDataDir verifies data directory creation.
func (s *ConfigSuite) TestEnsureDataDir() {
	err := EnsureDataDir()
	s.NoError(err)

	dir := DataDir()
	info, err := os.Stat(dir)
	s.NoError(err)
	s.True(info.IsDir())
}

// TestEnsureSettings verifies settings file creation, idempotency.
func (s *ConfigSuite) TestEnsureSettings() {
	err := EnsureDataDir()
	s.NoError(err)

	err = EnsureSettings()
	s.NoError(err)

	path := SettingsPath()
	info, err := os.Stat(path)
	s.NoError(err)
	s.False(info.IsDir())

	// Second call must not error (idempotent).
	err = EnsureSettings()
	s.NoError(err)
}

// TestDefault_TranscriptRetentionDays verifies that TranscriptRetentionDays
// defaults to 0 (no pruning of unprocessed rows — semantics enforced in
// TranscriptStore.PruneUnprocessedOlderThan).
func (s *ConfigSuite) TestDefault_TranscriptRetentionDays() {
	cfg := Default()
	s.Equal(0, cfg.TranscriptRetentionDays,
		"TranscriptRetentionDays must default to 0 (no-prune sentinel)")
}

func (s *ConfigSuite) TestDefault_RuleArbiterDefaults() {
	cfg := Default()
	s.False(cfg.RuleGovernanceEnabled)
	s.False(cfg.RuleArbiterEnabled)
	s.Equal(20, cfg.RuleArbiterBatchLimit)
	s.Equal(8000, cfg.RuleArbiterTimeoutMS)
	s.Equal(300, cfg.RuleArbiterIntervalSeconds)
}

func (s *ConfigSuite) TestRuleArbiterEnvOverrides() {
	s.T().Setenv("ENGRAM_RULE_GOVERNANCE_ENABLED", "true")
	s.T().Setenv("ENGRAM_RULE_ARBITER_ENABLED", "1")
	s.T().Setenv("ENGRAM_RULE_ARBITER_BATCH_LIMIT", "7")
	s.T().Setenv("ENGRAM_RULE_ARBITER_TIMEOUT_MS", "1500")
	s.T().Setenv("ENGRAM_RULE_ARBITER_INTERVAL_SECONDS", "42")

	cfg, err := Load()
	s.Require().NoError(err)
	s.True(cfg.RuleGovernanceEnabled)
	s.True(cfg.RuleArbiterEnabled)
	s.Equal(7, cfg.RuleArbiterBatchLimit)
	s.Equal(1500, cfg.RuleArbiterTimeoutMS)
	s.Equal(42, cfg.RuleArbiterIntervalSeconds)
}

func (s *ConfigSuite) TestRuleArbiterInvalidNumericEnvFallsBack() {
	s.T().Setenv("ENGRAM_RULE_ARBITER_BATCH_LIMIT", "-1")
	s.T().Setenv("ENGRAM_RULE_ARBITER_TIMEOUT_MS", "not-a-number")
	s.T().Setenv("ENGRAM_RULE_ARBITER_INTERVAL_SECONDS", "0")

	cfg, err := Load()
	s.Require().NoError(err)
	s.Equal(20, cfg.RuleArbiterBatchLimit)
	s.Equal(8000, cfg.RuleArbiterTimeoutMS)
	s.Equal(300, cfg.RuleArbiterIntervalSeconds)
}

// TestTranscriptRetentionDays_EnvParse verifies that
// ENGRAM_TRANSCRIPT_RETENTION_DAYS is parsed to the Config int field.
func (s *ConfigSuite) TestTranscriptRetentionDays_EnvParse() {
	s.T().Setenv("ENGRAM_TRANSCRIPT_RETENTION_DAYS", "30")
	cfg, err := Load()
	s.Require().NoError(err)
	s.Equal(30, cfg.TranscriptRetentionDays,
		"ENGRAM_TRANSCRIPT_RETENTION_DAYS=30 must set TranscriptRetentionDays to 30")
}

// TestTranscriptRetentionDays_EnvDefault verifies that an unset env var
// leaves TranscriptRetentionDays at 0.
func (s *ConfigSuite) TestTranscriptRetentionDays_EnvDefault() {
	// Ensure the env var is absent.
	s.T().Setenv("ENGRAM_TRANSCRIPT_RETENTION_DAYS", "")
	cfg, err := Load()
	s.Require().NoError(err)
	s.Equal(0, cfg.TranscriptRetentionDays,
		"unset ENGRAM_TRANSCRIPT_RETENTION_DAYS must leave TranscriptRetentionDays at 0")
}

// TestEnsureAll verifies full initialization.
func (s *ConfigSuite) TestEnsureAll() {
	err := EnsureAll()
	s.NoError(err)

	_, err = os.Stat(DataDir())
	s.NoError(err)
	_, err = os.Stat(SettingsPath())
	s.NoError(err)
}

// TestLoad_TableDriven tests configuration loading with various JSON scenarios.
func (s *ConfigSuite) TestLoad_TableDriven() {
	tests := []struct {
		name           string
		settingsJSON   string
		expectedModel  string
		expectedPort   int
		expectedObsObs int
	}{
		{
			name:           "no settings file",
			settingsJSON:   "",
			expectedPort:   DefaultWorkerPort,
			expectedModel:  DefaultModel,
			expectedObsObs: 100,
		},
		{
			name:           "custom port",
			settingsJSON:   `{"ENGRAM_WORKER_PORT": 38888}`,
			expectedPort:   38888,
			expectedModel:  DefaultModel,
			expectedObsObs: 100,
		},
		{
			name:           "custom model",
			settingsJSON:   `{"ENGRAM_MODEL": "sonnet"}`,
			expectedPort:   DefaultWorkerPort,
			expectedModel:  "sonnet",
			expectedObsObs: 100,
		},
		{
			name:           "custom observations",
			settingsJSON:   `{"ENGRAM_CONTEXT_OBSERVATIONS": 200}`,
			expectedPort:   DefaultWorkerPort,
			expectedModel:  DefaultModel,
			expectedObsObs: 200,
		},
		{
			name:           "multiple settings",
			settingsJSON:   `{"ENGRAM_WORKER_PORT": 39999, "ENGRAM_MODEL": "opus", "ENGRAM_CONTEXT_OBSERVATIONS": 50}`,
			expectedPort:   39999,
			expectedModel:  "opus",
			expectedObsObs: 50,
		},
		{
			name:           "invalid JSON returns defaults",
			settingsJSON:   `{invalid}`,
			expectedPort:   DefaultWorkerPort,
			expectedModel:  DefaultModel,
			expectedObsObs: 100,
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			tempDir, err := os.MkdirTemp("", "config-test-*")
			s.Require().NoError(err)
			defer os.RemoveAll(tempDir)

			os.Setenv("HOME", tempDir)
			os.Setenv("USERPROFILE", tempDir)

			err = os.MkdirAll(filepath.Join(tempDir, ".engram"), 0750)
			s.Require().NoError(err)

			if tt.settingsJSON != "" {
				writeErr := os.WriteFile(
					filepath.Join(tempDir, ".engram", "settings.json"),
					[]byte(tt.settingsJSON),
					0600,
				)
				s.Require().NoError(writeErr)
			}

			cfg, err := Load()
			s.NoError(err)
			s.NotNil(cfg)
			s.Equal(tt.expectedPort, cfg.WorkerPort)
			s.Equal(tt.expectedModel, cfg.Model)
			s.Equal(tt.expectedObsObs, cfg.ContextObservations)
		})
	}
}

// TestLoad_JSONContextSettings tests JSON-based context settings.
func (s *ConfigSuite) TestLoad_JSONContextSettings() {
	tempDir, err := os.MkdirTemp("", "config-test-ctx-*")
	s.Require().NoError(err)
	defer os.RemoveAll(tempDir)

	os.Setenv("HOME", tempDir)
	os.Setenv("USERPROFILE", tempDir)

	err = os.MkdirAll(filepath.Join(tempDir, ".engram"), 0750)
	s.Require().NoError(err)

	settingsJSON := `{
		"ENGRAM_CONTEXT_FULL_COUNT": 50,
		"ENGRAM_CONTEXT_SESSION_COUNT": 20,
		"ENGRAM_CONTEXT_OBS_TYPES": "bugfix,feature",
		"ENGRAM_CONTEXT_OBS_CONCEPTS": "security,performance",
		"ENGRAM_CONTEXT_RELEVANCE_THRESHOLD": 0.5,
		"ENGRAM_CONTEXT_MAX_PROMPT_RESULTS": 15,
		"ENGRAM_VECTOR_STORAGE_STRATEGY": "flat",
		"ENGRAM_HUB_THRESHOLD": 10,
		"ENGRAM_ENFORCE_SOURCE_PROJECT": false
	}`
	err = os.WriteFile(filepath.Join(tempDir, ".engram", "settings.json"), []byte(settingsJSON), 0600)
	s.Require().NoError(err)

	cfg, err := Load()
	s.Require().NoError(err)
	s.Equal(50, cfg.ContextFullCount)
	s.Equal(20, cfg.ContextSessionCount)
	s.Equal([]string{"bugfix", "feature"}, cfg.ContextObsTypes)
	s.Equal([]string{"security", "performance"}, cfg.ContextObsConcepts)
	s.InDelta(0.5, cfg.ContextRelevanceThreshold, 1e-9)
	s.Equal(15, cfg.ContextMaxPromptResults)
	s.Equal("flat", cfg.VectorStorageStrategy)
	s.Equal(10, cfg.HubThreshold)
	s.False(cfg.EnforceSourceProject)
}

func (s *ConfigSuite) TestSaveSettings_MergesOperatorUpdates() {
	tempDir, err := os.MkdirTemp("", "config-save-settings-*")
	s.Require().NoError(err)
	defer os.RemoveAll(tempDir)

	s.T().Setenv("HOME", tempDir)
	s.T().Setenv("USERPROFILE", tempDir)
	err = os.MkdirAll(filepath.Join(tempDir, ".engram"), 0750)
	s.Require().NoError(err)

	err = os.WriteFile(filepath.Join(tempDir, ".engram", "settings.json"), []byte(`{"ENGRAM_MODEL":"haiku"}`), 0600)
	s.Require().NoError(err)

	err = SaveSettings(map[string]any{
		"ENGRAM_ENFORCE_SOURCE_PROJECT": false,
		"ENGRAM_INJECT_UNIFIED":         false,
	})
	s.Require().NoError(err)

	cfg, err := Load()
	s.Require().NoError(err)
	s.Equal("haiku", cfg.Model)
	s.False(cfg.EnforceSourceProject)
	s.False(cfg.InjectUnified)
}

func (s *ConfigSuite) TestSaveSettings_SerializesConcurrentUpdates() {
	tempDir, err := os.MkdirTemp("", "config-save-settings-concurrent-*")
	s.Require().NoError(err)
	defer os.RemoveAll(tempDir)

	os.Setenv("HOME", tempDir)
	os.Setenv("USERPROFILE", tempDir)
	err = os.MkdirAll(filepath.Join(tempDir, ".engram"), 0750)
	s.Require().NoError(err)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, updates := range []map[string]any{
		{"ENGRAM_ENFORCE_SOURCE_PROJECT": false},
		{"ENGRAM_INJECT_UNIFIED": false},
	} {
		wg.Add(1)
		go func(updates map[string]any) {
			defer wg.Done()
			errs <- SaveSettings(updates)
		}(updates)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		s.Require().NoError(err)
	}

	data, err := os.ReadFile(filepath.Join(tempDir, ".engram", "settings.json"))
	s.Require().NoError(err)
	settings := map[string]any{}
	s.Require().NoError(json.Unmarshal(data, &settings))
	s.Equal(false, settings["ENGRAM_ENFORCE_SOURCE_PROJECT"])
	s.Equal(false, settings["ENGRAM_INJECT_UNIFIED"])

	cfg, err := Load()
	s.Require().NoError(err)
	s.False(cfg.EnforceSourceProject)
	s.False(cfg.InjectUnified)
}

func (s *ConfigSuite) TestReload_DetectsOperatorSettingsChanges() {
	tempDir, err := os.MkdirTemp("", "config-reload-operator-*")
	s.Require().NoError(err)
	defer os.RemoveAll(tempDir)

	s.T().Setenv("HOME", tempDir)
	s.T().Setenv("USERPROFILE", tempDir)
	err = os.MkdirAll(filepath.Join(tempDir, ".engram"), 0750)
	s.Require().NoError(err)

	_, _, err = Reload()
	s.Require().NoError(err)

	err = SaveSettings(map[string]any{
		"ENGRAM_ENFORCE_SOURCE_PROJECT": false,
		"ENGRAM_INJECT_UNIFIED":         false,
	})
	s.Require().NoError(err)

	cfg, changed, err := Reload()
	s.Require().NoError(err)
	s.False(cfg.EnforceSourceProject)
	s.True(cfg.InjectUnified)
	s.Contains(changed, "enforce_source_project")
	s.Contains(changed, "inject_unified (requires restart)")

	persisted, err := Load()
	s.Require().NoError(err)
	s.False(persisted.InjectUnified)
}

// TestLoad_DBPathFromJSON verifies ENGRAM_DB_PATH in JSON settings is applied.
func (s *ConfigSuite) TestLoad_DBPathFromJSON() {
	tempDir, err := os.MkdirTemp("", "config-test-dbpath-*")
	s.Require().NoError(err)
	defer os.RemoveAll(tempDir)

	os.Setenv("HOME", tempDir)
	os.Setenv("USERPROFILE", tempDir)
	err = os.MkdirAll(filepath.Join(tempDir, ".engram"), 0750)
	s.Require().NoError(err)

	customPath := "/custom/db/path.db"
	settingsJSON := `{"ENGRAM_DB_PATH": "` + customPath + `"}`
	err = os.WriteFile(filepath.Join(tempDir, ".engram", "settings.json"), []byte(settingsJSON), 0600)
	s.Require().NoError(err)

	cfg, err := Load()
	s.Require().NoError(err)
	s.Equal(customPath, cfg.DBPath)
}

// TestLoad_EnvOverrides verifies environment variable overrides take effect.
func (s *ConfigSuite) TestLoad_EnvOverrides() {
	// Create a temp dir with a settings file setting some values
	tempDir, err := os.MkdirTemp("", "config-env-override-*")
	s.Require().NoError(err)
	defer os.RemoveAll(tempDir)

	os.Setenv("HOME", tempDir)
	os.Setenv("USERPROFILE", tempDir)
	err = os.MkdirAll(filepath.Join(tempDir, ".engram"), 0750)
	s.Require().NoError(err)

	// Env vars override JSON settings
	s.T().Setenv("ENGRAM_DB_PATH", "/env-override/engram.db")
	s.T().Setenv("ENGRAM_WORKER_HOST", "10.0.0.1")
	s.T().Setenv("ENGRAM_AUTH_ADMIN_TOKEN", "secret-token")
	s.T().Setenv("ENGRAM_CONTEXT_MAX_TOKENS", "4000")
	s.T().Setenv("DATABASE_DSN", "postgres://localhost/test")
	s.T().Setenv("DATABASE_MAX_CONNS", "20")
	s.T().Setenv("WORKSTATION_ID", "ws-001")

	cfg, err := Load()
	s.Require().NoError(err)

	s.Equal("/env-override/engram.db", cfg.DBPath)
	s.Equal("10.0.0.1", cfg.WorkerHost)
	s.Equal("secret-token", cfg.WorkerToken)
	s.Equal(4000, cfg.ContextMaxTokens)
	s.Equal("postgres://localhost/test", cfg.DatabaseDSN)
	s.Equal(20, cfg.DatabaseMaxConns)
	s.Equal("ws-001", cfg.WorkstationID)
}

// TestLoad_EnvOverrides_Limits verifies inject limit env overrides.
func (s *ConfigSuite) TestLoad_EnvOverrides_Limits() {
	tempDir, err := os.MkdirTemp("", "config-env-limits-*")
	s.Require().NoError(err)
	defer os.RemoveAll(tempDir)

	os.Setenv("HOME", tempDir)
	os.Setenv("USERPROFILE", tempDir)
	err = os.MkdirAll(filepath.Join(tempDir, ".engram"), 0750)
	s.Require().NoError(err)

	s.T().Setenv("ENGRAM_ALWAYS_INJECT_LIMIT", "30")
	s.T().Setenv("ENGRAM_PROJECT_INJECT_LIMIT", "25")
	s.T().Setenv("ENGRAM_OUTCOME_RECORDER_INTERVAL_MINUTES", "5")

	cfg, err := Load()
	s.Require().NoError(err)
	s.Equal(30, cfg.AlwaysInjectLimit)
	s.Equal(25, cfg.ProjectInjectLimit)
	s.Equal(5, cfg.OutcomeRecorderIntervalMinutes)
}

// TestLoad_Telemetry verifies ENGRAM_TELEMETRY_ENABLED=false turns off telemetry.
func (s *ConfigSuite) TestLoad_Telemetry() {
	tempDir, err := os.MkdirTemp("", "config-telemetry-*")
	s.Require().NoError(err)
	defer os.RemoveAll(tempDir)

	os.Setenv("HOME", tempDir)
	os.Setenv("USERPROFILE", tempDir)
	err = os.MkdirAll(filepath.Join(tempDir, ".engram"), 0750)
	s.Require().NoError(err)

	s.T().Setenv("ENGRAM_TELEMETRY_ENABLED", "false")
	cfg, err := Load()
	s.Require().NoError(err)
	s.False(cfg.TelemetryEnabled)

	// "0" also disables
	s.T().Setenv("ENGRAM_TELEMETRY_ENABLED", "0")
	cfg, err = Load()
	s.Require().NoError(err)
	s.False(cfg.TelemetryEnabled)
}

// TestLoad_EncryptionKeys verifies ENGRAM_VAULT_KEY and fallback ENGRAM_ENCRYPTION_KEY.
func (s *ConfigSuite) TestLoad_EncryptionKeys() {
	tempDir, err := os.MkdirTemp("", "config-enc-*")
	s.Require().NoError(err)
	defer os.RemoveAll(tempDir)

	os.Setenv("HOME", tempDir)
	os.Setenv("USERPROFILE", tempDir)
	err = os.MkdirAll(filepath.Join(tempDir, ".engram"), 0750)
	s.Require().NoError(err)

	// Primary key name takes precedence
	s.T().Setenv("ENGRAM_VAULT_KEY", "primary-vault-key")
	s.T().Setenv("ENGRAM_ENCRYPTION_KEY", "fallback-key")
	cfg, err := Load()
	s.Require().NoError(err)
	s.Equal("primary-vault-key", cfg.EncryptionKey)

	// Without primary, fallback is used
	os.Unsetenv("ENGRAM_VAULT_KEY")
	s.T().Setenv("ENGRAM_ENCRYPTION_KEY", "only-enc-key")
	cfg, err = Load()
	s.Require().NoError(err)
	s.Equal("only-enc-key", cfg.EncryptionKey)
}

// TestLoad_EncryptionKeyFile verifies ENGRAM_ENCRYPTION_KEY_FILE env override.
func (s *ConfigSuite) TestLoad_EncryptionKeyFile() {
	tempDir, err := os.MkdirTemp("", "config-enc-file-*")
	s.Require().NoError(err)
	defer os.RemoveAll(tempDir)

	os.Setenv("HOME", tempDir)
	os.Setenv("USERPROFILE", tempDir)
	err = os.MkdirAll(filepath.Join(tempDir, ".engram"), 0750)
	s.Require().NoError(err)

	s.T().Setenv("ENGRAM_ENCRYPTION_KEY_FILE", "/vault/key.hex")
	cfg, err := Load()
	s.Require().NoError(err)
	s.Equal("/vault/key.hex", cfg.EncryptionKeyFile)
}

// TestLoad_AuthentikSSO verifies Authentik SSO env overrides.
func (s *ConfigSuite) TestLoad_AuthentikSSO() {
	tempDir, err := os.MkdirTemp("", "config-authentik-*")
	s.Require().NoError(err)
	defer os.RemoveAll(tempDir)

	os.Setenv("HOME", tempDir)
	os.Setenv("USERPROFILE", tempDir)
	err = os.MkdirAll(filepath.Join(tempDir, ".engram"), 0750)
	s.Require().NoError(err)

	s.T().Setenv("ENGRAM_AUTHENTIK_ENABLED", "true")
	s.T().Setenv("ENGRAM_AUTHENTIK_AUTO_PROVISION", "1")
	s.T().Setenv("ENGRAM_AUTHENTIK_TRUSTED_PROXIES", "192.168.1.1,10.0.0.1")

	cfg, err := Load()
	s.Require().NoError(err)
	s.True(cfg.AuthentikEnabled)
	s.True(cfg.AuthentikAutoProvision)
	s.Equal([]string{"192.168.1.1", "10.0.0.1"}, cfg.AuthentikTrustedProxies)
}

// TestLoad_AuthSkipLocal verifies ENGRAM_AUTH_SKIP_LOCAL and ENGRAM_AUTH_TRUSTED_PROXY.
func (s *ConfigSuite) TestLoad_AuthSkipLocal() {
	tempDir, err := os.MkdirTemp("", "config-auth-*")
	s.Require().NoError(err)
	defer os.RemoveAll(tempDir)

	os.Setenv("HOME", tempDir)
	os.Setenv("USERPROFILE", tempDir)
	err = os.MkdirAll(filepath.Join(tempDir, ".engram"), 0750)
	s.Require().NoError(err)

	s.T().Setenv("ENGRAM_AUTH_SKIP_LOCAL", "true")
	s.T().Setenv("ENGRAM_AUTH_TRUSTED_PROXY", "172.16.0.1")

	cfg, err := Load()
	s.Require().NoError(err)
	s.True(cfg.AuthSkipLocal)
	s.Equal("172.16.0.1", cfg.AuthTrustedProxy)
}

// TestLoad_EnforceSourceProjectEnvOverride verifies env override for EnforceSourceProject.
func (s *ConfigSuite) TestLoad_EnforceSourceProjectEnvOverride() {
	tempDir, err := os.MkdirTemp("", "config-esp-*")
	s.Require().NoError(err)
	defer os.RemoveAll(tempDir)

	os.Setenv("HOME", tempDir)
	os.Setenv("USERPROFILE", tempDir)
	err = os.MkdirAll(filepath.Join(tempDir, ".engram"), 0750)
	s.Require().NoError(err)

	s.T().Setenv("ENGRAM_ENFORCE_SOURCE_PROJECT", "false")
	cfg, err := Load()
	s.Require().NoError(err)
	s.False(cfg.EnforceSourceProject)
}

// TestLoad_LogBufferSize verifies ENGRAM_LOG_BUFFER_SIZE env override.
func (s *ConfigSuite) TestLoad_LogBufferSize() {
	tempDir, err := os.MkdirTemp("", "config-logbuf-*")
	s.Require().NoError(err)
	defer os.RemoveAll(tempDir)

	os.Setenv("HOME", tempDir)
	os.Setenv("USERPROFILE", tempDir)
	err = os.MkdirAll(filepath.Join(tempDir, ".engram"), 0750)
	s.Require().NoError(err)

	s.T().Setenv("ENGRAM_LOG_BUFFER_SIZE", "50000")
	cfg, err := Load()
	s.Require().NoError(err)
	s.Equal(50000, cfg.LogBufferSize)
}

// TestLoad_CollectionConfig verifies COLLECTION_CONFIG env override.
func (s *ConfigSuite) TestLoad_CollectionConfig() {
	tempDir, err := os.MkdirTemp("", "config-collection-*")
	s.Require().NoError(err)
	defer os.RemoveAll(tempDir)

	os.Setenv("HOME", tempDir)
	os.Setenv("USERPROFILE", tempDir)
	err = os.MkdirAll(filepath.Join(tempDir, ".engram"), 0750)
	s.Require().NoError(err)

	s.T().Setenv("COLLECTION_CONFIG", "/my/collections.yml")
	cfg, err := Load()
	s.Require().NoError(err)
	s.Equal("/my/collections.yml", cfg.CollectionConfigPath)
}

// TestReload verifies that Reload returns a new config and detects changed fields.
func (s *ConfigSuite) TestReload() {
	tempDir, err := os.MkdirTemp("", "config-reload-*")
	s.Require().NoError(err)
	defer os.RemoveAll(tempDir)

	os.Setenv("HOME", tempDir)
	os.Setenv("USERPROFILE", tempDir)
	err = os.MkdirAll(filepath.Join(tempDir, ".engram"), 0750)
	s.Require().NoError(err)

	// Initial load
	cfg1, _, err := Reload()
	s.Require().NoError(err)
	s.NotNil(cfg1)

	// Reload again — no changes
	cfg2, changed, err := Reload()
	s.Require().NoError(err)
	s.NotNil(cfg2)
	s.Empty(changed)
}

// TestReload_DetectsChanges verifies Reload detects model and port changes.
func (s *ConfigSuite) TestReload_DetectsChanges() {
	tempDir, err := os.MkdirTemp("", "config-reload-chg-*")
	s.Require().NoError(err)
	defer os.RemoveAll(tempDir)

	os.Setenv("HOME", tempDir)
	os.Setenv("USERPROFILE", tempDir)
	err = os.MkdirAll(filepath.Join(tempDir, ".engram"), 0750)
	s.Require().NoError(err)

	// Write initial settings
	err = os.WriteFile(filepath.Join(tempDir, ".engram", "settings.json"),
		[]byte(`{"ENGRAM_MODEL": "haiku"}`), 0600)
	s.Require().NoError(err)

	_, _, err = Reload()
	s.Require().NoError(err)

	// Update settings to a different model
	err = os.WriteFile(filepath.Join(tempDir, ".engram", "settings.json"),
		[]byte(`{"ENGRAM_MODEL": "sonnet"}`), 0600)
	s.Require().NoError(err)

	cfg, changed, err := Reload()
	s.Require().NoError(err)
	s.Equal("sonnet", cfg.Model)
	s.Contains(changed, "model")
}

// TestGet verifies the global config getter returns a valid non-nil config.
func TestGet(t *testing.T) {
	origHome := os.Getenv("HOME")
	tempDir, err := os.MkdirTemp("", "config-get-test-*")
	require.NoError(t, err)
	defer func() {
		os.Setenv("HOME", origHome)
		os.Setenv("USERPROFILE", origHome)
		os.RemoveAll(tempDir)
	}()
	os.Setenv("HOME", tempDir)
	os.Setenv("USERPROFILE", tempDir)

	err = os.MkdirAll(filepath.Join(tempDir, ".engram"), 0750)
	require.NoError(t, err)

	cfg := Get()
	require.NotNil(t, cfg)
	assert.Greater(t, cfg.WorkerPort, 0)
	assert.NotEmpty(t, cfg.Model)
}

// TestGetWorkerPort_WithEnv verifies GetWorkerPort reads ENGRAM_WORKER_PORT env.
func TestGetWorkerPort_WithEnv(t *testing.T) {
	origEnv := os.Getenv("ENGRAM_WORKER_PORT")
	defer os.Setenv("ENGRAM_WORKER_PORT", origEnv)

	os.Setenv("ENGRAM_WORKER_PORT", "45678")
	port := GetWorkerPort()
	assert.Equal(t, 45678, port)

	os.Setenv("ENGRAM_WORKER_PORT", "not-a-number")
	port = GetWorkerPort()
	assert.Greater(t, port, 0) // falls back to config

	os.Setenv("ENGRAM_WORKER_PORT", "0")
	port = GetWorkerPort()
	assert.Greater(t, port, 0) // zero is invalid

	os.Unsetenv("ENGRAM_WORKER_PORT")
	port = GetWorkerPort()
	assert.Greater(t, port, 0)
}

// TestGetWorkerHost verifies GetWorkerHost env priority then config then default.
func TestGetWorkerHost(t *testing.T) {
	origEnv := os.Getenv("ENGRAM_WORKER_HOST")
	defer os.Setenv("ENGRAM_WORKER_HOST", origEnv)

	// Env variable takes priority
	os.Setenv("ENGRAM_WORKER_HOST", "192.168.1.100")
	host := GetWorkerHost()
	assert.Equal(t, "192.168.1.100", host)

	// No env — falls back to config (default "127.0.0.1")
	os.Unsetenv("ENGRAM_WORKER_HOST")
	host = GetWorkerHost()
	assert.NotEmpty(t, host)
}

// TestGetWorkerToken verifies GetWorkerToken reads ENGRAM_AUTH_ADMIN_TOKEN env.
func TestGetWorkerToken(t *testing.T) {
	origEnv := os.Getenv("ENGRAM_AUTH_ADMIN_TOKEN")
	defer os.Setenv("ENGRAM_AUTH_ADMIN_TOKEN", origEnv)

	os.Setenv("ENGRAM_AUTH_ADMIN_TOKEN", "my-admin-token")
	token := GetWorkerToken()
	assert.Equal(t, "my-admin-token", token)

	os.Unsetenv("ENGRAM_AUTH_ADMIN_TOKEN")
	// Falls back to config value (likely empty in test env)
	token = GetWorkerToken()
	_ = token // may be empty string; just ensure no panic
}

// TestGetDatabaseDSN verifies GetDatabaseDSN reads DATABASE_DSN env.
func TestGetDatabaseDSN(t *testing.T) {
	origEnv := os.Getenv("DATABASE_DSN")
	defer os.Setenv("DATABASE_DSN", origEnv)

	os.Setenv("DATABASE_DSN", "postgres://user:pass@localhost/testdb")
	dsn := GetDatabaseDSN()
	assert.Equal(t, "postgres://user:pass@localhost/testdb", dsn)

	os.Unsetenv("DATABASE_DSN")
	dsn = GetDatabaseDSN() // falls back to config
	_ = dsn
}

func TestLoadReadsAdminTokenAndDatabaseDSNFromSecretFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(EnvAdminToken, "")
	t.Setenv(EnvDatabaseDSN, "")
	tokenFile := filepath.Join(home, "admin-token")
	dsnFile := filepath.Join(home, "database-dsn")
	require.NoError(t, os.WriteFile(tokenFile, []byte("file-admin-token\n"), 0o600))
	require.NoError(t, os.WriteFile(dsnFile, []byte("postgres://file-user:file-pass@postgres/engram\n"), 0o600))
	t.Setenv(EnvAdminTokenFile, tokenFile)
	t.Setenv(EnvDatabaseDSNFile, dsnFile)

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "file-admin-token", cfg.WorkerToken)
	assert.Equal(t, "postgres://file-user:file-pass@postgres/engram", cfg.DatabaseDSN)

	t.Setenv(EnvAdminToken, "direct-admin-token")
	t.Setenv(EnvDatabaseDSN, "postgres://direct@postgres/engram")
	cfg, err = Load()
	require.NoError(t, err)
	assert.Equal(t, "direct-admin-token", cfg.WorkerToken)
	assert.Equal(t, "postgres://direct@postgres/engram", cfg.DatabaseDSN)
}

func TestLoadRejectsMissingOrEmptySecretFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(EnvAdminToken, "")
	t.Setenv(EnvAdminTokenFile, filepath.Join(home, "missing"))
	_, err := Load()
	require.ErrorContains(t, err, EnvAdminTokenFile)

	empty := filepath.Join(home, "empty")
	require.NoError(t, os.WriteFile(empty, nil, 0o600))
	t.Setenv(EnvAdminTokenFile, empty)
	_, err = Load()
	require.ErrorContains(t, err, "secret file is empty")
}

// TestGetCollectionConfigPath verifies path fallback when env is unset.
func TestGetCollectionConfigPath(t *testing.T) {
	origEnv := os.Getenv("COLLECTION_CONFIG")
	defer os.Setenv("COLLECTION_CONFIG", origEnv)

	os.Setenv("COLLECTION_CONFIG", "/explicit/collections.yml")
	path := GetCollectionConfigPath()
	assert.Equal(t, "/explicit/collections.yml", path)

	os.Unsetenv("COLLECTION_CONFIG")
	path = GetCollectionConfigPath()
	assert.Contains(t, path, "collections.yml")
}

// TestGetWorkstationID verifies WORKSTATION_ID env is read correctly.
func TestGetWorkstationID(t *testing.T) {
	origEnv := os.Getenv("WORKSTATION_ID")
	defer os.Setenv("WORKSTATION_ID", origEnv)

	os.Setenv("WORKSTATION_ID", "ws-prod-1")
	id := GetWorkstationID()
	assert.Equal(t, "ws-prod-1", id)

	os.Unsetenv("WORKSTATION_ID")
	id = GetWorkstationID()
	assert.Empty(t, id)
}

// TestSplitTrim verifies the splitTrim helper.
func TestSplitTrim(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: []string{},
		},
		{
			name:     "single value",
			input:    "bugfix",
			expected: []string{"bugfix"},
		},
		{
			name:     "multiple values",
			input:    "bugfix,feature,refactor",
			expected: []string{"bugfix", "feature", "refactor"},
		},
		{
			name:     "values with spaces",
			input:    " bugfix , feature , refactor ",
			expected: []string{"bugfix", "feature", "refactor"},
		},
		{
			name:     "empty values filtered",
			input:    "bugfix,,feature,,",
			expected: []string{"bugfix", "feature"},
		},
		{
			name:     "only commas",
			input:    ",,,",
			expected: []string{},
		},
		{
			name:     "spaces only segments",
			input:    " , , ",
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := splitTrim(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestDefaultObservationTypes verifies the default observation type list.
func TestDefaultObservationTypes(t *testing.T) {
	expected := []string{
		"bugfix", "feature", "refactor", "change", "discovery", "decision",
	}
	assert.Equal(t, expected, DefaultObservationTypes)
}

// TestDefaultObservationConcepts verifies the default observation concept list.
func TestDefaultObservationConcepts(t *testing.T) {
	expected := []string{
		"how-it-works", "why-it-exists", "what-changed",
		"problem-solution", "gotcha", "pattern", "trade-off",
	}
	assert.Equal(t, expected, DefaultObservationConcepts)
}

// TestCriticalConcepts verifies the critical concepts list.
func TestCriticalConcepts(t *testing.T) {
	expected := []string{
		"gotcha", "pattern", "problem-solution", "trade-off",
	}
	assert.Equal(t, expected, CriticalConcepts)
}

// TestLoad_ContextSettings tests loading of context settings from JSON file.
func TestLoad_ContextSettings(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "config-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	origHome := os.Getenv("HOME")
	origUserProfile := os.Getenv("USERPROFILE")
	os.Setenv("HOME", tempDir)
	os.Setenv("USERPROFILE", tempDir)
	defer os.Setenv("HOME", origHome)
	defer os.Setenv("USERPROFILE", origUserProfile)

	err = os.MkdirAll(filepath.Join(tempDir, ".engram"), 0750)
	require.NoError(t, err)

	settingsJSON := `{
		"ENGRAM_CONTEXT_FULL_COUNT": 50,
		"ENGRAM_CONTEXT_SESSION_COUNT": 20,
		"ENGRAM_CONTEXT_OBS_TYPES": "bugfix,feature",
		"ENGRAM_CONTEXT_OBS_CONCEPTS": "security,performance"
	}`
	err = os.WriteFile(
		filepath.Join(tempDir, ".engram", "settings.json"),
		[]byte(settingsJSON),
		0600,
	)
	require.NoError(t, err)

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, 50, cfg.ContextFullCount)
	assert.Equal(t, 20, cfg.ContextSessionCount)
	assert.Equal(t, []string{"bugfix", "feature"}, cfg.ContextObsTypes)
	assert.Equal(t, []string{"security", "performance"}, cfg.ContextObsConcepts)
}

func (s *ConfigSuite) TestDefault_RuleRouterDefaults() {
	cfg := Default()
	s.False(cfg.RuleRouterEnabled)
	s.Equal(8, cfg.RuleRouterKernelMax)
	s.Equal(12, cfg.RuleRouterContextualMax)
	s.Equal(4800, cfg.RuleRouterMaxRenderedChars)
	s.True(cfg.RuleRouterTelemetryBestEffort)
}

func (s *ConfigSuite) TestRuleRouterEnvOverrides() {
	s.T().Setenv("ENGRAM_RULE_ROUTER_ENABLED", "1")
	s.T().Setenv("ENGRAM_RULE_ROUTER_KERNEL_MAX", "3")
	s.T().Setenv("ENGRAM_RULE_ROUTER_CONTEXTUAL_MAX", "5")
	s.T().Setenv("ENGRAM_RULE_ROUTER_MAX_RENDERED_CHARS", "2048")
	s.T().Setenv("ENGRAM_RULE_ROUTER_TELEMETRY_BEST_EFFORT", "false")

	cfg, err := Load()
	s.Require().NoError(err)
	s.True(cfg.RuleRouterEnabled)
	s.Equal(3, cfg.RuleRouterKernelMax)
	s.Equal(5, cfg.RuleRouterContextualMax)
	s.Equal(2048, cfg.RuleRouterMaxRenderedChars)
	s.False(cfg.RuleRouterTelemetryBestEffort)
}

func (s *ConfigSuite) TestRuleRouterInvalidNumericEnvFallsBack() {
	s.T().Setenv("ENGRAM_RULE_ROUTER_KERNEL_MAX", "0")
	s.T().Setenv("ENGRAM_RULE_ROUTER_CONTEXTUAL_MAX", "-1")
	s.T().Setenv("ENGRAM_RULE_ROUTER_MAX_RENDERED_CHARS", "not-a-number")

	cfg, err := Load()
	s.Require().NoError(err)
	s.Equal(8, cfg.RuleRouterKernelMax)
	s.Equal(12, cfg.RuleRouterContextualMax)
	s.Equal(4800, cfg.RuleRouterMaxRenderedChars)
}
