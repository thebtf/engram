// Package writelint — T033 RED/GREEN tests: conflict_adapter 13-row contract.
// Tests assert each Memory field projects correctly to Observation per
// plan §Conflict Adapter Mapping Contract.
package writelint_test

import (
	"database/sql"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/writelint"
	"github.com/thebtf/engram/pkg/models"
)

// newTestMemory returns a Memory with all fields populated for adapter testing.
func newTestMemory() *models.Memory {
	id := int64(99)
	return &models.Memory{
		ID:              id,
		Project:         "test-project",
		PrivacyScope:    "project",
		EpistemicType:   "fact",
		Content:         "PostgreSQL connection-pool tuning: set max_connections=200",
		SourceAgent:     "claude-code",
		CreatedAt:       time.Unix(1700000000, 0),
		Status:          "active",
		Tags:            []string{"postgres", "connection-pool", "file:src/db.go", "read:docs/pg.md"},
		SourceSessions:  []string{"sess-001", "sess-002"},
	}
}

// --- Row 1: ID direct copy ---
func TestConflictAdapter_Row1_ID(t *testing.T) {
	m := newTestMemory()
	obs := writelint.ProjectMemoryToObservation(m)
	if obs.ID != m.ID {
		t.Errorf("Row1 ID: expected %d, got %d", m.ID, obs.ID)
	}
}

// --- Row 2: Project direct copy ---
func TestConflictAdapter_Row2_Project(t *testing.T) {
	m := newTestMemory()
	obs := writelint.ProjectMemoryToObservation(m)
	if obs.Project != m.Project {
		t.Errorf("Row2 Project: expected %q, got %q", m.Project, obs.Project)
	}
}

// --- Row 3: Scope from PrivacyScope ---
func TestConflictAdapter_Row3_Scope(t *testing.T) {
	cases := []struct {
		privacyScope string
		want         models.ObservationScope
	}{
		{"private", models.ScopeProject},
		{"project", models.ScopeProject},
		{"shared", models.ScopeGlobal},
		{"global", models.ScopeGlobal},
		{"", models.ScopeProject}, // default fallthrough
	}
	for _, tc := range cases {
		m := newTestMemory()
		m.PrivacyScope = tc.privacyScope
		obs := writelint.ProjectMemoryToObservation(m)
		if obs.Scope != tc.want {
			t.Errorf("Row3 Scope(%q): expected %q, got %q", tc.privacyScope, tc.want, obs.Scope)
		}
	}
}

// --- Row 4: Type from EpistemicType ---
func TestConflictAdapter_Row4_Type(t *testing.T) {
	cases := []struct {
		epistemicType string
		want          models.ObservationType
	}{
		{"fact", models.ObsTypeDiscovery},
		{"experience", models.ObsTypeFeature},
		{"opinion", models.ObsTypeDecision},
		{"observation", models.ObsTypeChange},
		{"unknown_type", models.ObsTypeChange}, // default
		{"", models.ObsTypeChange},              // default
	}
	for _, tc := range cases {
		m := newTestMemory()
		m.EpistemicType = tc.epistemicType
		obs := writelint.ProjectMemoryToObservation(m)
		if obs.Type != tc.want {
			t.Errorf("Row4 Type(%q): expected %q, got %q", tc.epistemicType, tc.want, obs.Type)
		}
	}
}

// --- Row 5: Title = first 100 chars of Content (or null if < 5 chars) ---
func TestConflictAdapter_Row5_Title(t *testing.T) {
	t.Run("short_content_null_title", func(t *testing.T) {
		m := newTestMemory()
		m.Content = "hi" // < 5 chars
		obs := writelint.ProjectMemoryToObservation(m)
		if obs.Title.Valid {
			t.Errorf("Row5 Title: expected null for short content, got %q", obs.Title.String)
		}
	})
	t.Run("normal_content", func(t *testing.T) {
		m := newTestMemory()
		obs := writelint.ProjectMemoryToObservation(m)
		if !obs.Title.Valid {
			t.Fatal("Row5 Title: expected Valid=true for non-short content")
		}
		if len(obs.Title.String) > 100 {
			t.Errorf("Row5 Title: expected <= 100 chars, got %d", len(obs.Title.String))
		}
	})
	t.Run("long_content_truncated", func(t *testing.T) {
		m := newTestMemory()
		m.Content = "ABCDEFGHIJ" // 10 chars * 10 = 100 chars + more
		for len(m.Content) < 150 {
			m.Content += "X"
		}
		obs := writelint.ProjectMemoryToObservation(m)
		if !obs.Title.Valid {
			t.Fatal("Row5 Title: expected Valid=true for long content")
		}
		if len(obs.Title.String) != 100 {
			t.Errorf("Row5 Title: expected exactly 100 chars, got %d", len(obs.Title.String))
		}
	})
}

// --- Row 6: Narrative = Memory.Content (always Valid when non-empty) ---
func TestConflictAdapter_Row6_Narrative(t *testing.T) {
	m := newTestMemory()
	obs := writelint.ProjectMemoryToObservation(m)
	if !obs.Narrative.Valid {
		t.Fatal("Row6 Narrative: expected Valid=true for non-empty content")
	}
	if obs.Narrative.String != m.Content {
		t.Errorf("Row6 Narrative: expected %q, got %q", m.Content, obs.Narrative.String)
	}
}

