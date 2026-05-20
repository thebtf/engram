// Package retrieval provides hybrid search and scoring for memory retrieval.
package retrieval

import (
	"math"
	"time"

	"github.com/thebtf/engram/pkg/models"
)

// ScoredMemory wraps a memory with its composite retrieval score.
type ScoredMemory struct {
	Memory     *models.Memory `json:"memory"`
	Score      float64        `json:"score"`
	Relevance  float64        `json:"relevance"`
	Recency    float64        `json:"recency"`
	Importance float64        `json:"importance"`
}

const (
	WeightRelevance  = 0.4
	WeightRecency    = 0.3
	WeightImportance = 0.3
	RecencyDecay     = 0.995
)

// Score computes the 3-signal composite score for a memory.
// Source: Generative Agents (ablation-confirmed).
func Score(m *models.Memory, relevance float64, now time.Time) ScoredMemory {
	hoursSinceAccess := now.Sub(m.CreatedAt).Hours()
	if m.LastRetrievedAt != nil {
		hoursSinceAccess = now.Sub(*m.LastRetrievedAt).Hours()
	}
	if hoursSinceAccess < 0 {
		hoursSinceAccess = 0
	}

	recency := math.Pow(RecencyDecay, hoursSinceAccess)

	importance := m.ImportanceBase
	if m.Confidence > 0 {
		importance = (importance + m.Confidence) / 2
	}
	if m.CitationCount > 0 && m.InjectionCount > 0 {
		citationRate := float64(m.CitationCount) / float64(m.InjectionCount)
		importance = importance*0.7 + citationRate*0.3
	}
	if importance > 1 {
		importance = 1
	}

	composite := WeightRelevance*relevance + WeightRecency*recency + WeightImportance*importance

	return ScoredMemory{
		Memory:     m,
		Score:      composite,
		Relevance:  relevance,
		Recency:    recency,
		Importance: importance,
	}
}
