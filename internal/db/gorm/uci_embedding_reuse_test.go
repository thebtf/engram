package gorm

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ucidomain "github.com/thebtf/engram/internal/uci"
)

func TestUCIEmbeddingWorkerReusesInputAcrossAnalysisProfileIDs(t *testing.T) {
	fixture := openUCIEmbeddingJobsFixture(t)
	publication := fixture.publication
	ctx := context.Background()
	artifact := publication.admitArtifact(t, publication.source.SourceID, "worker-reuse", "func SharedEmbedding() string { return \"same input\" }\n", UCIParseArtifactComplete)
	memberships := []ucidomain.IndexMembership{uciPublicationPresentMembership("shared.go", artifact)}
	first := fixture.publish(t, "worker-reuse-first", publication.checkout, nil, ucidomain.IndexJobInitial, []uciPublicationArtifact{artifact}, memberships)
	firstAuthorized := uciEmbeddingJobsAuthorize(t, publication, first.Context)
	catalog := uciEmbeddingJobsCatalog{
		first.Context.ViewID: {Ref: first.Context, AuthRealm: publication.realm},
	}
	resolver := ucidomain.NewContextResolver(catalog, publication.authorizer, nil)
	provider := &uciEmbeddingReuseProvider{model: fixture.profile.Model}
	uciEmbeddingReuseComplete(t, fixture, resolver, provider, firstAuthorized)
	require.Len(t, provider.inputs(), 1, "the first completed View must make one real worker provider invocation")
	require.Equal(t, 1, provider.calls)
	firstCandidate := uciSemanticCandidateAtPath(t, fixture.store, firstAuthorized, "shared.go", artifact.Artifact.ArtifactID)
	firstInput, firstDigest, err := ucidomain.SemanticEmbeddingInput(fixture.profile, firstCandidate)
	require.NoError(t, err)
	require.Equal(t, []string{firstInput}, provider.inputs())
	firstVector, found, err := fixture.store.LookupCandidateEmbedding(ctx, firstAuthorized, fixture.profile, firstCandidate)
	require.NoError(t, err)
	require.True(t, found, "the first worker result must be readable through its authorized View")
	firstJob := uciEmbeddingJobsOnlyJobForView(t, publication, first.Context.ViewID)
	require.NotNil(t, firstJob.EmbeddingProfileID)
	require.Len(t, uciEmbeddingJobsLinks(t, publication, *firstJob.EmbeddingProfileID), 1)

	// Local registration issues a new UUID even when all interpretation inputs match.
	original := publication.profile
	secondProfile, err := publication.context.CreateProfile(ctx, CreateProfileInput{
		ParserBundleDigest:   original.ParserBundleDigest,
		ResolverRevision:     original.ResolverRevision,
		ChunkerRevision:      original.ChunkerRevision,
		IgnorePolicyDigest:   original.IgnorePolicyDigest,
		BuildContextJSON:     original.BuildContextJSON,
		SecretPolicyRevision: original.SecretPolicyRevision,
	})
	require.NoError(t, err)
	require.NotEqual(t, original.ProfileID, secondProfile.ProfileID)
	publication.profile = secondProfile
	second := fixture.publish(t, "worker-reuse-second", publication.sibling, nil, ucidomain.IndexJobInitial, []uciPublicationArtifact{artifact}, memberships)
	require.Equal(t, first.Context.SourceID, second.Context.SourceID)
	require.NotEqual(t, first.Context.CheckoutID, second.Context.CheckoutID)
	require.NotEqual(t, first.Context.AnalysisProfileID, second.Context.AnalysisProfileID)
	secondAuthorized := uciEmbeddingJobsAuthorize(t, publication, second.Context)
	secondCandidate := uciSemanticCandidateAtPath(t, fixture.store, secondAuthorized, "shared.go", artifact.Artifact.ArtifactID)
	secondInput, secondDigest, err := ucidomain.SemanticEmbeddingInput(fixture.profile, secondCandidate)
	require.NoError(t, err)
	require.Equal(t, firstInput, secondInput, "compare the exact canonical provider bytes, not cosmetic profile IDs")
	require.Equal(t, firstDigest, secondDigest)
	catalog[second.Context.ViewID] = ucidomain.ContextRecord{Ref: second.Context, AuthRealm: publication.realm}
	uciEmbeddingReuseComplete(t, fixture, resolver, provider, secondAuthorized)

	secondVector, found, err := fixture.store.LookupCandidateEmbedding(ctx, secondAuthorized, fixture.profile, secondCandidate)
	require.NoError(t, err)
	require.True(t, found, "the second View must have a usable candidate link, not merely a succeeded job")
	require.Equal(t, firstVector, secondVector)
	var secondJob UCIJob
	require.NoError(t, publication.db.Where("target_view_id = ? AND job_kind = ?", second.Context.ViewID, "embed").First(&secondJob).Error)
	require.NotNil(t, secondJob.EmbeddingProfileID)
	require.Len(t, uciEmbeddingJobsLinks(t, publication, *secondJob.EmbeddingProfileID), 1)

	t.Run("dimension_remains_closed", func(t *testing.T) {
		incompatible := fixture.profile
		incompatible.Dimension = 768
		_, err := ucidomain.NewEmbeddingWorker(incompatible, provider, fixture.store, resolver, ucidomain.DefaultEmbeddingWorkerLimits())
		require.Error(t, err)
	})
	t.Run("selected_view_cannot_borrow_candidate", func(t *testing.T) {
		_, _, err := fixture.store.LookupCandidateEmbedding(ctx, secondAuthorized, fixture.profile, firstCandidate)
		require.Error(t, err)
	})
	t.Run("content_proof_cannot_be_forged", func(t *testing.T) {
		forged := secondCandidate
		forged.Text += " changed"
		_, found, err := fixture.store.LookupCandidateEmbedding(ctx, secondAuthorized, fixture.profile, forged)
		require.NoError(t, err)
		require.False(t, found)
	})
	t.Run("auth_realm_cannot_be_borrowed", func(t *testing.T) {
		_, err := resolver.Authorize(ctx, ucidomain.ResolveContextInput{
			ClientSessionID: "worker-reuse-wrong-realm-" + publication.token,
			AuthRealm:       publication.realm + "-other",
			Principal:       publication.principal,
			Ref:             &second.Context,
		})
		require.Error(t, err)
	})
	inputs := provider.inputs()
	t.Logf("source=%s first_analysis=%s second_analysis=%s input_digest=%s provider_calls=%d second_view_readable=%t", first.Context.SourceID, first.Context.AnalysisProfileID, second.Context.AnalysisProfileID, firstDigest, provider.calls, found)
	assert.Equal(t, 1, provider.calls, "a completed equal canonical input/effective vector profile in another authorized checkout must not invoke the provider again")
	if len(inputs) == 2 {
		assert.Equal(t, inputs[0], inputs[1], "the observed duplicate bill must be for byte-equal provider input")
	}
}

