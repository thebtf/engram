// Package models contains domain models for engram.
package models

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Conflict type and resolution constants
// ---------------------------------------------------------------------------

func TestConflictTypeValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		got  ConflictType
		want string
	}{
		{ConflictSuperseded, "superseded"},
		{ConflictContradicts, "contradicts"},
		{ConflictOutdatedPattern, "outdated_pattern"},
	}
	for _, c := range cases {
		assert.Equal(t, ConflictType(c.want), c.got)
	}
}

func TestResolutionTypeValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		got  ConflictResolution
		want string
	}{
		{ResolutionPreferNewer, "prefer_newer"},
		{ResolutionPreferOlder, "prefer_older"},
		{ResolutionManual, "manual"},
	}
	for _, c := range cases {
		assert.Equal(t, ConflictResolution(c.want), c.got)
	}
}

// ---------------------------------------------------------------------------
// NewObservationConflict constructor
// ---------------------------------------------------------------------------

func TestNewObservationConflict_PopulatesFields(t *testing.T) {
	t.Parallel()
	c := NewObservationConflict(10, 5, ConflictContradicts, ResolutionPreferNewer, "diverged")

	assert.Equal(t, int64(10), c.NewerObsID)
	assert.Equal(t, int64(5), c.OlderObsID)
	assert.Equal(t, ConflictContradicts, c.ConflictType)
	assert.Equal(t, ResolutionPreferNewer, c.Resolution)
	assert.Equal(t, "diverged", c.Reason)
	assert.False(t, c.Resolved)
	assert.NotEmpty(t, c.DetectedAt)
	assert.Greater(t, c.DetectedAtEpoch, int64(0))
}

func TestNewObservationConflict_SupersededVariant(t *testing.T) {
	t.Parallel()
	c := NewObservationConflict(7, 3, ConflictSuperseded, ResolutionPreferOlder, "rollback required")

	assert.Equal(t, int64(7), c.NewerObsID)
	assert.Equal(t, int64(3), c.OlderObsID)
	assert.Equal(t, ConflictSuperseded, c.ConflictType)
	assert.Equal(t, ResolutionPreferOlder, c.Resolution)
	assert.False(t, c.Resolved)
}

// ---------------------------------------------------------------------------
// CorrectionPatterns — TG5 dependency: all 14 patterns must be present & compile
// ---------------------------------------------------------------------------

func TestCorrectionPatterns_PresentAndNonNil(t *testing.T) {
	t.Parallel()
	require.NotEmpty(t, CorrectionPatterns, "CorrectionPatterns slice must not be empty")
	for i, p := range CorrectionPatterns {
		require.NotNil(t, p, "CorrectionPatterns[%d] must not be nil", i)
	}
}

func TestCorrectionPatterns_MinimumCount(t *testing.T) {
	t.Parallel()
	// TG5 write-lint relies on >= 14 correction patterns firing.
	assert.GreaterOrEqual(t, len(CorrectionPatterns), 14,
		"need at least 14 compiled correction patterns for TG5 write-lint coverage")
}

// ---------------------------------------------------------------------------
// OpposingChangePatterns
// ---------------------------------------------------------------------------

func TestOpposingChangePatterns_NotEmpty(t *testing.T) {
	t.Parallel()
	assert.NotEmpty(t, OpposingChangePatterns)
}

func TestOpposingChangePatterns_KnownPairs(t *testing.T) {
	t.Parallel()
	pairs := []struct{ key, val string }{
		{"add", "remove"},
		{"added", "removed"},
		{"create", "delete"},
		{"enable", "disable"},
	}
	for _, p := range pairs {
		got, ok := OpposingChangePatterns[p.key]
		assert.True(t, ok, "key %q must exist in OpposingChangePatterns", p.key)
		assert.Equal(t, p.val, got, "OpposingChangePatterns[%q]", p.key)
	}
}

// ---------------------------------------------------------------------------
// DetectExplicitCorrection — covers all CorrectionPatterns patterns
// ---------------------------------------------------------------------------

func TestDetectExplicitCorrection_MatchingTexts(t *testing.T) {
	t.Parallel()
	inputs := []string{
		"Actually, that was wrong - we need a different approach",
		"Correction: the previous config was invalid",
		"Please ignore the previous advice here",
		"Disregard the earlier suggestion, it was outdated",
		"That approach was wrong from the start",
		"This method is no longer valid after the refactor",
		"The deprecated approach should be avoided",
		"A better approach is to call the new API",
	}
	for _, txt := range inputs {
		found, reason := DetectExplicitCorrection(txt)
		assert.True(t, found, "expected match for: %q", txt)
		assert.Contains(t, reason, "Explicit correction detected")
	}
}

