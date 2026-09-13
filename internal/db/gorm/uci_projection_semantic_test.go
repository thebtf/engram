package gorm

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	ucidomain "github.com/thebtf/engram/internal/uci"
)

func TestUCIProjectionStoreSemanticMethodsKeepVectorsScopedAndCovered(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	ctx := context.Background()
	profile := ucidomain.VectorProfile{
		ProviderRef:           "semantic-provider-" + fixture.token,
		Model:                 "semantic-model-" + fixture.token,
		Dimension:             1536,
		PreprocessingRevision: "semantic-preprocessing-" + fixture.token,
		IncludeRelativePath:   true,
	}

	oldArtifact := fixture.admitArtifact(t, fixture.source.SourceID, "semantic-shared", `func SemanticShared() string { return "semantic old" }
`, UCIParseArtifactComplete)
	oldPublished := uciSemanticPublish(t, fixture, uciSemanticPublishInput{
		key: "semantic-primary-v1", checkout: fixture.checkout, jobKind: ucidomain.IndexJobInitial,
		artifacts:   []uciPublicationArtifact{oldArtifact},
		memberships: []ucidomain.IndexMembership{uciPublicationPresentMembership("shared/semantic.go", oldArtifact)},
	})
	oldAuthorized := uciSemanticAuthorize(t, fixture, oldPublished.Context)
	oldCandidate := uciSemanticCandidateAtPath(t, fixture.projection, oldAuthorized, "shared/semantic.go", oldArtifact.Artifact.ArtifactID)
	oldVector := uciSemanticVector(1, 0)
	require.NoError(t, fixture.projection.StoreCandidateEmbedding(ctx, oldAuthorized, profile, oldCandidate, oldVector))
	cached, found, err := fixture.projection.LookupCandidateEmbedding(ctx, oldAuthorized, profile, oldCandidate)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, oldVector, cached)

	for _, mismatch := range []struct {
		name    string
		profile ucidomain.VectorProfile
	}{
		{
			name: "provider",
			profile: func() ucidomain.VectorProfile {
				mismatch := profile
				mismatch.ProviderRef += "-other"
				return mismatch
			}(),
		},
		{
			name: "model",
			profile: func() ucidomain.VectorProfile {
				mismatch := profile
				mismatch.Model += "-other"
				return mismatch
			}(),
		},
		{
			name: "preprocessing",
			profile: func() ucidomain.VectorProfile {
				mismatch := profile
				mismatch.PreprocessingRevision += "-other"
				return mismatch
			}(),
		},
		{
			name: "relative-path-policy",
			profile: func() ucidomain.VectorProfile {
				mismatch := profile
				mismatch.IncludeRelativePath = false
				return mismatch
			}(),
		},
	} {
		t.Run("lookup_misses_"+mismatch.name, func(t *testing.T) {
			cached, found, err := fixture.projection.LookupCandidateEmbedding(ctx, oldAuthorized, mismatch.profile, oldCandidate)
			require.NoError(t, err)
			require.False(t, found)
			require.Nil(t, cached)
		})
	}

	siblingArtifact := fixture.admitArtifact(t, fixture.source.SourceID, "semantic-shared", `func SemanticShared() string { return "semantic sibling" }
`, UCIParseArtifactComplete)
	siblingPublished := uciSemanticPublish(t, fixture, uciSemanticPublishInput{
		key: "semantic-sibling-v1", checkout: fixture.sibling, jobKind: ucidomain.IndexJobInitial,
		artifacts:   []uciPublicationArtifact{siblingArtifact},
		memberships: []ucidomain.IndexMembership{uciPublicationPresentMembership("shared/semantic.go", siblingArtifact)},
	})
	siblingAuthorized := uciSemanticAuthorize(t, fixture, siblingPublished.Context)
	siblingCandidate := uciSemanticCandidateAtPath(t, fixture.projection, siblingAuthorized, "shared/semantic.go", siblingArtifact.Artifact.ArtifactID)
	require.Equal(t, oldCandidate.RelativePath, siblingCandidate.RelativePath)
	require.Equal(t, oldCandidate.EntityKey, siblingCandidate.EntityKey)
	require.NotEqual(t, oldCandidate.Proof.ContentDigest, siblingCandidate.Proof.ContentDigest)

	cached, found, err = fixture.projection.LookupCandidateEmbedding(ctx, siblingAuthorized, profile, siblingCandidate)
	require.NoError(t, err)
	require.False(t, found, "another authorized View must not reuse a same-path, same-entity vector with different bytes")
	require.Nil(t, cached)
	siblingVector := uciSemanticVector(0.99, 0.1)
	require.NoError(t, fixture.projection.StoreCandidateEmbedding(ctx, siblingAuthorized, profile, siblingCandidate, siblingVector))
	cached, found, err = fixture.projection.LookupCandidateEmbedding(ctx, siblingAuthorized, profile, siblingCandidate)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, siblingVector, cached)

	currentArtifact := fixture.admitArtifact(t, fixture.source.SourceID, "semantic-shared", `func SemanticShared() string { return "semantic current" }
`, UCIParseArtifactComplete)
	otherCurrentArtifact := fixture.admitArtifact(t, fixture.source.SourceID, "semantic-other", `func SemanticOther() string { return "semantic other" }
`, UCIParseArtifactComplete)
	currentPublished := uciSemanticPublish(t, fixture, uciSemanticPublishInput{
		key: "semantic-primary-v2", checkout: fixture.checkout, parent: uciPublicationParent(oldPublished), jobKind: ucidomain.IndexJobReconcile,
		artifacts: []uciPublicationArtifact{currentArtifact, otherCurrentArtifact},
		memberships: []ucidomain.IndexMembership{
			uciPublicationPresentMembership("shared/semantic.go", currentArtifact),
			uciPublicationPresentMembership("other/semantic.go", otherCurrentArtifact),
		},
	})
	currentAuthorized := uciSemanticAuthorize(t, fixture, currentPublished.Context)
	currentCandidate := uciSemanticCandidateAtPath(t, fixture.projection, currentAuthorized, "shared/semantic.go", currentArtifact.Artifact.ArtifactID)
	otherCurrentCandidate := uciSemanticCandidateAtPath(t, fixture.projection, currentAuthorized, "other/semantic.go", otherCurrentArtifact.Artifact.ArtifactID)
	require.Equal(t, oldCandidate.EntityKey, currentCandidate.EntityKey)
	require.NotEqual(t, oldCandidate.Proof.ArtifactID, currentCandidate.Proof.ArtifactID)
	require.NotEqual(t, oldCandidate.Proof.ContentDigest, currentCandidate.Proof.ContentDigest)

	cached, found, err = fixture.projection.LookupCandidateEmbedding(ctx, currentAuthorized, profile, currentCandidate)
	require.NoError(t, err)
	require.False(t, found, "a changed current artifact must not receive the superseded artifact vector")
	require.Nil(t, cached)

	queryVector := uciSemanticVector(1, 0)
	currentVector := uciSemanticVector(0.8, 0.6)
	otherCurrentVector := uciSemanticVector(0, 1)
	require.NoError(t, fixture.projection.StoreCandidateEmbedding(ctx, currentAuthorized, profile, currentCandidate, currentVector))

	incomplete, err := fixture.projection.SelectSemanticCandidates(ctx, currentAuthorized, profile, queryVector, uciSemanticSearchSpec())
	require.NoError(t, err)
	require.Equal(t, ucidomain.IndexCoverageComplete, incomplete.Coverage)
	require.InDelta(t, 0.5, incomplete.VectorCoverage, 0.000001)
	require.Empty(t, incomplete.Candidates, "semantic candidates must not be returned until every eligible current candidate has a compatible vector")

	require.NoError(t, fixture.projection.StoreCandidateEmbedding(ctx, currentAuthorized, profile, otherCurrentCandidate, otherCurrentVector))
	complete, err := fixture.projection.SelectSemanticCandidates(ctx, currentAuthorized, profile, queryVector, uciSemanticSearchSpec())
	require.NoError(t, err)
	require.Equal(t, ucidomain.IndexCoverageComplete, complete.Coverage)
	require.Equal(t, float64(1), complete.VectorCoverage)
	require.Len(t, complete.Candidates, 2)
	require.Equal(t, []string{currentArtifact.Artifact.ArtifactID, otherCurrentArtifact.Artifact.ArtifactID}, []string{
		complete.Candidates[0].Proof.ArtifactID,
		complete.Candidates[1].Proof.ArtifactID,
	})
	for _, candidate := range complete.Candidates {
		require.Equal(t, currentPublished.Context, candidate.Context, "semantic citations must retain the exact current ContextRef")
	}
	require.NotEqual(t, oldArtifact.Artifact.ArtifactID, complete.Candidates[0].Proof.ArtifactID)
	require.NotEqual(t, siblingArtifact.Artifact.ArtifactID, complete.Candidates[0].Proof.ArtifactID)
	require.Greater(t, complete.Candidates[0].Score, complete.Candidates[1].Score)
	filteredSpec := uciSemanticSearchSpec()
	filteredSpec.Filter.PathPrefix = "shared"
	filtered, err := fixture.projection.SelectSemanticCandidates(ctx, currentAuthorized, profile, queryVector, filteredSpec)
	require.NoError(t, err)
	require.Equal(t, float64(1), filtered.VectorCoverage)
	require.Len(t, filtered.Candidates, 1)
	require.Equal(t, "shared/semantic.go", filtered.Candidates[0].RelativePath, "semantic ranking must apply the literal path prefix inside the selected View")

	// The old and sibling vectors are both closer to queryVector than currentVector.
	// Repeating the current-view query proves it ranks inside the View instead of taking
	// global top-N results and filtering them afterward.
	repeated, err := fixture.projection.SelectSemanticCandidates(ctx, currentAuthorized, profile, queryVector, uciSemanticSearchSpec())
	require.NoError(t, err)
	require.Equal(t, complete, repeated)
}

