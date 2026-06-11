// Package writelint — T038: 13-row golden contract test for conflict_adapter.
// One assertion per row in the Conflict Adapter Mapping Contract (plan.md).
// This file is the authoritative golden test; it fails fast on any mapping drift.
package writelint_test

import (
	"database/sql"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/writelint"
	"github.com/thebtf/engram/pkg/models"
)

// ---------------------------------------------------------------------------
// Golden contract fixture
// ---------------------------------------------------------------------------

func goldenMemory() *models.Memory {
	ts := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	return &models.Memory{
		ID:             42,
		Project:        "engram-golden",
		PrivacyScope:   "shared",
		EpistemicType:  "fact",
		Content:        "This is a long golden content string that should produce a title from its first 100 characters.",
		Tags:           []string{"lang:go", "file:internal/writelint/orchestrator.go", "read:cmd/engram/main.go"},
		SourceAgent:    "claude-code",
		CreatedAt:      ts,
		SourceSessions: []string{"sess-abc-123", "sess-def-456"},
		Status:         "active",
	}
}

// TestConflictAdapter_Row01_ID verifies Row 1: ID → direct copy.
func TestConflictAdapter_Row01_ID(t *testing.T) {
	m := goldenMemory()
	obs := writelint.ProjectMemoryToObservation(m)
	if obs.ID != m.ID {
		t.Errorf("Row 1 ID: got %d want %d", obs.ID, m.ID)
	}
}

// TestConflictAdapter_Row02_Project verifies Row 2: Project → direct copy.
func TestConflictAdapter_Row02_Project(t *testing.T) {
	m := goldenMemory()
	obs := writelint.ProjectMemoryToObservation(m)
	if obs.Project != m.Project {
		t.Errorf("Row 2 Project: got %q want %q", obs.Project, m.Project)
	}
}

// TestConflictAdapter_Row03_Scope verifies Row 3: PrivacyScope → Scope mapping.
// shared/global → ScopeGlobal; private/project → ScopeProject.
func TestConflictAdapter_Row03_Scope(t *testing.T) {
	cases := []struct {
		privacy string
		want    models.ObservationScope
	}{
		{"shared", models.ScopeGlobal},
		{"global", models.ScopeGlobal},
		{"private", models.ScopeProject},
		{"project", models.ScopeProject},
		{"", models.ScopeProject}, // default
	}
	for _, tc := range cases {
		m := goldenMemory()
		m.PrivacyScope = tc.privacy
		obs := writelint.ProjectMemoryToObservation(m)
		if obs.Scope != tc.want {
			t.Errorf("Row 3 Scope privacy=%q: got %v want %v", tc.privacy, obs.Scope, tc.want)
		}
	}
}

// TestConflictAdapter_Row04_Type verifies Row 4: EpistemicType → ObsType mapping.
func TestConflictAdapter_Row04_Type(t *testing.T) {
	cases := []struct {
		epistemic string
		want      models.ObservationType
	}{
		{"fact", models.ObsTypeDiscovery},
		{"experience", models.ObsTypeFeature},
		{"opinion", models.ObsTypeDecision},
		{"observation", models.ObsTypeChange},
		{"", models.ObsTypeChange}, // default
	}
	for _, tc := range cases {
		m := goldenMemory()
		m.EpistemicType = tc.epistemic
		obs := writelint.ProjectMemoryToObservation(m)
		if obs.Type != tc.want {
			t.Errorf("Row 4 Type epistemic=%q: got %v want %v", tc.epistemic, obs.Type, tc.want)
		}
	}
}

// TestConflictAdapter_Row05_Title verifies Row 5: Title = first 100 chars of Content;
// NullString{Valid:false} if content < 5 chars.
func TestConflictAdapter_Row05_Title(t *testing.T) {
	// Case: content >= 5 chars → valid title
	m := goldenMemory()
	obs := writelint.ProjectMemoryToObservation(m)
	if !obs.Title.Valid {
		t.Fatal("Row 5 Title: expected Valid=true for long content")
	}
	want := m.Content
	if len(want) > 100 {
		want = want[:100]
	}
	if obs.Title.String != want {
		t.Errorf("Row 5 Title: got %q want %q", obs.Title.String, want)
	}

	// Case: content < 5 chars → invalid title
	m2 := goldenMemory()
	m2.Content = "ab"
	obs2 := writelint.ProjectMemoryToObservation(m2)
	if obs2.Title.Valid {
		t.Errorf("Row 5 Title: expected Valid=false for short content, got String=%q", obs2.Title.String)
	}
}

