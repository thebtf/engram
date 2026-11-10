// Package writelint contains conflict adapter integration checks.
package writelint_test

import (
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
