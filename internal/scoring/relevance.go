// Package scoring provides importance and relevance score calculation for observations.
package scoring

import (
	"math"
)

// RelevanceConfig contains parameters for the relevance score formula.
type RelevanceConfig struct {
	// BaseDecayRate controls how fast relevance drops with age (default 0.1).
	BaseDecayRate float64 `json:"base_decay_rate"`
	// AccessDecayRate controls the access recency weight (default 0.05).
	AccessDecayRate float64 `json:"access_decay_rate"`
	// RelationWeight scales the relation count bonus (default 0.3).
	RelationWeight float64 `json:"relation_weight"`
	// MinRelevance is the floor value for relevance scores (default 0.001).
	MinRelevance float64 `json:"min_relevance"`
}

// DefaultRelevanceConfig returns the default relevance configuration.
func DefaultRelevanceConfig() *RelevanceConfig {
	return &RelevanceConfig{
		BaseDecayRate:   0.1,
		AccessDecayRate: 0.05,
		RelationWeight:  0.3,
		MinRelevance:    0.001,
	}
}

// RelevanceCalculator computes relevance scores using the automem-inspired formula.
type RelevanceCalculator struct {
	config *RelevanceConfig
}

// NewRelevanceCalculator creates a new relevance calculator.
func NewRelevanceCalculator(config *RelevanceConfig) *RelevanceCalculator {
	if config == nil {
		config = DefaultRelevanceConfig()
	}
	return &RelevanceCalculator{config: config}
}

// RelevanceParams contains input parameters for relevance calculation.
type RelevanceParams struct {
	// AgeDays is the number of days since the observation was created.
	AgeDays float64
	// AccessRecencyDays is days since last retrieval. If never accessed, use AgeDays.
	AccessRecencyDays float64
	// RelationCount is the total number of inbound + outbound relations.
	RelationCount int
	// ImportanceScore is the existing importance score (typically 0-2 range).
	ImportanceScore float64
	// AvgRelConfidence is the average confidence of this observation's relations (default 0.5).
	AvgRelConfidence float64
}

// CalculateRelevance computes the relevance score for an observation.
//
// Formula:
//
//	decayFactor   = exp(-baseDecayRate * ageDays)
//	accessFactor  = exp(-accessDecayRate * accessRecencyDays)
//	relFactor     = 1.0 + relationWeight * log1p(relCount)
//	relevance     = decayFactor * (0.3 + 0.3*accessFactor) * relFactor * (0.5 + importance) * (0.7 + 0.3*confidence)
func (r *RelevanceCalculator) CalculateRelevance(params RelevanceParams) float64 {
	decayFactor := math.Exp(-r.config.BaseDecayRate * params.AgeDays)
	accessFactor := math.Exp(-r.config.AccessDecayRate * params.AccessRecencyDays)
	relFactor := 1.0 + r.config.RelationWeight*math.Log1p(float64(params.RelationCount))

	relevance := decayFactor *
		(0.3 + 0.3*accessFactor) *
		relFactor *
		(0.5 + params.ImportanceScore) *
		(0.7 + 0.3*params.AvgRelConfidence)

	if relevance < r.config.MinRelevance {
		return r.config.MinRelevance
	}
	return relevance
}

// RelevanceComponents returns a breakdown of the relevance calculation.
type RelevanceComponents struct {
	DecayFactor      float64 `json:"decay_factor"`
	AccessFactor     float64 `json:"access_factor"`
	RelationFactor   float64 `json:"relation_factor"`
	ImportanceFactor float64 `json:"importance_factor"`
	ConfidenceFactor float64 `json:"confidence_factor"`
	FinalRelevance   float64 `json:"final_relevance"`
}

// CalculateComponents returns the individual components of the relevance calculation.
func (r *RelevanceCalculator) CalculateComponents(params RelevanceParams) RelevanceComponents {
	decayFactor := math.Exp(-r.config.BaseDecayRate * params.AgeDays)
	accessFactor := math.Exp(-r.config.AccessDecayRate * params.AccessRecencyDays)
	relFactor := 1.0 + r.config.RelationWeight*math.Log1p(float64(params.RelationCount))
	importanceFactor := 0.5 + params.ImportanceScore
	confidenceFactor := 0.7 + 0.3*params.AvgRelConfidence

	relevance := decayFactor * (0.3 + 0.3*accessFactor) * relFactor * importanceFactor * confidenceFactor

	if relevance < r.config.MinRelevance {
		relevance = r.config.MinRelevance
	}

	return RelevanceComponents{
		DecayFactor:      decayFactor,
		AccessFactor:     accessFactor,
		RelationFactor:   relFactor,
		ImportanceFactor: importanceFactor,
		ConfidenceFactor: confidenceFactor,
		FinalRelevance:   relevance,
	}
}

// GetConfig returns the current relevance configuration.
func (r *RelevanceCalculator) GetConfig() *RelevanceConfig {
	return r.config
}
