package gorm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	ucidomain "github.com/thebtf/engram/internal/uci"
)

func TestUCIStatusStoreReadsExactViewLifecycleAndScopedCounts(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	ctx := context.Background()
	store := NewUCIProjectionStore(fixture.db)

	sharedSource := "func StatusShared() {}\n"
	firstArtifact := fixture.admitArtifact(t, fixture.source.SourceID, "status-first", sharedSource, UCIParseArtifactComplete)
	secondArtifact := fixture.admitArtifact(t, fixture.source.SourceID, "status-second", sharedSource, UCIParseArtifactComplete)
	initialMemberships := []ucidomain.IndexMembership{
		uciPublicationPresentMembership("first.go", firstArtifact),
		uciPublicationPresentMembership("second.go", secondArtifact),
	}
	initialDraft := newUCIPublicationDraft(
		[]ucidomain.IndexPart{uciPublicationPart([]uciPublicationArtifact{firstArtifact, secondArtifact}, initialMemberships, nil, []ucidomain.IndexEdgeReplacement{{SourcePath: "first.go"}, {SourcePath: "second.go"}})},
		initialMemberships,
		[]ucidomain.IndexEdgeReplacement{{SourcePath: "first.go"}, {SourcePath: "second.go"}},
	)
	initialDraft.fsSeq = 17
	_, initial := fixture.publish(t, fixture.publisher, fixture.caller("status-initial"), "status-initial", fixture.checkout, fixture.profile.ProfileID, nil, ucidomain.IndexManifestFull, ucidomain.IndexJobInitial, initialDraft)
	require.NoError(t, fixture.db.Model(&UCIView{}).Where("view_id = ?", initial.Context.ViewID).Update("dirty", true).Error)
	initialAuthorized := uciStatusStoreAuthorize(t, fixture, initial.Context)

	semanticProfile := ucidomain.VectorProfile{
		ProviderRef:           "status-provider-" + fixture.token,
		Model:                 "status-model-" + fixture.token,
		Dimension:             1536,
		PreprocessingRevision: "status-preprocessing-" + fixture.token,
		IncludeRelativePath:   false,
	}
	firstCandidate := uciSemanticCandidateAtPath(t, fixture.projection, initialAuthorized, "first.go", firstArtifact.Artifact.ArtifactID)
	secondCandidate := uciSemanticCandidateAtPath(t, fixture.projection, initialAuthorized, "second.go", secondArtifact.Artifact.ArtifactID)
	require.NoError(t, fixture.projection.StoreCandidateEmbedding(ctx, initialAuthorized, semanticProfile, firstCandidate, uciSemanticVector(1, 0)))
	require.NoError(t, fixture.projection.StoreCandidateEmbedding(ctx, initialAuthorized, semanticProfile, secondCandidate, uciSemanticVector(1, 0)))
	var readyEmbeddingRows int64
	require.NoError(t, fixture.db.Model(&UCIEmbedding{}).Where("source_id = ? AND status = ?", fixture.source.SourceID, UCIEmbeddingReady).Count(&readyEmbeddingRows).Error)
	require.Equal(t, int64(1), readyEmbeddingRows, "two scoped chunks deliberately reuse one compatible embedding")

	current, err := store.LoadIndexStatus(ctx, initialAuthorized)
	require.NoError(t, err)
	require.Equal(t, initial.Context, current.Context)
	require.Equal(t, ucidomain.IndexStatusSourceActive, current.SourceState)
	require.Equal(t, ucidomain.IndexStatusCheckoutRegistered, current.CheckoutState)
	require.Equal(t, ucidomain.IndexStatusViewPublished, current.ViewState)
	require.Equal(t, ucidomain.IndexStatusCurrentViewSelected, current.CurrentViewRelation)
	require.True(t, current.Dirty)
	require.Equal(t, int64(17), current.ObservedFSSeq)
	require.False(t, current.PublishedAt.IsZero())
	require.False(t, current.ScanStartedAt.IsZero())
	require.False(t, current.ScanCompletedAt.IsZero())
	require.Equal(t, uint64(2), current.ChunkCount)
	require.Equal(t, uint64(2), current.ReadyEmbeddingCount)
	require.Equal(t, initialDraft.coverage, current.Coverage)
	requireUCIStatusStoreFreshness(t, current.Freshness, ucidomain.QueryFreshnessObservedCurrent, ucidomain.QueryFreshnessWatchWatermark, ucidomain.QueryEnrichmentCurrent, 17, &[]int64{0}[0])

	siblingArtifact := fixture.admitArtifact(t, fixture.source.SourceID, "status-sibling", "func StatusSibling() {}\n", UCIParseArtifactComplete)
	siblingMemberships := []ucidomain.IndexMembership{uciPublicationPresentMembership("sibling.go", siblingArtifact)}
	siblingDraft := newUCIPublicationDraft(
		[]ucidomain.IndexPart{uciPublicationPart([]uciPublicationArtifact{siblingArtifact}, siblingMemberships, nil, []ucidomain.IndexEdgeReplacement{{SourcePath: "sibling.go"}})},
		siblingMemberships,
		[]ucidomain.IndexEdgeReplacement{{SourcePath: "sibling.go"}},
	)
	_, sibling := fixture.publish(t, fixture.publisher, fixture.caller("status-sibling"), "status-sibling", fixture.sibling, fixture.profile.ProfileID, nil, ucidomain.IndexManifestFull, ucidomain.IndexJobInitial, siblingDraft)
	siblingAuthorized := uciStatusStoreAuthorize(t, fixture, sibling.Context)
	siblingCandidate := uciSemanticCandidateAtPath(t, fixture.projection, siblingAuthorized, "sibling.go", siblingArtifact.Artifact.ArtifactID)
	require.NoError(t, fixture.projection.StoreCandidateEmbedding(ctx, siblingAuthorized, semanticProfile, siblingCandidate, uciSemanticVector(0, 1)))

	isolated, err := store.LoadIndexStatus(ctx, initialAuthorized)
	require.NoError(t, err)
	require.Equal(t, uint64(2), isolated.ChunkCount, "another checkout's chunks must not contribute")
	require.Equal(t, uint64(2), isolated.ReadyEmbeddingCount, "another checkout's ready embeddings must not contribute")

	queuedJobID := uuid.NewString()
	checkoutID := fixture.checkout.CheckoutID
	incarnationID := fixture.checkout.IncarnationID
	profileID := fixture.profile.ProfileID
	parentViewID := initial.Context.ViewID
	targetGeneration := initial.Context.Generation + 1
	now := time.Now().UTC()
	require.NoError(t, fixture.db.Create(&UCIJob{
		JobID:                queuedJobID,
		SourceID:             fixture.source.SourceID,
		CheckoutID:           &checkoutID,
		JobKind:              "embed",
		InputFingerprint:     "status-queued-" + fixture.token,
		TargetGeneration:     &targetGeneration,
		State:                UCIJobQueued,
		Attempt:              0,
		Counts:               `{}`,
		IncarnationID:        &incarnationID,
		ProfileID:            &profileID,
		ExpectedParentViewID: &parentViewID,
		CreatedAt:            now,
		UpdatedAt:            now,
	}).Error)

	catchingUp, err := store.LoadIndexStatus(ctx, initialAuthorized)
	require.NoError(t, err)
	require.Equal(t, uint64(1), catchingUp.PendingJobCount)
	require.NotNil(t, catchingUp.RelevantJob)
	require.Equal(t, ucidomain.IndexStatusJobQueued, catchingUp.RelevantJob.State)
	require.Equal(t, &targetGeneration, catchingUp.RelevantJob.TargetGeneration)
	requireUCIStatusStoreFreshness(t, catchingUp.Freshness, ucidomain.QueryFreshnessCatchingUp, ucidomain.QueryFreshnessWatchWatermark, ucidomain.QueryEnrichmentPending, 17, nil)

	require.NoError(t, fixture.db.Model(&UCIJob{}).Where("job_id = ?", queuedJobID).Update("state", UCIJobObsolete).Error)
	currentArtifact := fixture.admitArtifact(t, fixture.source.SourceID, "status-current", "func StatusCurrent() {}\n", UCIParseArtifactPartial)
	currentMemberships := []ucidomain.IndexMembership{uciPublicationPresentMembership("current.go", currentArtifact)}
	currentDraft := newUCIPublicationDraft(
		[]ucidomain.IndexPart{uciPublicationPart([]uciPublicationArtifact{currentArtifact}, currentMemberships, nil, []ucidomain.IndexEdgeReplacement{{SourcePath: "current.go"}})},
		currentMemberships,
		[]ucidomain.IndexEdgeReplacement{{SourcePath: "current.go"}},
	)
	currentDraft.fsSeq = 23
	currentDraft.coverage = ucidomain.IndexCoverage{
		Structural:           ucidomain.IndexCoveragePartial,
		Lexical:              ucidomain.IndexCoveragePartial,
		Vector:               ucidomain.IndexCoverageUnavailable,
		ExcludedFiles:        2,
		UnreadableFiles:      3,
		UnresolvedReferences: 5,
	}
	_, currentPublished := fixture.publish(t, fixture.publisher, fixture.caller("status-current"), "status-current", fixture.checkout, fixture.profile.ProfileID, uciPublicationParent(initial), ucidomain.IndexManifestFull, ucidomain.IndexJobReconcile, currentDraft)
	currentAuthorized := uciStatusStoreAuthorize(t, fixture, currentPublished.Context)

	historical, err := store.LoadIndexStatus(ctx, initialAuthorized)
	require.NoError(t, err)
	require.Equal(t, ucidomain.IndexStatusViewSuperseded, historical.ViewState)
	require.Equal(t, ucidomain.IndexStatusCurrentViewDifferent, historical.CurrentViewRelation)
	require.Equal(t, uint64(2), historical.ChunkCount, "historical status must retain the selected View's temporal membership count")
	require.Equal(t, uint64(2), historical.ReadyEmbeddingCount, "historical status must retain the selected View's compatible embedding count")
	requireUCIStatusStoreFreshness(t, historical.Freshness, ucidomain.QueryFreshnessHistorical, ucidomain.QueryFreshnessPinnedHistory, ucidomain.QueryEnrichmentCurrent, 17, nil)

	partial, err := store.LoadIndexStatus(ctx, currentAuthorized)
	require.NoError(t, err)
	require.Equal(t, ucidomain.IndexStatusViewPublished, partial.ViewState)
	require.Equal(t, ucidomain.IndexStatusCurrentViewSelected, partial.CurrentViewRelation)
	require.Equal(t, currentDraft.coverage, partial.Coverage)
	require.Equal(t, uint64(1), partial.ChunkCount)
	require.Equal(t, uint64(0), partial.ReadyEmbeddingCount)
	requireUCIStatusStoreFreshness(t, partial.Freshness, ucidomain.QueryFreshnessObservedCurrent, ucidomain.QueryFreshnessWatchWatermark, ucidomain.QueryEnrichmentCurrent, 23, &[]int64{0}[0])

	service := ucidomain.NewIndexStatusService(store)
	_, err = service.Status(ctx, currentAuthorized, "server-issued-barrier")
	require.ErrorIs(t, err, ucidomain.ErrIndexStatusBarrierUnavailable)
	var statusErr *ucidomain.IndexStatusError
	require.ErrorAs(t, err, &statusErr)
	require.Equal(t, ucidomain.IndexStatusBarrierUnavailable, statusErr.Code())

	require.NoError(t, fixture.db.Model(&UCISource{}).Where("source_id = ?", fixture.source.SourceID).Update("state", UCISourceOffline).Error)
	sourceOffline, err := store.LoadIndexStatus(ctx, currentAuthorized)
	require.NoError(t, err)
	require.Equal(t, ucidomain.IndexStatusSourceOffline, sourceOffline.SourceState)
	requireUCIStatusStoreFreshness(t, sourceOffline.Freshness, ucidomain.QueryFreshnessOffline, ucidomain.QueryFreshnessNone, ucidomain.QueryEnrichmentUnavailable, 23, nil)

	require.NoError(t, fixture.db.Model(&UCISource{}).Where("source_id = ?", fixture.source.SourceID).Update("state", UCISourceActive).Error)
	require.NoError(t, fixture.db.Model(&UCICheckout{}).Where("checkout_id = ?", fixture.checkout.CheckoutID).Update("state", UCICheckoutOffline).Error)
	checkoutOffline, err := store.LoadIndexStatus(ctx, currentAuthorized)
	require.NoError(t, err)
	require.Equal(t, ucidomain.IndexStatusSourceActive, checkoutOffline.SourceState)
	require.Equal(t, ucidomain.IndexStatusCheckoutOffline, checkoutOffline.CheckoutState)
	requireUCIStatusStoreFreshness(t, checkoutOffline.Freshness, ucidomain.QueryFreshnessOffline, ucidomain.QueryFreshnessNone, ucidomain.QueryEnrichmentUnavailable, 23, nil)
}