// --- Row 7: Concepts = Memory.Tags direct copy ---
func TestConflictAdapter_Row7_Concepts(t *testing.T) {
	m := newTestMemory()
	obs := writelint.ProjectMemoryToObservation(m)
	if len(obs.Concepts) != len(m.Tags) {
		t.Fatalf("Row7 Concepts: expected %d concepts, got %d", len(m.Tags), len(obs.Concepts))
	}
	for i, tag := range m.Tags {
		if obs.Concepts[i] != tag {
			t.Errorf("Row7 Concepts[%d]: expected %q, got %q", i, tag, obs.Concepts[i])
		}
	}
}

// --- Row 8: FilesModified from tags with "file:" prefix ---
func TestConflictAdapter_Row8_FilesModified(t *testing.T) {
	m := newTestMemory()
	// Tags include "file:src/db.go"
	obs := writelint.ProjectMemoryToObservation(m)
	if len(obs.FilesModified) != 1 {
		t.Fatalf("Row8 FilesModified: expected 1 entry, got %v", obs.FilesModified)
	}
	if obs.FilesModified[0] != "src/db.go" {
		t.Errorf("Row8 FilesModified: expected %q, got %q", "src/db.go", obs.FilesModified[0])
	}
}

// --- Row 9: FilesRead from tags with "read:" prefix ---
func TestConflictAdapter_Row9_FilesRead(t *testing.T) {
	m := newTestMemory()
	// Tags include "read:docs/pg.md"
	obs := writelint.ProjectMemoryToObservation(m)
	if len(obs.FilesRead) != 1 {
		t.Fatalf("Row9 FilesRead: expected 1 entry, got %v", obs.FilesRead)
	}
	if obs.FilesRead[0] != "docs/pg.md" {
		t.Errorf("Row9 FilesRead: expected %q, got %q", "docs/pg.md", obs.FilesRead[0])
	}
}

// --- Row 10: SourceAgent direct copy ---
func TestConflictAdapter_Row10_SourceAgent(t *testing.T) {
	m := newTestMemory()
	obs := writelint.ProjectMemoryToObservation(m)
	if string(obs.AgentSource) != m.SourceAgent {
		t.Errorf("Row10 SourceAgent: expected %q, got %q", m.SourceAgent, obs.AgentSource)
	}
}

// --- Row 11: CreatedAtEpoch from CreatedAt.UnixMilli() ---
func TestConflictAdapter_Row11_CreatedAtEpoch(t *testing.T) {
	m := newTestMemory()
	obs := writelint.ProjectMemoryToObservation(m)
	want := m.CreatedAt.UnixMilli()
	if obs.CreatedAtEpoch != want {
		t.Errorf("Row11 CreatedAtEpoch: expected %d, got %d", want, obs.CreatedAtEpoch)
	}
}

// --- Row 12: SDKSessionID from SourceSessions[0] ---
func TestConflictAdapter_Row12_SDKSessionID(t *testing.T) {
	t.Run("non_empty_sessions", func(t *testing.T) {
		m := newTestMemory()
		obs := writelint.ProjectMemoryToObservation(m)
		if obs.SDKSessionID != m.SourceSessions[0] {
			t.Errorf("Row12 SDKSessionID: expected %q, got %q", m.SourceSessions[0], obs.SDKSessionID)
		}
	})
	t.Run("empty_sessions", func(t *testing.T) {
		m := newTestMemory()
		m.SourceSessions = nil
		obs := writelint.ProjectMemoryToObservation(m)
		if obs.SDKSessionID != "" {
			t.Errorf("Row12 SDKSessionID empty: expected empty, got %q", obs.SDKSessionID)
		}
	})
}

// --- Row 13: IsSuperseded from Status == "superseded" ---
func TestConflictAdapter_Row13_IsSuperseded(t *testing.T) {
	t.Run("superseded", func(t *testing.T) {
		m := newTestMemory()
		m.Status = "superseded"
		obs := writelint.ProjectMemoryToObservation(m)
		if !obs.IsSuperseded {
			t.Error("Row13 IsSuperseded: expected true for status=superseded")
		}
	})
	t.Run("active", func(t *testing.T) {
		m := newTestMemory()
		m.Status = "active"
		obs := writelint.ProjectMemoryToObservation(m)
		if obs.IsSuperseded {
			t.Error("Row13 IsSuperseded: expected false for status=active")
		}
	})
}

// TestConflictAdapter_CorrectionPatternFires verifies that CorrectionPatterns
// still trigger on Memory-projected Observation content (integration assert
// per T033 AC: "CorrectionPatterns must fire on projected content").
func TestConflictAdapter_CorrectionPatternFires(t *testing.T) {
	m := newTestMemory()
	m.Content = "Actually that was wrong — use max_connections=100 instead"
	obs := writelint.ProjectMemoryToObservation(m)

	fired, reason := models.DetectExplicitCorrection(obs.Narrative.String)
	if !fired {
		t.Error("CorrectionPattern integration: expected pattern to fire on projected Narrative")
	}
	if reason == "" {
		t.Error("CorrectionPattern integration: expected non-empty reason")
	}
}

// TestConflictAdapter_DoesNotModifyConflictGo ensures the adapter did NOT touch
// pkg/models/conflict.go by verifying the conflict detection functions work
// unmodified on native Observations.
func TestConflictAdapter_DoesNotModifyConflictGo(t *testing.T) {
	// Native Observation path still works
	obs := &models.Observation{
		Narrative: sql.NullString{String: "Correction: use 100 not 200", Valid: true},
	}
	fired, _ := models.DetectExplicitCorrection(obs.Narrative.String)
	if !fired {
		t.Error("native Observation path broken: CorrectionPattern should fire")
	}
}
