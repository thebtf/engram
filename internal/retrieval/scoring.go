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

	// tsEvidenceThreshold gates the rank-5 reinforcement term. ts_alpha and ts_beta both
	// default to 1.0, so a memory with no accumulated citation feedback has ts_alpha+ts_beta
	// == 2.0 and a posterior mean of exactly 0.5. Blending that 0.5 into importance would
	// drag a no-evidence memory toward the prior (up OR down, depending on its base) for no
	// real reason, so the reinforcement term applies only when the sum EXCEEDS this threshold —
	// i.e. at least one real feedback event (citation, uncited-injection, or violation) has
	// moved a prior off 1.0.
	tsEvidenceThreshold = 2.0

	// feedbackBlend is the share of the importance signal contributed by the citation/
	// reinforcement term; (1 - feedbackBlend) is retained from the base+confidence importance.
	// Unchanged from the raw-citation-rate term it replaces — rank-5 improves the QUALITY of
	// this term (smoothed, outcome-weighted, violation-aware posterior), not its weight.
	feedbackBlend = 0.3
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
	// Reinforcement (rank-5): blend the Thompson posterior mean of the citation-outcome priors
	// into importance, so memories that have proven useful on repeat injection surface higher on
	// the recall path. ts_alpha/ts_beta are maintained by the session-end feedback loop
	// (BatchIncrementCited/Uncited/Violated) — outcome-weighted and violation-aware, and
	// Bayesian-smoothed for small samples — so the posterior is strictly more signal than the raw
	// citation_count/injection_count rate it replaces. As of rank-2, those increments reflect
	// genuine citations only (echoed injections are stripped before detection), so the signal is
	// trustworthy. The raw-rate branch is retained as a fallback for memories whose counts predate
	// the ts priors (cited before migration 105 added the columns): their ts sum sits at the 2.0
	// prior, so without the fallback they would silently lose a boost they previously had.
	switch {
	case m.TsAlpha+m.TsBeta > tsEvidenceThreshold:
		tsPosterior := m.TsAlpha / (m.TsAlpha + m.TsBeta)
		importance = importance*(1-feedbackBlend) + tsPosterior*feedbackBlend
	case m.CitationCount > 0 && m.InjectionCount > 0:
		citationRate := float64(m.CitationCount) / float64(m.InjectionCount)
		importance = importance*(1-feedbackBlend) + citationRate*feedbackBlend
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
