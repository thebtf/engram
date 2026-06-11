package retrieval

// RankingExplanation contains the per-signal score breakdown for a single
// result returned by HybridSearch. Produced when HybridOptions.Explain is true.
// Fields mirror the FR-C4 formula:
//
//	score = 0.4×relevance + 0.3×recency + 0.3×importance
type RankingExplanation struct {
	// MemoryID is the ID of the ranked memory.
	MemoryID int64 `json:"memory_id"`
	// Relevance is the cosine similarity of the query embedding against the
	// memory embedding (0–1). For FTS-only results (no embedding service) this
	// is approximated from the FTS rank normalised to [0,1].
	Relevance float64 `json:"relevance"`
	// Recency is 0.995^hours_since_access (0–1 decay).
	Recency float64 `json:"recency"`
	// Importance is the normalised composite importance score (0–1).
	Importance float64 `json:"importance"`
	// FusedScore is the final composite score after FR-C4 weighting.
	FusedScore float64 `json:"fused_score"`
	// SourceTier indicates which retrieval tier produced this result.
	// Values: "tier0_exact", "tier1_fts", "tier1_vector", "tier2_graph".
	SourceTier string `json:"source_tier"`
}
