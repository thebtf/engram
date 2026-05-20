package lifecycle

import (
	"math"
	"time"
)

// ConfidenceInputs holds the measurable evidence used to compute confidence.
type ConfidenceInputs struct {
	RecurrenceCount int
	CitationCount   int
	InjectionCount  int
	LastConfirmed   *time.Time
	UserRatingDelta float64
}

// ComputeConfidence calculates an evidence-based confidence score.
// Confidence is computed from measurable evidence per G12 — never from LLM judgment.
//
// Components:
//   - Base:        0.5
//   - Recurrence:  min(0.3, recurrence_count × 0.1)
//   - Citation:    min(0.3, citation_rate × 0.3)
//   - User rating: ±0.2 from explicit feedback
//   - Freshness:   +0.1 if confirmed within 7 days
func ComputeConfidence(inputs ConfidenceInputs) float64 {
	base := 0.5

	recurrence := math.Min(0.3, float64(inputs.RecurrenceCount)*0.1)

	injections := math.Max(float64(inputs.InjectionCount), 1)
	citationRate := float64(inputs.CitationCount) / injections
	citation := math.Min(0.3, citationRate*0.3)

	freshness := 0.0
	if inputs.LastConfirmed != nil && time.Since(*inputs.LastConfirmed).Hours() < 7*24 {
		freshness = 0.1
	}

	return clamp(base+recurrence+citation+inputs.UserRatingDelta+freshness, 0, 1)
}
