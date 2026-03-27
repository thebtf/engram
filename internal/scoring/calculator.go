// Package scoring provides importance score calculation for observations.
package scoring

import (
	"math"
	"time"

	"github.com/thebtf/engram/pkg/models"
)

// Calculator computes importance scores for observations.
type Calculator struct {
	config *models.ScoringConfig
}

// NewCalculator creates a new scoring calculator.
// If config is nil, uses the default configuration.
func NewCalculator(config *models.ScoringConfig) *Calculator {
	if config == nil {
		config = models.DefaultScoringConfig()
	}
	return &Calculator{config: config}
}

// Calculate computes the importance score for an observation at the given time.
//
// The scoring formula:
//
//	FinalScore = (BaseScore × TypeWeight × RecencyDecay) + FeedbackContrib + ConceptContrib + RetrievalContrib + UtilityContrib
//
// Where:
//   - BaseScore = 1.0
//   - TypeWeight = observation type multiplier (e.g., bugfix=1.3, change=0.9)
//   - RecencyDecay = 0.5^(age_days / half_life_days) - halves every 7 days by default
//   - FeedbackContrib = user_feedback × feedback_weight
//   - ConceptContrib = sum(concept_weights) × concept_weight_factor
//   - RetrievalContrib = log2(retrieval_count + 1) × 0.1 × retrieval_weight
func (c *Calculator) Calculate(obs *models.Observation, now time.Time) float64 {
	return c.CalculateComponents(obs, now).FinalScore
}

// CalculateComponents returns the individual components of the importance score.
// Useful for debugging and explaining scores to users.
// This is the core calculation method - Calculate() delegates to this.
func (c *Calculator) CalculateComponents(obs *models.Observation, now time.Time) ScoreComponents {
	// 1. Get base type weight
	typeWeight := models.TypeBaseScore(obs.Type)

	// 2. Calculate recency decay: 0.5^(age_days / half_life_days)
	ageDays := now.Sub(time.UnixMilli(obs.CreatedAtEpoch)).Hours() / 24.0
	if ageDays < 0 {
		ageDays = 0 // Handle future timestamps gracefully
	}
	recencyDecay := math.Pow(0.5, ageDays/c.config.RecencyHalfLifeDays)

	// Core score = 1.0 × type_weight × recency_decay
	coreScore := 1.0 * typeWeight * recencyDecay

	// Source-aware decay: backfill observations carry lower base importance.
	// Formula: importance *= max(0.3, 1.0 - age_years * 0.2)
	sourcePenalty := 1.0
	if obs.SourceType == models.SourceBackfill {
		ageYears := ageDays / 365.25
		sourcePenalty = math.Max(0.3, 1.0-ageYears*0.2)
		coreScore *= sourcePenalty
	}

	// 3. User feedback contribution: feedback × weight
	feedbackContrib := float64(obs.UserFeedback) * c.config.FeedbackWeight

	// 4. Concept boost contribution: sum of matching concept weights × factor
	conceptBoost := 0.0
	for _, concept := range obs.Concepts {
		if weight, ok := c.config.ConceptWeights[concept]; ok {
			conceptBoost += weight
		}
	}
	conceptContrib := conceptBoost * c.config.ConceptWeight

	// 5. Retrieval boost: log2(count + 1) × 0.1 × weight (diminishing returns)
	// Temporal decay: boost decays based on time since last retrieval.
	// Decay applies ONLY to RetrievalContrib, NOT to CoreScore — RecencyDecay
	// already penalizes by created_at, so double-decay would over-penalize.
	retrievalContrib := 0.0
	if obs.RetrievalCount > 0 {
		// log2(count + 1) gives diminishing returns: 1→1, 3→2, 7→3, 15→4, etc.
		retrievalBoost := math.Log2(float64(obs.RetrievalCount)+1) * 0.1

		// Apply temporal decay based on last retrieval time
		if obs.LastRetrievedAt.Valid && obs.LastRetrievedAt.Int64 > 0 {
			daysSinceLastRetrieval := now.Sub(time.UnixMilli(obs.LastRetrievedAt.Int64)).Hours() / 24.0
			if daysSinceLastRetrieval < 0 {
				daysSinceLastRetrieval = 0
			}
			retrievalBoost *= math.Exp(-0.05 * daysSinceLastRetrieval)
		}

		retrievalContrib = retrievalBoost * c.config.RetrievalWeight
	}

	// 6. Utility contribution: (utility_score - 0.5) × weight, centered around neutral
	utilityContrib := (obs.UtilityScore - 0.5) * c.config.UtilityWeight

	// 7. Effectiveness contribution: closed-loop signal from injection outcomes.
	// Requires at least 10 injections for statistical significance; otherwise treated as neutral (0.5).
	effectivenessContrib := 0.0
	if obs.EffectivenessInjections >= 10 {
		effectivenessContrib = (obs.EffectivenessScore - 0.5) * c.config.EffectivenessWeight
	}

	// Final score with minimum threshold
	finalScore := coreScore + feedbackContrib + conceptContrib + retrievalContrib + utilityContrib + effectivenessContrib
	if finalScore < c.config.MinScore {
		finalScore = c.config.MinScore
	}

	return ScoreComponents{
		TypeWeight:           typeWeight,
		RecencyDecay:         recencyDecay,
		SourcePenalty:        sourcePenalty,
		CoreScore:            coreScore,
		FeedbackContrib:      feedbackContrib,
		ConceptContrib:       conceptContrib,
		RetrievalContrib:     retrievalContrib,
		UtilityContrib:       utilityContrib,
		EffectivenessContrib: effectivenessContrib,
		FinalScore:           finalScore,
		AgeDays:              ageDays,
	}
}

// ScoreComponents contains the breakdown of an importance score calculation.
type ScoreComponents struct {
	TypeWeight           float64 `json:"type_weight"`
	RecencyDecay         float64 `json:"recency_decay"`
	SourcePenalty        float64 `json:"source_penalty"`
	CoreScore            float64 `json:"core_score"`
	FeedbackContrib      float64 `json:"feedback_contrib"`
	ConceptContrib       float64 `json:"concept_contrib"`
	RetrievalContrib     float64 `json:"retrieval_contrib"`
	UtilityContrib       float64 `json:"utility_contrib"`
	EffectivenessContrib float64 `json:"effectiveness_contrib"`
	FinalScore           float64 `json:"final_score"`
	AgeDays              float64 `json:"age_days"`
}

// BatchCalculate computes scores for multiple observations.
// Returns a map of observation ID to calculated score.
func (c *Calculator) BatchCalculate(observations []*models.Observation, now time.Time) map[int64]float64 {
	scores := make(map[int64]float64, len(observations))
	for _, obs := range observations {
		scores[obs.ID] = c.Calculate(obs, now)
	}
	return scores
}

// RecalculateThreshold returns the minimum duration before an observation
// should have its score recalculated. This prevents excessive recalculation
// while ensuring scores stay reasonably fresh.
func (c *Calculator) RecalculateThreshold() time.Duration {
	// Recalculate at most every 6 hours
	// This balances freshness with performance
	return 6 * time.Hour
}

// UpdateConfig updates the calculator's scoring configuration.
// This allows runtime tuning of scoring parameters.
func (c *Calculator) UpdateConfig(config *models.ScoringConfig) {
	if config != nil {
		c.config = config
	}
}

// GetConfig returns the current scoring configuration.
func (c *Calculator) GetConfig() *models.ScoringConfig {
	return c.config
}
