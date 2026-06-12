// Package models contains domain models for engram.
package models

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// ObservationType constants
// ---------------------------------------------------------------------------

func TestObsTypeConstants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		got  ObservationType
		want string
	}{
		{ObsTypeDiscovery, "discovery"},
		{ObsTypeDecision, "decision"},
		{ObsTypeBugfix, "bugfix"},
		{ObsTypeFeature, "feature"},
		{ObsTypeRefactor, "refactor"},
		{ObsTypeChange, "change"},
		{ObsTypeGuidance, "guidance"},
		{ObsTypePitfall, "pitfall"},
		{ObsTypeOperational, "operational"},
		{ObsTypeTimeline, "timeline"},
	}
	for _, c := range cases {
		c := c
		t.Run(string(c.got), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, ObservationType(c.want), c.got)
		})
	}
}

// ---------------------------------------------------------------------------
// AgentSource constants and validation
// ---------------------------------------------------------------------------

func TestAgentSourceConstants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		got  AgentSource
		want string
	}{
		{AgentClaude, "claude-code"},
		{AgentCodex, "codex"},
		{AgentGemini, "gemini"},
		{AgentOther, "other"},
		{AgentUnknown, "unknown"},
	}
	for _, c := range cases {
		c := c
		t.Run(string(c.got), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, AgentSource(c.want), c.got)
		})
	}
}

func TestIsValidAgentSource(t *testing.T) {
	t.Parallel()
	valid := []string{"claude-code", "codex", "gemini", "other", "unknown"}
	for _, s := range valid {
		s := s
		t.Run("valid/"+s, func(t *testing.T) {
			t.Parallel()
			assert.True(t, IsValidAgentSource(s))
		})
	}
	invalid := []string{"gpt-4", "llama", ""}
	for _, s := range invalid {
		s := s
		t.Run("invalid/"+s, func(t *testing.T) {
			t.Parallel()
			assert.False(t, IsValidAgentSource(s))
		})
	}
}

func TestSourceCrossModelConstant(t *testing.T) {
	t.Parallel()
	assert.Equal(t, SourceType("cross_model"), SourceCrossModel)
}

// ---------------------------------------------------------------------------
// ObservationScope constants
// ---------------------------------------------------------------------------

func TestObservationScopeConstants(t *testing.T) {
	t.Parallel()
	assert.Equal(t, ObservationScope("project"), ScopeProject)
	assert.Equal(t, ObservationScope("global"), ScopeGlobal)
}

// ---------------------------------------------------------------------------
// GlobalizableConcepts
// ---------------------------------------------------------------------------

func TestGlobalizableConcepts_Contents(t *testing.T) {
	t.Parallel()
	required := []string{
		"best-practice", "pattern", "anti-pattern", "architecture",
		"security", "performance", "testing", "debugging", "workflow", "tooling",
	}
	assert.Equal(t, required, GlobalizableConcepts)
}

// ---------------------------------------------------------------------------
// classifyFileScopes
// ---------------------------------------------------------------------------

func TestClassifyFileScopes_Frontend(t *testing.T) {
	t.Parallel()
	scopes := classifyFileScopes([]string{"src/App.tsx", "styles.css"})
	assert.Contains(t, scopes, "scope:frontend")
}

func TestClassifyFileScopes_Backend(t *testing.T) {
	t.Parallel()
	scopes := classifyFileScopes([]string{"internal/mcp/server.go", "cmd/worker/main.go"})
	assert.Contains(t, scopes, "scope:backend")
}

func TestClassifyFileScopes_TestFiles(t *testing.T) {
	t.Parallel()
	scopes := classifyFileScopes([]string{"internal/scoring/calculator_test.go"})
	assert.Contains(t, scopes, "scope:tests")
	assert.Contains(t, scopes, "scope:backend")
}

func TestClassifyFileScopes_MultiSegmentPath(t *testing.T) {
	t.Parallel()
	scopes := classifyFileScopes([]string{"internal/api/auth_handler_test.go"})
	assert.Contains(t, scopes, "scope:backend")
	assert.Contains(t, scopes, "scope:api")
	assert.Contains(t, scopes, "scope:auth")
	assert.Contains(t, scopes, "scope:tests")
}

