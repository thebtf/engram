package uci

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pgvector/pgvector-go"
	"github.com/thebtf/engram/internal/embedding"
)

const semanticEmbeddingDimension = 1536

// TestUCIVectorProfileCacheReusesOnlyExactCompatibleInput deliberately uses a
// deterministic embedder only for cache-key behavior. It is not semantic
// acceptance evidence; TestUCISemanticRealProviderConceptualHitMatchesScopedPostgresBaseline
// is the only test in this file that may satisfy FR-07.
func TestUCIVectorProfileCacheReusesOnlyExactCompatibleInput(t *testing.T) {
	fixture := newSemanticTestFixture()
	profile := semanticTestProfile("uci-semantic-test-model")
	store := newSemanticMemoryStore()
	provider := &semanticTestEmbedder{
		model:  profile.Model,
		vector: semanticTestVector(1),
	}
	service := NewSemanticService(profile, provider, store, &semanticTestLexicalStore{})
	ctx := context.Background()
	authorized := newAuthorizedContext(fixture.contextA)

	if err := service.EnsureCandidateEmbedding(ctx, authorized, fixture.current); err != nil {
		t.Fatalf("first EnsureCandidateEmbedding() error = %v", err)
	}
	if err := service.EnsureCandidateEmbedding(ctx, authorized, fixture.current); err != nil {
		t.Fatalf("exact retry EnsureCandidateEmbedding() error = %v", err)
	}
	if got := provider.CallCount(); got != 1 {
		t.Fatalf("exact compatible input provider calls = %d, want 1 cache miss followed by reuse", got)
	}

	changed := fixture.current
	changed.Text = "A transaction-scoped advisory lock serializes writers and avoids racing overwrites after a saved source change."
	changed.Span.ByteEnd = int64(len(changed.Text))
	changed.Proof.ContentDigest = semanticTestDigest(changed.Text)
	changed.Proof.FactsDigest = semanticTestDigest("facts:" + changed.Text)
	if err := service.EnsureCandidateEmbedding(ctx, authorized, changed); err != nil {
		t.Fatalf("changed-byte EnsureCandidateEmbedding() error = %v", err)
	}
	if got := provider.CallCount(); got != 2 {
		t.Fatalf("changed-byte provider calls = %d, want 2 because a prior content digest is not reusable", got)
	}

	for _, tc := range []struct {
		name    string
		profile VectorProfile
		context ContextRef
	}{
		{
			name: "different provider profile",
			profile: func() VectorProfile {
				other := profile
				other.ProviderRef = "uci-semantic-test-provider-v2"
				return other
			}(),
			context: fixture.contextA,
		},
		{
			name: "different model",
			profile: func() VectorProfile {
				other := profile
				other.Model = "uci-semantic-test-model-v2"
				return other
			}(),
			context: fixture.contextA,
		},
		{
			name: "different preprocessing revision",
			profile: func() VectorProfile {
				other := profile
				other.PreprocessingRevision = "uci-semantic-preprocess/identity-v2"
				return other
			}(),
			context: fixture.contextA,
		},
		{
			name:    "different authorized view",
			profile: profile,
			context: fixture.contextB,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := fixture.current
			candidate.Context = tc.context
			candidate.Proof.ArtifactID = semanticTestArtifactID(tc.name)
			candidate.Proof.FactsDigest = semanticTestDigest("facts:" + tc.name)
			candidate.Text = fixture.current.Text + " // " + tc.name
			candidate.Span.ByteEnd = int64(len(candidate.Text))
			candidate.Proof.ContentDigest = semanticTestDigest(candidate.Text)

			candidateProvider := &semanticTestEmbedder{
				model:  tc.profile.Model,
				vector: semanticTestVector(float32(len(tc.name) + 2)),
			}
			candidateService := NewSemanticService(tc.profile, candidateProvider, store, &semanticTestLexicalStore{})
			if err := candidateService.EnsureCandidateEmbedding(ctx, newAuthorizedContext(tc.context), candidate); err != nil {
				t.Fatalf("EnsureCandidateEmbedding() error = %v", err)
			}
			if got := candidateProvider.CallCount(); got != 1 {
				t.Fatalf("provider calls = %d, want 1 because the prior cache entry is incompatible", got)
			}
		})
	}
}

func TestUCISemanticEmbeddingInputUsesChunkScopedV2Identity(t *testing.T) {
	profile := semanticTestProfile("uci-semantic-test-model")
	candidate := newSemanticTestFixture().current

	input, digest, err := SemanticEmbeddingInput(profile, candidate)
	if err != nil {
		t.Fatalf("SemanticEmbeddingInput() error = %v", err)
	}
	var canonical struct {
		Schema        string `json:"schema"`
		ContentDigest string `json:"content_digest"`
	}
	if err := json.Unmarshal([]byte(input), &canonical); err != nil {
		t.Fatalf("decode canonical input: %v", err)
	}
	if got, want := canonical.Schema, "engram.uci-semantic-input/2"; got != want {
		t.Fatalf("input schema = %q, want %q", got, want)
	}
	if got, want := canonical.ContentDigest, string(semanticTestDigest(candidate.Text)); got != want {
		t.Fatalf("input content digest = %q, want exact chunk digest %q", got, want)
	}

	changedBlobProof := candidate
	changedBlobProof.Proof.ContentDigest = semanticTestDigest("whole-file-version-two")
	proofInput, proofDigest, err := SemanticEmbeddingInput(profile, changedBlobProof)
	if err != nil {
		t.Fatalf("SemanticEmbeddingInput(changed blob proof) error = %v", err)
	}
	if proofInput != input || proofDigest != digest {
		t.Fatalf("changed full blob proof changed v2 input identity: input=%q digest=%q, want %q %q", proofInput, proofDigest, input, digest)
	}

	for _, changed := range []struct {
		name      string
		candidate QueryCandidate
		profile   VectorProfile
	}{
		{
			name: "text",
			candidate: func() QueryCandidate {
				value := candidate
				value.Text += " changed"
				value.Span.ByteEnd = int64(len(value.Text))
				value.Proof.ContentDigest = semanticTestDigest("whole-file-text-changed")
				return value
			}(),
			profile: profile,
		},
		{
			name: "relative path",
			candidate: func() QueryCandidate {
				value := candidate
				value.RelativePath = "internal/storage/other.go"
				return value
			}(),
			profile: profile,
		},
		{
			name:      "preprocessing profile",
			candidate: candidate,
			profile: func() VectorProfile {
				value := profile
				value.PreprocessingRevision = "uci-semantic-preprocess/chunk-v3"
				return value
			}(),
		},
	} {
		t.Run(changed.name, func(t *testing.T) {
			changedInput, changedDigest, err := SemanticEmbeddingInput(changed.profile, changed.candidate)
			if err != nil {
				t.Fatalf("SemanticEmbeddingInput() error = %v", err)
			}
			if changedInput == input || changedDigest == digest {
				t.Fatalf("%s did not change semantic input identity", changed.name)
			}
		})
	}
}

