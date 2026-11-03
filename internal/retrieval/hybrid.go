package retrieval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/sync/errgroup"

	"github.com/thebtf/engram/internal/embedding"
	"github.com/thebtf/engram/internal/graph"
	"github.com/thebtf/engram/internal/rankfusion"
	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/pkg/models"
)

// RRF performs Reciprocal Rank Fusion on two ranked result lists.
// It delegates to the cycle-free shared authority so storage-bound consumers
// can reuse identical ordering without importing the retrieval orchestration.
func RRF(listA, listB []int64, k int) []int64 {
	return rankfusion.RRF(listA, listB, k)
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
	// Access is mandatory for caller-facing shared search. Nil retains the trusted
	// internal HybridSearch contract; it never substitutes for missing authorized stores.
	Access *scope.MemoryReadAccess
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
	// SkipTier0 disables the exact content-hash short-circuit. Adapters can retry
	// Tier1 when a visible exact hit does not satisfy their non-security filters.
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
	var searchStore SearchMemoryStore
	var searchEmbStore SearchEmbeddingStore
	var searchGraphStore SearchGraphStore
	if opts.Access != nil {
		var ok bool
		searchStore, ok = memStore.(SearchMemoryStore)
		if !ok {
			return nil, nil, fmt.Errorf("memory search: authorized memory reader unavailable")
		}
		if embStore != nil && len(opts.QueryVec) > 0 && tierAllowed("tier1_vector") {
			searchEmbStore, ok = embStore.(SearchEmbeddingStore)
			if !ok {
				return nil, nil, fmt.Errorf("memory search: authorized vector reader unavailable")
			}
		}
		if opts.ExpandGraph && gStore != nil && tierAllowed("tier2_graph") {
			searchGraphStore, ok = gStore.(SearchGraphStore)
			if !ok {
				return nil, nil, fmt.Errorf("memory search: authorized graph reader unavailable")
			}
		}
	}

	// Tier0 can be skipped when an exact hit fails adapter-level type/tag filters.
	if !opts.SkipTier0 && tierAllowed("tier0_exact") {
		queryHash := contentHash(query)
		// The bounded pool contains only authorized rows for public search.
		var candidates []*models.Memory
		var err error
		if searchStore != nil {
			candidates, err = searchStore.ListForSearch(ctx, project, 200, *opts.Access)
		} else {
			candidates, err = memStore.List(ctx, project, 200)
		}
		if err == nil {
			for _, m := range candidates {
				if contentHash(m.Content) == queryHash {
					if searchStore != nil {
						ref := scope.MemoryReference(m)
						current, getErr := searchStore.GetByRefsForSearch(ctx, project, []scope.MemoryRef{ref}, *opts.Access)
						if getErr != nil {
							return nil, nil, getErr
						}
						if len(current) != 1 || !searchMemoryMatches(current[0], project, ref, *opts.Access) {
							continue
						}
						m = current[0]
					}
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
		ftsIDs     []int64
		vecIDs     []int64
		vecSims    map[int64]float64 // memory_id → cosine similarity
		ftsRefs    map[int64]scope.MemoryRef
		vecRefs    map[int64]scope.MemoryRef
		ftsPartial map[int64]bool
	)

	eg, egCtx := errgroup.WithContext(ctx)

	// FTS branch.
	if tierAllowed("tier1_fts") {
		eg.Go(func() error {
			var rows []*models.Memory
			var err error
			if searchStore != nil {
				rows, ftsPartial, err = searchStore.SearchFTSForSearch(egCtx, project, query, ftsLimit, *opts.Access)
			} else {
				rows, err = memStore.SearchFTS(egCtx, project, query, ftsLimit)
			}
			if err != nil {
				// FTS failure is non-fatal; log via the caller — here we degrade silently.
				return nil
			}
			ftsIDs = make([]int64, len(rows))
			if searchStore != nil {
				ftsRefs = make(map[int64]scope.MemoryRef, len(rows))
			}
			for i, m := range rows {
				ftsIDs[i] = m.ID
				if searchStore != nil {
					ftsRefs[m.ID] = scope.MemoryReference(m)
				}
			}
			return nil
		})
	}

	// Vector candidates always retain project scope. Caller-facing search also
	// applies read policy before the vector ordering and candidate cap.
	useVector := embStore != nil && len(opts.QueryVec) > 0 && tierAllowed("tier1_vector")
	if useVector {
		eg.Go(func() error {
			vecThreshold := opts.VecThreshold // 0 → embedding store default (0.7)
			var results []embedding.SimilarResult
			var err error
			if searchEmbStore != nil {
				results, err = searchEmbStore.FindSimilarForSearch(egCtx, project, opts.QueryVec, vecLimit, vecThreshold, *opts.Access)
			} else {
				results, err = embStore.FindSimilarForProject(egCtx, project, opts.QueryVec, vecLimit, vecThreshold)
			}
			if err != nil {
				return nil // degrade to FTS-only
			}
			vecIDs = make([]int64, len(results))
			vecSims = make(map[int64]float64, len(results))
			if searchStore != nil {
				vecRefs = make(map[int64]scope.MemoryRef, len(results))
			}
			for i, r := range results {
				vecIDs[i] = r.MemoryID
				vecSims[r.MemoryID] = r.Similarity
				if searchStore != nil {
					vecRefs[r.MemoryID] = scope.MemoryRef{ID: r.MemoryID, Version: r.Version, ContentDigest: r.ContentDigest}
				}
			}
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return nil, nil, err
	}

	// Concurrent legs may have selected different bodies for one ID. Resolve
	// those exact references before either fusion credit or its candidate cap.
	if searchStore != nil {
		var err error
		ftsIDs, vecIDs, err = reconcileSearchLegs(ctx, project, searchStore, *opts.Access,
			ftsIDs, vecIDs, ftsRefs, vecRefs, vecSims)
		if err != nil {
			return nil, nil, err
		}
	}

	// Fuse with RRF.
	const rrfK = 60
	fusedIDs := RRF(ftsIDs, vecIDs, rrfK)
	if len(fusedIDs) == 0 {
		return nil, nil, nil
	}
	// Bound fusion after each leg has authorized its candidate ordering and cap.
	fetchN := limit * 5
	if fetchN > len(fusedIDs) {
		fetchN = len(fusedIDs)
	}
	fusedIDs = fusedIDs[:fetchN]

	// Recheck selected versions, body digests and current policy before scoring.
	var memories []*models.Memory
	var err error
	if searchStore != nil {
		refs := make([]scope.MemoryRef, 0, len(fusedIDs))
		for _, id := range fusedIDs {
			ref, ok := ftsRefs[id]
			if !ok {
				ref = vecRefs[id]
			}
			refs = append(refs, ref)
		}
		memories, err = searchStore.GetByRefsForSearch(ctx, project, refs, *opts.Access)
	} else {
		memories, err = memStore.GetByIDs(ctx, project, fusedIDs)
	}
	if err != nil {
		return nil, nil, err
	}

	// Determine source tier per memory.
	ftsSet := idSet(ftsIDs)
	vecSet := idSet(vecIDs)

	// Preserve lexical rank within its evidence class. A successful OR-recovery
	// pass is partial-query evidence, not a strict top hit with relevance 0.9.
	ftsRankMap := make(map[int64]int, len(ftsIDs))
	for rank, id := range ftsIDs {
		ftsRankMap[id] = rank
	}

	// Score all candidates.
	scored := make([]ScoredMemory, 0, len(memories))
	explMap := make(map[int64]RankingExplanation, len(memories))

	for _, m := range memories {
		// Real vector similarity remains authoritative for vector-supported rows.
		// Lexical-only rows retain rank ordering, with a lower ceiling for OR-only
		// recovery. Full literal matches are promoted out of that partial class.
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
			// Strict/literal relevance spans [0.3, 0.9]. Partial OR recovery spans
			// [0.3, 0.6], below the vector leg's default 0.7 similarity floor.
			// Recovery remains useful on its own without claiming stronger evidence
			// than an independently supported semantic match.
			span := 0.6
			relevance = 0.9
			if ftsPartial[m.ID] {
				span = 0.3
				relevance = 0.6
			}
			if len(ftsIDs) > 1 {
				relevance = 0.3 + span*(1.0-float64(rank)/float64(len(ftsIDs)))
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
		var neighbours []*models.Memory
		var gErr error
		if searchGraphStore != nil {
			seeds := make([]int64, len(top5))
			for i, sm := range top5 {
				seeds[i] = sm.Memory.ID
			}
			refs, neighborErr := searchGraphStore.NeighborsForSearch(tier2Ctx, project, seeds, fusedIDs, 20, *opts.Access)
			gErr = neighborErr
			if gErr == nil {
				neighbours, gErr = searchStore.GetByRefsForSearch(tier2Ctx, project, refs, *opts.Access)
			}
		} else {
			// Trusted internal callers retain the existing traversal contract.
			neighbourSet := make(map[int64]bool)
			for _, sm := range top5 {
				results, tErr := gStore.Traverse(tier2Ctx, sm.Memory.ID, 1, nil)
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
			nIDs := make([]int64, 0, len(neighbourSet))
			for id := range neighbourSet {
				nIDs = append(nIDs, id)
			}
			sort.Slice(nIDs, func(i, j int) bool { return nIDs[i] < nIDs[j] })
			if len(nIDs) > 20 {
				nIDs = nIDs[:20]
			}
			if len(nIDs) > 0 {
				neighbours, gErr = memStore.GetByIDs(tier2Ctx, project, nIDs)
			}
		}
		if gErr == nil {
			for _, m := range neighbours {
				// Graph penalty: 0.85 multiplier on the composite score.
				sm := Score(m, 0.5, now)
				sm.Score *= 0.85
				sm.orderKey = sm.Score // keep the sort key aligned with the penalized composite
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
		// Revalidate immediately before the external payload and its candidate cap.
		if searchStore != nil {
			scored, err = revalidateSearchResults(ctx, project, scored, searchStore, *opts.Access)
			if err != nil {
				return nil, nil, err
			}
		}
		if len(scored) > 1 {
			rerankApplyCrossEncoder(ctx, opts.Reranker, query, scored)
		}
	}
	// A provider call can overlap ACL, lifecycle or version changes. Do not return
	// stale bodies or let those references consume the final visible limit.
	if searchStore != nil {
		scored, err = revalidateSearchResults(ctx, project, scored, searchStore, *opts.Access)
		if err != nil {
			return nil, nil, err
		}
	}

	// Final sort: orderKey desc → ID asc (deterministic tie-breaker). orderKey equals
	// the composite Score for every candidate unless a cross-encoder reranked the pool,
	// in which case the reranked candidates carry synthetic descending keys above the
	// tail. Sorting by orderKey (not Score) lets the public Score stay an honest [0,1].
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].orderKey != scored[j].orderKey {
			return scored[i].orderKey > scored[j].orderKey
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
		// Partial selection: move the RerankMaxCandidates highest-ranked candidates to
		// the front so indices [0,RerankMaxCandidates) are the rerank pool. Sort by
		// orderKey (== composite Score pre-rerank) — A full sort is acceptable (n is
		// bounded by limit*5) and keeps the mapping simple.
		sort.Slice(scored, func(i, j int) bool {
			if scored[i].orderKey != scored[j].orderKey {
				return scored[i].orderKey > scored[j].orderKey
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

	// Find the max orderKey among the reranked pool so the rerank keys sit strictly
	// ABOVE every un-reranked tail candidate (indices [n,len)) — reranked results must
	// always outrank the tail that the cross-encoder never saw. Seed from scored[0]
	// (not 0.0) so the invariant holds even if Score() ever emits negatives — the
	// pool was sorted desc above when n<len, and for n==len every candidate is in the
	// pool, so this max is the true global max regardless of sign.
	maxFused := scored[0].orderKey
	for i := 1; i < n; i++ {
		if scored[i].orderKey > maxFused {
			maxFused = scored[i].orderKey
		}
	}
	base := maxFused + float64(len(results)) + 1

	// Assign strictly-descending keys in the reranker's returned order. results[0] is
	// most-relevant → highest key. We rewrite orderKey (the sort key), NOT Score — the
	// public Score field stays the honest composite in [0,1] so callers that display
	// or threshold `score` see the documented value even after a rerank reorder. The
	// raw relevance lands in RerankScore for observability. Out-of-range indices are
	// skipped (client already guards, belt-and-suspenders here).
	for rank, r := range results {
		if r.Index < 0 || r.Index >= n {
			continue
		}
		scored[r.Index].orderKey = base - float64(rank)
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
