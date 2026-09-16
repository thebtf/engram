package gorm

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	ucidomain "github.com/thebtf/engram/internal/uci"
)

func TestUCIVersionedReadStoreReadsBoundedHistoricalBytesAndIsolatesCurrentView(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	ctx := context.Background()

	oldArtifact := fixture.insertArtifact(t, fixture.source.SourceID, "versioned-read-shared", `func VersionedReadShared() string { return "old" } // trailing-secret-must-not-leak
`, UCIParseArtifactComplete)
	oldStart, oldEnd, oldText := uciVersionedReadAddBoundedChunk(t, fixture, &oldArtifact, `return "old"`)
	oldArtifact.Proof = uciVersionedReadDescribeArtifact(t, fixture, oldArtifact)
	oldPublished := uciVersionedReadPublish(t, fixture, uciVersionedReadPublishInput{key: "versioned-read-v1", path: "shared/versioned_read.go", checkout: fixture.checkout, kind: ucidomain.IndexJobInitial, artifact: oldArtifact})
	oldAuthorized := uciSemanticAuthorize(t, fixture, oldPublished.Context)
	oldSpec := uciVersionedReadSpecForPathAndSpan(t, fixture, oldAuthorized, "shared/versioned_read.go", oldStart, oldEnd)
	oldSpec.MaxBytes += len(` } // trailing-secret-must-not-leak`)
	require.Greater(t, oldSpec.MaxBytes, int(oldEnd-oldStart), "regression requires a response bound larger than the exact span")

	oldResult, err := fixture.projection.ReadExact(ctx, oldAuthorized, oldSpec)
	require.NoError(t, err)
	require.Equal(t, ucidomain.IndexCoverageComplete, oldResult.Coverage)
	require.NotNil(t, oldResult.Hit)
	require.Equal(t, oldText, oldResult.Hit.Text)
	require.Equal(t, int64(len(oldText)), oldResult.Hit.SourceByteLength)
	require.Equal(t, oldSpec.Entity, oldResult.Hit.Entity)
	require.Equal(t, oldSpec.Span, oldResult.Hit.Span)
	require.Equal(t, oldSpec.ContentDigest, oldResult.Hit.ContentDigest)

	currentArtifact := fixture.insertArtifact(t, fixture.source.SourceID, "versioned-read-shared", `func VersionedReadShared() string { return "new" }
`, UCIParseArtifactComplete)
	currentStart, currentEnd, currentText := uciVersionedReadAddBoundedChunk(t, fixture, &currentArtifact, `return "new"`)
	currentArtifact.Proof = uciVersionedReadDescribeArtifact(t, fixture, currentArtifact)
	currentPublished := uciVersionedReadPublish(t, fixture, uciVersionedReadPublishInput{key: "versioned-read-v2", path: "shared/versioned_read.go", checkout: fixture.checkout, parent: uciPublicationParent(oldPublished), kind: ucidomain.IndexJobReconcile, artifact: currentArtifact})
	currentAuthorized := uciSemanticAuthorize(t, fixture, currentPublished.Context)
	currentSpec := uciVersionedReadSpecForPathAndSpan(t, fixture, currentAuthorized, "shared/versioned_read.go", currentStart, currentEnd)

	oldResult, err = fixture.projection.ReadExact(ctx, oldAuthorized, oldSpec)
	require.NoError(t, err)
	require.NotNil(t, oldResult.Hit)
	require.Equal(t, oldText, oldResult.Hit.Text, "superseded Views must retain their stored historical bytes")

	currentResult, err := fixture.projection.ReadExact(ctx, currentAuthorized, currentSpec)
	require.NoError(t, err)
	require.NotNil(t, currentResult.Hit)
	require.Equal(t, currentText, currentResult.Hit.Text)
	require.NotEqual(t, oldResult.Hit.Text, currentResult.Hit.Text)

	foreignResult, err := fixture.projection.ReadExact(ctx, currentAuthorized, oldSpec)
	require.NoError(t, err)
	require.Nil(t, foreignResult.Hit, "an old View citation must not materialize through the current View")
	require.Nil(t, foreignResult.Unavailable)
	require.Equal(t, ucidomain.IndexCoverageComplete, foreignResult.Coverage)
}