func TestUCISemanticProviderFailuresRemainLexicalAndVisible(t *testing.T) {
	fixture := newSemanticTestFixture()
	profile := semanticTestProfile("uci-semantic-test-model")
	for _, tc := range []struct {
		name     string
		provider SemanticEmbedder
		reason   string
	}{
		{name: "provider absent", provider: nil, reason: "vector_provider_unavailable"},
		{name: "provider quota exhausted", provider: &semanticTestEmbedder{model: profile.Model, err: semanticTestStatusError{code: 429}}, reason: "vector_provider_quota"},
		{name: "provider deadline exceeded", provider: &semanticTestEmbedder{model: profile.Model, err: context.DeadlineExceeded}, reason: "vector_provider_timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			semanticRequireProviderFailureFallback(t, fixture, profile, tc.provider, tc.reason)
		})
	}
}

func semanticRequireProviderFailureFallback(t *testing.T, fixture semanticTestFixture, profile VectorProfile, provider SemanticEmbedder, reason string) {
	t.Helper()
	lexical := &semanticTestLexicalStore{candidates: []QueryCandidate{fixture.lexical}}
	store := newSemanticMemoryStore()
	service := NewSemanticService(profile, provider, store, lexical)
	result, err := service.Query(context.Background(), newAuthorizedContext(fixture.contextA), semanticTestQuerySpec("fallback-token"))
	if err != nil {
		t.Fatalf("Query() error = %v, want lexical-only degraded response", err)
	}
	semanticAssertResponseBoundTo(t, result.Response, fixture.contextA)
	if result.Response.Retrieval == nil {
		t.Fatal("degraded response retrieval = nil")
	}
	if got := result.Response.Retrieval.Mode; got != QueryRetrievalLexical {
		t.Fatalf("degraded response retrieval mode = %q, want lexical", got)
	}
	if !semanticTestContains(result.Response.Retrieval.DegradationReasons, reason) {
		t.Fatalf("degradation reasons = %#v, want %q", result.Response.Retrieval.DegradationReasons, reason)
	}
	items := semanticResponseItems(t, result)
	if len(items) != 1 {
		t.Fatalf("lexical fallback item count = %d, want 1", len(items))
	}
	if got, want := items[0].Ref.EntityKey, fixture.lexical.EntityKey; got != want {
		t.Fatalf("lexical fallback entity key = %q, want %q", got, want)
	}
	if got, want := items[0].MatchSources, []QueryMatchSource{QueryMatchFTS}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lexical fallback match sources = %#v, want %#v", got, want)
	}
	if got := store.SelectCallCount(); got != 0 {
		t.Fatalf("semantic store calls = %d, want 0 after provider degradation", got)
	}
}

func TestUCISemanticQueryProviderTimeoutLeavesParentAliveForLexicalFallback(t *testing.T) {
	fixture := newSemanticTestFixture()
	profile := semanticTestProfile("uci-semantic-test-model")
	lexical := &semanticTestLexicalStore{candidates: []QueryCandidate{fixture.lexical}}
	service := NewSemanticService(profile, &semanticBlockingEmbedder{model: profile.Model}, newSemanticMemoryStore(), lexical)
	service.queryProviderBudget = 25 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	result, err := service.Query(ctx, newAuthorizedContext(fixture.contextA), semanticTestQuerySpec("fallback-token"))
	if err != nil {
		t.Fatalf("Query() error = %v, want lexical fallback", err)
	}
	if ctx.Err() != nil {
		t.Fatalf("query provider timeout cancelled the parent context: %v", ctx.Err())
	}
	if elapsed := time.Since(started); elapsed >= 500*time.Millisecond {
		t.Fatalf("query provider fallback elapsed = %s, want bounded child deadline", elapsed)
	}
	if result.Response.Retrieval == nil || result.Response.Retrieval.Mode != QueryRetrievalLexical || !semanticTestContains(result.Response.Retrieval.DegradationReasons, "vector_provider_timeout") {
		t.Fatalf("query provider fallback retrieval = %#v", result.Response.Retrieval)
	}
	items := semanticResponseItems(t, result)
	if len(items) != 1 || items[0].Ref.EntityKey != fixture.lexical.EntityKey {
		t.Fatalf("query provider fallback items = %#v", items)
	}
}

func TestUCISemanticQueryMapsCompleteScopedHybridPage(t *testing.T) {
	fixture := newSemanticTestFixture()
	profile := semanticTestProfile("uci-semantic-test-model")
	store := newSemanticMemoryStore()
	store.semanticResult = SemanticStoreResult{
		Candidates: []SemanticCandidate{
			{Candidate: fixture.current, MatchSources: []QueryMatchSource{QueryMatchFTS, QueryMatchVector}},
			{Candidate: fixture.lexical, MatchSources: []QueryMatchSource{QueryMatchFTS, QueryMatchVector}},
			{Candidate: fixture.distractorA, MatchSources: []QueryMatchSource{QueryMatchVector}},
		},
		Coverage:       IndexCoverageComplete,
		VectorCoverage: 1,
	}
	service := NewSemanticService(profile, &semanticTestEmbedder{model: profile.Model, vector: semanticTestVector(1)}, store, &semanticTestLexicalStore{})

	result, err := service.Query(context.Background(), newAuthorizedContext(fixture.contextA), semanticTestQuerySpec("fallback-token"))
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	semanticAssertResponseBoundTo(t, result.Response, fixture.contextA)
	if result.Response.Retrieval == nil || result.Response.Retrieval.Mode != QueryRetrievalHybrid {
		t.Fatalf("retrieval = %#v, want complete hybrid result", result.Response.Retrieval)
	}
	if result.Response.Retrieval.VectorCoverage == nil || *result.Response.Retrieval.VectorCoverage != 1 {
		t.Fatalf("vector coverage = %#v, want complete coverage", result.Response.Retrieval.VectorCoverage)
	}
	if len(result.Response.Retrieval.DegradationReasons) != 0 {
		t.Fatalf("complete hybrid degradation reasons = %#v, want none", result.Response.Retrieval.DegradationReasons)
	}

	matchSources := make(map[string][]QueryMatchSource)
	for _, item := range semanticResponseItems(t, result) {
		matchSources[item.Ref.EntityKey] = item.MatchSources
	}
	for entityKey, want := range map[string][]QueryMatchSource{
		fixture.current.EntityKey:     {QueryMatchFTS, QueryMatchVector},
		fixture.lexical.EntityKey:     {QueryMatchFTS, QueryMatchVector},
		fixture.distractorA.EntityKey: {QueryMatchVector},
	} {
		if got := matchSources[entityKey]; !reflect.DeepEqual(got, want) {
			t.Fatalf("match sources for %q = %#v, want %#v", entityKey, got, want)
		}
	}
}

