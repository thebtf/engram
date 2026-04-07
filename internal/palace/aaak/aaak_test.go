package aaak

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectEmotions_Basic(t *testing.T) {
	codes := DetectEmotions("I'm scared and anxious about the deployment")
	assert.Contains(t, codes, "fear")
	assert.Contains(t, codes, "anx")
}

func TestDetectEmotions_Empty(t *testing.T) {
	codes := DetectEmotions("")
	assert.Nil(t, codes)
}

func TestDetectEmotions_NoDuplicates(t *testing.T) {
	codes := DetectEmotions("happy happy happy excited delighted")
	count := 0
	for _, c := range codes {
		if c == "joy" {
			count++
		}
	}
	assert.Equal(t, 1, count, "joy should appear only once")
}

func TestDetectFlags_Basic(t *testing.T) {
	flags := DetectFlags("We decided to migrate the database architecture")
	assert.Contains(t, flags, "DECISION")
	assert.Contains(t, flags, "TECHNICAL")
}

func TestDetectFlags_Empty(t *testing.T) {
	flags := DetectFlags("")
	assert.Nil(t, flags)
}

func TestDetectFlags_Pivot(t *testing.T) {
	flags := DetectFlags("This was a turning point for the entire project")
	assert.Contains(t, flags, "PIVOT")
}

func TestExtractTopics_Basic(t *testing.T) {
	topics := ExtractTopics("PostgreSQL uses pgvector for embedding search in the engram server", 3)
	assert.NotEmpty(t, topics)
	// Should include domain-specific terms, not stopwords
	for _, topic := range topics {
		assert.False(t, stopWords[topic], "topic %q should not be a stopword", topic)
	}
}

func TestExtractTopics_CamelCaseBoost(t *testing.T) {
	topics := ExtractTopics("The FalkorDB and PostgreSQL databases. Also falkordb is used here.", 3)
	assert.Contains(t, topics, "falkordb", "CamelCase terms should be boosted")
}

func TestExtractTopics_Empty(t *testing.T) {
	topics := ExtractTopics("", 5)
	assert.Nil(t, topics)
}

func TestExtractTopics_ZeroMax(t *testing.T) {
	topics := ExtractTopics("hello world", 0)
	assert.Nil(t, topics)
}

func TestGenerateCode_Natural(t *testing.T) {
	code := GenerateCode("PostgreSQL", map[string]bool{})
	assert.Equal(t, "POS", code)
}

func TestGenerateCode_Collision(t *testing.T) {
	existing := map[string]bool{"POS": true}
	code := GenerateCode("PostgreSQL", existing)
	assert.NotEqual(t, "POS", code, "should not return taken code")
	assert.Len(t, code, 3)
}

func TestGenerateCode_Empty(t *testing.T) {
	code := GenerateCode("", map[string]bool{})
	assert.Equal(t, "UNK", code)
}

func TestGenerateCode_ShortName(t *testing.T) {
	code := GenerateCode("Go", map[string]bool{})
	assert.Len(t, code, 3)
}

func TestCompress_Basic(t *testing.T) {
	narrative := "We decided to use PostgreSQL with pgvector for the engram server architecture. This was a turning point — we were anxious about migration but hopeful about performance."
	meta := CompressMeta{
		EntityCodes: map[string]string{
			"postgresql": "POS",
			"pgvector":   "PGV",
			"engram":     "ENG",
		},
		Project: "engram",
		Type:    "decision",
	}

	result := Compress(narrative, meta)
	assert.NotEmpty(t, result)
	assert.Contains(t, result, "|") // wire format has pipe separators

	// Should contain entity codes
	parts := strings.Split(result, "|")
	assert.GreaterOrEqual(t, len(parts), 5, "wire format should have 5 sections")

	// Entities section should have codes
	entitySection := parts[0]
	assert.True(t, strings.Contains(entitySection, "POS") || strings.Contains(entitySection, "PGV") || strings.Contains(entitySection, "ENG"),
		"should contain at least one entity code")
}

func TestCompress_Empty(t *testing.T) {
	result := Compress("", CompressMeta{})
	assert.Equal(t, "", result)
}

func TestCompress_CompressionRatio(t *testing.T) {
	// Representative engram observation narrative
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
			"falkordb": "FAL", "splade": "SPL",
		},
	}

	compressed := Compress(narrative, meta)
	assert.NotEmpty(t, compressed)

	// Check character-level compression ratio (proxy for token ratio)
	ratio := float64(len(narrative)) / float64(len(compressed))
	t.Logf("Compression ratio: %.1fx (original: %d chars, compressed: %d chars)",
		ratio, len(narrative), len(compressed))

	// Character ratio should be substantial (token ratio will be higher)
	assert.Greater(t, ratio, 5.0, "character compression ratio should be >5x")
}

func TestDecode_Basic(t *testing.T) {
	aaak := `POS,PGV,ENG|postgresql,engram,architecture|"three-layer memory architecture"|anx,hope|DECISION,TECHNICAL`
	result, err := Decode(aaak)
	require.NoError(t, err)

	assert.Equal(t, []string{"POS", "PGV", "ENG"}, result.Entities)
	assert.Equal(t, []string{"postgresql", "engram", "architecture"}, result.Topics)
	assert.Equal(t, []string{"three-layer memory architecture"}, result.Quotes)
	assert.Equal(t, []string{"anx", "hope"}, result.Emotions)
	assert.Equal(t, []string{"DECISION", "TECHNICAL"}, result.Flags)
}

func TestDecode_Empty(t *testing.T) {
	result, err := Decode("")
	require.NoError(t, err)
	assert.Empty(t, result.Entities)
}

func TestRoundTrip(t *testing.T) {
	narrative := "We decided to deploy the PostgreSQL server with optimistic hope for better performance"
	meta := CompressMeta{
		EntityCodes: map[string]string{"postgresql": "POS"},
	}

	compressed := Compress(narrative, meta)
	decoded, err := Decode(compressed)
	require.NoError(t, err)

	// Verify round-trip preserves key information
	assert.NotEmpty(t, decoded.Entities, "round-trip should preserve entities")
	assert.NotEmpty(t, decoded.Topics, "round-trip should preserve topics")
	assert.NotEmpty(t, decoded.Flags, "round-trip should preserve flags")
}

func TestCompress_Unicode(t *testing.T) {
	meta := CompressMeta{
		EntityCodes: map[string]string{"кириллица": "KIR"},
	}
	result := Compress("Решили использовать кириллица для тестирования", meta)
	assert.NotEmpty(t, result)
	assert.Contains(t, result, "KIR")
}

func TestCompress_LongText(t *testing.T) {
	// Generate a long text (~5000 chars)
	long := strings.Repeat("The PostgreSQL database server handles all persistence. ", 100)
	meta := CompressMeta{
		EntityCodes: map[string]string{"postgresql": "POS"},
	}
	result := Compress(long, meta)
	assert.NotEmpty(t, result)
	// Compressed should be much shorter than original
	assert.Less(t, len(result), len(long)/5, "compressed should be <20% of original")
}
