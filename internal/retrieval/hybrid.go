package retrieval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/thebtf/engram/internal/embedding"
	"github.com/thebtf/engram/internal/graph"
	"github.com/thebtf/engram/pkg/models"
)

// RRF performs Reciprocal Rank Fusion on two ranked result lists.
// k is the RRF constant (typically 60).
func RRF(listA, listB []int64, k int) []int64 {
	if k <= 0 {
		k = 60
	}
	scores := make(map[int64]float64)
	for rank, id := range listA {
		scores[id] += 1.0 / float64(rank+k+1)
	}
	for rank, id := range listB {
		scores[id] += 1.0 / float64(rank+k+1)
	}

	type scored struct {
		id    int64
		score float64
	}
	var merged []scored
	for id, s := range scores {
		merged = append(merged, scored{id: id, score: s})
	}
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].score > merged[j].score
	})

	result := make([]int64, len(merged))
	for i, s := range merged {
		result[i] = s.id
	}
	return result
}

// MemoryStoreInterface is the minimal interface HybridSearch needs from the GORM memory store.
// Using an interface keeps the package free of a concrete DB import.
type MemoryStoreInterface interface {
	SearchFTS(ctx context.Context, project, query string, limit int) ([]*models.Memory, error)
	GetByIDs(ctx context.Context, ids []int64) ([]*models.Memory, error)
	List(ctx context.Context, project string, limit int) ([]*models.Memory, error)
}

// EmbeddingStoreInterface is the minimal interface over the embedding store used by HybridSearch.
type EmbeddingStoreInterface interface {
	FindSimilar(ctx context.Context, queryVec []float32, limit int, threshold float64) ([]embedding.SimilarResult, error)
}

// GraphStoreInterface is the minimal interface over graph.Store used for Tier2 expansion.
type GraphStoreInterface interface {
	Traverse(ctx context.Context, startID int64, maxDepth int, edgeTypes []string) ([]graph.TraversalResult, error)
}

// HybridOptions configures an individual HybridSearch call.
type HybridOptions struct {
	// QueryVec is the embedding of the query string. When nil and no embedding
	// service is available, retrieval degrades gracefully to FTS-only (Tier1).
	QueryVec []float32
	// TierFilter restricts results to the listed tiers. Empty = all tiers.
	// Valid values: "tier0_exact", "tier1_fts", "tier1_vector", "tier2_graph".
	TierFilter []string
	// MinConfidence is a post-scoring floor applied to the fused score [0,1].
	// Candidates below this threshold are dropped before the result is returned.
	MinConfidence float64
	// ExpandGraph enables Tier2: 1-hop graph neighbours of the top-5 Tier1
	// results are fetched and merged with a 0.85 multiplicative score penalty.
	// Requires GraphStore to be set; silently skipped when nil.
	//
	// Graph expansion penalty (decision): 0.15 multiplicative reduction.
	// Rationale: neighbours are semantically proximate but not directly matched
	// by the query; a 15% discount preserves their visibility without letting
	// them crowd out direct matches.
	ExpandGraph bool
	// Explain enables per-result score breakdown. Populated in each ScoredMemory.
	Explain bool
}

