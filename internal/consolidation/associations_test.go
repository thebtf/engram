package consolidation

import (
	"database/sql"
	"testing"
	"time"

	"github.com/thebtf/claude-mnemonic-plus/pkg/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
)

// AssociationsSuite tests unexported consolidation association helpers.
type AssociationsSuite struct {
	suite.Suite
	engine *AssociationEngine
}

func TestAssociationsSuite(t *testing.T) {
	suite.Run(t, new(AssociationsSuite))
}

func (s *AssociationsSuite) SetupTest() {
	s.engine = &AssociationEngine{config: DefaultAssociationConfig()}
}

func makeObservation(id int64, obsType models.ObservationType, createdAtMs int64, facts ...string) *models.Observation {
	return &models.Observation{
		ID:             id,
		Type:           obsType,
		CreatedAtEpoch:  createdAtMs,
		Facts:          facts,
		Title:          sql.NullString{String: "", Valid: false},
		Narrative:      sql.NullString{String: "", Valid: false},
		ImportanceScore: 0,
	}
}

func (s *AssociationsSuite) TestApplyTypePairRules_ContradictsExplainsSharesThemeParallelAndNil() {
	base := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC).UnixMilli()

	tests := []struct {
		name          string
		aType         models.ObservationType
		bType         models.ObservationType
		ageDiffDays   int
		similarity    float64
		expectedType  models.RelationType
		expectNil     bool
	}{
		{name: "contradicts for two decisions with low similarity", aType: models.ObsTypeDecision, bType: models.ObsTypeDecision, ageDiffDays: 50, similarity: 0.1, expectedType: models.RelationContradicts},
		{name: "explains for discovery and feature", aType: models.ObsTypeDiscovery, bType: models.ObsTypeFeature, ageDiffDays: 50, similarity: 0.9, expectedType: models.RelationExplains},
		{name: "explains for bugfix and refactor", aType: models.ObsTypeBugfix, bType: models.ObsTypeRefactor, ageDiffDays: 50, similarity: 0.9, expectedType: models.RelationExplains},
		{name: "shares theme for high similarity any type", aType: models.ObsTypeChange, bType: models.ObsTypeDiscovery, ageDiffDays: 90, similarity: 0.95, expectedType: models.RelationSharesTheme},
		{name: "parallel context when close in time and low similarity", aType: models.ObsTypeDecision, bType: models.ObsTypeFeature, ageDiffDays: 3, similarity: 0.1, expectedType: models.RelationParallelCtx},
		{name: "no relation when no condition matches", aType: models.ObsTypeFeature, bType: models.ObsTypeChange, ageDiffDays: 90, similarity: 0.6, expectNil: true},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			a := makeObservation(1, tt.aType, base-(int64(tt.ageDiffDays)*24*60*60*1000))
			b := makeObservation(2, tt.bType, base+0)
			result := s.engine.applyTypePairRules(a, b, tt.similarity)

			if tt.expectNil {
				assert.Nil(s.T(), result)
				return
			}

			assert.NotNil(s.T(), result)
			assert.Equal(s.T(), tt.expectedType, result.RelationType)
			assert.Equal(s.T(), int64(1), result.SourceID)
			assert.Equal(s.T(), int64(2), result.TargetID)
			assert.NotEmpty(s.T(), result.Reason)

			if tt.expectedType == models.RelationContradicts {
				assert.InDelta(s.T(), 0.6, result.Confidence, 1e-12)
			} else if tt.expectedType == models.RelationExplains {
				assert.InDelta(s.T(), 0.7, result.Confidence, 1e-12)
			} else if tt.expectedType == models.RelationSharesTheme {
				assert.InDelta(s.T(), tt.similarity, result.Confidence, 1e-12)
			} else if tt.expectedType == models.RelationParallelCtx {
				assert.InDelta(s.T(), 0.5, result.Confidence, 1e-12)
			}
		})
	}
}