func TestUCIProjectionStoreSelectCandidatesScopesLiteralPathPrefixBeforeLimit(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	ctx := context.Background()

	outside := fixture.admitArtifact(t, fixture.source.SourceID, "path-prefix-outside", "// outside\nfunc PathPrefixNeedle() {}\n", UCIParseArtifactComplete)
	literal := fixture.admitArtifact(t, fixture.source.SourceID, "path-prefix-literal", "// literal\nfunc PathPrefixNeedle() {}\n", UCIParseArtifactComplete)
	nested := fixture.admitArtifact(t, fixture.source.SourceID, "path-prefix-nested", "// nested\nfunc PathPrefixNeedle() {}\n", UCIParseArtifactComplete)
	wildcard := fixture.admitArtifact(t, fixture.source.SourceID, "path-prefix-wildcard", "// wildcard\nfunc PathPrefixNeedle() {}\n", UCIParseArtifactComplete)
	primary := uciSemanticPublish(t, fixture, uciSemanticPublishInput{
		key: "path-prefix-primary", checkout: fixture.checkout, jobKind: ucidomain.IndexJobInitial,
		artifacts: []uciPublicationArtifact{outside, literal, nested, wildcard},
		memberships: []ucidomain.IndexMembership{
			uciPublicationPresentMembership("a-before.go", outside),
			uciPublicationPresentMembership("src/special%_dir/target.go", literal),
			uciPublicationPresentMembership("src/special%_dir/nested/child.go", nested),
			uciPublicationPresentMembership("src/special!xdir/wildcard.go", wildcard),
		},
	})

	sibling := fixture.admitArtifact(t, fixture.source.SourceID, "path-prefix-sibling", "// sibling\nfunc PathPrefixNeedle() {}\n", UCIParseArtifactComplete)
	siblingView := uciSemanticPublish(t, fixture, uciSemanticPublishInput{
		key: "path-prefix-sibling", checkout: fixture.sibling, jobKind: ucidomain.IndexJobInitial,
		artifacts:   []uciPublicationArtifact{sibling},
		memberships: []ucidomain.IndexMembership{uciPublicationPresentMembership("src/special%_dir/foreign.go", sibling)},
	})
	require.Equal(t, primary.Context.SourceID, siblingView.Context.SourceID)
	require.NotEqual(t, primary.Context.ViewID, siblingView.Context.ViewID)

	authorized := uciSemanticAuthorize(t, fixture, primary.Context)
	directory, err := fixture.projection.SelectCandidates(ctx, authorized, ucidomain.QuerySpec{
		Mode:   ucidomain.QueryModeFTS,
		Text:   "PathPrefixNeedle",
		Filter: ucidomain.QueryFilter{PathPrefix: "src/special%_dir"},
		Order:  ucidomain.QueryOrderPath,
		Limit:  1,
	})
	require.NoError(t, err)
	require.Equal(t, ucidomain.IndexCoverageComplete, directory.Coverage)
	require.Len(t, directory.Candidates, 2, "the store returns limit+1 after filtering inside the selected View")
	paths := make([]string, 0, len(directory.Candidates))
	for _, candidate := range directory.Candidates {
		paths = append(paths, candidate.RelativePath)
		require.NotEqual(t, outside.Artifact.ArtifactID, candidate.Proof.ArtifactID, "a globally earlier nonmatching row must not consume the limit")
		require.NotEqual(t, wildcard.Artifact.ArtifactID, candidate.Proof.ArtifactID, "percent and underscore must be literal prefix characters")
		require.NotEqual(t, sibling.Artifact.ArtifactID, candidate.Proof.ArtifactID, "another View of the same source must never enter the selected universe")
	}
	require.ElementsMatch(t, []string{"src/special%_dir/target.go", "src/special%_dir/nested/child.go"}, paths)

	file, err := fixture.projection.SelectCandidates(ctx, authorized, ucidomain.QuerySpec{
		Mode:   ucidomain.QueryModeFTS,
		Text:   "PathPrefixNeedle",
		Filter: ucidomain.QueryFilter{PathPrefix: "src/special%_dir/target.go"},
		Order:  ucidomain.QueryOrderPath,
		Limit:  1,
	})
	require.NoError(t, err)
	require.Len(t, file.Candidates, 1)
	require.Equal(t, literal.Artifact.ArtifactID, file.Candidates[0].Proof.ArtifactID)
	require.Equal(t, "src/special%_dir/target.go", file.Candidates[0].RelativePath)
}

