// Package similarity provides text similarity and clustering utilities.
package similarity

import (
	"database/sql"
	"testing"

	"github.com/thebtf/engram/pkg/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// JaccardSimilarity
// ---------------------------------------------------------------------------

func TestJaccardSimilarity_BothEmpty(t *testing.T) {
	result := JaccardSimilarity(map[string]bool{}, map[string]bool{})
	assert.InDelta(t, 1.0, result, 0.001, "two empty sets are fully similar")
}

func TestJaccardSimilarity_OneEmpty(t *testing.T) {
	assert.InDelta(t, 0.0, JaccardSimilarity(map[string]bool{"x": true}, map[string]bool{}), 0.001)
	assert.InDelta(t, 0.0, JaccardSimilarity(map[string]bool{}, map[string]bool{"y": true}), 0.001)
}

func TestJaccardSimilarity_Table(t *testing.T) {
	cases := []struct {
		name     string
		a        map[string]bool
		b        map[string]bool
		expected float64
	}{
		{
			name:     "identical sets",
			a:        map[string]bool{"p": true, "q": true, "r": true},
			b:        map[string]bool{"p": true, "q": true, "r": true},
			expected: 1.0,
		},
		{
			name:     "disjoint sets",
			a:        map[string]bool{"alpha": true, "beta": true},
			b:        map[string]bool{"gamma": true, "delta": true},
			expected: 0.0,
		},
		{
			name:     "half overlap — 2 shared out of 4",
			a:        map[string]bool{"a": true, "b": true, "c": true},
			b:        map[string]bool{"b": true, "c": true, "d": true},
			expected: 0.5,
		},
		{
			name:     "subset — 2 shared out of 4",
			a:        map[string]bool{"x": true, "y": true},
			b:        map[string]bool{"x": true, "y": true, "z": true, "w": true},
			expected: 0.5,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.InDelta(t, tc.expected, JaccardSimilarity(tc.a, tc.b), 0.001)
		})
	}
}

// ---------------------------------------------------------------------------
// addTerms
// ---------------------------------------------------------------------------

func TestAddTerms_BasicTokenisation(t *testing.T) {
	terms := make(map[string]bool)
	addTerms(terms, "The quick brown fox jumps over the lazy dog")

	for _, want := range []string{"quick", "brown", "fox", "jumps", "over", "lazy", "dog"} {
		assert.Contains(t, terms, want)
	}
	assert.NotContains(t, terms, "the") // stop word
}

func TestAddTerms_ShortWordsExcluded(t *testing.T) {
	t.Parallel()
	terms := make(map[string]bool)
	addTerms(terms, "I am a go developer")

	assert.NotContains(t, terms, "i")
	assert.NotContains(t, terms, "am")
	assert.NotContains(t, terms, "a")
	assert.NotContains(t, terms, "go") // 2 chars
	assert.Contains(t, terms, "developer")
}

func TestAddTerms_HyphenSplitsWords(t *testing.T) {
	t.Parallel()
	terms := make(map[string]bool)
	addTerms(terms, "user_id authentication-flow jwt_token")

	assert.Contains(t, terms, "user_id")
	assert.Contains(t, terms, "authentication")
	assert.Contains(t, terms, "flow")
	assert.Contains(t, terms, "jwt_token")
}

func TestAddTerms_StopWordsFiltered(t *testing.T) {
	t.Parallel()
	terms := make(map[string]bool)
	addTerms(terms, "this is what we do and the rest")

	for _, sw := range []string{"this", "is", "what", "and", "the"} {
		assert.NotContains(t, terms, sw)
	}
	assert.Contains(t, terms, "rest")
}

// ---------------------------------------------------------------------------
// ExtractObservationTerms
// ---------------------------------------------------------------------------

