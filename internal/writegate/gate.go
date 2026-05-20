package writegate

import (
	"context"

	"github.com/thebtf/engram/pkg/models"
)

// GateResult contains the write-gate verdict and quality signals.
type GateResult struct {
	Decision        string  `json:"gate_result"`      // "pass" or "flag"
	NoveltyScore    float64 `json:"novelty_score"`    // 1 - max_jaccard
	MaxJaccard      float64 `json:"max_jaccard"`      // highest similarity found
	SimilarExisting *int64  `json:"similar_existing"` // ID of most similar memory, nil if none
}

// Check evaluates incoming content against stored memories.
// Returns "flag" when novelty < 0.3 (max_jaccard > 0.7).
func Check(ctx context.Context, content string, storedMemories []*models.Memory) GateResult {
	result := GateResult{
		Decision:     "pass",
		NoveltyScore: 1.0,
		MaxJaccard:   0.0,
	}

	if content == "" || len(storedMemories) == 0 {
		return result
	}

	for _, mem := range storedMemories {
		sim := Jaccard(content, mem.Content)
		if sim > result.MaxJaccard {
			result.MaxJaccard = sim
			id := mem.ID
			result.SimilarExisting = &id
		}
	}

	result.NoveltyScore = 1.0 - result.MaxJaccard
	if result.NoveltyScore < 0.3 {
		result.Decision = "flag"
	}

	return result
}