type uciSemanticPublishInput struct {
	key         string
	checkout    *UCICheckout
	parent      *ucidomain.ContextRef
	jobKind     ucidomain.IndexJobKind
	artifacts   []uciPublicationArtifact
	memberships []ucidomain.IndexMembership
}

func uciSemanticPublish(t *testing.T, fixture *uciPublicationFixture, input uciSemanticPublishInput) ucidomain.IndexPublishedView {
	t.Helper()
	replacements := make([]ucidomain.IndexEdgeReplacement, 0, len(input.memberships))
	for _, membership := range input.memberships {
		replacements = append(replacements, ucidomain.IndexEdgeReplacement{SourcePath: membership.PathKey})
	}
	draft := newUCIPublicationDraft(
		[]ucidomain.IndexPart{uciPublicationPart(input.artifacts, input.memberships, nil, replacements)},
		input.memberships,
		replacements,
	)
	draft.coverage.Vector = ucidomain.IndexCoverageComplete
	_, published := fixture.publish(t, fixture.publisher, fixture.caller("semantic-"+input.key), input.key, input.checkout, fixture.profile.ProfileID, input.parent, ucidomain.IndexManifestFull, input.jobKind, draft)
	return published
}

func uciSemanticAuthorize(t *testing.T, fixture *uciPublicationFixture, ref ucidomain.ContextRef) ucidomain.AuthorizedContext {
	t.Helper()

	resolver := ucidomain.NewContextResolver(uciSemanticCatalog{
		ref.ViewID: {Ref: ref, AuthRealm: fixture.realm},
	}, fixture.authorizer, nil)
	authorized, err := resolver.Authorize(context.Background(), ucidomain.ResolveContextInput{
		ClientSessionID: "semantic-client-" + fixture.token + "-" + ref.ViewID,
		AuthRealm:       fixture.realm,
		Principal:       fixture.principal,
		Ref:             &ref,
	})
	require.NoError(t, err)
	require.Equal(t, ref, authorized.Ref())
	return authorized
}