// TestUCISemanticQueryTraversesOneStoreFusedOrdering verifies continuation
// offsets are applied only to the already-fused store ordering.
func TestUCISemanticQueryTraversesOneStoreFusedOrdering(t *testing.T) {
	fixture := newSemanticTestFixture()
	lexical := []QueryCandidate{
		semanticTestPaginationCandidate(fixture.contextA, "lexical-0", "b/lexical-0.go", 30),
		semanticTestPaginationCandidate(fixture.contextA, "lexical-1", "d/lexical-1.go", 20),
		semanticTestPaginationCandidate(fixture.contextA, "lexical-2", "f/lexical-2.go", 10),
	}
	vector := []QueryCandidate{
		semanticTestPaginationCandidate(fixture.contextA, "vector-0", "c/vector-0.go", 30),
		semanticTestPaginationCandidate(fixture.contextA, "vector-1", "e/vector-1.go", 20),
		semanticTestPaginationCandidate(fixture.contextA, "vector-2", "g/vector-2.go", 10),
	}
	winner := semanticTestPaginationCandidate(fixture.contextA, "winner", "a/winner.go", 1)
	store := &semanticPagingStore{fused: []SemanticCandidate{
		semanticTestFusedCandidate(winner),
		semanticTestFusedCandidate(lexical[0]),
		semanticTestFusedCandidate(vector[0]),
		semanticTestFusedCandidate(lexical[1]),
		semanticTestFusedCandidate(vector[1]),
		semanticTestFusedCandidate(lexical[2]),
		semanticTestFusedCandidate(vector[2]),
	}}
	service := NewSemanticService(semanticTestProfile("uci-semantic-test-model"), &semanticTestEmbedder{
		model:  "uci-semantic-test-model",
		vector: semanticTestVector(1),
	}, store, store)
	spec := semanticTestQuerySpec("fusion")
	spec.Limit = 2

	want := []string{
		"symbol:winner",
		"symbol:lexical-0",
		"symbol:vector-0",
		"symbol:lexical-1",
		"symbol:vector-1",
		"symbol:lexical-2",
		"symbol:vector-2",
	}
	got := make([]string, 0, len(want))
	for {
		result, err := service.Query(context.Background(), newAuthorizedContext(fixture.contextA), spec)
		if err != nil {
			t.Fatalf("Query() error = %v", err)
		}
		for _, item := range semanticResponseItems(t, result) {
			got = append(got, item.Ref.EntityKey)
		}
		if result.Response.Continuation == nil || result.Response.Continuation.Value == nil {
			break
		}
		token := *result.Response.Continuation.Value
		spec.Continuation = &token
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fused pages = %#v, want %#v", got, want)
	}
	for index, call := range store.calls {
		if wantOffset := index * 2; call.Offset != wantOffset || call.Limit != 2 {
			t.Fatalf("fused call %d = %#v, want offset=%d limit=2", index, call, wantOffset)
		}
	}
}

func TestUCISemanticQueryContinuationRejectsChangedRankingVector(t *testing.T) {
	fixture := newSemanticTestFixture()
	store := &semanticPagingStore{fused: []SemanticCandidate{
		semanticTestFusedCandidate(fixture.current),
		semanticTestFusedCandidate(fixture.distractorA),
	}}
	provider := &semanticTestEmbedder{model: "uci-semantic-test-model", vector: semanticTestVector(1)}
	service := NewSemanticService(semanticTestProfile(provider.model), provider, store, store)
	spec := semanticTestQuerySpec("ranking-binding")
	spec.Limit = 1
	first, err := service.Query(context.Background(), newAuthorizedContext(fixture.contextA), spec)
	if err != nil {
		t.Fatalf("first Query() error = %v", err)
	}
	token := semanticTestContinuation(t, first)
	provider.vector = semanticTestVector(2)
	spec.Continuation = &token
	if _, err := service.Query(context.Background(), newAuthorizedContext(fixture.contextA), spec); err == nil || !strings.Contains(err.Error(), "continuation ranking") {
		t.Fatalf("changed-vector continuation error = %v, want ranking binding rejection", err)
	}
}

func TestUCISemanticQueryFallsBackWhenVectorCoverageIsIncomplete(t *testing.T) {
	fixture := newSemanticTestFixture()
	profile := semanticTestProfile("uci-semantic-test-model")
	lexical := &semanticTestLexicalStore{candidates: []QueryCandidate{fixture.lexical}}
	store := newSemanticMemoryStore()
	store.semanticResult = SemanticStoreResult{
		Coverage:       IndexCoveragePartial,
		VectorCoverage: 0.5,
	}
	service := NewSemanticService(profile, &semanticTestEmbedder{model: profile.Model, vector: semanticTestVector(1)}, store, lexical)

	result, err := service.Query(context.Background(), newAuthorizedContext(fixture.contextA), semanticTestQuerySpec("fallback-token"))
	if err != nil {
		t.Fatalf("Query() error = %v, want lexical fallback", err)
	}
	semanticAssertResponseBoundTo(t, result.Response, fixture.contextA)
	if result.Response.Retrieval == nil || result.Response.Retrieval.Mode != QueryRetrievalLexical {
		t.Fatalf("retrieval = %#v, want lexical fallback", result.Response.Retrieval)
	}
	if !semanticTestContains(result.Response.Retrieval.DegradationReasons, "vector_coverage_incomplete") {
		t.Fatalf("degradation reasons = %#v, want incomplete vector coverage", result.Response.Retrieval.DegradationReasons)
	}
	items := semanticResponseItems(t, result)
	if len(items) != 1 || items[0].Ref.EntityKey != fixture.lexical.EntityKey || !reflect.DeepEqual(items[0].MatchSources, []QueryMatchSource{QueryMatchFTS}) {
		t.Fatalf("lexical fallback items = %#v", items)
	}
	if got := store.SelectCallCount(); got != 1 {
		t.Fatalf("semantic store calls = %d, want one incomplete coverage result", got)
	}
}

func TestUCISemanticRealProviderConceptualHitMatchesScopedPostgresBaseline(t *testing.T) {
	t.Run("real configured provider produces a non-lexical conceptual result", func(t *testing.T) {
		semanticRequireRealProviderConceptualHit(t)
	})
}

func semanticRequireRealProviderConceptualHit(t *testing.T) {
	t.Helper()
	real := newSemanticRealProviderFixture(t)
	fixture := newSemanticTestFixture()
	semanticRequireConceptualFixture(t, fixture)
	seeds, excluded := semanticRealProviderSeeds(fixture, real.profile)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	semanticEmbedRealProviderSeeds(t, ctx, real, seeds)
	result, call, spec := semanticRequireRealProviderQuery(t, ctx, real, fixture)
	baseline := semanticRequireRealProviderBaseline(t, ctx, real, fixture, call, spec, excluded)
	semanticRequireRealProviderResponse(t, result, baseline)
}

func semanticRequireConceptualFixture(t *testing.T, fixture semanticTestFixture) {
	t.Helper()
	if overlap := semanticLexicalOverlap(semanticConceptualQuery, fixture.current.Text); len(overlap) != 0 {
		t.Fatalf("conceptual fixture has lexical overlap %v; it cannot prove semantic retrieval", overlap)
	}
}