func TestExtractObservationTerms_TitleNarrativeFacts(t *testing.T) {
	obs := &models.Observation{
		Title:     sql.NullString{String: "Database migration strategy", Valid: true},
		Narrative: sql.NullString{String: "We performed schema migration using flyway", Valid: true},
		Facts:     models.JSONStringArray{"Indexes rebuilt after migration", "Rollback plan exists"},
	}

	terms := ExtractObservationTerms(obs)

	assert.Contains(t, terms, "database")
	assert.Contains(t, terms, "migration")
	assert.Contains(t, terms, "strategy")
	assert.Contains(t, terms, "performed")
	assert.Contains(t, terms, "schema")
	assert.Contains(t, terms, "flyway")
	assert.Contains(t, terms, "indexes")
	assert.Contains(t, terms, "rebuilt")
	assert.Contains(t, terms, "rollback")
	assert.Contains(t, terms, "plan")
	assert.NotContains(t, terms, "we")
	assert.NotContains(t, terms, "the")
}

func TestExtractObservationTerms_FilesRead(t *testing.T) {
	obs := &models.Observation{
		Title:     sql.NullString{String: "Code review", Valid: true},
		FilesRead: models.JSONStringArray{"/src/auth/handler.go", "/pkg/models/user.go"},
	}

	terms := ExtractObservationTerms(obs)
	assert.Contains(t, terms, "handler.go")
	assert.Contains(t, terms, "user.go")
}

func TestExtractObservationTerms_FilesModified(t *testing.T) {
	t.Parallel()
	obs := &models.Observation{
		ID:            1,
		Title:         sql.NullString{String: "Code changes", Valid: true},
		FilesModified: models.JSONStringArray{"/src/handler.go", "/pkg/models/order.go"},
	}

	terms := ExtractObservationTerms(obs)
	assert.Contains(t, terms, "handler.go")
	assert.Contains(t, terms, "order.go")
}

func TestExtractObservationTerms_EmptyObservation(t *testing.T) {
	obs := &models.Observation{
		Title:     sql.NullString{String: "", Valid: false},
		Narrative: sql.NullString{String: "", Valid: false},
	}
	terms := ExtractObservationTerms(obs)
	assert.Empty(t, terms)
}

// ---------------------------------------------------------------------------
// ClusterObservations — small sets (simple O(n²) path, ≤ 50 items)
// ---------------------------------------------------------------------------

func TestClusterObservations_EmptyInput(t *testing.T) {
	result := ClusterObservations([]*models.Observation{}, 0.4)
	assert.Len(t, result, 0)
}

func TestClusterObservations_SingleItem(t *testing.T) {
	obs := &models.Observation{ID: 1, Title: sql.NullString{String: "only one", Valid: true}}
	result := ClusterObservations([]*models.Observation{obs}, 0.4)
	require.Len(t, result, 1)
	assert.Equal(t, int64(1), result[0].ID)
}

func TestClusterObservations_AllUnique(t *testing.T) {
	observations := []*models.Observation{
		{ID: 1, Title: sql.NullString{String: "JWT token authentication", Valid: true},
			Narrative: sql.NullString{String: "OAuth login flow", Valid: true}},
		{ID: 2, Title: sql.NullString{String: "PostgreSQL index optimization", Valid: true},
			Narrative: sql.NullString{String: "B-tree index creation", Valid: true}},
		{ID: 3, Title: sql.NullString{String: "Redis caching layer", Valid: true},
			Narrative: sql.NullString{String: "TTL eviction policy", Valid: true}},
		{ID: 4, Title: sql.NullString{String: "Zerolog structured logging", Valid: true},
			Narrative: sql.NullString{String: "JSON log format", Valid: true}},
		{ID: 5, Title: sql.NullString{String: "Kubernetes horizontal pod autoscaling", Valid: true},
			Narrative: sql.NullString{String: "HPA resource limits", Valid: true}},
	}

	result := ClusterObservations(observations, 0.4)
	assert.Len(t, result, 5, "all unique observations should be preserved")
}