func TestUCIVersionedReadStoreReturnsEmptyForStaleAndCrossScopedEvidence(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	artifact := fixture.admitArtifact(t, fixture.source.SourceID, "versioned-read-refusal", `func VersionedReadRefusal() string { return "stored-only" }
`, UCIParseArtifactComplete)
	published := uciVersionedReadPublish(t, fixture, uciVersionedReadPublishInput{key: "versioned-read-refusal-v1", path: "refusal/versioned_read.go", checkout: fixture.checkout, kind: ucidomain.IndexJobInitial, artifact: artifact})
	authorized := uciSemanticAuthorize(t, fixture, published.Context)
	spec := uciVersionedReadSpecForPathAndSpan(t, fixture, authorized, "refusal/versioned_read.go", 0, int64(len(artifact.Body)))
	service := ucidomain.NewVersionedReadService(fixture.projection)

	for _, test := range []struct {
		name   string
		mutate func(*ucidomain.VersionedReadSpec)
	}{
		{
			name: "stale digest",
			mutate: func(candidate *ucidomain.VersionedReadSpec) {
				candidate.ContentDigest = ucidomain.QueryContentDigest(strings.Repeat("0", 64))
			},
		},
		{
			name: "cross source",
			mutate: func(candidate *ucidomain.VersionedReadSpec) {
				candidate.Entity.SourceID = fixture.foreign.SourceID
			},
		},
		{
			name: "cross view",
			mutate: func(candidate *ucidomain.VersionedReadSpec) {
				candidate.Entity.ViewID = fixture.sibling.CheckoutID
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := spec
			test.mutate(&candidate)
			result, err := fixture.projection.ReadExact(context.Background(), authorized, candidate)
			require.NoError(t, err)
			require.Nil(t, result.Hit, "stale or foreign evidence must be an authorized empty result before body materialization")
			require.Nil(t, result.Unavailable)
			require.Equal(t, ucidomain.IndexCoverageComplete, result.Coverage)
			response, err := service.Read(context.Background(), authorized, candidate)
			require.NoError(t, err)
			require.NoError(t, response.ValidatePreExposure())
			require.Equal(t, ucidomain.QueryStatusEmpty, response.Status)
			require.Nil(t, response.Exposure)
			require.NotNil(t, response.Items)
			require.Empty(t, *response.Items)
		})
	}
}

func TestUCIVersionedReadStoreReportsPartialAndUnavailableCoverage(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	artifact := fixture.admitArtifact(t, fixture.source.SourceID, "versioned-read-coverage", `func VersionedReadCoverage() string { return "coverage" }
`, UCIParseArtifactComplete)
	published := uciVersionedReadPublish(t, fixture, uciVersionedReadPublishInput{key: "versioned-read-coverage-v1", path: "coverage/versioned_read.go", checkout: fixture.checkout, kind: ucidomain.IndexJobInitial, artifact: artifact})
	authorized := uciSemanticAuthorize(t, fixture, published.Context)
	spec := uciVersionedReadSpecForPathAndSpan(t, fixture, authorized, "coverage/versioned_read.go", 0, int64(len(artifact.Body)))

	require.NoError(t, fixture.db.Exec(`UPDATE ci_views SET coverage_json = ? WHERE view_id = ?`, `{"structural":"partial","lexical":"complete","vector":"unavailable"}`, published.Context.ViewID).Error)
	partial, err := fixture.projection.ReadExact(context.Background(), authorized, spec)
	require.NoError(t, err)
	require.Equal(t, ucidomain.IndexCoveragePartial, partial.Coverage)
	require.NotNil(t, partial.Hit)
	require.Nil(t, partial.Unavailable)

	require.NoError(t, fixture.db.Exec(`UPDATE ci_views SET coverage_json = ? WHERE view_id = ?`, `{"structural":"unavailable","lexical":"unavailable","vector":"unavailable"}`, published.Context.ViewID).Error)
	unavailable, err := fixture.projection.ReadExact(context.Background(), authorized, spec)
	require.NoError(t, err)
	require.Equal(t, ucidomain.IndexCoverageUnavailable, unavailable.Coverage)
	require.Nil(t, unavailable.Hit)
	require.Equal(t, &ucidomain.QueryError{Code: ucidomain.QueryErrorBuildIncomplete}, unavailable.Unavailable)
}

func TestUCIVersionedReadStoreNeverFallsBackWithoutStoredBytes(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	artifact := fixture.admitArtifact(t, fixture.source.SourceID, "versioned-read-no-disk", `func VersionedReadNoDisk() string { return "persisted" }
`, UCIParseArtifactComplete)
	published := uciVersionedReadPublish(t, fixture, uciVersionedReadPublishInput{key: "versioned-read-no-disk-v1", path: "no_disk/versioned_read.go", checkout: fixture.checkout, kind: ucidomain.IndexJobInitial, artifact: artifact})
	authorized := uciSemanticAuthorize(t, fixture, published.Context)
	spec := uciVersionedReadSpecForPathAndSpan(t, fixture, authorized, "no_disk/versioned_read.go", 0, int64(len(artifact.Body)))

	require.NoError(t, fixture.db.Exec(`UPDATE ci_blobs SET storage_state = ?, safe_content = NULL WHERE blob_id = ?`, UCIBlobMetadataOnly, artifact.Blob.BlobID).Error)
	result, err := fixture.projection.ReadExact(context.Background(), authorized, spec)
	require.NoError(t, err)
	require.Nil(t, result.Hit, "a missing stored body is not replaced with any current working-copy bytes")
	require.Nil(t, result.Unavailable)
	require.Equal(t, ucidomain.IndexCoverageComplete, result.Coverage)
}