func semanticRealProviderSeeds(fixture semanticTestFixture, profile VectorProfile) ([]semanticTestSeed, []string) {
	wrongProvider := profile
	wrongProvider.ProviderRef = profile.ProviderRef + "-wrong"
	wrongModel := profile
	wrongModel.Model = profile.Model + "-wrong"
	wrongPreprocessing := profile
	wrongPreprocessing.PreprocessingRevision = profile.PreprocessingRevision + "-wrong"
	stale := fixture.current
	stale.EntityKey = "symbol:advisory-lock-retired"
	stale.Text = "A transaction lock used an obsolete byte sequence."
	stale.Span.ByteEnd = int64(len(stale.Text))
	stale.Proof.ArtifactID = semanticTestArtifactID("retired")
	stale.Proof.ContentDigest = semanticTestDigest(stale.Text)
	stale.Proof.FactsDigest = semanticTestDigest("facts:" + stale.Text)
	foreign := fixture.current
	foreign.EntityKey = "symbol:advisory-lock-foreign-view"
	foreign.Context = fixture.contextB
	foreign.Proof.ArtifactID = semanticTestArtifactID("foreign")
	foreign.Proof.FactsDigest = semanticTestDigest("facts:foreign")
	wrongProfileCandidate := fixture.current
	wrongProfileCandidate.EntityKey = "symbol:advisory-lock-wrong-provider"
	wrongProfileCandidate.Proof.ArtifactID = semanticTestArtifactID("wrong-provider")
	wrongProfileCandidate.Proof.FactsDigest = semanticTestDigest("facts:wrong-provider")
	wrongModelCandidate := fixture.current
	wrongModelCandidate.EntityKey = "symbol:advisory-lock-wrong-model"
	wrongModelCandidate.Proof.ArtifactID = semanticTestArtifactID("wrong-model")
	wrongModelCandidate.Proof.FactsDigest = semanticTestDigest("facts:wrong-model")
	wrongPreprocessingCandidate := fixture.current
	wrongPreprocessingCandidate.EntityKey = "symbol:advisory-lock-wrong-preprocessing"
	wrongPreprocessingCandidate.Proof.ArtifactID = semanticTestArtifactID("wrong-preprocessing")
	wrongPreprocessingCandidate.Proof.FactsDigest = semanticTestDigest("facts:wrong-preprocessing")
	seeds := []semanticTestSeed{
		{candidate: fixture.current, profile: profile, currentDigest: fixture.current.Proof.ContentDigest},
		{candidate: fixture.distractorA, profile: profile, currentDigest: fixture.distractorA.Proof.ContentDigest},
		{candidate: fixture.distractorB, profile: profile, currentDigest: fixture.distractorB.Proof.ContentDigest},
		{candidate: stale, profile: profile, currentDigest: fixture.current.Proof.ContentDigest},
		{candidate: foreign, profile: profile, currentDigest: foreign.Proof.ContentDigest},
		{candidate: wrongProfileCandidate, profile: wrongProvider, currentDigest: wrongProfileCandidate.Proof.ContentDigest},
		{candidate: wrongModelCandidate, profile: wrongModel, currentDigest: wrongModelCandidate.Proof.ContentDigest},
		{candidate: wrongPreprocessingCandidate, profile: wrongPreprocessing, currentDigest: wrongPreprocessingCandidate.Proof.ContentDigest},
	}
	return seeds, []string{stale.EntityKey, foreign.EntityKey, wrongProfileCandidate.EntityKey, wrongModelCandidate.EntityKey, wrongPreprocessingCandidate.EntityKey}
}

func semanticEmbedRealProviderSeeds(t *testing.T, ctx context.Context, real semanticRealProviderFixture, seeds []semanticTestSeed) {
	t.Helper()
	inputs := make([]string, len(seeds))
	for index, seed := range seeds {
		input, _, err := SemanticEmbeddingInput(seed.profile, seed.candidate)
		if err != nil {
			t.Fatalf("build canonical semantic input for seed %q: %v", seed.candidate.EntityKey, err)
		}
		inputs[index] = input
	}
	vectors, err := real.provider.Embed(ctx, inputs)
	if err != nil {
		t.Fatalf("real provider Embed(seed corpus) error = %v", err)
	}
	if len(vectors) != len(seeds) {
		t.Fatalf("real provider seed vector count = %d, want %d", len(vectors), len(seeds))
	}
	for index, seed := range seeds {
		semanticRequireRealVector(t, vectors[index], real.profile.Dimension, "seed "+seed.candidate.EntityKey)
		if err := real.store.Insert(seed.candidate, seed.profile, vectors[index], seed.currentDigest); err != nil {
			t.Fatalf("insert real provider vector for %q: %v", seed.candidate.EntityKey, err)
		}
	}
}

func semanticRequireRealProviderQuery(t *testing.T, ctx context.Context, real semanticRealProviderFixture, fixture semanticTestFixture) (QueryResult, semanticPostgresSelectCall, QuerySpec) {
	t.Helper()
	spec := semanticTestQuerySpec(semanticConceptualQuery)
	service := NewSemanticService(real.profile, real.provider, real.store, &semanticTestLexicalStore{})
	result, err := service.Query(ctx, newAuthorizedContext(fixture.contextA), spec)
	if err != nil {
		t.Fatalf("Query() with real provider error = %v", err)
	}
	if result.Response.Retrieval == nil {
		t.Fatal("real semantic response retrieval = nil")
	}
	if got := result.Response.Retrieval.Mode; got != QueryRetrievalHybrid {
		t.Fatalf("real semantic retrieval mode = %q, want hybrid", got)
	}
	if result.Response.Retrieval.VectorCoverage == nil || *result.Response.Retrieval.VectorCoverage <= 0 {
		t.Fatalf("real semantic vector coverage = %#v, want a positive scoped value", result.Response.Retrieval.VectorCoverage)
	}
	semanticAssertResponseBoundTo(t, result.Response, fixture.contextA)
	call, ok := real.store.LastSelectCall()
	if !ok {
		t.Fatal("semantic store received no vector selection call")
	}
	if got := real.provider.LastInput(); len(got) != 1 || !strings.Contains(strings.ToLower(got[0]), strings.ToLower(semanticConceptualQuery)) {
		t.Fatalf("real provider query input = %#v, want one input containing conceptual query %q", got, semanticConceptualQuery)
	}
	if got := call.Context; !semanticContextRefsEqual(got, fixture.contextA) {
		t.Fatalf("semantic store context = %#v, want %#v", got, fixture.contextA)
	}
	if !semanticProfilesEqual(call.Profile, real.profile) {
		t.Fatalf("semantic store profile = %#v, want %#v", call.Profile, real.profile)
	}
	semanticRequireRealVector(t, call.Vector, real.profile.Dimension, "provider-generated query")
	return result, call, spec
}

func semanticRequireRealProviderBaseline(t *testing.T, ctx context.Context, real semanticRealProviderFixture, fixture semanticTestFixture, call semanticPostgresSelectCall, spec QuerySpec, excluded []string) []semanticPostgresBaselineHit {
	t.Helper()
	baseline, err := real.store.ExactBaseline(ctx, newAuthorizedContext(fixture.contextA), real.profile, call.Vector, spec)
	if err != nil {
		t.Fatalf("exact PostgreSQL scoped distance baseline error = %v", err)
	}
	if len(baseline) != 3 {
		t.Fatalf("exact PostgreSQL scoped baseline count = %d, want only three current compatible candidates", len(baseline))
	}
	if got, want := baseline[0].Candidate.EntityKey, fixture.current.EntityKey; got != want {
		t.Fatalf("exact PostgreSQL conceptual top hit = %q, want %q", got, want)
	}
	for _, forbidden := range excluded {
		if semanticBaselineContains(baseline, forbidden) {
			t.Fatalf("exact PostgreSQL baseline leaked incompatible candidate %q", forbidden)
		}
	}
	return baseline
}

func semanticRequireRealProviderResponse(t *testing.T, result QueryResult, baseline []semanticPostgresBaselineHit) {
	t.Helper()
	items := semanticResponseItems(t, result)
	if len(items) != len(baseline) {
		t.Fatalf("semantic response item count = %d, want exact baseline count %d", len(items), len(baseline))
	}
	for index, hit := range baseline {
		if got, want := items[index].Ref.EntityKey, hit.Candidate.EntityKey; got != want {
			t.Fatalf("semantic response item %d entity key = %q, want exact PostgreSQL baseline %q", index, got, want)
		}
		if got, want := items[index].MatchSources, []QueryMatchSource{QueryMatchVector}; !reflect.DeepEqual(got, want) {
			t.Fatalf("semantic response item %d match sources = %#v, want vector provenance %#v", index, got, want)
		}
	}
}

