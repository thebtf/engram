// Package models contains domain models for engram.
package models

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// RelationType constants
// ---------------------------------------------------------------------------

func TestRelationTypeConstants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		got  RelationType
		want string
	}{
		{RelationCauses, "causes"},
		{RelationFixes, "fixes"},
		{RelationSupersedes, "supersedes"},
		{RelationDependsOn, "depends_on"},
		{RelationRelatesTo, "relates_to"},
		{RelationEvolvesFrom, "evolves_from"},
		{RelationLeadsTo, "leads_to"},
		{RelationSimilarTo, "similar_to"},
		{RelationContradicts, "contradicts"},
		{RelationReinforces, "reinforces"},
		{RelationInvalidatedBy, "invalidated_by"},
		{RelationExplains, "explains"},
		{RelationSharesTheme, "shares_theme"},
		{RelationParallelCtx, "parallel_context"},
		{RelationSummarizes, "summarizes"},
		{RelationPartOf, "part_of"},
		{RelationPrefersOver, "prefers_over"},
		{RelationModifies, "modifies"},
		{RelationReads, "reads"},
		{RelationFollows, "follows"},
		{RelationPromptedBy, "prompted_by"},
		{RelationReferences, "references"},
		{RelationReferencedBy, "referenced_by"},
	}
	for _, c := range cases {
		c := c
		t.Run(string(c.got), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, RelationType(c.want), c.got)
		})
	}
}

// ---------------------------------------------------------------------------
// AllRelationTypes completeness
// ---------------------------------------------------------------------------

// TestAllRelationTypes_Completeness verifies that AllRelationTypes contains exactly
// the full set of RelationType constants — no extras, no omissions.
// This guards against the production slice drifting from the constants definition.
func TestAllRelationTypes_Completeness(t *testing.T) {
	t.Parallel()

	// Canonical set derived from the constants declared in relation.go.
	expected := []RelationType{
		RelationCauses,
		RelationFixes,
		RelationSupersedes,
		RelationDependsOn,
		RelationRelatesTo,
		RelationEvolvesFrom,
		RelationLeadsTo,
		RelationSimilarTo,
		RelationContradicts,
		RelationReinforces,
		RelationInvalidatedBy,
		RelationExplains,
		RelationSharesTheme,
		RelationParallelCtx,
		RelationSummarizes,
		RelationPartOf,
		RelationPrefersOver,
		RelationModifies,
		RelationReads,
		RelationFollows,
		RelationPromptedBy,
		RelationReferences,
		RelationReferencedBy,
	}

	assert.ElementsMatch(t, expected, AllRelationTypes,
		"AllRelationTypes must contain exactly all RelationType constants — update AllRelationTypes when adding a new constant")
}

// ---------------------------------------------------------------------------
// DetectFileOverlapRelation
// ---------------------------------------------------------------------------

