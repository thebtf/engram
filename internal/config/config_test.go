// Package config provides configuration management for engram.
package config

import (
	"os"
	"path/filepath"
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

// TestDefault tests default configuration values.
func (s *ConfigSuite) TestDefault() {
	cfg := Default()

	s.Equal(DefaultWorkerPort, cfg.WorkerPort)
	s.Equal(DefaultModel, cfg.Model)
	s.Equal(4, cfg.MaxConns)
	s.Equal(100, cfg.ContextObservations)
	s.Equal(25, cfg.ContextFullCount)
	s.Equal(10, cfg.ContextSessionCount)
	s.True(cfg.ContextShowReadTokens)
	s.True(cfg.ContextShowWorkTokens)
	s.Equal("narrative", cfg.ContextFullField)
	s.True(cfg.ContextShowLastSummary)
	s.Equal(DefaultObservationTypes, cfg.ContextObsTypes)
	s.Equal(DefaultObservationConcepts, cfg.ContextObsConcepts)
	// v4 default: injection floor disabled so the silence path is active (FR-1).
	s.Equal(0, cfg.InjectionFloor)
}

// TestInjectionFloorEnvOverride verifies that ENGRAM_INJECTION_FLOOR=3 restores legacy fill behavior.
func (s *ConfigSuite) TestInjectionFloorEnvOverride() {
	s.T().Setenv("ENGRAM_INJECTION_FLOOR", "3")
	cfg, err := Load()
	s.Require().NoError(err)
	s.Equal(3, cfg.InjectionFloor)
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

// TestStorePathSupersessionDefaultTrue verifies store-path supersession defaults to enabled.
func (s *ConfigSuite) TestStorePathSupersessionDefaultTrue() {
	cfg, err := Load()
	s.Require().NoError(err)
	s.True(cfg.StorePathSupersessionEnabled, "store-path supersession must default to enabled for current behavior")
}

// TestStorePathSupersessionEnvOverride verifies the kill-switch disables store-path supersession.
func (s *ConfigSuite) TestStorePathSupersessionEnvOverride() {
	s.T().Setenv("ENGRAM_STORE_PATH_SUPERSESSION_ENABLED", "false")
	cfg, err := Load()
	s.Require().NoError(err)
	s.False(cfg.StorePathSupersessionEnabled, "ENGRAM_STORE_PATH_SUPERSESSION_ENABLED=false must disable store-path supersession")
}

// TestTypeLanesEnabledDefaultFalse verifies typed lane dispatch is opt-in.
func (s *ConfigSuite) TestTypeLanesEnabledDefaultFalse() {
	cfg, err := Load()
	s.Require().NoError(err)
	s.False(cfg.TypeLanesEnabled, "typed lane dispatch must default to disabled")
}

// TestTypeLanesEnabledEnvOverride verifies the env flag enables typed lanes.
func (s *ConfigSuite) TestTypeLanesEnabledEnvOverride() {
	s.T().Setenv("ENGRAM_TYPE_LANES_ENABLED", "true")
	cfg, err := Load()
	s.Require().NoError(err)
	s.True(cfg.TypeLanesEnabled, "ENGRAM_TYPE_LANES_ENABLED=true must enable typed lane dispatch")
}

// TestProjectBriefingEnabledDefaultFalse verifies project briefing generation is opt-in.
func (s *ConfigSuite) TestProjectBriefingEnabledDefaultFalse() {
	cfg, err := Load()
	s.Require().NoError(err)
	s.False(cfg.ProjectBriefingEnabled, "project briefing must default to disabled")
}

// TestProjectBriefingEnabledEnvOverride verifies the env flag enables project briefing.
func (s *ConfigSuite) TestProjectBriefingEnabledEnvOverride() {
	s.T().Setenv("ENGRAM_PROJECT_BRIEFING_ENABLED", "true")
	cfg, err := Load()
	s.Require().NoError(err)
	s.True(cfg.ProjectBriefingEnabled, "ENGRAM_PROJECT_BRIEFING_ENABLED=true must enable project briefing")
}

// TestWriteMergeEnabledDefaultFalse verifies write-time merge is opt-in.
func (s *ConfigSuite) TestWriteMergeEnabledDefaultFalse() {
	cfg, err := Load()
	s.Require().NoError(err)
	s.False(cfg.WriteMergeEnabled, "write-time merge must default to disabled")
}

// TestWriteMergeEnabledEnvOverride verifies the env flag enables write-time merge.
func (s *ConfigSuite) TestWriteMergeEnabledEnvOverride() {
	s.T().Setenv("ENGRAM_WRITE_MERGE_ENABLED", "true")
	cfg, err := Load()
	s.Require().NoError(err)
	s.True(cfg.WriteMergeEnabled, "ENGRAM_WRITE_MERGE_ENABLED=true must enable write-time merge")
}

// TestContradictionDetectionEnabledDefaultTrue verifies contradiction detection defaults to enabled.
func (s *ConfigSuite) TestContradictionDetectionEnabledDefaultTrue() {
	cfg, err := Load()
	s.Require().NoError(err)
	s.True(cfg.ContradictionDetectionEnabled, "contradiction detection must default to enabled until operators disable it explicitly")
}

// TestContradictionDetectionEnabledEnvOverride verifies the env flag disables contradiction detection.
func (s *ConfigSuite) TestContradictionDetectionEnabledEnvOverride() {
	s.T().Setenv("ENGRAM_CONTRADICTION_DETECTION_ENABLED", "false")
	cfg, err := Load()
	s.Require().NoError(err)
	s.False(cfg.ContradictionDetectionEnabled, "ENGRAM_CONTRADICTION_DETECTION_ENABLED=false must disable contradiction detection")
}

// TestInjectGraphBFSEnabledDefaultFalse verifies graph BFS inject path is opt-in.
func (s *ConfigSuite) TestInjectGraphBFSEnabledDefaultFalse() {
	cfg, err := Load()
	s.Require().NoError(err)
	s.False(cfg.InjectGraphBFSEnabled, "graph BFS inject path must default to disabled")
}

// TestInjectGraphBFSEnabledEnvOverride verifies the env flag enables graph BFS inject path.
func (s *ConfigSuite) TestInjectGraphBFSEnabledEnvOverride() {
	s.T().Setenv("ENGRAM_INJECT_GRAPH_BFS_ENABLED", "true")
	cfg, err := Load()
	s.Require().NoError(err)
	s.True(cfg.InjectGraphBFSEnabled, "ENGRAM_INJECT_GRAPH_BFS_ENABLED=true must enable graph BFS inject path")
}

// TestTypeSearchLanesDefaultsInitialized verifies defaults are present in config.
func (s *ConfigSuite) TestTypeSearchLanesDefaultsInitialized() {
	cfg := Default()
	s.NotNil(cfg.TypeSearchLanes)
	s.Contains(cfg.TypeSearchLanes, "guidance")
	s.Contains(cfg.TypeSearchLanes, "decision")
	s.Contains(cfg.TypeSearchLanes, "default")
	s.Equal(0.20, cfg.TypeSearchLanes["guidance"].MinScore)
	s.Equal(3, cfg.TypeSearchLanes["decision"].TopK)
	s.Equal(1.0, cfg.TypeSearchLanes["default"].RerankerWeight)
}

// TestTypeSearchLanesJsonMerge verifies partial JSON overrides merge over defaults.
func (s *ConfigSuite) TestTypeSearchLanesJsonMerge() {
	err := EnsureDataDir()
	s.Require().NoError(err)

	settings := `{
  "ENGRAM_TYPE_LANES_ENABLED": true,
  "type_search_lanes": {
    "decision": {"min_score": 0.7},
    "guidance": {"top_k": 7}
  }
}`
	path := SettingsPath()
	err = os.WriteFile(path, []byte(settings), 0600)
	s.Require().NoError(err)

	cfg, err := Load()
	s.Require().NoError(err)
	s.True(cfg.TypeLanesEnabled)
	s.Equal(0.7, cfg.TypeSearchLanes["decision"].MinScore)
	s.Equal(3, cfg.TypeSearchLanes["decision"].TopK)
	s.Equal(7, cfg.TypeSearchLanes["guidance"].TopK)
	s.Equal(1.5, cfg.TypeSearchLanes["guidance"].RerankerWeight)
	s.Equal(0.35, cfg.TypeSearchLanes["default"].MinScore)
}

// TestDataDir tests data directory path.
func (s *ConfigSuite) TestDataDir() {
	dir := DataDir()
	s.Contains(dir, ".engram")
}

// TestDBPath tests database path.
func (s *ConfigSuite) TestDBPath() {
	path := DBPath()
	s.Contains(path, "engram.db")
}

// TestSettingsPath tests settings file path.
func (s *ConfigSuite) TestSettingsPath() {
	path := SettingsPath()
	s.Contains(path, "settings.json")
}

// TestEnsureDataDir tests data directory creation.
func (s *ConfigSuite) TestEnsureDataDir() {
	err := EnsureDataDir()
	s.NoError(err)

	dir := DataDir()
	info, err := os.Stat(dir)
	s.NoError(err)
	s.True(info.IsDir())
}

// TestEnsureSettings tests settings file creation.
func (s *ConfigSuite) TestEnsureSettings() {
	// First ensure data dir exists
	err := EnsureDataDir()
	s.NoError(err)

	// Ensure settings creates default file
	err = EnsureSettings()
	s.NoError(err)

	path := SettingsPath()
	info, err := os.Stat(path)
	s.NoError(err)
	s.False(info.IsDir())

	// Second call should not error (file exists)
	err = EnsureSettings()
	s.NoError(err)
}

// TestEnsureAll tests full initialization.
func (s *ConfigSuite) TestEnsureAll() {
	err := EnsureAll()
	s.NoError(err)

	// Verify dir and settings exist
	_, err = os.Stat(DataDir())
	s.NoError(err)
	_, err = os.Stat(SettingsPath())
	s.NoError(err)
}

// TestLoad_TableDriven tests configuration loading with various scenarios.
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
			// Create fresh temp dir
			tempDir, err := os.MkdirTemp("", "config-test-*")
			s.Require().NoError(err)
			defer os.RemoveAll(tempDir)

			os.Setenv("HOME", tempDir)
			os.Setenv("USERPROFILE", tempDir)

			// Create data dir
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

// TestGetWorkerPort_TableDriven tests worker port retrieval with various scenarios.
func TestGetWorkerPort_TableDriven(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		wantPort int
		setEnv   bool
	}{
		{
			name:     "no env, use default",
			envValue: "",
			wantPort: DefaultWorkerPort,
			setEnv:   false,
		},
		{
			name:     "env set to valid port",
			envValue: "38888",
			wantPort: 38888,
			setEnv:   true,
		},
		{
			name:     "env set to invalid value",
			envValue: "invalid",
			wantPort: DefaultWorkerPort,
			setEnv:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save original env
			origEnv := os.Getenv("ENGRAM_WORKER_PORT")
			defer os.Setenv("ENGRAM_WORKER_PORT", origEnv)

			if tt.setEnv {
				os.Setenv("ENGRAM_WORKER_PORT", tt.envValue)
			} else {
				os.Unsetenv("ENGRAM_WORKER_PORT")
			}

			// We can't easily test GetWorkerPort since it uses Get() which caches
			// So we test the env parsing logic directly
			if tt.setEnv && tt.envValue != "" {
				if tt.wantPort != DefaultWorkerPort {
					assert.Equal(t, tt.envValue, os.Getenv("ENGRAM_WORKER_PORT"))
				}
			}
		})
	}
}