func TestClassifyFileScopes_NoPseudoMatch(t *testing.T) {
	t.Parallel()
	scopes := classifyFileScopes([]string{"internal/mcp/tools_memory.go"})
	assert.NotContains(t, scopes, "scope:api")
	assert.NotContains(t, scopes, "scope:auth")
}

func TestClassifyFileScopes_EmptyInputs(t *testing.T) {
	t.Parallel()
	assert.Empty(t, classifyFileScopes(nil))
	assert.Empty(t, classifyFileScopes([]string{}))
	assert.Empty(t, classifyFileScopes([]string{""}))
}

// ---------------------------------------------------------------------------
// DetermineScope
// ---------------------------------------------------------------------------

func TestDetermineScope_Table(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		concepts []string
		want     ObservationScope
	}{
		{"empty → project", []string{}, ScopeProject},
		{"no globalizable → project", []string{"custom", "project-specific"}, ScopeProject},
		{"security → global", []string{"security"}, ScopeGlobal},
		{"best-practice → global", []string{"best-practice"}, ScopeGlobal},
		{"performance → global", []string{"performance"}, ScopeGlobal},
		{"testing → global", []string{"testing"}, ScopeGlobal},
		{"pattern → global", []string{"pattern"}, ScopeGlobal},
		{"mixed with globalizable → global", []string{"custom", "security"}, ScopeGlobal},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, DetermineScope(tc.concepts))
		})
	}
}

// ---------------------------------------------------------------------------
// ClassifyMemoryType
// ---------------------------------------------------------------------------

func TestClassifyMemoryType_GuidanceShortcut(t *testing.T) {
	t.Parallel()
	obs := &ParsedObservation{
		Type:     ObsTypeGuidance,
		Concepts: []string{"architecture", "pattern"}, // would trigger decision otherwise
	}
	assert.Equal(t, MemTypeGuidance, ClassifyMemoryType(obs))
}

func TestClassifyMemoryType_NonGuidanceUsesConceptMatch(t *testing.T) {
	t.Parallel()
	obs := &ParsedObservation{
		Type:     ObsTypeDiscovery,
		Concepts: []string{"architecture"},
	}
	assert.Equal(t, MemTypeDecision, ClassifyMemoryType(obs))
}

// ---------------------------------------------------------------------------
// ParsedObservation field access
// ---------------------------------------------------------------------------

func TestParsedObservation_Fields(t *testing.T) {
	t.Parallel()
	obs := &ParsedObservation{
		Type:          ObsTypeBugfix,
		Title:         "Fix connection leak",
		Subtitle:      "In database pool",
		Narrative:     "Connections were not returned after timeout",
		Facts:         []string{"Added defer close", "Added test coverage"},
		Concepts:      []string{"database", "reliability"},
		FilesRead:     []string{"pool.go"},
		FilesModified: []string{"pool.go", "pool_test.go"},
		FileMtimes:    map[string]int64{"pool.go": 1700000000},
	}

	assert.Equal(t, ObsTypeBugfix, obs.Type)
	assert.Equal(t, "Fix connection leak", obs.Title)
	assert.Equal(t, "In database pool", obs.Subtitle)
	assert.Len(t, obs.Facts, 2)
	assert.Len(t, obs.Concepts, 2)
	assert.Len(t, obs.FilesRead, 1)
	assert.Len(t, obs.FilesModified, 2)
	assert.Equal(t, int64(1700000000), obs.FileMtimes["pool.go"])
}

func TestParsedObservation_FileMtimesSerializable(t *testing.T) {
	t.Parallel()
	obs := &ParsedObservation{
		Type:       ObsTypeDiscovery,
		Title:      "Mtime check",
		FileMtimes: map[string]int64{"handler.go": 9876543210},
	}
	data, err := json.Marshal(obs.FileMtimes)
	require.NoError(t, err)
	assert.Contains(t, string(data), "handler.go")
	assert.Contains(t, string(data), "9876543210")
}