func (s *AssociationsSuite) TestIsInsightOrPattern_TruthTable() {
	tests := []struct {
		name  string
		aType models.ObservationType
		bType models.ObservationType
		exp   bool
	}{
		{name: "discovery with refactor", aType: models.ObsTypeDiscovery, bType: models.ObsTypeRefactor, exp: true},
		{name: "feature with bugfix", aType: models.ObsTypeFeature, bType: models.ObsTypeBugfix, exp: true},
		{name: "refactor with change", aType: models.ObsTypeRefactor, bType: models.ObsTypeChange, exp: false},
		{name: "discovery with feature", aType: models.ObsTypeDiscovery, bType: models.ObsTypeFeature, exp: true},
		{name: "change with decision", aType: models.ObsTypeChange, bType: models.ObsTypeDecision, exp: false},
		{name: "bugfix with feature", aType: models.ObsTypeBugfix, bType: models.ObsTypeFeature, exp: true},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			a := &models.Observation{Type: tt.aType}
			b := &models.Observation{Type: tt.bType}
			assert.Equal(s.T(), tt.exp, isInsightOrPattern(a, b))
		})
	}
}

func (s *AssociationsSuite) TestAgeDifferenceDays_ComputesAbsoluteDifferenceInDays() {
	dayMs := int64(24 * 60 * 60 * 1000)
	tests := []struct {
		name string
		a    int64
		b    int64
		exp  float64
	}{
		{name: "same timestamp", a: 0, b: 0, exp: 0},
		{name: "five days", a: 0, b: 5 * dayMs, exp: 5},
		{name: "negative order same magnitude", a: 8 * dayMs, b: 3 * dayMs, exp: 5},
		{name: "absolute difference", a: -5 * dayMs, b: 0, exp: 5},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			assert.InDelta(s.T(), tt.exp, ageDifferenceDays(tt.a, tt.b), 1e-12)
		})
	}
}

func (s *AssociationsSuite) TestObservationText_UsesTitleNarrativeFacts() {
	tests := []struct {
		name     string
		obs      *models.Observation
		expected string
	}{
		{
			name: "title narrative and facts",
			obs: &models.Observation{
				Title:     sql.NullString{String: "Title", Valid: true},
				Narrative: sql.NullString{String: "Narrative", Valid: true},
				Facts:     []string{"fact one", "", "fact two"},
			},
			expected: "Title Narrative fact one fact two",
		},
		{
			name: "title only",
			obs: &models.Observation{
				Title: sql.NullString{String: "Title", Valid: true},
			},
			expected: "Title",
		},
		{
			name: "narrative only",
			obs: &models.Observation{
				Narrative: sql.NullString{String: "Narrative", Valid: true},
			},
			expected: "Narrative",
		},
		{
			name: "facts only",
			obs: &models.Observation{
				Facts: []string{"A", "", "B"},
			},
			expected: "A B",
		},
		{
			name: "empty observation",
			obs:  &models.Observation{},
			expected: "",
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			assert.Equal(s.T(), tt.expected, observationText(tt.obs))
		})
	}
}

func (s *AssociationsSuite) TestSampleObservations_Behavior() {
	all := []*models.Observation{
		{ID: 1},
		{ID: 2},
		{ID: 3},
		{ID: 4},
	}

	for _, tc := range []struct {
		name       string
		n          int
		expectedN  int
		shouldSame bool
	}{
		{name: "sample smaller", n: 2, expectedN: 2},
		{name: "sample equal", n: 4, expectedN: 4, shouldSame: true},
		{name: "sample zero", n: 0, expectedN: 0},
	} {
		s.Run(tc.name, func() {
			result := sampleObservations(all, tc.n)
			assert.Len(s.T(), result, tc.expectedN)
			if tc.shouldSame {
				assert.Same(s.T(), all, result)
			}

			seen := map[int64]bool{}
			for _, obs := range result {
				assert.NotNil(s.T(), obs)
				seen[obs.ID] = true
			}
			if tc.expectedN == 2 {
				assert.Len(s.T(), seen, 2)
			}
		})
	}
}