func TestUCIStatusStoreReturnsClosedNotFoundForMismatchedTuple(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	ref := ucidomain.ContextRef{
		SourceID:          fixture.source.SourceID,
		CheckoutID:        fixture.checkout.CheckoutID,
		ViewID:            uuid.NewString(),
		AnalysisProfileID: uuid.NewString(),
		Generation:        1,
	}
	authorized := uciStatusStoreAuthorize(t, fixture, ref)

	_, err := NewUCIProjectionStore(fixture.db).LoadIndexStatus(context.Background(), authorized)
	require.ErrorIs(t, err, ucidomain.ErrIndexStatusNotFound)
	var statusErr *ucidomain.IndexStatusError
	require.True(t, errors.As(err, &statusErr))
	require.Equal(t, ucidomain.IndexStatusNotFound, statusErr.Code())
}

func uciStatusStoreAuthorize(t *testing.T, fixture *uciPublicationFixture, ref ucidomain.ContextRef) ucidomain.AuthorizedContext {
	t.Helper()
	resolver := ucidomain.NewContextResolver(uciStatusStoreCatalog{
		ref.ViewID: {Ref: ref, AuthRealm: fixture.realm},
	}, fixture.authorizer, nil)
	authorized, err := resolver.Authorize(context.Background(), ucidomain.ResolveContextInput{
		ClientSessionID: "status-store-client-" + fixture.token + "-" + ref.ViewID,
		AuthRealm:       fixture.realm,
		Principal:       fixture.principal,
		Ref:             &ref,
	})
	require.NoError(t, err)
	return authorized
}

type uciStatusStoreCatalog map[string]ucidomain.ContextRecord

func (catalog uciStatusStoreCatalog) LoadContext(_ context.Context, ref ucidomain.ContextRef) (ucidomain.ContextRecord, error) {
	record, found := catalog[ref.ViewID]
	if !found {
		return ucidomain.ContextRecord{}, errors.New("status store fixture context is not registered")
	}
	return record, nil
}

func requireUCIStatusStoreFreshness(
	t *testing.T,
	freshness ucidomain.QueryFreshness,
	state ucidomain.QueryFreshnessState,
	method ucidomain.QueryFreshnessMethod,
	watermarkState ucidomain.QueryEnrichmentState,
	sequence int64,
	pending *int64,
) {
	t.Helper()
	require.NoError(t, freshness.Validate())
	require.Equal(t, state, freshness.State)
	require.Equal(t, method, freshness.Method)
	require.Equal(t, watermarkState, freshness.EnrichmentWatermark.State)
	require.Equal(t, sequence, freshness.EnrichmentWatermark.Sequence)
	require.Equal(t, pending, freshness.PendingChanges)
}