func TestDetectFileOverlapRelation_Table(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		newer        *Observation
		older        *Observation
		wantRelType  RelationType
		wantMinConf  float64
		wantRelation bool
	}{
		{
			name: "no shared files",
			newer: &Observation{
				ID:            1,
				FilesModified: []string{"widget.go", "api.go"},
			},
			older: &Observation{
				ID:            2,
				FilesModified: []string{"config.go", "env.go"},
			},
			wantRelation: false,
		},
		{
			name: "shared modified files",
			newer: &Observation{
				ID:            10,
				Type:          ObsTypeRefactor,
				FilesModified: []string{"shared.go", "extra.go"},
			},
			older: &Observation{
				ID:            11,
				Type:          ObsTypeRefactor,
				FilesModified: []string{"shared.go", "other.go"},
			},
			wantRelation:  true,
			wantRelType:   RelationSupersedes,
			wantMinConf:   0.5,
		},
		{
			name: "bugfix on feature file",
			newer: &Observation{
				ID:            20,
				Type:          ObsTypeBugfix,
				FilesModified: []string{"handler.go"},
			},
			older: &Observation{
				ID:            21,
				Type:          ObsTypeFeature,
				FilesModified: []string{"handler.go"},
			},
			wantRelation:  true,
			wantRelType:   RelationFixes,
			wantMinConf:   0.6,
		},
		{
			name: "newer reads older modified",
			newer: &Observation{
				ID:            30,
				Type:          ObsTypeChange,
				FilesRead:     []string{"dep.go"},
				FilesModified: []string{"consumer.go"},
			},
			older: &Observation{
				ID:            31,
				Type:          ObsTypeDecision,
				FilesModified: []string{"dep.go"},
			},
			wantRelation:  true,
			wantMinConf:   0.5,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := DetectFileOverlapRelation(tc.newer, tc.older)

			if tc.wantRelation {
				require.NotNil(t, result, "expected non-nil relation")
				if tc.wantRelType != "" {
					assert.Equal(t, tc.wantRelType, result.RelationType)
				}
				assert.GreaterOrEqual(t, result.Confidence, tc.wantMinConf)
				assert.Equal(t, DetectionSourceFileOverlap, result.DetectionSource)
			} else {
				assert.Nil(t, result, "expected nil relation")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// DetectConceptOverlapRelation
// ---------------------------------------------------------------------------

func TestDetectConceptOverlapRelation_Table(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		newer       *Observation
		older       *Observation
		wantMinConf float64
		wantSource  RelationDetectionSource
		expect      bool
	}{
		{
			name:   "no shared concepts",
			newer:  &Observation{ID: 1, Concepts: []string{"database", "migration"}},
			older:  &Observation{ID: 2, Concepts: []string{"caching", "eviction"}},
			expect: false,
		},
		{
			name:        "single high-value shared concept",
			newer:       &Observation{ID: 3, Concepts: []string{"security", "tls"}},
			older:       &Observation{ID: 4, Concepts: []string{"security", "cert"}},
			expect:      true,
			wantMinConf: 0.4,
			wantSource:  DetectionSourceConceptOverlap,
		},
		{
			name:        "multiple shared concepts",
			newer:       &Observation{ID: 5, Concepts: []string{"auth", "api", "validation"}},
			older:       &Observation{ID: 6, Concepts: []string{"auth", "api", "logging"}},
			expect:      true,
			wantMinConf: 0.5,
			wantSource:  DetectionSourceConceptOverlap,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := DetectConceptOverlapRelation(tc.newer, tc.older)
			if tc.expect {
				require.NotNil(t, result)
				assert.GreaterOrEqual(t, result.Confidence, tc.wantMinConf)
				assert.Equal(t, tc.wantSource, result.DetectionSource)
			} else {
				assert.Nil(t, result)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// DetectTypeProgressionRelation
// ---------------------------------------------------------------------------

func TestDetectTypeProgressionRelation_Table(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		newerType   ObservationType
		olderType   ObservationType
		wantRelType RelationType
		expect      bool
	}{
		{"bugfix → discovery = fixes", ObsTypeBugfix, ObsTypeDiscovery, RelationFixes, true},
		{"bugfix → feature = fixes", ObsTypeBugfix, ObsTypeFeature, RelationFixes, true},
		{"feature → decision = depends_on", ObsTypeFeature, ObsTypeDecision, RelationDependsOn, true},
		{"refactor → discovery = evolves_from", ObsTypeRefactor, ObsTypeDiscovery, RelationEvolvesFrom, true},
		{"discovery → bugfix = no progression", ObsTypeDiscovery, ObsTypeBugfix, "", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			newer := &Observation{ID: 1, Type: tc.newerType}
			older := &Observation{ID: 2, Type: tc.olderType}
			result := DetectTypeProgressionRelation(newer, older)

			if tc.expect {
				require.NotNil(t, result)
				assert.Equal(t, tc.wantRelType, result.RelationType)
				assert.Equal(t, DetectionSourceTypeProgression, result.DetectionSource)
			} else {
				assert.Nil(t, result)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// DetectTemporalProximityRelation
// ---------------------------------------------------------------------------

func TestDetectTemporalProximityRelation_Table(t *testing.T) {
	t.Parallel()
	base := int64(1700000000000)

	cases := []struct {
		name         string
		newerSession string
		olderSession string
		newerTime    int64
		olderTime    int64
		expect       bool
	}{
		{
			name:         "same session within 5 min — related",
			newerSession: "sess-A",
			olderSession: "sess-A",
			newerTime:    base + 120_000, // 2 min later
			olderTime:    base,
			expect:       true,
		},
		{
			name:         "same session beyond 5 min — not related",
			newerSession: "sess-A",
			olderSession: "sess-A",
			newerTime:    base + 600_000, // 10 min later
			olderTime:    base,
			expect:       false,
		},
		{
			name:         "different sessions — not related",
			newerSession: "sess-A",
			olderSession: "sess-B",
			newerTime:    base + 30_000,
			olderTime:    base,
			expect:       false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			newer := &Observation{ID: 1, SDKSessionID: tc.newerSession, CreatedAtEpoch: tc.newerTime}
			older := &Observation{ID: 2, SDKSessionID: tc.olderSession, CreatedAtEpoch: tc.olderTime}
			result := DetectTemporalProximityRelation(newer, older)

			if tc.expect {
				require.NotNil(t, result)
				assert.Equal(t, DetectionSourceTemporalProximity, result.DetectionSource)
			} else {
				assert.Nil(t, result)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// DetectNarrativeMentionRelation
// ---------------------------------------------------------------------------

func TestDetectNarrativeMentionRelation_Table(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		narrative   string
		wantRelType RelationType
		expect      bool
	}{
		{"fixes language", "This commit fixes the authentication regression", RelationFixes, true},
		{"causes language", "This change caused memory pressure in production", RelationCauses, true},
		{"supersedes language", "This pattern supersedes the v1 approach", RelationSupersedes, true},
		{"depends on language", "This module depends on the session store", RelationDependsOn, true},
		{"no relation keyword", "Added pagination to the list endpoint", "", false},
	}

	older := &Observation{ID: 99}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			newer := &Observation{
				ID:        1,
				Narrative: sql.NullString{String: tc.narrative, Valid: true},
			}
			result := DetectNarrativeMentionRelation(newer, older)
			if tc.expect {
				require.NotNil(t, result)
				assert.Equal(t, tc.wantRelType, result.RelationType)
				assert.Equal(t, DetectionSourceNarrativeMention, result.DetectionSource)
			} else {
				assert.Nil(t, result)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// DetectRelationsWithExisting
// ---------------------------------------------------------------------------

func TestDetectRelationsWithExisting_FindsRelation(t *testing.T) {
	t.Parallel()
	newer := &Observation{
		ID:            100,
		SDKSessionID:  "sess-1",
		Project:       "backend",
		Type:          ObsTypeBugfix,
		FilesModified: []string{"auth.go"},
		Concepts:      []string{"security", "auth"},
		Narrative:     sql.NullString{String: "Fixed security vulnerability in auth module", Valid: true},
	}
	pool := []*Observation{
		{
			ID:            200,
			SDKSessionID:  "sess-1",
			Project:       "backend",
			Type:          ObsTypeDiscovery,
			FilesModified: []string{"auth.go"},
			Concepts:      []string{"security"},
		},
		{
			ID:            201,
			SDKSessionID:  "sess-2",
			Project:       "backend",
			Type:          ObsTypeFeature,
			FilesModified: []string{"other.go"},
			Concepts:      []string{"logging"},
		},
		{
			ID:           202,
			SDKSessionID: "sess-1",
			Project:      "other-proj", // different project — should be skipped
			Type:         ObsTypeDiscovery,
		},
	}

	results := DetectRelationsWithExisting(newer, pool, 0.4)

	require.NotEmpty(t, results, "expected at least one relation")

	var foundObs200 bool
	for _, r := range results {
		if r.TargetID == 200 {
			foundObs200 = true
			assert.GreaterOrEqual(t, r.Confidence, 0.5)
		}
		assert.NotEqual(t, int64(202), r.TargetID, "must not relate to different-project obs")
	}
	assert.True(t, foundObs200, "must detect relation with obs 200")
}

func TestDetectRelationsWithExisting_EmptyPool(t *testing.T) {
	t.Parallel()
	newer := &Observation{
		ID:      50,
		Project: "proj",
		Type:    ObsTypeBugfix,
	}
	results := DetectRelationsWithExisting(newer, []*Observation{}, 0.4)
	assert.Empty(t, results)
}
