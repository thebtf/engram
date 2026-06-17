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
	// RerankScore is the cross-encoder relevance score [0,1] when a reranker ran
	// (rank-4), or the sentinel -1 when it did not (no reranker configured, or it
	// errored and the fusion order was kept). Observability only — it does NOT feed
	// the composite Score() formula above; the reranker REPLACES the result ORDER,
	// it does not blend into the fused score. Lets callers distinguish CE-ordered
	// results from fusion-ordered fallback.
	RerankScore float64 `json:"rerank_score"`
	// orderKey is the value the final sort orders by (desc). It equals the composite
	// Score for every candidate UNTIL a cross-encoder reranks the pool, at which point
	// the rerank stage rewrites orderKey (NOT Score) with a synthetic descending key.
	// This keeps the public Score field an honest composite in [0,1] — callers that
	// display, compare, or threshold `score` see the documented value even when a
	// reranker reordered the results. Unexported → never serialized.
	orderKey float64
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
		Memory:      m,
		Score:       composite,
		Relevance:   relevance,
		Recency:     recency,
		Importance:  importance,
		RerankScore: -1,        // sentinel: no cross-encoder ran (rank-4); set by the rerank stage when it does
		orderKey:    composite, // sort key starts equal to the composite; the rerank stage rewrites it
	}
}