// SemanticEmbedder is intentionally exercised through *embedding.Client in the
// real-provider test. The local implementation below exists only for exact
// cache and degraded-path contracts.
type semanticTestEmbedder struct {
	mu     sync.Mutex
	model  string
	vector []float32
	err    error
	calls  int
	inputs [][]string
}

var _ SemanticEmbedder = (*semanticTestEmbedder)(nil)

func (provider *semanticTestEmbedder) Model() string {
	return provider.model
}

func (provider *semanticTestEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	provider.mu.Lock()
	provider.calls++
	provider.inputs = append(provider.inputs, append([]string(nil), texts...))
	err := provider.err
	provider.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	vectors := make([][]float32, len(texts))
	for index := range texts {
		vectors[index] = append([]float32(nil), provider.vector...)
	}
	return vectors, nil
}

func (provider *semanticTestEmbedder) CallCount() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.calls
}

type semanticBlockingEmbedder struct {
	model string
}

func (provider *semanticBlockingEmbedder) Model() string {
	return provider.model
}

func (*semanticBlockingEmbedder) Embed(ctx context.Context, _ []string) ([][]float32, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// semanticTestStatusError deliberately exposes the same structural HTTP status
// signal a production embedding adapter must preserve for quota classification.
type semanticTestStatusError struct {
	code int
}

func (err semanticTestStatusError) Error() string {
	return fmt.Sprintf("embedding provider HTTP %d", err.code)
}

func (err semanticTestStatusError) StatusCode() int {
	return err.code
}

// semanticRecordingEmbedder delegates to the production embedding client while
// recording UCI inputs for the real-provider fixture's query assertion.
type semanticRecordingEmbedder struct {
	client *embedding.Client

	mu     sync.Mutex
	inputs [][]string
}

var _ SemanticEmbedder = (*semanticRecordingEmbedder)(nil)

func newSemanticRecordingEmbedderFromEnvironment() (*semanticRecordingEmbedder, error) {
	client, err := embedding.NewClientWithSettings(context.Background(), nil)
	if err != nil {
		return nil, err
	}
	return &semanticRecordingEmbedder{client: client}, nil
}

func (provider *semanticRecordingEmbedder) Model() string {
	return provider.client.Model()
}

func (provider *semanticRecordingEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	provider.mu.Lock()
	provider.inputs = append(provider.inputs, append([]string(nil), texts...))
	provider.mu.Unlock()
	return provider.client.Embed(ctx, texts)
}

func (provider *semanticRecordingEmbedder) LastInput() []string {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.inputs) == 0 {
		return nil
	}
	return append([]string(nil), provider.inputs[len(provider.inputs)-1]...)
}

type semanticTestLexicalStore struct {
	candidates []QueryCandidate

	mu    sync.Mutex
	calls int
}

var _ QueryStore = (*semanticTestLexicalStore)(nil)

func (store *semanticTestLexicalStore) SelectCandidates(_ context.Context, authorized AuthorizedContext, _ QuerySpec) (QueryStoreResult, error) {
	store.mu.Lock()
	store.calls++
	store.mu.Unlock()

	selected := authorized.Ref()
	result := QueryStoreResult{Coverage: IndexCoverageComplete}
	for _, candidate := range store.candidates {
		if semanticContextRefsEqual(candidate.Context, selected) {
			result.Candidates = append(result.Candidates, candidate)
		}
	}
	return result, nil
}

type semanticMemoryStore struct {
	mu sync.Mutex

	cache          map[semanticMemoryCacheKey][]float32
	selectCalls    int
	semanticResult SemanticStoreResult
	semanticErr    error
}

type semanticMemoryCacheKey struct {
	SourceID              string
	CheckoutID            string
	ViewID                string
	AnalysisProfileID     string
	Generation            int64
	ProviderRef           string
	Model                 string
	Dimension             int
	PreprocessingRevision string
	IncludeRelativePath   bool
	EmbeddingInputDigest  IndexDigest
}

var _ SemanticStore = (*semanticMemoryStore)(nil)

func newSemanticMemoryStore() *semanticMemoryStore {
	return &semanticMemoryStore{
		cache:          make(map[semanticMemoryCacheKey][]float32),
		semanticResult: SemanticStoreResult{Coverage: IndexCoverageComplete, VectorCoverage: 1},
	}
}

func (store *semanticMemoryStore) LookupCandidateEmbedding(_ context.Context, authorized AuthorizedContext, profile VectorProfile, candidate QueryCandidate) ([]float32, bool, error) {
	if !semanticContextRefsEqual(authorized.Ref(), candidate.Context) {
		return nil, false, fmt.Errorf("semantic test store: candidate is outside authorized context")
	}
	key := semanticMemoryKey(authorized.Ref(), profile, candidate.Proof.ContentDigest)
	store.mu.Lock()
	defer store.mu.Unlock()
	vector, found := store.cache[key]
	return append([]float32(nil), vector...), found, nil
}

func (store *semanticMemoryStore) StoreCandidateEmbedding(_ context.Context, authorized AuthorizedContext, profile VectorProfile, candidate QueryCandidate, vector []float32) error {
	if !semanticContextRefsEqual(authorized.Ref(), candidate.Context) {
		return fmt.Errorf("semantic test store: candidate is outside authorized context")
	}
	key := semanticMemoryKey(authorized.Ref(), profile, candidate.Proof.ContentDigest)
	store.mu.Lock()
	defer store.mu.Unlock()
	store.cache[key] = append([]float32(nil), vector...)
	return nil
}

func (store *semanticMemoryStore) SelectHybridCandidates(_ context.Context, _ AuthorizedContext, _ VectorProfile, _ []float32, _ QuerySpec) (SemanticStoreResult, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.selectCalls++
	return store.semanticResult, store.semanticErr
}

func (store *semanticMemoryStore) SelectCallCount() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.selectCalls
}

func semanticMemoryKey(ref ContextRef, profile VectorProfile, digest IndexDigest) semanticMemoryCacheKey {
	return semanticMemoryCacheKey{
		SourceID:              ref.SourceID,
		CheckoutID:            ref.CheckoutID,
		ViewID:                ref.ViewID,
		AnalysisProfileID:     ref.AnalysisProfileID,
		Generation:            ref.Generation,
		ProviderRef:           profile.ProviderRef,
		Model:                 profile.Model,
		Dimension:             profile.Dimension,
		PreprocessingRevision: profile.PreprocessingRevision,
		IncludeRelativePath:   profile.IncludeRelativePath,
		EmbeddingInputDigest:  digest,
	}
}

type semanticPostgresStore struct {
	tx *sql.Tx

	mu          sync.Mutex
	records     map[string]QueryCandidate
	nextRecord  int
	selectCalls []semanticPostgresSelectCall
}

type semanticPostgresSelectCall struct {
	Context ContextRef
	Profile VectorProfile
	Vector  []float32
	Spec    QuerySpec
}

type semanticPostgresBaselineHit struct {
	Candidate QueryCandidate
	Distance  float64
}

var _ SemanticStore = (*semanticPostgresStore)(nil)