type uciVersionedReadPublishInput struct {
	key, path string
	checkout  *UCICheckout
	parent    *ucidomain.ContextRef
	kind      ucidomain.IndexJobKind
	artifact  uciPublicationArtifact
}

func uciVersionedReadPublish(t *testing.T, fixture *uciPublicationFixture, input uciVersionedReadPublishInput) ucidomain.IndexPublishedView {
	t.Helper()
	membership := uciPublicationPresentMembership(input.path, input.artifact)
	replacement := ucidomain.IndexEdgeReplacement{SourcePath: membership.PathKey}
	draft := newUCIPublicationDraft(
		[]ucidomain.IndexPart{uciPublicationPart([]uciPublicationArtifact{input.artifact}, []ucidomain.IndexMembership{membership}, nil, []ucidomain.IndexEdgeReplacement{replacement})},
		[]ucidomain.IndexMembership{membership},
		[]ucidomain.IndexEdgeReplacement{replacement},
	)
	_, published := fixture.publish(t, fixture.publisher, fixture.caller("versioned-read-"+input.key), fixture.publishInput(input.key, input.checkout, fixture.profile.ProfileID, input.parent, ucidomain.IndexManifestFull, input.kind, draft))
	return published
}

func uciVersionedReadDescribeArtifact(t *testing.T, fixture *uciPublicationFixture, artifact uciPublicationArtifact) ucidomain.IndexArtifactProof {
	t.Helper()
	proof, err := fixture.projection.DescribeIndexArtifact(context.Background(), fixture.source.SourceID, artifact.Artifact.ArtifactID)
	require.NoError(t, err)
	require.Equal(t, artifact.Artifact.ArtifactID, proof.ArtifactID)
	require.Equal(t, ucidomain.IndexDigest(artifact.Blob.ContentDigest), proof.ContentDigest)
	return proof
}

func uciVersionedReadAddBoundedChunk(t *testing.T, fixture *uciPublicationFixture, artifact *uciPublicationArtifact, text string) (int64, int64, string) {
	t.Helper()
	start := int64(strings.Index(string(artifact.Body), text))
	require.GreaterOrEqual(t, start, int64(0))
	end := start + int64(len(text))
	symbolKey := artifact.Definition.LocalSymbolKey
	_, err := fixture.projection.UpsertChunk(context.Background(), UpsertUCIChunkInput{
		SourceID:      artifact.Artifact.SourceID,
		ArtifactID:    artifact.Artifact.ArtifactID,
		SymbolKey:     &symbolKey,
		ChunkKind:     "statement",
		Ordinal:       1,
		ByteStart:     start,
		ByteEnd:       end,
		ContentDigest: uciPublicationDigestBytes([]byte(text)),
		TextForSearch: text,
	})
	require.NoError(t, err)
	return start, end, text
}

func uciVersionedReadSpecForPathAndSpan(t *testing.T, fixture *uciPublicationFixture, authorized ucidomain.AuthorizedContext, path string, start, end int64) ucidomain.VersionedReadSpec {
	t.Helper()
	selected, err := fixture.projection.SelectCandidates(context.Background(), authorized, ucidomain.QuerySpec{
		Mode:  ucidomain.QueryModeExactRelativePath,
		Text:  path,
		Order: ucidomain.QueryOrderPath,
		Limit: 50,
	})
	require.NoError(t, err)
	require.NotNil(t, selected.Candidates)
	for _, candidate := range selected.Candidates {
		if candidate.Span.ByteStart != start || candidate.Span.ByteEnd != end {
			continue
		}
		digest := strings.TrimPrefix(string(candidate.Proof.ContentDigest), "sha256:")
		spec := ucidomain.VersionedReadSpec{
			Entity: ucidomain.QueryEntityRef{
				SourceID:  candidate.Context.SourceID,
				ViewID:    candidate.Context.ViewID,
				EntityKey: candidate.EntityKey,
			},
			Span: ucidomain.QuerySpan{
				ByteStart: candidate.Span.ByteStart,
				ByteEnd:   candidate.Span.ByteEnd,
				LineStart: int64(candidate.Span.LineStart),
				LineEnd:   int64(candidate.Span.LineEnd),
			},
			ContentDigest: ucidomain.QueryContentDigest(digest),
			MaxBytes:      int(candidate.Span.ByteEnd - candidate.Span.ByteStart),
		}
		require.NoError(t, spec.Validate())
		return spec
	}
	t.Fatalf("no candidate at %d:%d for %q", start, end, path)
	return ucidomain.VersionedReadSpec{}
}