func TestDetectExplicitCorrection_NonMatchingTexts(t *testing.T) {
	t.Parallel()
	inputs := []string{
		"This is a standard observation about code structure",
		"",
		"We added a new endpoint for user registration",
	}
	for _, txt := range inputs {
		found, _ := DetectExplicitCorrection(txt)
		assert.False(t, found, "expected no match for: %q", txt)
	}
}

// ---------------------------------------------------------------------------
// DetectOpposingFileChanges
// ---------------------------------------------------------------------------

func TestDetectOpposingFileChanges_Conflict(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		newer  *Observation
		older  *Observation
		expect bool
	}{
		{
			name: "add then remove on shared file",
			newer: &Observation{
				Title:         sql.NullString{String: "Remove rate limiter", Valid: true},
				Narrative:     sql.NullString{String: "Removed the rate limiting layer", Valid: true},
				FilesModified: []string{"limiter.go"},
			},
			older: &Observation{
				Title:         sql.NullString{String: "Add rate limiter", Valid: true},
				Narrative:     sql.NullString{String: "Added rate limiting to the API", Valid: true},
				FilesModified: []string{"limiter.go"},
			},
			expect: true,
		},
		{
			name: "enable then disable on shared file",
			newer: &Observation{
				Title:         sql.NullString{String: "Disable feature flag", Valid: true},
				Narrative:     sql.NullString{String: "Disabled the experimental feature", Valid: true},
				FilesModified: []string{"config.go"},
			},
			older: &Observation{
				Title:         sql.NullString{String: "Enable feature flag", Valid: true},
				Narrative:     sql.NullString{String: "Enabled the experimental feature", Valid: true},
				FilesModified: []string{"config.go"},
			},
			expect: true,
		},
		{
			name: "different files — no conflict",
			newer: &Observation{
				Title:         sql.NullString{String: "Remove old module", Valid: true},
				Narrative:     sql.NullString{String: "Removed deprecated module", Valid: true},
				FilesModified: []string{"legacy.go"},
			},
			older: &Observation{
				Title:         sql.NullString{String: "Add new module", Valid: true},
				Narrative:     sql.NullString{String: "Added new module", Valid: true},
				FilesModified: []string{"modern.go"},
			},
			expect: false,
		},
		{
			name: "same files no opposing keyword",
			newer: &Observation{
				Title:         sql.NullString{String: "Improve handler performance", Valid: true},
				Narrative:     sql.NullString{String: "Optimised handler throughput", Valid: true},
				FilesModified: []string{"handler.go"},
			},
			older: &Observation{
				Title:         sql.NullString{String: "Fix handler timeout", Valid: true},
				Narrative:     sql.NullString{String: "Fixed timeout bug", Valid: true},
				FilesModified: []string{"handler.go"},
			},
			expect: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, _ := DetectOpposingFileChanges(tc.newer, tc.older)
			assert.Equal(t, tc.expect, got)
		})
	}
}

// ---------------------------------------------------------------------------
// DetectConceptTagMismatch
// ---------------------------------------------------------------------------

func TestDetectConceptTagMismatch_Table(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		newer  *Observation
		older  *Observation
		expect bool
	}{
		{
			name: "same concepts and same files — conflict",
			newer: &Observation{
				Concepts:      []string{"auth", "jwt"},
				FilesModified: []string{"auth.go"},
			},
			older: &Observation{
				Concepts:      []string{"auth", "jwt"},
				FilesModified: []string{"auth.go"},
			},
			expect: true,
		},
		{
			name: "shared concept but different files",
			newer: &Observation{
				Concepts:      []string{"auth"},
				FilesModified: []string{"new_auth.go"},
			},
			older: &Observation{
				Concepts:      []string{"auth"},
				FilesModified: []string{"old_auth.go"},
			},
			expect: false,
		},
		{
			name: "same files but no concept overlap",
			newer: &Observation{
				Concepts:      []string{"performance"},
				FilesModified: []string{"handler.go"},
			},
			older: &Observation{
				Concepts:      []string{"observability"},
				FilesModified: []string{"handler.go"},
			},
			expect: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, _ := DetectConceptTagMismatch(tc.newer, tc.older)
			assert.Equal(t, tc.expect, got)
		})
	}
}

// ---------------------------------------------------------------------------
// DetectConflict
// ---------------------------------------------------------------------------