func newSemanticPostgresStore(tx *sql.Tx) *semanticPostgresStore {
	return &semanticPostgresStore{
		tx:      tx,
		records: make(map[string]QueryCandidate),
	}
}

func (store *semanticPostgresStore) LookupCandidateEmbedding(ctx context.Context, authorized AuthorizedContext, profile VectorProfile, candidate QueryCandidate) ([]float32, bool, error) {
	if !semanticContextRefsEqual(authorized.Ref(), candidate.Context) {
		return nil, false, fmt.Errorf("semantic PostgreSQL store: candidate is outside authorized context")
	}
	ref := authorized.Ref()
	var vector pgvector.Vector
	err := store.tx.QueryRowContext(ctx, `
		SELECT vector
		FROM uci_semantic_test_vectors
		WHERE source_id = $1
		  AND checkout_id = $2
		  AND view_id = $3
		  AND analysis_profile_id = $4
		  AND generation = $5
		  AND provider_ref = $6
		  AND model = $7
		  AND dimension = $8
		  AND preprocessing_revision = $9
		  AND include_relative_path = $10
		  AND embedding_input_digest = $11
		  AND embedding_input_digest = current_content_digest
		LIMIT 1`,
		ref.SourceID,
		ref.CheckoutID,
		ref.ViewID,
		ref.AnalysisProfileID,
		ref.Generation,
		profile.ProviderRef,
		profile.Model,
		profile.Dimension,
		profile.PreprocessingRevision,
		profile.IncludeRelativePath,
		candidate.Proof.ContentDigest,
	).Scan(&vector)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return append([]float32(nil), vector.Slice()...), true, nil
}

func (store *semanticPostgresStore) StoreCandidateEmbedding(ctx context.Context, authorized AuthorizedContext, profile VectorProfile, candidate QueryCandidate, vector []float32) error {
	if !semanticContextRefsEqual(authorized.Ref(), candidate.Context) {
		return fmt.Errorf("semantic PostgreSQL store: candidate is outside authorized context")
	}
	return store.insert(ctx, candidate, profile, vector, candidate.Proof.ContentDigest)
}

func (store *semanticPostgresStore) SelectHybridCandidates(ctx context.Context, authorized AuthorizedContext, profile VectorProfile, vector []float32, spec QuerySpec) (SemanticStoreResult, error) {
	store.mu.Lock()
	store.selectCalls = append(store.selectCalls, semanticPostgresSelectCall{
		Context: authorized.Ref(),
		Profile: profile,
		Vector:  append([]float32(nil), vector...),
		Spec:    spec,
	})
	store.mu.Unlock()

	baseline, err := store.ExactBaseline(ctx, authorized, profile, vector, spec)
	if err != nil {
		return SemanticStoreResult{}, err
	}
	result := SemanticStoreResult{Coverage: IndexCoverageComplete, VectorCoverage: 1}
	for _, hit := range baseline {
		candidate := hit.Candidate
		candidate.Score = 1 - hit.Distance
		result.Candidates = append(result.Candidates, SemanticCandidate{Candidate: candidate, MatchSources: []QueryMatchSource{QueryMatchVector}})
	}
	return result, nil
}

func (store *semanticPostgresStore) Insert(candidate QueryCandidate, profile VectorProfile, vector []float32, currentDigest IndexDigest) error {
	return store.insert(context.Background(), candidate, profile, vector, currentDigest)
}

func (store *semanticPostgresStore) insert(ctx context.Context, candidate QueryCandidate, profile VectorProfile, vector []float32, currentDigest IndexDigest) error {
	if !candidate.Context.valid() {
		return fmt.Errorf("semantic PostgreSQL store: invalid candidate context")
	}
	if len(vector) != profile.Dimension {
		return fmt.Errorf("semantic PostgreSQL store: vector dimension = %d, want %d", len(vector), profile.Dimension)
	}
	store.mu.Lock()
	store.nextRecord++
	recordKey := fmt.Sprintf("candidate-%d", store.nextRecord)
	store.mu.Unlock()

	ref := candidate.Context
	_, err := store.tx.ExecContext(ctx, `
		INSERT INTO uci_semantic_test_vectors (
			record_key,
			source_id,
			checkout_id,
			view_id,
			analysis_profile_id,
			generation,
			provider_ref,
			model,
			dimension,
			preprocessing_revision,
			include_relative_path,
			embedding_input_digest,
			current_content_digest,
			vector
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14::vector)`,
		recordKey,
		ref.SourceID,
		ref.CheckoutID,
		ref.ViewID,
		ref.AnalysisProfileID,
		ref.Generation,
		profile.ProviderRef,
		profile.Model,
		profile.Dimension,
		profile.PreprocessingRevision,
		profile.IncludeRelativePath,
		candidate.Proof.ContentDigest,
		currentDigest,
		pgvector.NewVector(vector),
	)
	if err != nil {
		return err
	}
	store.mu.Lock()
	store.records[recordKey] = candidate
	store.mu.Unlock()
	return nil
}

