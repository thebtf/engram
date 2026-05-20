package writegate

import (
	"context"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/embedding"
	"github.com/thebtf/engram/pkg/models"
)

// MemoryLifecycleUpdater is the subset of MemoryStore needed by CheckCosine.
// Using a minimal interface keeps writegate free from a direct gorm package dependency.
type MemoryLifecycleUpdater interface {
	UpdateLifecycleFields(ctx context.Context, id int64, fields map[string]any) error
}

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

// cosineThreshold is the cosine similarity above which a memory is considered
// a semantic duplicate and flagged via UpdateLifecycleFields.
const cosineThreshold = 0.95

// CheckCosine runs an async cosine similarity search for memoryID's content
// against all embedded chunks. When the top result exceeds cosineThreshold it
// flags the stored memory as a semantic duplicate by setting status="flagged"
// via memStore.UpdateLifecycleFields.
//
// Designed to run in a goroutine — all errors are logged, none are returned.
// embClient and embStore must be non-nil; memStore must be non-nil.
func CheckCosine(
	ctx context.Context,
	memoryID int64,
	content string,
	embClient *embedding.Client,
	embStore *embedding.Store,
	memStore MemoryLifecycleUpdater,
) {
	if embClient == nil || embStore == nil || memStore == nil {
		return
	}

	vectors, err := embClient.Embed(ctx, []string{content})
	if err != nil {
		log.Error().Err(err).Int64("memory_id", memoryID).Msg("writegate cosine: embed failed")
		return
	}
	if len(vectors) == 0 || len(vectors[0]) == 0 {
		return
	}

	results, err := embStore.FindSimilar(ctx, vectors[0], 5, cosineThreshold)
	if err != nil {
		log.Error().Err(err).Int64("memory_id", memoryID).Msg("writegate cosine: find similar failed")
		return
	}

	for _, r := range results {
		// Skip the memory we just stored — it will match itself once its own
		// chunk is committed, but we want matches against other memories.
		if r.MemoryID == memoryID {
			continue
		}
		if r.Similarity >= cosineThreshold {
			log.Warn().
				Int64("memory_id", memoryID).
				Int64("duplicate_of", r.MemoryID).
				Float64("similarity", r.Similarity).
				Msg("writegate cosine: semantic duplicate flagged")
			if updateErr := memStore.UpdateLifecycleFields(ctx, memoryID, map[string]any{
				"status": "flagged",
			}); updateErr != nil {
				log.Error().Err(updateErr).Int64("memory_id", memoryID).Msg("writegate cosine: flag update failed")
			}
			return
		}
	}
}