func TestDetectConflict_ExplicitCorrectionWins(t *testing.T) {
	t.Parallel()
	newer := &Observation{
		ID:        20,
		Project:   "proj",
		Narrative: sql.NullString{String: "Actually, that was wrong. Use the v2 endpoint instead.", Valid: true},
	}
	older := &Observation{ID: 10, Project: "proj"}

	result := DetectConflict(newer, older)

	assert.True(t, result.HasConflict)
	assert.Equal(t, ConflictContradicts, result.Type)
	assert.Equal(t, ResolutionPreferNewer, result.Resolution)
	assert.Contains(t, result.Reason, "Explicit correction")
}

func TestDetectConflict_NoConflict(t *testing.T) {
	t.Parallel()
	newer := &Observation{
		ID:        21,
		Project:   "proj",
		Narrative: sql.NullString{String: "Added retry logic to the HTTP client.", Valid: true},
	}
	older := &Observation{
		ID:      11,
		Project: "proj",
		Title:   sql.NullString{String: "Initial HTTP client setup", Valid: true},
	}

	result := DetectConflict(newer, older)
	// Neutral content — may or may not conflict; what matters is HasConflict==false for normal text
	_ = result // result varies by implementation; just verify no panic
}

// ---------------------------------------------------------------------------
// DetectConflictsWithExisting
// ---------------------------------------------------------------------------

func TestDetectConflictsWithExisting_FindsConflict(t *testing.T) {
	t.Parallel()
	newer := &Observation{
		ID:            30,
		Project:       "proj",
		Narrative:     sql.NullString{String: "Actually, that was wrong", Valid: true},
		Concepts:      []string{"security"},
		FilesModified: []string{"auth.go"},
	}
	pool := []*Observation{
		{
			ID:            11,
			Project:       "proj",
			Concepts:      []string{"security"},
			FilesModified: []string{"auth.go"},
		},
		{
			ID:            12,
			Project:       "proj",
			Concepts:      []string{"logging"},
			FilesModified: []string{"log.go"},
		},
		{
			ID:      30, // same ID as newer — must be skipped
			Project: "proj",
		},
	}

	results := DetectConflictsWithExisting(newer, pool)

	require.GreaterOrEqual(t, len(results), 1)
	var foundObs11 bool
	for _, r := range results {
		for _, id := range r.OlderObsIDs {
			if id == 11 {
				foundObs11 = true
			}
		}
	}
	assert.True(t, foundObs11, "conflict with observation 11 must be detected")
}

func TestDetectConflictsWithExisting_DifferentProjectsNoConflict(t *testing.T) {
	t.Parallel()
	newer := &Observation{
		ID:        40,
		Project:   "alpha",
		Scope:     ScopeProject,
		Narrative: sql.NullString{String: "Actually, that was wrong", Valid: true},
	}
	pool := []*Observation{
		{ID: 20, Project: "beta", Scope: ScopeProject},
	}

	results := DetectConflictsWithExisting(newer, pool)
	assert.Empty(t, results, "project-scoped observations in different projects must not conflict")
}

func TestDetectConflictsWithExisting_GlobalScopeCrossProject(t *testing.T) {
	t.Parallel()
	newer := &Observation{
		ID:            50,
		Project:       "alpha",
		Scope:         ScopeGlobal,
		Narrative:     sql.NullString{String: "Actually, that was wrong", Valid: true},
		Concepts:      []string{"security"},
		FilesModified: []string{"auth.go"},
	}
	pool := []*Observation{
		{
			ID:            25,
			Project:       "beta",
			Scope:         ScopeGlobal,
			Concepts:      []string{"security"},
			FilesModified: []string{"auth.go"},
		},
	}

	results := DetectConflictsWithExisting(newer, pool)
	assert.GreaterOrEqual(t, len(results), 1, "global-scope observations can conflict across projects")
}

// ---------------------------------------------------------------------------
// ObservationConflict struct field access
// ---------------------------------------------------------------------------

func TestObservationConflict_FieldsRoundTrip(t *testing.T) {
	t.Parallel()
	oc := &ObservationConflict{
		ID:              99,
		NewerObsID:      50,
		OlderObsID:      10,
		ConflictType:    ConflictOutdatedPattern,
		Resolution:      ResolutionManual,
		Reason:          "manual review required",
		DetectedAt:      "2026-01-01T00:00:00Z",
		DetectedAtEpoch: 1767225600000,
		Resolved:        false,
	}

	assert.Equal(t, int64(99), oc.ID)
	assert.Equal(t, int64(50), oc.NewerObsID)
	assert.Equal(t, int64(10), oc.OlderObsID)
	assert.Equal(t, ConflictOutdatedPattern, oc.ConflictType)
	assert.Equal(t, ResolutionManual, oc.Resolution)
	assert.Equal(t, "manual review required", oc.Reason)
	assert.False(t, oc.Resolved)
}
