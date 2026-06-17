package retrieval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/sync/errgroup"

	"github.com/thebtf/engram/internal/embedding"
	"github.com/thebtf/engram/internal/graph"
	"github.com/thebtf/engram/pkg/models"
)

// RRF performs Reciprocal Rank Fusion on two ranked result lists.
// k is the RRF constant (typically 60).
// Tie-breaking is deterministic: score desc → best source rank asc → ID asc.
func RRF(listA, listB []int64, k int) []int64 {
	if k <= 0 {
		k = 60
	}
	scores := make(map[int64]float64)
	// bestRank tracks the minimum (best) rank seen for each ID across both lists.
	bestRank := make(map[int64]int)
	initRank := func(id int64, rank int) {
		if r, ok := bestRank[id]; !ok || rank < r {
			bestRank[id] = rank
		}
	}
	for rank, id := range listA {
		scores[id] += 1.0 / float64(rank+k+1)
		initRank(id, rank)
	}
	for rank, id := range listB {
		scores[id] += 1.0 / float64(rank+k+1)
		initRank(id, rank)
	}

	type scored struct {
		id    int64
		score float64
		best  int
	}
	merged := make([]scored, 0, len(scores))
	for id, s := range scores {
		merged = append(merged, scored{id: id, score: s, best: bestRank[id]})
	}
	// Deterministic tie-breaker: score desc → best source rank asc → ID asc.
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].score != merged[j].score {
			return merged[i].score > merged[j].score
		}
		if merged[i].best != merged[j].best {
			return merged[i].best < merged[j].best
		}
		return merged[i].id < merged[j].id
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
	// GetByIDs fetches memories by ID list scoped to the given project.
	// project must be non-empty; the implementation must filter by project to
	// prevent cross-project leakage when IDs arrive from the unscoped vector leg.
	GetByIDs(ctx context.Context, project string, ids []int64) ([]*models.Memory, error)
	List(ctx context.Context, project string, limit int) ([]*models.Memory, error)
}

// EmbeddingStoreInterface is the minimal interface over the embedding store used by HybridSearch.
// FindSimilarForProject is the project-scoped variant required by HybridSearch to prevent
// cross-project leakage through the vector leg. content_chunks has no project column, so the
// implementation must JOIN to memories and filter by project there.
type EmbeddingStoreInterface interface {
	FindSimilarForProject(ctx context.Context, project string, queryVec []float32, limit int, threshold float64) ([]embedding.SimilarResult, error)
}

// GraphStoreInterface is the minimal interface over graph.Store used for Tier2 expansion.
type GraphStoreInterface interface {
	Traverse(ctx context.Context, startID int64, maxDepth int, edgeTypes []string) ([]graph.TraversalResult, error)
}

// CrossEncoder is the minimal interface HybridSearch needs to rerank the fused
// candidate pool with a cross-encoder (rank-4, 2026-06-17). It is threaded as an
// optional field on HybridOptions and is nil-guarded: when absent the fusion order
// is kept unchanged. The concrete implementation (internal/reranking.Client) targets
// a LiteLLM /rerank endpoint over HTTP — this is a NEW build, not the v5-demolished
// ONNX reranker (see AGENTS.md "V5 DEMOLITION GUARD").
//
// Rank returns the passages reordered most-relevant-first as Index/RelevanceScore
// pairs, where Index points back into the input passages slice. A returned error
// MUST be treated by the caller as "keep fusion order" — the reranker can never
// block or fail recall.
type CrossEncoder interface {
	Rank(ctx context.Context, query string, passages []string) ([]RerankResult, error)
}