// HybridSearch runs the tiered retrieval pipeline (FR-C4) for a project and query.
//
// Tier 0 — exact content-hash match: SHA256 hash of query compared against all
//
//	candidate content hashes. Returns immediately if a match is found.
//
// Tier 1 — FTS + vector hybrid via RRF (<100ms budget):
//
//	FTS (SearchFTS) and vector (FindSimilar) queries run concurrently via errgroup.
//	When embeddingStore is nil or queryVec is empty, degrades to FTS-only.
//	Results are fused with RRF then scored with the FR-C4 formula.
//
// Tier 2 — graph expansion (opt-in, <200ms budget):
//
//	1-hop neighbours of the top-5 Tier1 results are fetched via graphStore.Traverse
//	and scored with a 0.85 multiplicative penalty relative to their Tier1 score.
//
// Returns a ranked []ScoredMemory with optional explanation fields populated.
func HybridSearch(
	ctx context.Context,
	project, query string,
	limit int,
	memStore MemoryStoreInterface,
	embStore EmbeddingStoreInterface,
	gStore GraphStoreInterface,
	opts HybridOptions,
) ([]ScoredMemory, []RankingExplanation, error) {
	if limit <= 0 {
		limit = 10
	}
	now := time.Now()
	tierAllowed := buildTierSet(opts.TierFilter)

	// Tier 0 — exact content-hash match.
	if tierAllowed("tier0_exact") {
		queryHash := contentHash(query)
		// Fetch a candidate pool via List (no index on content; practical for small projects).
		// For large deployments an indexed content_hash column would be preferred; that is
		// a follow-on migration — see NFR-C2 for the <100ms budget rationale.
		candidates, err := memStore.List(ctx, project, 200)
		if err == nil {
			for _, m := range candidates {
				if contentHash(m.Content) == queryHash {
					sm := Score(m, 1.0, now)
					sm.Memory = m
					var expl []RankingExplanation
					if opts.Explain {
						expl = []RankingExplanation{{
							MemoryID:   m.ID,
							Relevance:  1.0,
							Recency:    sm.Recency,
							Importance: sm.Importance,
							FusedScore: sm.Score,
							SourceTier: "tier0_exact",
						}}
					}
					return []ScoredMemory{sm}, expl, nil
				}
			}
		}
		// Tier0 miss is not an error; fall through to Tier1.
	}

	if !tierAllowed("tier1_fts") && !tierAllowed("tier1_vector") {
		// Caller filtered out all Tier1 tiers; return empty.
		return nil, nil, nil
	}

	// Tier 1 — concurrent FTS + vector.
	const ftsLimit = 50
	const vecLimit = 50

	var (
		ftsIDs  []int64
		vecIDs  []int64
		vecSims map[int64]float64 // memory_id → cosine similarity
	)

	eg, egCtx := errgroup.WithContext(ctx)

	// FTS branch.
	if tierAllowed("tier1_fts") {
		eg.Go(func() error {
			rows, err := memStore.SearchFTS(egCtx, project, query, ftsLimit)
			if err != nil {
				// FTS failure is non-fatal; log via the caller — here we degrade silently.
				return nil
			}
			ftsIDs = make([]int64, len(rows))
			for i, m := range rows {
				ftsIDs[i] = m.ID
			}
			return nil
		})
	}

	// Vector branch — skipped when embedding store or query vector unavailable.
	useVector := embStore != nil && len(opts.QueryVec) > 0 && tierAllowed("tier1_vector")
	if useVector {
		eg.Go(func() error {
			results, err := embStore.FindSimilar(egCtx, opts.QueryVec, vecLimit, 0.5)
			if err != nil {
				return nil // degrade to FTS-only
			}
			vecIDs = make([]int64, len(results))
			vecSims = make(map[int64]float64, len(results))
			for i, r := range results {
				vecIDs[i] = r.MemoryID
				vecSims[r.MemoryID] = r.Similarity
			}
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return nil, nil, err
	}

	// Fuse with RRF.
	fusedIDs := RRF(ftsIDs, vecIDs, 60)
	if len(fusedIDs) == 0 {
		return nil, nil, nil
	}
	// Cap candidate fetch.
	fetchN := limit * 3
	if fetchN > len(fusedIDs) {
		fetchN = len(fusedIDs)
	}
	fusedIDs = fusedIDs[:fetchN]

	// Materialise candidate memories.
	memories, err := memStore.GetByIDs(ctx, fusedIDs)
	if err != nil {
		return nil, nil, err
	}

	// Determine source tier per memory.
	ftsSet := idSet(ftsIDs)
	vecSet := idSet(vecIDs)

	// Score all candidates.
	scored := make([]ScoredMemory, 0, len(memories))
	explMap := make(map[int64]RankingExplanation, len(memories))

	for _, m := range memories {
		// Relevance: vector cosine when available, else 0.5 (FTS rank unknown post-fusion).
		relevance := 0.5
		tier := "tier1_fts"
		if sim, ok := vecSims[m.ID]; ok {
			relevance = sim
			if ftsSet[m.ID] {
				tier = "tier1_fts" // appeared in both; label FTS (higher precision)
			} else {
				tier = "tier1_vector"
			}
		}
		sm := Score(m, relevance, now)
		if opts.MinConfidence > 0 && sm.Score < opts.MinConfidence {
			continue
		}
		scored = append(scored, sm)
		if opts.Explain {
			explMap[m.ID] = RankingExplanation{
				MemoryID:   m.ID,
				Relevance:  relevance,
				Recency:    sm.Recency,
				Importance: sm.Importance,
				FusedScore: sm.Score,
				SourceTier: tier,
			}
		}
		_ = ftsSet
		_ = vecSet
	}

	// Tier 2 — graph expansion (opt-in).
	if opts.ExpandGraph && gStore != nil && tierAllowed("tier2_graph") {
		top5 := scored
		if len(top5) > 5 {
			top5 = top5[:5]
		}
		// Collect 1-hop neighbour IDs from top-5.
		neighbourSet := make(map[int64]bool)
		for _, sm := range top5 {
			results, tErr := gStore.Traverse(ctx, sm.Memory.ID, 1, nil)
			if tErr != nil {
				continue
			}
			for _, r := range results {
				nID := r.TargetID
				if nID == sm.Memory.ID {
					nID = r.SourceID
				}
				if !idInList(nID, fusedIDs) {
					neighbourSet[nID] = true
				}
			}
		}
		if len(neighbourSet) > 0 {
			// Cap neighbour expansion to 20 (EC-C3).
			nIDs := make([]int64, 0, len(neighbourSet))
			for id := range neighbourSet {
				nIDs = append(nIDs, id)
				if len(nIDs) == 20 {
					break
				}
			}
			neighbours, gErr := memStore.GetByIDs(ctx, nIDs)
			if gErr == nil {
				for _, m := range neighbours {
					// Graph penalty: 0.85 multiplier.
					sm := Score(m, 0.5, now)
					sm.Score *= 0.85
					if opts.MinConfidence > 0 && sm.Score < opts.MinConfidence {
						continue
					}
					scored = append(scored, sm)
					if opts.Explain {
						explMap[m.ID] = RankingExplanation{
							MemoryID:   m.ID,
							Relevance:  0.5,
							Recency:    sm.Recency,
							Importance: sm.Importance,
							FusedScore: sm.Score,
							SourceTier: "tier2_graph",
						}
					}
				}
			}
		}
	}

	// Final sort by composite score descending.
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].Score > scored[j].Score
	})
	if len(scored) > limit {
		scored = scored[:limit]
	}

	// Build explanation slice in result order.
	var explanations []RankingExplanation
	if opts.Explain {
		explanations = make([]RankingExplanation, 0, len(scored))
		for _, sm := range scored {
			if e, ok := explMap[sm.Memory.ID]; ok {
				explanations = append(explanations, e)
			}
		}
	}

	return scored, explanations, nil
}

// contentHash returns a SHA256 hex fingerprint of a string.
// Used for Tier0 exact-match without a DB content_hash column.
func contentHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// buildTierSet returns a fast membership test function for the requested tiers.
// An empty filter means all tiers are allowed.
func buildTierSet(filter []string) func(string) bool {
	if len(filter) == 0 {
		return func(string) bool { return true }
	}
	m := make(map[string]bool, len(filter))
	for _, t := range filter {
		m[t] = true
	}
	return func(tier string) bool { return m[tier] }
}

func idSet(ids []int64) map[int64]bool {
	m := make(map[int64]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

func idInList(id int64, list []int64) bool {
	for _, v := range list {
		if v == id {
			return true
		}
	}
	return false
}
