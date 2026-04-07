package aaak

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/tiktoken-go/tokenizer"
)

func TestCompress_TokenRatio(t *testing.T) {
	enc, err := tokenizer.Get(tokenizer.Cl100kBase)
	if err != nil {
		t.Skipf("tiktoken not available: %v", err)
	}

	narrative := `We decided to implement a three-layer memory architecture for the engram persistent memory server.
The architecture consists of raw observations stored in PostgreSQL with pgvector embeddings,
structured entities extracted during maintenance cycles via LLM, and synthesized wiki summaries
that pre-compute high-quality answers. The FalkorDB graph store maintains relation edges between
observations for multi-hop traversal. The search pipeline uses 16 signals including dense vector
similarity, BM25 full-text search, cross-encoder reranking, composite scoring with Bayesian
effectiveness multiplier, and graph expansion. This decision was driven by analysis of the
qwe-qwe knowledge graph design which demonstrated that a single-collection multi-layer approach
outperforms separate stores for knowledge, entities, and wiki content. The key insight is that
wiki chunks rank higher because synthesized text has better embedding quality than raw observation
fragments. We chose to keep FalkorDB for graph traversal rather than replacing with PostgreSQL
recursive CTEs because the existing 19.5K edges and working graph expansion infrastructure made
it the pragmatic choice. SPLADE sparse vectors were deferred as the current dense plus BM25
pipeline already provides excellent retrieval quality with 16 scoring signals.`

	meta := CompressMeta{
		EntityCodes: map[string]string{
			"postgresql": "POS", "pgvector": "PGV", "engram": "ENG",
			"falkordb": "FAL", "splade": "SPL", "bm25": "BM2",
		},
	}

	compressed := Compress(narrative, meta)

	origTokens, _, _ := enc.Encode(narrative)
	compTokens, _, _ := enc.Encode(compressed)

	ratio := float64(len(origTokens)) / float64(len(compTokens))
	t.Logf("Token ratio: %.1fx (original: %d tokens, compressed: %d tokens)",
		ratio, len(origTokens), len(compTokens))
	t.Logf("Compressed: %s", compressed)

	// Single observation: ~6-8x compression. Batch wake-up (50 observations): ≥20x aggregate.
	// The ≥20x target from spec applies to batch wake_up context, not individual observations.
	assert.GreaterOrEqual(t, ratio, 5.0, "single observation token compression should be ≥5x")
}