// RerankResult mirrors reranking.PassageScore without importing that package
// (keeps internal/retrieval free of the concrete rerank client, same discipline as
// the store interfaces above). Exported so a wiring-layer adapter in another package
// can name it when implementing CrossEncoder. The concrete []reranking.PassageScore
// is adapted to []RerankResult at the call site.
type RerankResult struct {
	Index          int
	RelevanceScore float64
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
	// VecThreshold is the minimum cosine similarity for vector candidates [0,1].
	// When 0 the embedding store's default threshold applies (typically 0.7).
	// Maps to the legacy min_similarity parameter on recall(action="similar").
	VecThreshold float64
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
	// SkipTier0 disables the Tier0 exact content-hash short-circuit when true.
	// The caller sets this to re-run the pipeline via Tier1/Tier2 only after a
	// Tier0 hit was returned but then filtered out (e.g. by scope or tag
	// predicates at the MCP layer). Keeping this option here rather than moving
	// MCP-layer predicates into HybridSearch preserves the layer boundary: the
	// retrieval package stays filter-agnostic.
	SkipTier0 bool
	// Reranker, when non-nil, reorders the fused+scored candidate pool with a
	// cross-encoder before the final sort (rank-4). It runs on the full pool
	// (up to limit*5 candidates) while it is still un-truncated, so it can promote
	// a conceptually-relevant candidate that fusion ranked low. When nil, the fusion
	// order is kept unchanged (the pre-rank-4 behavior, byte-identical). A reranker
	// error never propagates — the pool keeps its fusion order. The pool handed to
	// the cross-encoder is capped at RerankMaxCandidates to bound HTTP latency.
	Reranker CrossEncoder
}