// TestConflictAdapter_Row05_TitleTruncatesAt100 verifies truncation at 100 chars.
func TestConflictAdapter_Row05_TitleTruncatesAt100(t *testing.T) {
	m := goldenMemory()
	m.Content = string(make([]byte, 200))
	for i := range []byte(m.Content) {
		m.Content = m.Content[:i] + "x" + m.Content[i+1:]
	}
	// 200-char string of 'x'
	obs := writelint.ProjectMemoryToObservation(m)
	if len(obs.Title.String) != 100 {
		t.Errorf("Row 5 Title truncation: expected 100 chars, got %d", len(obs.Title.String))
	}
}

// TestConflictAdapter_Row06_Narrative verifies Row 6: Narrative = Content, Valid=true.
func TestConflictAdapter_Row06_Narrative(t *testing.T) {
	m := goldenMemory()
	obs := writelint.ProjectMemoryToObservation(m)
	if !obs.Narrative.Valid {
		t.Fatal("Row 6 Narrative: expected Valid=true for non-empty content")
	}
	if obs.Narrative.String != m.Content {
		t.Errorf("Row 6 Narrative: got %q want %q", obs.Narrative.String, m.Content)
	}

	// Empty content → not set (zero value)
	m2 := goldenMemory()
	m2.Content = ""
	obs2 := writelint.ProjectMemoryToObservation(m2)
	if obs2.Narrative != (sql.NullString{}) {
		t.Errorf("Row 6 Narrative: expected zero NullString for empty content, got %+v", obs2.Narrative)
	}
}

// TestConflictAdapter_Row07_Concepts verifies Row 7: Concepts = Tags direct copy.
func TestConflictAdapter_Row07_Concepts(t *testing.T) {
	m := goldenMemory()
	obs := writelint.ProjectMemoryToObservation(m)
	if len(obs.Concepts) != len(m.Tags) {
		t.Fatalf("Row 7 Concepts: got len=%d want %d", len(obs.Concepts), len(m.Tags))
	}
	for i, tag := range m.Tags {
		if string(obs.Concepts[i]) != tag {
			t.Errorf("Row 7 Concepts[%d]: got %q want %q", i, obs.Concepts[i], tag)
		}
	}
}

// TestConflictAdapter_Row08_FilesModified verifies Row 8: tags with "file:" prefix stripped.
func TestConflictAdapter_Row08_FilesModified(t *testing.T) {
	m := goldenMemory()
	// goldenMemory has "file:internal/writelint/orchestrator.go"
	obs := writelint.ProjectMemoryToObservation(m)
	if len(obs.FilesModified) != 1 {
		t.Fatalf("Row 8 FilesModified: got %d entries want 1; %v", len(obs.FilesModified), obs.FilesModified)
	}
	want := "internal/writelint/orchestrator.go"
	if string(obs.FilesModified[0]) != want {
		t.Errorf("Row 8 FilesModified[0]: got %q want %q", obs.FilesModified[0], want)
	}
}

// TestConflictAdapter_Row09_FilesRead verifies Row 9: tags with "read:" prefix stripped.
func TestConflictAdapter_Row09_FilesRead(t *testing.T) {
	m := goldenMemory()
	// goldenMemory has "read:cmd/engram/main.go"
	obs := writelint.ProjectMemoryToObservation(m)
	if len(obs.FilesRead) != 1 {
		t.Fatalf("Row 9 FilesRead: got %d entries want 1; %v", len(obs.FilesRead), obs.FilesRead)
	}
	want := "cmd/engram/main.go"
	if string(obs.FilesRead[0]) != want {
		t.Errorf("Row 9 FilesRead[0]: got %q want %q", obs.FilesRead[0], want)
	}
}

// TestConflictAdapter_Row10_SourceAgent_Contract verifies Row 10: SourceAgent → direct copy.
func TestConflictAdapter_Row10_SourceAgent_Contract(t *testing.T) {
	m := goldenMemory()
	obs := writelint.ProjectMemoryToObservation(m)
	if string(obs.AgentSource) != m.SourceAgent {
		t.Errorf("Row 10 SourceAgent: got %q want %q", obs.AgentSource, m.SourceAgent)
	}
}

