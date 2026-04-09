// Package models contains domain models for engram.
package models

// ConceptWeight represents a configurable weight for a concept.
type ConceptWeight struct {
	Concept   string  `db:"concept" json:"concept"`
	UpdatedAt string  `db:"updated_at" json:"updated_at"`
	Weight    float64 `db:"weight" json:"weight"`
}

// UserFeedbackType represents the type of user feedback.
type UserFeedbackType int

const (
	// FeedbackNegative represents a thumbs down.
	FeedbackNegative UserFeedbackType = -1
	// FeedbackNeutral represents no feedback.
	FeedbackNeutral UserFeedbackType = 0
	// FeedbackPositive represents a thumbs up.
	FeedbackPositive UserFeedbackType = 1
)

// DefaultConceptWeights contains the default weights for concepts.
// Higher weights indicate more important concepts.
var DefaultConceptWeights = map[string]float64{
	// Critical concepts (0.25-0.30)
	"security": 0.30, // Security issues are most critical

	// High importance (0.20-0.25)
	"gotcha":        0.25, // Gotchas prevent future mistakes
	"best-practice": 0.20, // Best practices guide development
	"anti-pattern":  0.20, // Anti-patterns prevent bad code

	// Medium importance (0.10-0.15)
	"architecture":     0.15, // Architectural decisions have lasting impact
	"performance":      0.15, // Performance patterns matter for scale
	"error-handling":   0.15, // Error handling prevents failures
	"pattern":          0.10, // General patterns are useful
	"testing":          0.10, // Testing knowledge helps quality
	"debugging":        0.10, // Debugging tips save time
	"problem-solution": 0.10, // Problem-solution pairs are actionable
	"trade-off":        0.10, // Trade-offs inform decisions

	// Lower importance (0.05)
	"workflow":      0.05, // Workflow optimizations are nice-to-have
	"tooling":       0.05, // Tooling preferences are subjective
	"how-it-works":  0.05, // Understanding is foundational but less urgent
	"why-it-exists": 0.05, // Context is helpful but less actionable
	"what-changed":  0.05, // Changes are informational
}

// TypeBaseScores contains the base importance multipliers for each observation type.
// These are multiplied with the core score to weight different observation types.
var TypeBaseScores = map[ObservationType]float64{
	ObsTypeBugfix:      1.3, // Bugfixes are valuable - prevent regressions
	ObsTypeFeature:     1.2, // New features expand capabilities
	ObsTypeDiscovery:   1.1, // Discoveries inform future work
	ObsTypeDecision:    1.1, // Architectural decisions guide development
	ObsTypeRefactor:    1.0, // Refactoring is neutral
	ObsTypeChange:      0.9, // Minor changes are slightly less important
	ObsTypeGuidance:    1.4, // Guidance is most actionable - behavioral corrections
	ObsTypePitfall:     1.3, // Pitfalls prevent repeated mistakes (same as bugfix)
	ObsTypeOperational: 1.0, // Operational knowledge is neutral
	ObsTypeTimeline:    0.1, // Timeline events are structural metadata, not knowledge
}

// DefaultSourceHalfLives contains the default half-life in days for each source type.
// Used by the scoring calculator for source-aware importance decay.
var DefaultSourceHalfLives = map[SourceType]float64{
	SourceManual:         30.0, // Explicit user knowledge decays slowly
	SourceToolVerified:   21.0, // Verified by tool execution (Edit, Write, Bash)
	SourceToolRead:       14.0, // Read-only observation (Read, Grep, Glob)
	SourceWebFetch:       14.0, // External data (WebFetch, WebSearch)
	SourceTodoWrite:       7.0, // Task context, ephemeral
	SourceLLMDerived:     90.0, // Synthesized/curated knowledge
	SourceCrossModel:     60.0, // Multi-model consensus, high confidence
	SourceInstinctImport: 30.0, // Imported from prior system
	SourceBackfill:        7.0, // Already has separate penalty in calculator
	SourceUnknown:         7.0, // Conservative default
}

// ScoringConfig contains all scoring weights and parameters.
type ScoringConfig struct {
	ConceptWeights      map[string]float64     `json:"concept_weights"`
	SourceHalfLives     map[SourceType]float64 `json:"source_half_lives"`
	RecencyHalfLifeDays float64                `json:"recency_half_life_days"`
	FeedbackWeight      float64                `json:"feedback_weight"`
	ConceptWeight       float64                `json:"concept_weight"`
	RetrievalWeight     float64                `json:"retrieval_weight"`
	UtilityWeight       float64                `json:"utility_weight"`
	EffectivenessWeight float64                `json:"effectiveness_weight"`
	MinScore            float64                `json:"min_score"`
}

// DefaultScoringConfig returns the default scoring configuration.
func DefaultScoringConfig() *ScoringConfig {
	conceptWeights := make(map[string]float64, len(DefaultConceptWeights))
	for k, v := range DefaultConceptWeights {
		conceptWeights[k] = v
	}

	sourceHalfLives := make(map[SourceType]float64, len(DefaultSourceHalfLives))
	for k, v := range DefaultSourceHalfLives {
		sourceHalfLives[k] = v
	}

	return &ScoringConfig{
		RecencyHalfLifeDays: 7.0,  // Score halves every 7 days (fallback for unknown sources)
		FeedbackWeight:      0.30, // Feedback has moderate impact
		ConceptWeight:       0.20, // Concept weights have smaller impact
		RetrievalWeight:     0.15, // Retrieval has smallest impact
		UtilityWeight:       0.20, // Utility tracking has moderate impact
		EffectivenessWeight: 0.30, // Effectiveness from closed-loop learning has moderate impact
		ConceptWeights:      conceptWeights,
		SourceHalfLives:     sourceHalfLives,
		MinScore:            0.01, // Never completely disappear
	}
}

// TypeBaseScore returns the base weight for an observation type.
func TypeBaseScore(t ObservationType) float64 {
	if score, ok := TypeBaseScores[t]; ok {
		return score
	}
	return 1.0 // Default for unknown types
}