// RerankMaxCandidates caps how many fused candidates are sent to the cross-encoder
// in one /rerank call, bounding recall latency. With the default recall limit of 10
// the fused pool is limit*5 = 50, which sits at this cap; a larger caller limit or a
// filter-widened pool is trimmed to the top RerankMaxCandidates by fusion score
// before reranking.
const RerankMaxCandidates = 50

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
	// Use UTC to match DB timestamps stored in UTC; avoids ranking skew when
	// the server's local timezone is not UTC.
	now := time.Now().UTC()
	tierAllowed := buildTierSet(opts.TierFilter)

	// Tier 0 — exact content-hash match.
	// Skipped when SkipTier0 is set so callers can fall through to Tier1 after
	// a prior Tier0 result was fully filtered at the MCP layer (e.g. scope or
	// tag mismatch). See HybridOptions.SkipTier0 for rationale.
	if !opts.SkipTier0 && tierAllowed("tier0_exact") {
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
	// Uses FindSimilarForProject to scope results to the caller's project,
	// preventing cross-project leakage (content_chunks has no project column).
	useVector := embStore != nil && len(opts.QueryVec) > 0 && tierAllowed("tier1_vector")
	if useVector {
		eg.Go(func() error {
			vecThreshold := opts.VecThreshold // 0 → embedding store default (0.7)
			results, err := embStore.FindSimilarForProject(egCtx, project, opts.QueryVec, vecLimit, vecThreshold)
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
	const rrfK = 60
	fusedIDs := RRF(ftsIDs, vecIDs, rrfK)
	if len(fusedIDs) == 0 {
		return nil, nil, nil
	}
	// Cap candidate fetch.
	// Multiplier 5× (was 3×) to ensure enough candidates survive the MCP-layer
	// scope.Resolve privacy filter (F-TG1). If the top-ranked fused candidates
	// are private/shared rows invisible to the caller, a 3× cap could exhaust
	// before reaching visible matches; 5× keeps the cap tight while reducing
	// false-empty results under realistic private-heavy corpora.
	fetchN := limit * 5
	if fetchN > len(fusedIDs) {
		fetchN = len(fusedIDs)
	}
	fusedIDs = fusedIDs[:fetchN]

	// Materialise candidate memories — project-scoped to block cross-project leakage.
	// Candidates can include IDs from the unscoped-by-nature vector leg; filtering by
	// project here is the second defence (FindSimilarForProject is the first).
	memories, err := memStore.GetByIDs(ctx, project, fusedIDs)
	if err != nil {
		return nil, nil, err
	}

	// Determine source tier per memory.
	ftsSet := idSet(ftsIDs)
	vecSet := idSet(vecIDs)

	// Pre-compute FTS rank lookup for RRF-aware relevance estimation.
	// FTS rank position is used to normalise relevance for FTS-only candidates so
	// that relevance carries signal from the FTS ordering rather than a flat 0.5.
	// Formula: relevance = 1 / (rank + rrfK + 1), clamped to [0.1, 0.9] so it
	// stays distinguishable from exact (1.0) and zero-signal paths.
	// For vector candidates the actual cosine similarity is used directly (preferred).
	ftsRankMap := make(map[int64]int, len(ftsIDs))
	for rank, id := range ftsIDs {
		ftsRankMap[id] = rank
	}

	// Score all candidates.
	scored := make([]ScoredMemory, 0, len(memories))
	explMap := make(map[int64]RankingExplanation, len(memories))

	for _, m := range memories {
		// Relevance assignment (FR-C4):
		// - tier1_vector: cosine similarity from the embedding store (most precise).
		// - tier1_fts (vector also present): cosine similarity used even when FTS also
		//   matched, because cosine is a better-calibrated signal.
		// - tier1_fts-only: normalised RRF term 1/(rank+k+1) clamped to [0.1,0.9].
		//   Rationale: this carries the FTS ordering signal rather than a flat 0.5,
		//   which caused all FTS-only candidates to tie on relevance and sort by
		//   recency/importance only — discarding the FTS rank completely.
		var relevance float64
		tier := "tier1_fts"
		if sim, ok := vecSims[m.ID]; ok {
			// Vector match (with or without concurrent FTS match).
			relevance = sim
			if ftsSet[m.ID] {
				tier = "tier1_fts" // appeared in both; label FTS (higher precision search)
			} else {
				tier = "tier1_vector"
			}
		} else if rank, ok := ftsRankMap[m.ID]; ok {
			// FTS-only candidate: map rank to relevance in [0.3, 0.9] using linear
			// position within the FTS result list. rank-0 (best FTS match) → 0.9,
			// rank-(N-1) (worst) → 0.3. This carries the FTS ts_rank_cd ordering
			// through the FR-C4 relevance term rather than using a flat 0.5 which
			// discards rank entirely and collapses all FTS results to identical relevance.
			// Formula: relevance = 0.3 + 0.6 * (1 - rank/(len(ftsIDs)))
			// For N=1 (single FTS result) this produces exactly 0.9.
			n := len(ftsIDs)
			if n <= 1 {
				relevance = 0.9
			} else {
				relevance = 0.3 + 0.6*(1.0-float64(rank)/float64(n))
			}
		} else {
			// ID arrived outside both FTS and vector sets (e.g. Tier0 path quirk).
			relevance = 0.1
		}
		_ = vecSet
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
	}

	// Tier 2 — graph expansion (opt-in, budget ≤200ms total; traverse capped at 150ms).
	// On timeout the Tier1 result set is returned unmodified (graceful degradation).
	if opts.ExpandGraph && gStore != nil && tierAllowed("tier2_graph") {
		tier2Ctx, tier2Cancel := context.WithTimeout(ctx, 150*time.Millisecond)
		defer tier2Cancel()

		top5 := scored
		if len(top5) > 5 {
			top5 = top5[:5]
		}
		// Collect 1-hop neighbour IDs from top-5.
		neighbourSet := make(map[int64]bool)
		for _, sm := range top5 {
			results, tErr := gStore.Traverse(tier2Ctx, sm.Memory.ID, 1, nil)
			if tErr != nil {
				// Includes context.DeadlineExceeded — degrade silently.
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
			// Collect all IDs then sort ascending before truncation so the set is
			// deterministic — Go map iteration order is randomised.
			nIDs := make([]int64, 0, len(neighbourSet))
			for id := range neighbourSet {
				nIDs = append(nIDs, id)
			}
			sort.Slice(nIDs, func(i, j int) bool { return nIDs[i] < nIDs[j] })
			if len(nIDs) > 20 {
				nIDs = nIDs[:20]
			}
			// Use project-scoped GetByIDs to prevent cross-project leakage through
			// graph edges that may reference memories in other projects.
			neighbours, gErr := memStore.GetByIDs(tier2Ctx, project, nIDs)
			if gErr == nil {
				for _, m := range neighbours {
					// Graph penalty: 0.85 multiplier on the composite score.
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

	// Rank-4 cross-encoder rerank (2026-06-17). When a reranker is configured,
	// reorder the full fused+scored candidate pool by cross-encoder relevance BEFORE
	// the final sort + truncation below, so a conceptually-relevant candidate fusion
	// ranked low can still reach the caller's top-k. This REPLACES the order (it does
	// not blend into Score): on success we overwrite each candidate's Score with a
	// descending rank key derived from the reranker's returned order, and record the
	// raw relevance in RerankScore for observability. The reranker is nil-guarded and
	// failure-silent — any error keeps the fusion order (a missing/broken reranker
	// must NEVER block or fail recall). Mirrors the embedding-client degrade pattern.
	if opts.Reranker != nil && len(scored) > 1 {
		rerankApplyCrossEncoder(ctx, opts.Reranker, query, scored)
	}

	// Final sort: score desc → ID asc (deterministic tie-breaker).
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
		}
		return scored[i].Memory.ID < scored[j].Memory.ID
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

// rerankApplyCrossEncoder reorders scored in place by cross-encoder relevance
// (rank-4). It sends the top RerankMaxCandidates (by current fused Score, so the
// strongest fusion candidates are the ones reranked) to the cross-encoder, then
// rewrites each reranked candidate's Score with a strictly-descending key so the
// caller's existing final sort.Slice reproduces the reranker's order exactly — the
// truncation contract (scored[:limit]) is untouched. RerankScore records the raw
// relevance [0,1] for observability.
//
// Failure-silent by contract: a nil/empty reranker result or any error leaves scored
// unchanged (fusion order, RerankScore stays at the -1 sentinel). The reranker can
// never block or fail recall — that is the explicit anti-pattern this avoids.
func rerankApplyCrossEncoder(ctx context.Context, ce CrossEncoder, query string, scored []ScoredMemory) {
	// Cap the pool sent to the HTTP cross-encoder to bound latency. scored is not yet
	// sorted here, so select the top-N by current fused Score rather than slice head.
	n := len(scored)
	if n > RerankMaxCandidates {
		// Partial selection: move the RerankMaxCandidates highest-Score candidates to
		// the front so indices [0,RerankMaxCandidates) are the rerank pool. A full sort
		// is acceptable (n is bounded by limit*5) and keeps the mapping simple.
		sort.Slice(scored, func(i, j int) bool {
			if scored[i].Score != scored[j].Score {
				return scored[i].Score > scored[j].Score
			}
			return scored[i].Memory.ID < scored[j].Memory.ID
		})
		n = RerankMaxCandidates
	}

	passages := make([]string, n)
	for i := 0; i < n; i++ {
		passages[i] = scored[i].Memory.Content
	}

	results, err := ce.Rank(ctx, query, passages)
	if err != nil || len(results) == 0 {
		if err != nil {
			log.Debug().Err(err).Msg("rerank: cross-encoder failed; keeping fusion order")
		}
		return // failure-silent: fusion order preserved, RerankScore stays -1
	}

	// Find the max fused Score among the reranked pool so the rerank keys sit strictly
	// ABOVE every un-reranked tail candidate (indices [n,len)) — reranked results must
	// always outrank the tail that the cross-encoder never saw.
	maxFused := 0.0
	for i := 0; i < n; i++ {
		if scored[i].Score > maxFused {
			maxFused = scored[i].Score
		}
	}
	base := maxFused + float64(len(results)) + 1

	// Assign strictly-descending keys in the reranker's returned order. results[0] is
	// most-relevant → highest key. Out-of-range indices are skipped (client already
	// guards, belt-and-suspenders here).
	for rank, r := range results {
		if r.Index < 0 || r.Index >= n {
			continue
		}
		scored[r.Index].Score = base - float64(rank)
		scored[r.Index].RerankScore = r.RelevanceScore
	}
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