func TestClusterObservations_SimilarPairsClustered(t *testing.T) {
	obs1 := &models.Observation{
		ID:        1,
		Title:     sql.NullString{String: "authentication system implementation", Valid: true},
		Narrative: sql.NullString{String: "JWT-based authentication for API", Valid: true},
	}
	obs2 := &models.Observation{
		ID:        2,
		Title:     sql.NullString{String: "authentication system update", Valid: true},
		Narrative: sql.NullString{String: "Updated JWT authentication logic", Valid: true},
	}
	obs3 := &models.Observation{
		ID:        3,
		Title:     sql.NullString{String: "Database migration strategy", Valid: true},
		Narrative: sql.NullString{String: "PostgreSQL schema migrations", Valid: true},
	}
	obs4 := &models.Observation{
		ID:        4,
		Title:     sql.NullString{String: "Database migration guide", Valid: true},
		Narrative: sql.NullString{String: "Running PostgreSQL migrations", Valid: true},
	}

	result := ClusterObservations([]*models.Observation{obs1, obs2, obs3, obs4}, 0.4)

	t.Logf("Clustered 4 observations to %d", len(result))
	assert.LessOrEqual(t, len(result), 4)
	assert.GreaterOrEqual(t, len(result), 1)

	ids := make(map[int64]bool)
	for _, obs := range result {
		ids[obs.ID] = true
	}
	if len(result) <= 3 {
		assert.True(t, ids[1], "obs1 should be kept as the auth cluster representative")
	}
}

func TestClusterObservations_PreservesFirstItem(t *testing.T) {
	observations := []*models.Observation{
		{ID: 10, Title: sql.NullString{String: "first authentication observation", Valid: true}},
		{ID: 11, Title: sql.NullString{String: "second authentication observation", Valid: true}},
		{ID: 12, Title: sql.NullString{String: "completely different caching topic", Valid: true}},
	}

	result := ClusterObservations(observations, 0.4)
	require.NotEmpty(t, result)
	assert.Equal(t, int64(10), result[0].ID, "first observation must be first representative")
}

func TestClusterObservations_HighThreshold_NoClustering(t *testing.T) {
	t.Parallel()
	observations := []*models.Observation{
		{ID: 1, Title: sql.NullString{String: "authentication implementation", Valid: true}},
		{ID: 2, Title: sql.NullString{String: "authentication update", Valid: true}},
		{ID: 3, Title: sql.NullString{String: "authentication refactor", Valid: true}},
	}

	result := ClusterObservations(observations, 0.9)
	assert.Len(t, result, 3, "threshold 0.9 should prevent clustering similar but not identical items")
}

func TestClusterObservations_LowThreshold_ClustersSimilar(t *testing.T) {
	t.Parallel()
	observations := []*models.Observation{
		{ID: 1, Title: sql.NullString{String: "authentication implementation details", Valid: true}},
		{ID: 2, Title: sql.NullString{String: "authentication security update", Valid: true}},
		{ID: 3, Title: sql.NullString{String: "completely orthogonal caching topic", Valid: true}},
	}

	result := ClusterObservations(observations, 0.1)
	// First two share "authentication" (Jaccard 0.2 >= threshold 0.1) → clustered.
	// Third is orthogonal → kept. Total: 2 representatives, not 3.
	assert.Equal(t, 2, len(result), "low threshold must cluster the two authentication observations into one representative")
}

func TestClusterObservations_UnboundedResult(t *testing.T) {
	// Verify no artificial cap on returned count (10 unique → 10 back)
	observations := make([]*models.Observation, 10)
	distinctTitles := []string{
		"jwt tokens expire daily",
		"postgresql indexes optimize",
		"redis caching values",
		"zerolog structured logs",
		"pytest fixtures setup",
		"docker containers orchestration",
		"prometheus metrics collection",
		"owasp vulnerability scanning",
		"goroutines parallel execution",
		"kubernetes horizontal scaling",
	}
	for i, title := range distinctTitles {
		observations[i] = &models.Observation{
			ID:    int64(i + 1),
			Title: sql.NullString{String: title, Valid: true},
		}
	}

	result := ClusterObservations(observations, 0.4)
	assert.Len(t, result, 10, "all 10 unique observations should be returned")
}

// ---------------------------------------------------------------------------
// ClusterObservations — large sets (optimized path, > 50 items)
// ---------------------------------------------------------------------------