// ---------------------------------------------------------------------------
// Observation.CheckStaleness
// ---------------------------------------------------------------------------

func TestCheckStaleness_Table(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		stored  map[string]int64
		current map[string]int64
		want    bool
	}{
		{"empty stored — not stale", map[string]int64{}, map[string]int64{"f.go": 1}, false},
		{"matching mtimes — not stale", map[string]int64{"f.go": 1000}, map[string]int64{"f.go": 1000}, false},
		{"file changed — stale", map[string]int64{"f.go": 1000}, map[string]int64{"f.go": 2000}, true},
		{"file absent from current — not stale", map[string]int64{"f.go": 1000}, map[string]int64{}, false},
		{"nil current — not stale", map[string]int64{"f.go": 1000}, nil, false},
		{
			"multi-file partial change — stale",
			map[string]int64{"a.go": 100, "b.go": 200},
			map[string]int64{"a.go": 100, "b.go": 999},
			true,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			obs := &Observation{FileMtimes: tc.stored}
			assert.Equal(t, tc.want, obs.CheckStaleness(tc.current))
		})
	}
}

// ---------------------------------------------------------------------------
// Observation nullable fields
// ---------------------------------------------------------------------------

func TestObservation_NullFields(t *testing.T) {
	t.Parallel()
	obs := &Observation{
		ID:        1,
		Project:   "proj",
		Type:      ObsTypeChange,
		Title:     sql.NullString{Valid: false},
		Subtitle:  sql.NullString{Valid: false},
		Narrative: sql.NullString{Valid: false},
	}
	assert.False(t, obs.Title.Valid)
	assert.False(t, obs.Subtitle.Valid)
	assert.False(t, obs.Narrative.Valid)
}

func TestObservation_ValidFields(t *testing.T) {
	t.Parallel()
	obs := &Observation{
		ID:        2,
		Project:   "proj",
		Type:      ObsTypeRefactor,
		Title:     sql.NullString{String: "Rename package", Valid: true},
		Subtitle:  sql.NullString{String: "From old to new", Valid: true},
		Narrative: sql.NullString{String: "Renamed the package to align with conventions", Valid: true},
	}
	assert.True(t, obs.Title.Valid)
	assert.Equal(t, "Rename package", obs.Title.String)
	assert.True(t, obs.Subtitle.Valid)
}

// ---------------------------------------------------------------------------
// NewObservation
// ---------------------------------------------------------------------------

func TestNewObservation_ScopeFromConcepts(t *testing.T) {
	t.Parallel()
	parsed := &ParsedObservation{
		Type:          ObsTypeFeature,
		Title:         "Add TLS termination",
		Narrative:     "Enabled mutual TLS at the reverse proxy",
		Concepts:      []string{"security"},
		FilesModified: []string{"proxy.go"},
		FileMtimes:    map[string]int64{"proxy.go": 1700000000},
	}
	obs := NewObservation("sdk-001", "infra-proj", parsed, 3, 512)

	assert.Equal(t, "sdk-001", obs.SDKSessionID)
	assert.Equal(t, "infra-proj", obs.Project)
	assert.Equal(t, ScopeGlobal, obs.Scope) // security → global
	assert.Equal(t, ObsTypeFeature, obs.Type)
	assert.Equal(t, "Add TLS termination", obs.Title.String)
	assert.True(t, obs.Title.Valid)
	assert.Equal(t, int64(3), obs.PromptNumber.Int64)
	assert.Equal(t, int64(512), obs.DiscoveryTokens)
	assert.NotEmpty(t, obs.CreatedAt)
	assert.Greater(t, obs.CreatedAtEpoch, int64(0))
}

func TestNewObservation_ProjectScopeWhenNoGlobalizableConcepts(t *testing.T) {
	t.Parallel()
	parsed := &ParsedObservation{
		Type:     ObsTypeDecision,
		Title:    "Use short variable names",
		Concepts: []string{"style", "convention"},
	}
	obs := NewObservation("sdk-002", "my-proj", parsed, 1, 100)

	assert.Equal(t, ScopeProject, obs.Scope)
}