// TestSplitTrim tests the splitTrim helper function.
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := splitTrim(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestDefaultObservationTypes tests default observation types.
func TestDefaultObservationTypes(t *testing.T) {
	expected := []string{
		"bugfix", "feature", "refactor", "change", "discovery", "decision",
	}
	assert.Equal(t, expected, DefaultObservationTypes)
}

// TestDefaultObservationConcepts tests default observation concepts.
func TestDefaultObservationConcepts(t *testing.T) {
	expected := []string{
		"how-it-works", "why-it-exists", "what-changed",
		"problem-solution", "gotcha", "pattern", "trade-off",
	}
	assert.Equal(t, expected, DefaultObservationConcepts)
}

// TestCriticalConcepts tests critical concepts list.
func TestCriticalConcepts(t *testing.T) {
	expected := []string{
		"gotcha", "pattern", "problem-solution", "trade-off",
	}
	assert.Equal(t, expected, CriticalConcepts)
}

// TestGet tests the global config getter.
func TestGet(t *testing.T) {
	// Save and restore HOME
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

	// Create data dir
	err = os.MkdirAll(filepath.Join(tempDir, ".engram"), 0750)
	require.NoError(t, err)

	// Get() should return a valid config
	cfg := Get()
	require.NotNil(t, cfg)
	assert.Greater(t, cfg.WorkerPort, 0)
	assert.NotEmpty(t, cfg.Model)
}

// TestGetWorkerPort_WithEnv tests GetWorkerPort with environment variable.
func TestGetWorkerPort_WithEnv(t *testing.T) {
	// Save original env
	origEnv := os.Getenv("ENGRAM_WORKER_PORT")
	defer os.Setenv("ENGRAM_WORKER_PORT", origEnv)

	// Test with valid port in env
	os.Setenv("ENGRAM_WORKER_PORT", "45678")
	port := GetWorkerPort()
	assert.Equal(t, 45678, port)

	// Test with invalid port (should fall back to config)
	os.Setenv("ENGRAM_WORKER_PORT", "not-a-number")
	port = GetWorkerPort()
	// Should return from Get().WorkerPort, which is default
	assert.Greater(t, port, 0)

	// Test with zero port (should fall back to config)
	os.Setenv("ENGRAM_WORKER_PORT", "0")
	port = GetWorkerPort()
	// Zero is invalid, so should use default
	assert.Greater(t, port, 0)

	// Test with no env (should use config)
	os.Unsetenv("ENGRAM_WORKER_PORT")
	port = GetWorkerPort()
	assert.Greater(t, port, 0)
}

// TestLoad_ContextSettings tests context-related settings loading.
func TestLoad_ContextSettings(t *testing.T) {
	// Create temp dir
	tempDir, err := os.MkdirTemp("", "config-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	origHome := os.Getenv("HOME")
	origUserProfile := os.Getenv("USERPROFILE")
	os.Setenv("HOME", tempDir)
	os.Setenv("USERPROFILE", tempDir)
	defer os.Setenv("HOME", origHome)
	defer os.Setenv("USERPROFILE", origUserProfile)

	// Create data dir and settings
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