func uciSemanticCandidateAtPath(t *testing.T, store *UCIProjectionStore, authorized ucidomain.AuthorizedContext, path, artifactID string) ucidomain.QueryCandidate {
	t.Helper()

	selected, err := store.SelectCandidates(context.Background(), authorized, ucidomain.QuerySpec{
		Mode:  ucidomain.QueryModeExactRelativePath,
		Text:  path,
		Order: ucidomain.QueryOrderPath,
		Limit: 1,
	})
	require.NoError(t, err)
	require.Equal(t, ucidomain.IndexCoverageComplete, selected.Coverage)
	require.Len(t, selected.Candidates, 1)
	candidate := selected.Candidates[0]
	require.Equal(t, artifactID, candidate.Proof.ArtifactID)
	require.Equal(t, authorized.Ref(), candidate.Context)
	return candidate
}

func uciSemanticSearchSpec() ucidomain.QuerySpec {
	return ucidomain.QuerySpec{
		Mode:  ucidomain.QueryModeFTS,
		Text:  "semantic",
		Order: ucidomain.QueryOrderPath,
		Limit: 1,
	}
}

func uciSemanticVector(first, second float32) []float32 {
	vector := make([]float32, 1536)
	vector[0] = first
	vector[1] = second
	return vector
}

type uciSemanticCatalog map[string]ucidomain.ContextRecord

func (catalog uciSemanticCatalog) LoadContext(_ context.Context, ref ucidomain.ContextRef) (ucidomain.ContextRecord, error) {
	record, found := catalog[ref.ViewID]
	if !found {
		return ucidomain.ContextRecord{}, fmt.Errorf("semantic fixture context %q is not registered", ref.ViewID)
	}
	return record, nil
}