// TestConflictAdapter_Row11_CreatedAtEpoch_Contract verifies Row 11.
func TestConflictAdapter_Row11_CreatedAtEpoch_Contract(t *testing.T) {
	m := goldenMemory()
	obs := writelint.ProjectMemoryToObservation(m)
	want := m.CreatedAt.UnixMilli()
	if obs.CreatedAtEpoch != want {
		t.Errorf("Row 11 CreatedAtEpoch: got %d want %d", obs.CreatedAtEpoch, want)
	}
}

// TestConflictAdapter_Row12_SDKSessionID_Contract verifies Row 12.
func TestConflictAdapter_Row12_SDKSessionID_Contract(t *testing.T) {
	// Non-empty SourceSessions → first element
	m := goldenMemory()
	obs := writelint.ProjectMemoryToObservation(m)
	if obs.SDKSessionID != m.SourceSessions[0] {
		t.Errorf("Row 12 SDKSessionID: got %q want %q", obs.SDKSessionID, m.SourceSessions[0])
	}

	// Empty SourceSessions → ""
	m2 := goldenMemory()
	m2.SourceSessions = nil
	obs2 := writelint.ProjectMemoryToObservation(m2)
	if obs2.SDKSessionID != "" {
		t.Errorf("Row 12 SDKSessionID empty: got %q want %q", obs2.SDKSessionID, "")
	}
}

// TestConflictAdapter_Row13_IsSuperseded_Contract verifies Row 13.
func TestConflictAdapter_Row13_IsSuperseded_Contract(t *testing.T) {
	// active → false
	m := goldenMemory()
	obs := writelint.ProjectMemoryToObservation(m)
	if obs.IsSuperseded {
		t.Error("Row 13 IsSuperseded: expected false for status=active")
	}

	// superseded → true
	m2 := goldenMemory()
	m2.Status = "superseded"
	obs2 := writelint.ProjectMemoryToObservation(m2)
	if !obs2.IsSuperseded {
		t.Error("Row 13 IsSuperseded: expected true for status=superseded")
	}
}

// TestConflictAdapter_AllRows_GoldenFixture runs all 13 rows together to
// ensure no regression in the complete mapping.
func TestConflictAdapter_AllRows_GoldenFixture(t *testing.T) {
	m := goldenMemory()
	obs := writelint.ProjectMemoryToObservation(m)

	if obs.ID != 42 {
		t.Errorf("Row 1: ID mismatch")
	}
	if obs.Project != "engram-golden" {
		t.Errorf("Row 2: Project mismatch")
	}
	if obs.Scope != models.ScopeGlobal { // "shared" → ScopeGlobal (ObservationScope)
		t.Errorf("Row 3: Scope mismatch; want ScopeGlobal for 'shared', got %v", obs.Scope)
	}
	if obs.Type != models.ObsTypeDiscovery { // "fact" → Discovery
		t.Errorf("Row 4: Type mismatch; want Discovery for 'fact', got %v", obs.Type)
	}
	if !obs.Title.Valid || len(obs.Title.String) == 0 {
		t.Errorf("Row 5: Title not valid")
	}
	if !obs.Narrative.Valid || obs.Narrative.String != m.Content {
		t.Errorf("Row 6: Narrative mismatch")
	}
	if len(obs.Concepts) != 3 { // 3 tags in goldenMemory
		t.Errorf("Row 7: Concepts len got %d want 3", len(obs.Concepts))
	}
	if len(obs.FilesModified) != 1 {
		t.Errorf("Row 8: FilesModified len got %d want 1", len(obs.FilesModified))
	}
	if len(obs.FilesRead) != 1 {
		t.Errorf("Row 9: FilesRead len got %d want 1", len(obs.FilesRead))
	}
	if string(obs.AgentSource) != "claude-code" {
		t.Errorf("Row 10: AgentSource mismatch")
	}
	if obs.CreatedAtEpoch != m.CreatedAt.UnixMilli() {
		t.Errorf("Row 11: CreatedAtEpoch mismatch")
	}
	if obs.SDKSessionID != "sess-abc-123" {
		t.Errorf("Row 12: SDKSessionID mismatch; got %q want %q", obs.SDKSessionID, "sess-abc-123")
	}
	if obs.IsSuperseded {
		t.Errorf("Row 13: IsSuperseded should be false for status=active")
	}
}