type uciEmbeddingReuseProvider struct {
	model string
	mu    sync.Mutex
	calls int
	texts []string
}

func (provider *uciEmbeddingReuseProvider) Model() string { return provider.model }

func (provider *uciEmbeddingReuseProvider) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.calls++
	provider.texts = append(provider.texts, inputs...)
	vectors := make([][]float32, len(inputs))
	for index := range inputs {
		vectors[index] = uciEmbeddingJobsVector(1)
	}
	return vectors, nil
}

func (provider *uciEmbeddingReuseProvider) inputs() []string {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return append([]string(nil), provider.texts...)
}

func uciEmbeddingReuseComplete(t *testing.T, fixture *uciEmbeddingJobsFixture, resolver *ucidomain.ContextResolver, provider *uciEmbeddingReuseProvider, authorized ucidomain.AuthorizedContext) {
	t.Helper()
	worker, err := ucidomain.NewEmbeddingWorker(fixture.profile, provider, fixture.store, resolver, ucidomain.DefaultEmbeddingWorkerLimits())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx, "worker-reuse-"+fixture.publication.token) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Error("embedding worker failed to join after its last test consumer")
		}
	}()
	statusService := ucidomain.NewIndexStatusService(fixture.store, &fixture.profile)
	require.Eventually(t, func() bool {
		status, err := statusService.Status(ctx, authorized, "")
		require.NoError(t, err)
		return status.Embedding.JobState != nil && *status.Embedding.JobState == ucidomain.IndexStatusJobSucceeded && status.Embedding.Coverage == ucidomain.IndexCoverageComplete && status.Embedding.TotalCandidates == 1 && status.Embedding.ReadyCandidates == 1
	}, 10*time.Second, 20*time.Millisecond, "the real leased worker must complete and persist the exact View embedding")
}