// ---------------------------------------------------------------------------
// ParsedObservation.ToStoredObservation
// ---------------------------------------------------------------------------

func TestToStoredObservation_FieldMapping(t *testing.T) {
	t.Parallel()
	parsed := &ParsedObservation{
		Type:      ObsTypeOperational,
		Title:     "Rotate certs quarterly",
		Subtitle:  "TLS cert rotation",
		Narrative: "Certificates must be rotated every 90 days",
		Facts:     []string{"Automate via cron"},
		Concepts:  []string{"security"},
	}
	obs := parsed.ToStoredObservation()

	assert.Equal(t, ObsTypeOperational, obs.Type)
	assert.Equal(t, "Rotate certs quarterly", obs.Title.String)
	assert.True(t, obs.Title.Valid)
	assert.Equal(t, "TLS cert rotation", obs.Subtitle.String)
	assert.True(t, obs.Subtitle.Valid)
}

// ---------------------------------------------------------------------------
// JSONStringArray scanning
// ---------------------------------------------------------------------------

func TestJSONStringArray_Scan(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		input   interface{}
		want    JSONStringArray
		wantErr bool
	}{
		{"nil input", nil, nil, false},
		{"empty string", "", nil, false},
		{"json string", `["x","y"]`, JSONStringArray{"x", "y"}, false},
		{"json bytes", []byte(`["p","q","r"]`), JSONStringArray{"p", "q", "r"}, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var arr JSONStringArray
			err := arr.Scan(tc.input)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.want, arr)
			}
		})
	}
}

func TestJSONStringArray_Value_NilReturnsEmptyArray(t *testing.T) {
	t.Parallel()
	var arr JSONStringArray
	val, err := arr.Value()
	require.NoError(t, err)
	require.Equal(t, "[]", string(val.([]byte)))
}

// ---------------------------------------------------------------------------
// JSONInt64Map scanning
// ---------------------------------------------------------------------------

func TestJSONInt64Map_Scan(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		input   interface{}
		want    JSONInt64Map
		wantErr bool
	}{
		{"nil", nil, nil, false},
		{"empty str", "", nil, false},
		{"json str", `{"f.go":777}`, JSONInt64Map{"f.go": 777}, false},
		{"json bytes", []byte(`{"a.go":1,"b.go":2}`), JSONInt64Map{"a.go": 1, "b.go": 2}, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var m JSONInt64Map
			err := m.Scan(tc.input)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.want, m)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Observation JSON serialization
// ---------------------------------------------------------------------------

func TestObservation_JSONMarshal(t *testing.T) {
	t.Parallel()
	obs := &Observation{
		ID:      7,
		Project: "payroll-service",
		Type:    ObsTypePitfall,
		Title:   sql.NullString{String: "Race on shutdown", Valid: true},
		Scope:   ScopeProject,
	}
	data, err := json.Marshal(obs)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"id":7`)
	assert.Contains(t, string(data), `"project":"payroll-service"`)
	assert.Contains(t, string(data), `"type":"pitfall"`)
}

func TestObservation_JSONRoundTrip(t *testing.T) {
	t.Parallel()
	orig := &Observation{
		ID:             99,
		SDKSessionID:   "sess-99",
		Project:        "rt-proj",
		Type:           ObsTypeTimeline,
		Title:          sql.NullString{String: "Milestone reached", Valid: true},
		Subtitle:       sql.NullString{String: "v2.0 released", Valid: true},
		Narrative:      sql.NullString{String: "v2.0 shipped with new UI", Valid: true},
		Scope:          ScopeGlobal,
		CreatedAt:      "2026-01-01T00:00:00Z",
		CreatedAtEpoch: 1767225600000,
	}
	data, err := json.Marshal(orig)
	require.NoError(t, err)

	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &m))
	assert.Equal(t, float64(99), m["id"])
	assert.Equal(t, "rt-proj", m["project"])
	assert.Equal(t, "timeline", m["type"])
	assert.Equal(t, "Milestone reached", m["title"])
}