func (store *semanticPostgresStore) ExactBaseline(ctx context.Context, authorized AuthorizedContext, profile VectorProfile, vector []float32, spec QuerySpec) ([]semanticPostgresBaselineHit, error) {
	if len(vector) != profile.Dimension {
		return nil, fmt.Errorf("exact PostgreSQL baseline vector dimension = %d, want %d", len(vector), profile.Dimension)
	}
	ref := authorized.Ref()
	rows, err := store.tx.QueryContext(ctx, `
		SELECT record_key, vector <=> $1::vector AS distance
		FROM uci_semantic_test_vectors
		WHERE source_id = $2
		  AND checkout_id = $3
		  AND view_id = $4
		  AND analysis_profile_id = $5
		  AND generation = $6
		  AND provider_ref = $7
		  AND model = $8
		  AND dimension = $9
		  AND preprocessing_revision = $10
		  AND include_relative_path = $11
		  AND embedding_input_digest = current_content_digest
		ORDER BY vector <=> $12::vector, record_key ASC
		LIMIT $13`,
		pgvector.NewVector(vector),
		ref.SourceID,
		ref.CheckoutID,
		ref.ViewID,
		ref.AnalysisProfileID,
		ref.Generation,
		profile.ProviderRef,
		profile.Model,
		profile.Dimension,
		profile.PreprocessingRevision,
		profile.IncludeRelativePath,
		pgvector.NewVector(vector),
		spec.Limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	store.mu.Lock()
	defer store.mu.Unlock()
	hits := make([]semanticPostgresBaselineHit, 0, spec.Limit)
	for rows.Next() {
		var recordKey string
		var distance float64
		if err := rows.Scan(&recordKey, &distance); err != nil {
			return nil, err
		}
		candidate, found := store.records[recordKey]
		if !found {
			return nil, fmt.Errorf("exact PostgreSQL baseline missing record %q", recordKey)
		}
		hits = append(hits, semanticPostgresBaselineHit{Candidate: candidate, Distance: distance})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return hits, nil
}

func (store *semanticPostgresStore) LastSelectCall() (semanticPostgresSelectCall, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.selectCalls) == 0 {
		return semanticPostgresSelectCall{}, false
	}
	call := store.selectCalls[len(store.selectCalls)-1]
	call.Vector = append([]float32(nil), call.Vector...)
	return call, true
}

type semanticRealProviderFixture struct {
	profile  VectorProfile
	provider *semanticRecordingEmbedder
	store    *semanticPostgresStore
}

func newSemanticRealProviderFixture(t *testing.T) semanticRealProviderFixture {
	t.Helper()
	prerequisites := []string{}
	if strings.TrimSpace(os.Getenv("UCI_SEMANTIC_REAL_PROVIDER")) != "1" {
		prerequisites = append(prerequisites, "UCI_SEMANTIC_REAL_PROVIDER=1")
	}
	if strings.TrimSpace(os.Getenv("UCI_SEMANTIC_TEST_DSN")) == "" {
		prerequisites = append(prerequisites, "UCI_SEMANTIC_TEST_DSN (disposable PostgreSQL+pgvector database)")
	}
	if strings.TrimSpace(os.Getenv("ENGRAM_EMBEDDING_URL")) == "" {
		prerequisites = append(prerequisites, "ENGRAM_EMBEDDING_URL")
	}
	if strings.TrimSpace(os.Getenv("ENGRAM_EMBEDDING_MODEL")) == "" {
		prerequisites = append(prerequisites, "ENGRAM_EMBEDDING_MODEL")
	}
	if strings.TrimSpace(os.Getenv("UCI_SEMANTIC_PROVIDER_REF")) == "" {
		prerequisites = append(prerequisites, "UCI_SEMANTIC_PROVIDER_REF")
	}
	if strings.TrimSpace(os.Getenv("UCI_SEMANTIC_PREPROCESSING_REVISION")) == "" {
		prerequisites = append(prerequisites, "UCI_SEMANTIC_PREPROCESSING_REVISION")
	}
	if len(prerequisites) != 0 {
		t.Skip("real semantic-provider success requires " + strings.Join(prerequisites, ", ") + "; set ENGRAM_EMBEDDING_API_KEY too when the configured provider requires bearer authentication")
	}

	provider, err := newSemanticRecordingEmbedderFromEnvironment()
	if err != nil {
		t.Fatalf("construct configured real semantic test provider: %v", err)
	}
	db, err := sql.Open("pgx", os.Getenv("UCI_SEMANTIC_TEST_DSN"))
	if err != nil {
		t.Fatalf("open UCI semantic disposable PostgreSQL fixture: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("ping UCI semantic disposable PostgreSQL fixture: %v", err)
	}

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin isolated UCI semantic fixture transaction: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })

	var vectorExtensionCount int
	if err := tx.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM pg_extension WHERE extname = 'vector'`).Scan(&vectorExtensionCount); err != nil {
		t.Fatalf("inspect pgvector prerequisite: %v", err)
	}
	if vectorExtensionCount != 1 {
		t.Fatal("UCI_SEMANTIC_TEST_DSN lacks the required pgvector extension")
	}
	if _, err := tx.ExecContext(context.Background(), `
		CREATE TEMP TABLE uci_semantic_test_vectors (
			record_key TEXT PRIMARY KEY,
			source_id TEXT NOT NULL,
			checkout_id TEXT NOT NULL,
			view_id TEXT NOT NULL,
			analysis_profile_id TEXT NOT NULL,
			generation BIGINT NOT NULL,
			provider_ref TEXT NOT NULL,
			model TEXT NOT NULL,
			dimension INTEGER NOT NULL,
			preprocessing_revision TEXT NOT NULL,
			include_relative_path BOOLEAN NOT NULL,
			embedding_input_digest TEXT NOT NULL,
			current_content_digest TEXT NOT NULL,
			vector vector(1536) NOT NULL
		) ON COMMIT DROP`); err != nil {
		t.Fatalf("create isolated UCI semantic pgvector fixture: %v", err)
	}

	return semanticRealProviderFixture{
		profile: VectorProfile{
			ProviderRef:           strings.TrimSpace(os.Getenv("UCI_SEMANTIC_PROVIDER_REF")),
			Model:                 provider.Model(),
			Dimension:             semanticEmbeddingDimension,
			PreprocessingRevision: strings.TrimSpace(os.Getenv("UCI_SEMANTIC_PREPROCESSING_REVISION")),
			IncludeRelativePath:   true,
		},
		provider: provider,
		store:    newSemanticPostgresStore(tx),
	}
}

type semanticTestSeed struct {
	candidate     QueryCandidate
	profile       VectorProfile
	currentDigest IndexDigest
}

type semanticTestFixture struct {
	contextA    ContextRef
	contextB    ContextRef
	current     QueryCandidate
	distractorA QueryCandidate
	distractorB QueryCandidate
	lexical     QueryCandidate
}

const semanticConceptualQuery = "How are simultaneous edits kept from clobbering one another?"

func newSemanticTestFixture() semanticTestFixture {
	contextA := semanticTestContextRef(
		"22000000-0000-4000-8000-000000000043",
		"32000000-0000-4000-8000-000000000043",
		"42000000-0000-4000-8000-000000000043",
		"52000000-0000-4000-8000-000000000043",
		43,
	)
	contextB := semanticTestContextRef(
		contextA.SourceID,
		"32000000-0000-4000-8000-000000000044",
		"42000000-0000-4000-8000-000000000044",
		contextA.AnalysisProfileID,
		44,
	)
	return semanticTestFixture{
		contextA: contextA,
		contextB: contextB,
		current: semanticTestCandidate(
			contextA,
			"72000000-0000-4000-8000-000000000043",
			"symbol:advisory-lock",
			"AcquireUpdateLease",
			"storage.AcquireUpdateLease",
			"internal/storage/lease.go",
			"A transaction-scoped advisory lock serializes writers and avoids racing overwrites.",
		),
		distractorA: semanticTestCandidate(
			contextA,
			"72000000-0000-4000-8000-000000000044",
			"symbol:json-encoder",
			"EncodePayload",
			"wire.EncodePayload",
			"internal/wire/encode.go",
			"A hexadecimal encoder writes a JSON response to an HTTP socket.",
		),
		distractorB: semanticTestCandidate(
			contextA,
			"72000000-0000-4000-8000-000000000045",
			"symbol:identifier-linter",
			"CheckIdentifier",
			"lint.CheckIdentifier",
			"internal/lint/identifier.go",
			"A linter rejects identifiers that contain a tab character.",
		),
		lexical: semanticTestCandidate(
			contextA,
			"72000000-0000-4000-8000-000000000046",
			"symbol:lexical-fallback",
			"FallbackToken",
			"search.FallbackToken",
			"internal/search/fallback.go",
			"func FallbackToken() {}",
		),
	}
}

func semanticTestContextRef(sourceID, checkoutID, viewID, profileID string, generation int64) ContextRef {
	spaceID := "12000000-0000-4000-8000-000000000043"
	return ContextRef{
		SpaceID:           &spaceID,
		SourceID:          sourceID,
		CheckoutID:        checkoutID,
		ViewID:            viewID,
		AnalysisProfileID: profileID,
		Generation:        generation,
	}
}

func semanticTestCandidate(contextRef ContextRef, artifactID, entityKey, localName, qualifiedSymbol, relativePath, text string) QueryCandidate {
	return QueryCandidate{
		Context: contextRef,
		Proof: IndexArtifactProof{
			ArtifactID:         artifactID,
			ContentDigest:      semanticTestDigest(text),
			FactsDigest:        semanticTestDigest("facts:" + text),
			DefinitionCount:    1,
			ReferenceSiteCount: 0,
			ChunkCount:         1,
		},
		EntityKey:       entityKey,
		LocalName:       localName,
		QualifiedSymbol: qualifiedSymbol,
		RelativePath:    relativePath,
		Span: IndexSpan{
			ByteStart: 0,
			ByteEnd:   int64(len(text)),
			LineStart: 1,
			LineEnd:   1,
		},
		Text:     text,
		Kind:     QueryItemCode,
		Language: "go",
		Score:    1,
	}
}

func semanticTestProfile(model string) VectorProfile {
	return VectorProfile{
		ProviderRef:           "uci-semantic-test-provider",
		Model:                 model,
		Dimension:             semanticEmbeddingDimension,
		PreprocessingRevision: "uci-semantic-preprocess/identity-v1",
		IncludeRelativePath:   true,
	}
}

func semanticTestQuerySpec(text string) QuerySpec {
	return QuerySpec{
		ClientSessionID: "semantic-client-a",
		Mode:            QueryModeFTS,
		Text:            text,
		Filter:          QueryFilter{Languages: []string{"go"}},
		Order:           QueryOrderRelevance,
		Limit:           3,
	}
}

func semanticTestVector(seed float32) []float32 {
	vector := make([]float32, semanticEmbeddingDimension)
	vector[0] = seed
	vector[len(vector)/2] = seed / 2
	vector[len(vector)-1] = seed / 4
	return vector
}

func semanticTestDigest(value string) IndexDigest {
	sum := sha256.Sum256([]byte(value))
	return IndexDigest(fmt.Sprintf("sha256:%x", sum))
}

func semanticTestArtifactID(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	encoded := fmt.Sprintf("%x", sum)
	return encoded[0:8] + "-" + encoded[8:12] + "-4" + encoded[13:16] + "-8" + encoded[17:20] + "-" + encoded[20:32]
}

func semanticContextRefsEqual(left, right ContextRef) bool {
	return semanticOptionalStringsEqual(left.SpaceID, right.SpaceID) &&
		left.SourceID == right.SourceID &&
		left.CheckoutID == right.CheckoutID &&
		left.ViewID == right.ViewID &&
		left.AnalysisProfileID == right.AnalysisProfileID &&
		left.Generation == right.Generation
}

func semanticOptionalStringsEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func semanticProfilesEqual(left, right VectorProfile) bool {
	return left.ProviderRef == right.ProviderRef &&
		left.Model == right.Model &&
		left.Dimension == right.Dimension &&
		left.PreprocessingRevision == right.PreprocessingRevision &&
		left.IncludeRelativePath == right.IncludeRelativePath
}

func semanticResponseItems(t *testing.T, result QueryResult) []QueryItem {
	t.Helper()
	if result.Response.Items == nil {
		t.Fatal("response items = nil")
	}
	return *result.Response.Items
}

func semanticAssertResponseBoundTo(t *testing.T, response QueryResponse, want ContextRef) {
	t.Helper()
	if response.Contexts == nil || len(*response.Contexts) != 1 {
		t.Fatalf("response contexts = %#v, want one selected context", response.Contexts)
	}
	got := (*response.Contexts)[0]
	if got.SourceID != want.SourceID ||
		got.CheckoutID != want.CheckoutID ||
		got.ViewID != want.ViewID ||
		got.ProfileID != want.AnalysisProfileID ||
		got.Generation != want.Generation ||
		!semanticOptionalStringsEqual(got.SpaceID, want.SpaceID) {
		t.Fatalf("response context = %#v, want %#v", got, want)
	}
}

func semanticRequireRealVector(t *testing.T, vector []float32, dimension int, label string) {
	t.Helper()
	if len(vector) != dimension {
		t.Fatalf("%s vector dimension = %d, want profile dimension %d", label, len(vector), dimension)
	}
	for index, value := range vector {
		if math.IsNaN(float64(value)) {
			t.Fatalf("%s vector contains NaN at index %d", label, index)
		}
	}
}

func semanticLexicalOverlap(left, right string) []string {
	leftTokens := semanticTokens(left)
	rightTokens := semanticTokens(right)
	overlap := make([]string, 0)
	for token := range leftTokens {
		if _, found := rightTokens[token]; found {
			overlap = append(overlap, token)
		}
	}
	sort.Strings(overlap)
	return overlap
}

func semanticTokens(text string) map[string]struct{} {
	tokens := make(map[string]struct{})
	for _, token := range strings.FieldsFunc(strings.ToLower(text), func(character rune) bool {
		return !unicode.IsLetter(character) && !unicode.IsDigit(character)
	}) {
		if token != "" {
			tokens[token] = struct{}{}
		}
	}
	return tokens
}

func semanticBaselineContains(hits []semanticPostgresBaselineHit, entityKey string) bool {
	for _, hit := range hits {
		if hit.Candidate.EntityKey == entityKey {
			return true
		}
	}
	return false
}

func semanticTestContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

type semanticPagingStore struct {
	fused []SemanticCandidate
	calls []QuerySpec
}

var (
	_ QueryStore    = (*semanticPagingStore)(nil)
	_ SemanticStore = (*semanticPagingStore)(nil)
)

func (*semanticPagingStore) SelectCandidates(_ context.Context, _ AuthorizedContext, _ QuerySpec) (QueryStoreResult, error) {
	return QueryStoreResult{Coverage: IndexCoverageComplete}, nil
}

func (*semanticPagingStore) LookupCandidateEmbedding(context.Context, AuthorizedContext, VectorProfile, QueryCandidate) ([]float32, bool, error) {
	return nil, false, nil
}

func (*semanticPagingStore) StoreCandidateEmbedding(context.Context, AuthorizedContext, VectorProfile, QueryCandidate, []float32) error {
	return nil
}

func (store *semanticPagingStore) SelectHybridCandidates(_ context.Context, _ AuthorizedContext, _ VectorProfile, _ []float32, spec QuerySpec) (SemanticStoreResult, error) {
	store.calls = append(store.calls, spec)
	return SemanticStoreResult{
		Candidates:     semanticTestPageCandidates(store.fused, spec),
		Coverage:       IndexCoverageComplete,
		VectorCoverage: 1,
	}, nil
}

func semanticTestPageCandidates(candidates []SemanticCandidate, spec QuerySpec) []SemanticCandidate {
	if spec.Offset >= len(candidates) {
		return nil
	}
	end := spec.Offset + spec.Limit + 1
	if end > len(candidates) {
		end = len(candidates)
	}
	return candidates[spec.Offset:end]
}

func semanticTestFusedCandidate(candidate QueryCandidate) SemanticCandidate {
	return SemanticCandidate{Candidate: candidate, MatchSources: []QueryMatchSource{QueryMatchFTS, QueryMatchVector}}
}

func semanticTestPaginationCandidate(ref ContextRef, name, relativePath string, score float64) QueryCandidate {
	text := "func " + strings.ReplaceAll(name, "-", "_") + "() {}"
	candidate := semanticTestCandidate(ref, semanticTestArtifactID(name), "symbol:"+name, name, "fixture."+name, relativePath, text)
	candidate.Score = score
	return candidate
}

func semanticTestContinuation(t *testing.T, result QueryResult) string {
	t.Helper()
	if result.Response.Continuation == nil || result.Response.Continuation.Value == nil {
		t.Fatalf("continuation = %#v, want opaque value", result.Response.Continuation)
	}
	return *result.Response.Continuation.Value
}