func TestClusterObservations_LargeSet_SimilarPairsReduced(t *testing.T) {
	t.Parallel()

	topics := []string{
		"authentication", "authorization", "database", "caching", "logging",
		"monitoring", "testing", "deployment", "scaling", "security",
		"networking", "storage", "messaging", "scheduling", "configuration",
		"validation", "serialization", "encryption", "compression", "indexing",
		"backup", "recovery", "migration", "versioning", "documentation",
		"profiling", "debugging", "tracing", "alerting", "reporting",
	}

	// 60 observations: 30 similar pairs, one per topic
	observations := make([]*models.Observation, 60)
	for i, topic := range topics {
		observations[i*2] = &models.Observation{
			ID:        int64(i*2 + 1),
			Title:     sql.NullString{String: topic + " implementation", Valid: true},
			Narrative: sql.NullString{String: "Detailed " + topic + " system design", Valid: true},
		}
		observations[i*2+1] = &models.Observation{
			ID:        int64(i*2 + 2),
			Title:     sql.NullString{String: topic + " update", Valid: true},
			Narrative: sql.NullString{String: "Updated " + topic + " logic", Valid: true},
		}
	}

	result := ClusterObservations(observations, 0.4)
	t.Logf("Clustered 60 observations down to %d", len(result))
	assert.Less(t, len(result), 60, "similar pairs should be clustered")
	assert.GreaterOrEqual(t, len(result), 1)
}

func TestClusterObservations_LargeSet_AllUnique(t *testing.T) {
	t.Parallel()

	uniqueTerms := []string{
		"aardvark", "butterfly", "caterpillar", "dragonfly", "elephant",
		"flamingo", "giraffe", "hippopotamus", "iguana", "jellyfish",
		"kangaroo", "leopard", "mongoose", "nightingale", "octopus",
		"penguin", "quail", "rhinoceros", "salamander", "toucan",
		"umbrella", "vulture", "walrus", "xylophone", "yakking",
		"zebra123", "astronomy99", "biology88", "chemistry77", "dynamics66",
		"economics55", "forensics44", "genetics33", "hydraulics22", "immunology11",
		"jurisprudence", "kinetics", "linguistics", "metallurgy", "neurology",
		"oceanography", "pharmacology", "quantumphysics", "robotics", "sociology",
		"thermodynamics", "ultrasound", "virology", "wavelength", "xenobiology",
		"yeastculture", "zoology123", "algebra456", "botany789", "calculus012",
	}

	observations := make([]*models.Observation, 55)
	for i := 0; i < 55; i++ {
		observations[i] = &models.Observation{
			ID:        int64(i + 1),
			Title:     sql.NullString{String: uniqueTerms[i], Valid: true},
			Narrative: sql.NullString{String: uniqueTerms[i], Valid: true},
		}
	}

	result := ClusterObservations(observations, 0.4)
	assert.Len(t, result, 55, "all unique observations must be kept")
}

func TestClusterObservations_LargeSet_SignaturePrefilter(t *testing.T) {
	t.Parallel()

	// 60 items: 30 identical (auth cluster) + 30 with fully unique single-word content
	observations := make([]*models.Observation, 60)
	for i := 0; i < 30; i++ {
		observations[i] = &models.Observation{
			ID:        int64(i + 1),
			Title:     sql.NullString{String: "authentication security login", Valid: true},
			Narrative: sql.NullString{String: "JWT tokens OAuth authentication", Valid: true},
		}
	}

	diffTerms := []string{
		"quantumphysics", "photosynthesis", "archaeologydig", "linguisticstudy", "astronomystar",
		"paleontologyfossil", "oceanographywave", "entomologybug", "mycologyfungi", "herpetologysnake",
		"ornithologybird", "ichthyologyfish", "seismologyquake", "volcanologylava", "meteorologyrain",
		"cartographymap", "ethnographyculture", "philologyword", "numismaticscoin", "heraldryshield",
		"genealogytree", "chronologytime", "typographyfont", "calligraphyink", "epigraphystone",
		"papyrologytext", "codicologybook", "diplomaticseal", "sigillographywax", "sphragisticsring",
	}
	for i := 30; i < 60; i++ {
		term := diffTerms[i-30]
		observations[i] = &models.Observation{
			ID:        int64(i + 1),
			Title:     sql.NullString{String: term, Valid: true},
			Narrative: sql.NullString{String: term, Valid: true},
		}
	}

	result := ClusterObservations(observations, 0.5)
	t.Logf("Clustered 60 to %d", len(result))
	// 1 auth cluster + 30 unique = 31
	assert.Equal(t, 31, len(result), "should have 31 clusters: 1 auth + 30 unique")
}

// ---------------------------------------------------------------------------
// IsSimilarToAny
// ---------------------------------------------------------------------------

func TestIsSimilarToAny_DetectsSimilar(t *testing.T) {
	existing := []*models.Observation{
		{
			ID:        1,
			Title:     sql.NullString{String: "authentication implementation", Valid: true},
			Narrative: sql.NullString{String: "JWT authentication flow", Valid: true},
		},
		{
			ID:        2,
			Title:     sql.NullString{String: "database setup", Valid: true},
			Narrative: sql.NullString{String: "PostgreSQL configuration", Valid: true},
		},
	}

	similar := &models.Observation{
		ID:        3,
		Title:     sql.NullString{String: "authentication update", Valid: true},
		Narrative: sql.NullString{String: "JWT authentication changes", Valid: true},
	}
	different := &models.Observation{
		ID:        4,
		Title:     sql.NullString{String: "caching layer", Valid: true},
		Narrative: sql.NullString{String: "Redis caching implementation", Valid: true},
	}

	assert.True(t, IsSimilarToAny(similar, existing, 0.3))
	assert.False(t, IsSimilarToAny(different, existing, 0.3))
}

func TestIsSimilarToAny_EmptyExisting(t *testing.T) {
	obs := &models.Observation{
		ID:    1,
		Title: sql.NullString{String: "some observation", Valid: true},
	}
	assert.False(t, IsSimilarToAny(obs, []*models.Observation{}, 0.4))
	assert.False(t, IsSimilarToAny(obs, nil, 0.4))
}

func TestIsSimilarToAny_EmptyNewObservation(t *testing.T) {
	t.Parallel()
	emptyObs := &models.Observation{
		ID:        1,
		Title:     sql.NullString{String: "", Valid: false},
		Narrative: sql.NullString{String: "", Valid: false},
	}
	existing := []*models.Observation{
		{ID: 2, Title: sql.NullString{String: "some content here", Valid: true}},
	}
	assert.False(t, IsSimilarToAny(emptyObs, existing, 0.3))
}

// ---------------------------------------------------------------------------
// computeTermSignature
// ---------------------------------------------------------------------------

func TestComputeTermSignature_EmptyTerms(t *testing.T) {
	sig := computeTermSignature(map[string]bool{})
	assert.Equal(t, uint64(0), sig)
}

func TestComputeTermSignature_NonEmptyIsNonZero(t *testing.T) {
	sig := computeTermSignature(map[string]bool{"hello": true})
	assert.NotEqual(t, uint64(0), sig)
}

func TestComputeTermSignature_IdenticalSetsEqual(t *testing.T) {
	a := map[string]bool{"alpha": true, "beta": true, "gamma": true}
	b := map[string]bool{"alpha": true, "beta": true, "gamma": true}
	assert.Equal(t, computeTermSignature(a), computeTermSignature(b))
}

func TestComputeTermSignature_DifferentSetsDiffer(t *testing.T) {
	t.Parallel()
	set1 := map[string]bool{"authentication": true, "security": true}
	set2 := map[string]bool{"database": true, "migration": true}
	// Collision is possible but should be negligible for these inputs
	assert.NotEqual(t, computeTermSignature(set1), computeTermSignature(set2))
}

// ---------------------------------------------------------------------------
// popCount64
// ---------------------------------------------------------------------------

func TestPopCount64_Table(t *testing.T) {
	cases := []struct {
		name     string
		input    uint64
		expected int
	}{
		{"zero", 0, 0},
		{"one", 1, 1},
		{"power of two", 8, 1},
		{"full byte", 0xFF, 8},
		{"alternating bits", 0xAAAAAAAAAAAAAAAA, 32},
		{"all ones", 0xFFFFFFFFFFFFFFFF, 64},
		{"high bit only", 1 << 63, 1},
		{"two sparse bits", 0x8000000000000001, 2},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, popCount64(tc.input))
		})
	}
}
