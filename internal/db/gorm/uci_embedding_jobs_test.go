package gorm

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
	ucidomain "github.com/thebtf/engram/internal/uci"
)

type uciEmbeddingJobsFixture struct {
	publication *uciPublicationFixture
	store       *UCIProjectionStore
	profile     ucidomain.VectorProfile
	publisher   ucidomain.IndexStore
}

func TestUCIEmbeddingPublicationCreatesOneProfileViewJobAndReplays(t *testing.T) {
	fixture := openUCIEmbeddingJobsFixture(t)
	ctx := context.Background()
	artifact := fixture.publication.admitArtifact(t, fixture.publication.source.SourceID, "embedding-replay", "func EmbeddingReplay() {}\n", UCIParseArtifactComplete)
	memberships := []ucidomain.IndexMembership{uciPublicationPresentMembership("replay.go", artifact)}
	draft := uciEmbeddingJobsDraft([]uciPublicationArtifact{artifact}, memberships)
	caller := fixture.publication.caller("embedding-replay")

	build := fixture.publication.begin(t, fixture.publisher, caller, "embedding-replay", fixture.publication.checkout, fixture.publication.profile.ProfileID, nil, ucidomain.IndexManifestFull, ucidomain.IndexJobInitial)
	acks := fixture.publication.stageDraft(t, fixture.publisher, caller, build.Build, draft.parts)
	manifest := fixture.publication.manifest(t, acks, draft)
	published, err := fixture.publisher.Finalize(ctx, caller, ucidomain.IndexFinalizeInput{Build: build.Build, Manifest: manifest})
	require.NoError(t, err)

	replayed, err := fixture.publisher.Finalize(ctx, caller, ucidomain.IndexFinalizeInput{Build: build.Build, Manifest: manifest})
	require.NoError(t, err)
	require.Equal(t, published, replayed, "a lost finalize acknowledgement must replay the same View")

	job := uciEmbeddingJobsOnlyJobForView(t, fixture.publication, published.Context.ViewID)
	require.Equal(t, UCIJobQueued, job.State)
	require.Zero(t, job.Attempt)
	require.NotNil(t, job.CheckoutID)
	require.Equal(t, published.Context.CheckoutID, *job.CheckoutID)
	require.NotNil(t, job.IncarnationID)
	require.Equal(t, fixture.publication.checkout.IncarnationID, *job.IncarnationID)
	require.NotNil(t, job.ProfileID)
	require.Equal(t, published.Context.AnalysisProfileID, *job.ProfileID)
	require.NotNil(t, job.TargetViewID)
	require.Equal(t, published.Context.ViewID, *job.TargetViewID)
	require.NotNil(t, job.TargetGeneration)
	require.Equal(t, published.Context.Generation, *job.TargetGeneration)
	require.NotNil(t, job.EmbeddingProfileID)

	var profile UCIEmbeddingProfile
	require.NoError(t, fixture.publication.db.Where("embedding_profile_id = ?", *job.EmbeddingProfileID).First(&profile).Error)
	require.Equal(t, fixture.publication.profile.ProfileID, profile.AnalysisProfileID)
	require.Equal(t, fixture.profile.ProviderRef, profile.ProviderRef)
	require.Equal(t, fixture.profile.Model, profile.Model)
	require.Equal(t, fixture.profile.Dimension, profile.Dimension)
	require.Equal(t, fixture.profile.PreprocessingRevision, profile.PreprocessingRevision)
	require.Equal(t, fixture.profile.IncludeRelativePath, profile.IncludeRelativePath)

	var count int64
	require.NoError(t, fixture.publication.db.Model(&UCIJob{}).Where(
		"source_id = ? AND checkout_id = ? AND job_kind = ? AND target_view_id = ? AND embedding_profile_id = ?",
		fixture.publication.source.SourceID,
		fixture.publication.checkout.CheckoutID,
		"embed",
		published.Context.ViewID,
		*job.EmbeddingProfileID,
	).Count(&count).Error)
	require.Equal(t, int64(1), count, "replay must retain one durable profile/View obligation")
}

func TestUCIEmbeddingJobClaimLeaseFencesCompetingCompletion(t *testing.T) {
	fixture := openUCIEmbeddingJobsFixture(t)
	artifact := fixture.publication.admitArtifact(t, fixture.publication.source.SourceID, "embedding-lease", "func EmbeddingLease() {}\n", UCIParseArtifactComplete)
	published := fixture.publish(t, "embedding-lease", fixture.publication.checkout, nil, ucidomain.IndexJobInitial,
		[]uciPublicationArtifact{artifact},
		[]ucidomain.IndexMembership{uciPublicationPresentMembership("lease.go", artifact)},
	)

	authorized := uciEmbeddingJobsAuthorize(t, fixture.publication, published.Context)
	claim, claimed, err := fixture.store.ClaimEmbeddingJob(context.Background(), fixture.profile, "embedding-owner-"+fixture.publication.token, time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)

	_, competingClaimed, err := fixture.store.ClaimEmbeddingJob(context.Background(), fixture.profile, "embedding-competitor-"+fixture.publication.token, time.Minute)
	require.NoError(t, err)
	require.False(t, competingClaimed, "a live lease must prevent a second worker claim")

	competing := claim
	competing.Ref.LeaseOwner = "embedding-competitor-" + fixture.publication.token
	require.True(t, competing.ValidForEmbeddingJob())
	err = fixture.store.CompleteEmbeddingJob(context.Background(), competing, authorized)
	require.ErrorIs(t, err, ucidomain.ErrEmbeddingJobLeaseLost, "a competing lease fence cannot complete another worker's job")

	job := uciEmbeddingJobsJob(t, fixture.publication, claim.Ref.JobID)
	require.Equal(t, UCIJobRunning, job.State)
	require.Equal(t, claim.Attempt, job.Attempt)
	require.NotNil(t, job.LeaseOwner)
	require.Equal(t, claim.Ref.LeaseOwner, *job.LeaseOwner)
	require.NotNil(t, job.LeaseExpiry)

	require.NoError(t, fixture.publication.db.Model(&UCIJob{}).Where("job_id = ?", claim.Ref.JobID).Update("lease_expiry", time.Now().UTC().Add(-time.Second)).Error)
	takeover, claimed, err := fixture.store.ClaimEmbeddingJob(context.Background(), fixture.profile, "embedding-restart-"+fixture.publication.token, time.Minute)
	require.NoError(t, err)
	require.True(t, claimed, "an expired lease must be recoverable after worker restart")
	require.Equal(t, claim.Ref.LeaseEpoch+1, takeover.Ref.LeaseEpoch)
	require.Equal(t, claim.Attempt+1, takeover.Attempt)
	require.ErrorIs(t, fixture.store.RenewEmbeddingJob(context.Background(), claim.Ref, time.Minute), ucidomain.ErrEmbeddingJobLeaseLost)
	require.NoError(t, fixture.store.RenewEmbeddingJob(context.Background(), takeover.Ref, time.Minute))
}

func TestUCIEmbeddingPreparationEnumeratesOnlyTextCandidates(t *testing.T) {
	fixture := openUCIEmbeddingJobsFixture(t)
	ctx := context.Background()
	textArtifact := fixture.publication.admitArtifact(t, fixture.publication.source.SourceID, "embedding-text", "func EmbeddingText() {}\n", UCIParseArtifactComplete)
	factsOnlyArtifact := uciEmbeddingJobsFactsOnlyArtifact(t, fixture.publication)
	require.Zero(t, factsOnlyArtifact.Proof.ChunkCount)

	published := fixture.publish(t, "embedding-text", fixture.publication.checkout, nil, ucidomain.IndexJobInitial,
		[]uciPublicationArtifact{textArtifact, factsOnlyArtifact},
		[]ucidomain.IndexMembership{
			uciPublicationPresentMembership("text.go", textArtifact),
			uciPublicationPresentMembership("facts-only.go", factsOnlyArtifact),
		},
	)
	authorized := uciEmbeddingJobsAuthorize(t, fixture.publication, published.Context)
	claim, claimed, err := fixture.store.ClaimEmbeddingJob(ctx, fixture.profile, "embedding-text-worker-"+fixture.publication.token, time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)

	batch, err := fixture.store.PrepareEmbeddingBatch(ctx, claim, authorized, 16)
	require.NoError(t, err)
	require.Len(t, batch.Candidates, 1, "only an admitted searchable text chunk belongs in the durable denominator")
	require.Equal(t, []int{0}, batch.MissingInputIndexes)
	require.Equal(t, textArtifact.Chunk.ChunkID, batch.Candidates[0].Key.ChunkID)
	require.Equal(t, textArtifact.Artifact.ArtifactID, batch.Candidates[0].Candidate.Proof.ArtifactID)
	require.NotEqual(t, factsOnlyArtifact.Artifact.ArtifactID, batch.Candidates[0].Candidate.Proof.ArtifactID)
	require.Equal(t, string(textArtifact.Body), batch.Candidates[0].Candidate.Text)
	require.NotEmpty(t, batch.Candidates[0].Input)
}

func TestUCIEmbeddingCompletionPersistsImmutableArtifactsAndCoverage(t *testing.T) {
	fixture := openUCIEmbeddingJobsFixture(t)
	ctx := context.Background()
	first := fixture.publication.admitArtifact(t, fixture.publication.source.SourceID, "embedding-first", "func EmbeddingFirst() string { return \"first\" }\n", UCIParseArtifactComplete)
	second := fixture.publication.admitArtifact(t, fixture.publication.source.SourceID, "embedding-second", "func EmbeddingSecond() string { return \"second\" }\n", UCIParseArtifactComplete)
	published := fixture.publish(t, "embedding-complete", fixture.publication.checkout, nil, ucidomain.IndexJobInitial,
		[]uciPublicationArtifact{first, second},
		[]ucidomain.IndexMembership{
			uciPublicationPresentMembership("first.go", first),
			uciPublicationPresentMembership("second.go", second),
		},
	)
	authorized := uciEmbeddingJobsAuthorize(t, fixture.publication, published.Context)
	claim, claimed, err := fixture.store.ClaimEmbeddingJob(ctx, fixture.profile, "embedding-complete-worker-"+fixture.publication.token, time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)

	batch, err := fixture.store.PrepareEmbeddingBatch(ctx, claim, authorized, 16)
	require.NoError(t, err)
	require.Len(t, batch.Candidates, 2)
	require.Equal(t, []int{0, 1}, batch.MissingInputIndexes)
	vectors := make([][]float32, len(batch.MissingInputIndexes))
	for vectorIndex := range batch.MissingInputIndexes {
		vectors[vectorIndex] = uciEmbeddingJobsVector(float32(vectorIndex + 1))
	}

	require.NoError(t, fixture.store.CommitEmbeddingBatch(ctx, claim, authorized, batch, vectors))
	embeddingsBefore := uciEmbeddingJobsEmbeddings(t, fixture.publication, claim.Ref.EmbeddingProfileID)
	linksBefore := uciEmbeddingJobsLinks(t, fixture.publication, claim.Ref.EmbeddingProfileID)
	require.Len(t, embeddingsBefore, 2)
	require.Len(t, linksBefore, 2)
	for vectorIndex, candidateIndex := range batch.MissingInputIndexes {
		candidate := batch.Candidates[candidateIndex]
		var embedding UCIEmbedding
		require.NoError(t, fixture.publication.db.Where(
			"embedding_profile_id = ? AND embedding_input_digest = ? AND source_id = ?",
			claim.Ref.EmbeddingProfileID,
			string(candidate.InputDigest),
			fixture.publication.source.SourceID,
		).First(&embedding).Error)
		require.Equal(t, UCIEmbeddingReady, embedding.Status)
		require.NotNil(t, embedding.Vector)
		require.Equal(t, vectors[vectorIndex], embedding.Vector.Slice())

		var link UCIChunkEmbedding
		require.NoError(t, fixture.publication.db.Where(
			"source_id = ? AND chunk_id = ? AND embedding_profile_id = ? AND embedding_input_digest = ?",
			fixture.publication.source.SourceID,
			candidate.Key.ChunkID,
			claim.Ref.EmbeddingProfileID,
			string(candidate.InputDigest),
		).First(&link).Error)
		require.Equal(t, embedding.EmbeddingID, link.EmbeddingID)
	}

	require.NoError(t, fixture.store.CommitEmbeddingBatch(ctx, claim, authorized, batch, vectors), "a provider retry with the same acknowledged batch is idempotent")
	require.Equal(t, embeddingsBefore, uciEmbeddingJobsEmbeddings(t, fixture.publication, claim.Ref.EmbeddingProfileID), "a replay must not rewrite immutable vector artifacts")
	require.Equal(t, linksBefore, uciEmbeddingJobsLinks(t, fixture.publication, claim.Ref.EmbeddingProfileID), "a replay must not duplicate candidate links")

	exhausted, err := fixture.store.PrepareEmbeddingBatch(ctx, claim, authorized, 16)
	require.NoError(t, err)
	require.Empty(t, exhausted.Candidates)
	require.Empty(t, exhausted.MissingInputIndexes)
	require.True(t, exhausted.Exhausted)
	require.NoError(t, fixture.store.CompleteEmbeddingJob(ctx, claim, authorized))

	job := uciEmbeddingJobsJob(t, fixture.publication, claim.Ref.JobID)
	require.Equal(t, UCIJobSucceeded, job.State)
	require.Nil(t, job.ErrorCode)
	require.Nil(t, job.LeaseOwner)
	require.Nil(t, job.LeaseExpiry)

	status := fixture.status(t, authorized)
	require.Equal(t, uint64(2), status.ReadyEmbeddingCount)
	require.NotNil(t, status.Embedding.EmbeddingProfileID)
	require.Equal(t, claim.Ref.EmbeddingProfileID, *status.Embedding.EmbeddingProfileID)
	require.NotEqual(t, fixture.profile.ProviderRef, *status.Embedding.EmbeddingProfileID, "status exposes an opaque durable profile ID, not the provider location")
	require.Equal(t, ucidomain.IndexCoverageComplete, status.Embedding.Coverage)
	require.Equal(t, uint64(2), status.Embedding.TotalCandidates)
	require.Equal(t, uint64(2), status.Embedding.ReadyCandidates)
	require.Zero(t, status.Embedding.PendingJobs)
	require.NotNil(t, status.Embedding.JobState)
	require.Equal(t, ucidomain.IndexStatusJobSucceeded, *status.Embedding.JobState)
	require.Nil(t, status.Embedding.ErrorCode)
	require.Nil(t, status.Embedding.RetryAfter)
}

func TestUCIEmbeddingReusesUnchangedInputAcrossCheckouts(t *testing.T) {
	fixture := openUCIEmbeddingJobsFixture(t)
	ctx := context.Background()
	artifact := fixture.publication.admitArtifact(t, fixture.publication.source.SourceID, "embedding-shared", "func EmbeddingShared() string { return \"shared\" }\n", UCIParseArtifactComplete)
	memberships := []ucidomain.IndexMembership{uciPublicationPresentMembership("shared.go", artifact)}

	first := fixture.publish(t, "embedding-shared-first", fixture.publication.checkout, nil, ucidomain.IndexJobInitial, []uciPublicationArtifact{artifact}, memberships)
	firstAuthorized := uciEmbeddingJobsAuthorize(t, fixture.publication, first.Context)
	firstClaim, claimed, err := fixture.store.ClaimEmbeddingJob(ctx, fixture.profile, "embedding-shared-first-worker-"+fixture.publication.token, time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	firstBatch, err := fixture.store.PrepareEmbeddingBatch(ctx, firstClaim, firstAuthorized, 16)
	require.NoError(t, err)
	require.Equal(t, []int{0}, firstBatch.MissingInputIndexes)
	require.NoError(t, fixture.store.CommitEmbeddingBatch(ctx, firstClaim, firstAuthorized, firstBatch, [][]float32{uciEmbeddingJobsVector(1)}))
	firstExhausted, err := fixture.store.PrepareEmbeddingBatch(ctx, firstClaim, firstAuthorized, 16)
	require.NoError(t, err)
	require.True(t, firstExhausted.Exhausted)
	require.NoError(t, fixture.store.CompleteEmbeddingJob(ctx, firstClaim, firstAuthorized))

	second := fixture.publish(t, "embedding-shared-second", fixture.publication.sibling, nil, ucidomain.IndexJobInitial, []uciPublicationArtifact{artifact}, memberships)
	secondAuthorized := uciEmbeddingJobsAuthorize(t, fixture.publication, second.Context)
	secondClaim, claimed, err := fixture.store.ClaimEmbeddingJob(ctx, fixture.profile, "embedding-shared-second-worker-"+fixture.publication.token, time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	secondBatch, err := fixture.store.PrepareEmbeddingBatch(ctx, secondClaim, secondAuthorized, 16)
	require.NoError(t, err)
	require.Len(t, secondBatch.Candidates, 1)
	require.Empty(t, secondBatch.MissingInputIndexes, "unchanged source input in another checkout must be a cache hit, not a provider bill")
	require.True(t, secondBatch.Exhausted)
	require.NoError(t, fixture.store.CompleteEmbeddingJob(ctx, secondClaim, secondAuthorized))

	require.Len(t, uciEmbeddingJobsEmbeddings(t, fixture.publication, firstClaim.Ref.EmbeddingProfileID), 1)
	require.Len(t, uciEmbeddingJobsLinks(t, fixture.publication, firstClaim.Ref.EmbeddingProfileID), 1)
	status := fixture.status(t, secondAuthorized)
	require.Equal(t, ucidomain.IndexCoverageComplete, status.Embedding.Coverage)
	require.Equal(t, uint64(1), status.Embedding.TotalCandidates)
	require.Equal(t, uint64(1), status.Embedding.ReadyCandidates)
}

func TestUCIEmbeddingReusesUnchangedChunksAcrossFileVersions(t *testing.T) {
	fixture := openUCIEmbeddingJobsFixture(t)
	ctx := context.Background()

	legacyProfile := fixture.profile
	legacyProfile.PreprocessingRevision = "uci-semantic-preprocess/chunk-v1"
	v2Profile := legacyProfile
	v2Profile.PreprocessingRevision = "uci-semantic-preprocess/chunk-v2"
	legacyPublisher := uciEmbeddingJobsPublisher(t, fixture.publication, legacyProfile)
	v2Publisher := uciEmbeddingJobsPublisher(t, fixture.publication, v2Profile)

	initialGo := uciIndexAdmissionFixtureFrame(t, fixture.publication, "pkg/fixture.go", uciEmbeddingJobsVersionedGoSource("UCIWatcherSLO001"))
	parserCanary := uciIndexAdmissionTypeScriptFixtureFrame(t, fixture.publication)
	parserCanary.Memberships[0].PathKey = "parser-canary.ts"
	parserCanary.Memberships[0].DisplayPath = "parser-canary.ts"
	initialParts, err := fixture.store.AdmitIndexFrames(ctx, fixture.publication.source.SourceID, fixture.publication.profile.ProfileID, []ucidomain.IndexAdmissionFrame{initialGo, parserCanary})
	require.NoError(t, err)
	require.Len(t, initialParts, 2)

	legacyView := fixture.publishAdmitted(t, legacyPublisher, "chunk-v1", nil, ucidomain.IndexJobInitial, initialParts)
	legacyAuthorized := uciEmbeddingJobsAuthorize(t, fixture.publication, legacyView.Context)
	legacyClaim, claimed, err := fixture.store.ClaimEmbeddingJob(ctx, legacyProfile, "embedding-chunk-v1-worker-"+fixture.publication.token, time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	legacyBatch, err := fixture.store.PrepareEmbeddingBatch(ctx, legacyClaim, legacyAuthorized, 16)
	require.NoError(t, err)
	require.Len(t, legacyBatch.Candidates, 5)
	require.Len(t, legacyBatch.MissingInputIndexes, 5)
	uciEmbeddingJobsCommitBatch(t, fixture, legacyClaim, legacyAuthorized, legacyBatch, 1)

	next, exhausted, err := fixture.store.EnsureCurrentEmbeddingJobs(ctx, v2Profile, "", 16)
	require.NoError(t, err)
	require.True(t, exhausted)
	require.Equal(t, fixture.publication.checkout.CheckoutID, next)
	v2Initial := legacyView
	v2InitialJobs := uciEmbeddingJobsJobsForView(t, fixture.publication, v2Initial.Context.ViewID)
	require.Len(t, v2InitialJobs, 2)
	var v2InitialJob UCIJob
	for _, job := range v2InitialJobs {
		if job.EmbeddingProfileID != nil && *job.EmbeddingProfileID != legacyClaim.Ref.EmbeddingProfileID {
			v2InitialJob = job
			break
		}
	}
	require.NotEmpty(t, v2InitialJob.JobID)
	require.Equal(t, UCIJobQueued, v2InitialJob.State)
	require.NotNil(t, v2InitialJob.EmbeddingProfileID)
	require.NotEqual(t, legacyClaim.Ref.EmbeddingProfileID, *v2InitialJob.EmbeddingProfileID, "the preprocessing revision must create an isolated profile")

	v2Candidates, exhausted, err := loadUCIEmbeddingCandidatePage(ctx, fixture.publication.db, v2Initial.Context, nil, 16, v2Profile)
	require.NoError(t, err)
	require.True(t, exhausted)
	require.Len(t, v2Candidates, 5)
	packageCandidate := uciEmbeddingJobsCandidateByEntity(t, v2Candidates, "go:fixture/pkg:fixture")
	parserCandidate := uciEmbeddingJobsCandidateByEntity(t, v2Candidates, "parser-canary.ts:0")
	foreignVector := pgvector.NewVector(uciEmbeddingJobsVector(91))
	_, err = fixture.store.UpsertEmbedding(ctx, UpsertUCIEmbeddingInput{
		EmbeddingProfileID:   *v2InitialJob.EmbeddingProfileID,
		EmbeddingInputDigest: string(packageCandidate.InputDigest),
		Vector:               &foreignVector,
		SourceID:             fixture.publication.foreign.SourceID,
		ProtectionDomain:     packageCandidate.ProtectionDomain,
		CompletionSeq:        v2Initial.Context.Generation,
		Status:               UCIEmbeddingReady,
	})
	require.NoError(t, err)
	otherProtectionVector := pgvector.NewVector(uciEmbeddingJobsVector(92))
	_, err = fixture.store.UpsertEmbedding(ctx, UpsertUCIEmbeddingInput{
		EmbeddingProfileID:   *v2InitialJob.EmbeddingProfileID,
		EmbeddingInputDigest: string(parserCandidate.InputDigest),
		Vector:               &otherProtectionVector,
		SourceID:             fixture.publication.source.SourceID,
		ProtectionDomain:     "source-restricted",
		CompletionSeq:        v2Initial.Context.Generation,
		Status:               UCIEmbeddingReady,
	})
	require.NoError(t, err)

	v2InitialAuthorized := uciEmbeddingJobsAuthorize(t, fixture.publication, v2Initial.Context)
	v2InitialClaim, claimed, err := fixture.store.ClaimEmbeddingJob(ctx, v2Profile, "embedding-chunk-v2-initial-worker-"+fixture.publication.token, time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(t, *v2InitialJob.EmbeddingProfileID, v2InitialClaim.Ref.EmbeddingProfileID)
	v2InitialBatch, err := fixture.store.PrepareEmbeddingBatch(ctx, v2InitialClaim, v2InitialAuthorized, 16)
	require.NoError(t, err)
	require.Len(t, v2InitialBatch.Candidates, 5)
	require.Len(t, v2InitialBatch.MissingInputIndexes, 5, "v1, foreign-source, and foreign-protection vectors cannot satisfy the v2 View")
	uciEmbeddingJobsCommitBatch(t, fixture, v2InitialClaim, v2InitialAuthorized, v2InitialBatch, 10)

	legacyLinks := uciEmbeddingJobsLinks(t, fixture.publication, legacyClaim.Ref.EmbeddingProfileID)
	v2InitialLinks := uciEmbeddingJobsLinks(t, fixture.publication, v2InitialClaim.Ref.EmbeddingProfileID)
	require.Len(t, legacyLinks, 5)
	require.Len(t, v2InitialLinks, 5)
	v2LinksByChunk := make(map[string]UCIChunkEmbedding, len(v2InitialLinks))
	for _, link := range v2InitialLinks {
		v2LinksByChunk[link.ChunkID] = link
	}
	for _, legacyLink := range legacyLinks {
		v2Link, found := v2LinksByChunk[legacyLink.ChunkID]
		require.True(t, found, "the same immutable chunk must have a v2 link")
		require.Equal(t, v2InitialClaim.Ref.EmbeddingProfileID, v2Link.EmbeddingProfileID)
		require.NotEqual(t, legacyLink.EmbeddingProfileID, v2Link.EmbeddingProfileID, "v1 and v2 links must remain profile-isolated")
	}

	changedGo := uciIndexAdmissionFixtureFrame(t, fixture.publication, "pkg/fixture.go", uciEmbeddingJobsVersionedGoSource("UCIWatcherSLO002"))
	changedGoPart, err := fixture.store.AdmitIndexFrame(ctx, fixture.publication.source.SourceID, fixture.publication.profile.ProfileID, changedGo)
	require.NoError(t, err)
	v2Next := fixture.publishAdmitted(t, v2Publisher, "chunk-v2-next", &legacyView.Context, ucidomain.IndexJobReconcile, []ucidomain.IndexPart{changedGoPart, initialParts[1]})
	v2NextAuthorized := uciEmbeddingJobsAuthorize(t, fixture.publication, v2Next.Context)
	v2NextClaim, claimed, err := fixture.store.ClaimEmbeddingJob(ctx, v2Profile, "embedding-chunk-v2-next-worker-"+fixture.publication.token, time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	v2NextBatch, err := fixture.store.PrepareEmbeddingBatch(ctx, v2NextClaim, v2NextAuthorized, 16)
	require.NoError(t, err)
	require.Len(t, v2NextBatch.Candidates, 5)
	missingEntities := make(map[string]struct{}, len(v2NextBatch.MissingInputIndexes))
	for _, index := range v2NextBatch.MissingInputIndexes {
		missingEntities[v2NextBatch.Candidates[index].Candidate.EntityKey] = struct{}{}
	}
	require.Equal(t, map[string]struct{}{
		"go:fixture/func:SharedTarget":     {},
		"go:fixture/func:UCIWatcherSLO002": {},
		"pkg/fixture.go:3":                 {},
	}, missingEntities, "only semantic chunks changed by the file version require provider input")
	for _, cachedEntity := range []string{"go:fixture/pkg:fixture", "parser-canary.ts:0"} {
		_, missing := missingEntities[cachedEntity]
		require.False(t, missing, "%s must be a v2 cache hit", cachedEntity)
	}

	v2NextPackage := uciEmbeddingJobsCandidateByEntity(t, v2NextBatch.Candidates, "go:fixture/pkg:fixture")
	uciEmbeddingJobsCommitBatch(t, fixture, v2NextClaim, v2NextAuthorized, v2NextBatch, 20)
	var packageVectorCount int64
	require.NoError(t, fixture.publication.db.Model(&UCIEmbedding{}).Where(
		"embedding_profile_id = ? AND embedding_input_digest = ? AND source_id = ? AND protection_domain = ?",
		v2NextClaim.Ref.EmbeddingProfileID,
		string(v2NextPackage.InputDigest),
		fixture.publication.source.SourceID,
		v2NextPackage.ProtectionDomain,
	).Count(&packageVectorCount).Error)
	require.Equal(t, int64(1), packageVectorCount, "the unchanged package chunk must link to its existing vector")
	var currentSourceVectors int64
	require.NoError(t, fixture.publication.db.Model(&UCIEmbedding{}).Where(
		"embedding_profile_id = ? AND source_id = ? AND protection_domain = ?",
		v2NextClaim.Ref.EmbeddingProfileID,
		fixture.publication.source.SourceID,
		"source-private",
	).Count(&currentSourceVectors).Error)
	require.Equal(t, int64(8), currentSourceVectors, "five initial v2 vectors plus three changed semantic chunks")
}

func TestUCIEmbeddingTransientFailureRetainsAttemptAndStatus(t *testing.T) {
	fixture := openUCIEmbeddingJobsFixture(t)
	artifact := fixture.publication.admitArtifact(t, fixture.publication.source.SourceID, "embedding-retry", "func EmbeddingRetry() {}\n", UCIParseArtifactComplete)
	published := fixture.publish(t, "embedding-retry", fixture.publication.checkout, nil, ucidomain.IndexJobInitial,
		[]uciPublicationArtifact{artifact},
		[]ucidomain.IndexMembership{uciPublicationPresentMembership("retry.go", artifact)},
	)
	authorized := uciEmbeddingJobsAuthorize(t, fixture.publication, published.Context)
	claim, claimed, err := fixture.store.ClaimEmbeddingJob(context.Background(), fixture.profile, "embedding-retry-worker-"+fixture.publication.token, time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)

	require.NoError(t, fixture.store.FailEmbeddingJob(context.Background(), claim.Ref, ucidomain.EmbeddingFailure{
		Code:        ucidomain.EmbeddingFailureProviderUnavailable,
		Disposition: ucidomain.EmbeddingFailureRetry,
	}))
	job := uciEmbeddingJobsJob(t, fixture.publication, claim.Ref.JobID)
	require.Equal(t, UCIJobRetryScheduled, job.State)
	require.Equal(t, claim.Attempt, job.Attempt)
	require.NotNil(t, job.ErrorCode)
	require.Equal(t, string(ucidomain.EmbeddingFailureProviderUnavailable), *job.ErrorCode)
	require.NotNil(t, job.RetryAfter)
	require.True(t, job.RetryAfter.After(job.UpdatedAt))
	require.Nil(t, job.LeaseOwner)
	require.Nil(t, job.LeaseExpiry)

	status := fixture.status(t, authorized)
	require.NotNil(t, status.Embedding.EmbeddingProfileID)
	require.Equal(t, ucidomain.IndexCoveragePartial, status.Embedding.Coverage)
	require.Equal(t, uint64(1), status.Embedding.TotalCandidates)
	require.Zero(t, status.Embedding.ReadyCandidates)
	require.Equal(t, uint64(1), status.Embedding.PendingJobs)
	require.NotNil(t, status.Embedding.JobState)
	require.Equal(t, ucidomain.IndexStatusJobRetryScheduled, *status.Embedding.JobState)
	require.NotNil(t, status.Embedding.ErrorCode)
	require.Equal(t, ucidomain.EmbeddingFailureProviderUnavailable, *status.Embedding.ErrorCode)
	require.NotNil(t, status.Embedding.RetryAfter)

	require.NoError(t, fixture.publication.db.Model(&UCIJob{}).Where("job_id = ?", claim.Ref.JobID).Update("retry_after", time.Now().UTC().Add(-time.Second)).Error)
	recoveredClaim, claimed, err := fixture.store.ClaimEmbeddingJob(context.Background(), fixture.profile, "embedding-recovered-worker-"+fixture.publication.token, time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(t, claim.Attempt+1, recoveredClaim.Attempt)
	recoveredBatch, err := fixture.store.PrepareEmbeddingBatch(context.Background(), recoveredClaim, authorized, 16)
	require.NoError(t, err)
	require.Equal(t, []int{0}, recoveredBatch.MissingInputIndexes)
	require.NoError(t, fixture.store.CommitEmbeddingBatch(context.Background(), recoveredClaim, authorized, recoveredBatch, [][]float32{uciEmbeddingJobsVector(1)}))
	recoveredExhausted, err := fixture.store.PrepareEmbeddingBatch(context.Background(), recoveredClaim, authorized, 16)
	require.NoError(t, err)
	require.True(t, recoveredExhausted.Exhausted)
	require.NoError(t, fixture.store.CompleteEmbeddingJob(context.Background(), recoveredClaim, authorized))

	recovered := uciEmbeddingJobsJob(t, fixture.publication, recoveredClaim.Ref.JobID)
	require.Equal(t, UCIJobSucceeded, recovered.State)
	require.Equal(t, claim.Attempt+1, recovered.Attempt)
	require.Nil(t, recovered.ErrorCode)
	recoveredStatus := fixture.status(t, authorized)
	require.Equal(t, ucidomain.IndexCoverageComplete, recoveredStatus.Embedding.Coverage)
	require.Equal(t, uint64(1), recoveredStatus.Embedding.ReadyCandidates)
	require.Zero(t, recoveredStatus.Embedding.PendingJobs)
}

func TestUCIEmbeddingDriftObsoletesStaleWork(t *testing.T) {
	t.Run("profile", func(t *testing.T) {
		fixture := openUCIEmbeddingJobsFixture(t)
		artifact := fixture.publication.admitArtifact(t, fixture.publication.source.SourceID, "embedding-profile-drift", "func EmbeddingProfileDrift() {}\n", UCIParseArtifactComplete)
		published := fixture.publish(t, "embedding-profile-drift", fixture.publication.checkout, nil, ucidomain.IndexJobInitial,
			[]uciPublicationArtifact{artifact},
			[]ucidomain.IndexMembership{uciPublicationPresentMembership("profile-drift.go", artifact)},
		)
		oldClaim, claimed, err := fixture.store.ClaimEmbeddingJob(context.Background(), fixture.profile, "embedding-profile-worker-"+fixture.publication.token, time.Minute)
		require.NoError(t, err)
		require.True(t, claimed)

		driftedProfile := fixture.profile
		driftedProfile.Model += "-next"
		next, exhausted, err := fixture.store.EnsureCurrentEmbeddingJobs(context.Background(), driftedProfile, "", 16)
		require.NoError(t, err)
		require.True(t, exhausted)
		require.Equal(t, fixture.publication.checkout.CheckoutID, next)

		old := uciEmbeddingJobsJob(t, fixture.publication, oldClaim.Ref.JobID)
		require.Equal(t, UCIJobObsolete, old.State)
		require.NotNil(t, old.ErrorCode)
		require.Equal(t, string(ucidomain.EmbeddingFailureTargetObsolete), *old.ErrorCode)
		require.Nil(t, old.LeaseOwner)
		require.Nil(t, old.LeaseExpiry)

		jobs := uciEmbeddingJobsJobsForView(t, fixture.publication, published.Context.ViewID)
		require.Len(t, jobs, 2)
		var replacement UCIJob
		for _, job := range jobs {
			if job.JobID != old.JobID {
				replacement = job
			}
		}
		require.NotEmpty(t, replacement.JobID)
		require.Equal(t, UCIJobQueued, replacement.State)
		require.NotNil(t, old.EmbeddingProfileID)
		require.NotNil(t, replacement.EmbeddingProfileID)
		require.NotEqual(t, *old.EmbeddingProfileID, *replacement.EmbeddingProfileID)
	})

	t.Run("view", func(t *testing.T) {
		fixture := openUCIEmbeddingJobsFixture(t)
		initialArtifact := fixture.publication.admitArtifact(t, fixture.publication.source.SourceID, "embedding-view-drift-initial", "func EmbeddingViewInitial() {}\n", UCIParseArtifactComplete)
		initial := fixture.publish(t, "embedding-view-drift-initial", fixture.publication.checkout, nil, ucidomain.IndexJobInitial,
			[]uciPublicationArtifact{initialArtifact},
			[]ucidomain.IndexMembership{uciPublicationPresentMembership("view-drift.go", initialArtifact)},
		)
		oldClaim, claimed, err := fixture.store.ClaimEmbeddingJob(context.Background(), fixture.profile, "embedding-view-worker-"+fixture.publication.token, time.Minute)
		require.NoError(t, err)
		require.True(t, claimed)

		currentArtifact := fixture.publication.admitArtifact(t, fixture.publication.source.SourceID, "embedding-view-drift-current", "func EmbeddingViewCurrent() {}\n", UCIParseArtifactComplete)
		current := fixture.publish(t, "embedding-view-drift-current", fixture.publication.checkout, uciPublicationParent(initial), ucidomain.IndexJobReconcile,
			[]uciPublicationArtifact{currentArtifact},
			[]ucidomain.IndexMembership{uciPublicationPresentMembership("view-drift.go", currentArtifact)},
		)
		require.NotEqual(t, initial.Context.ViewID, current.Context.ViewID)

		old := uciEmbeddingJobsJob(t, fixture.publication, oldClaim.Ref.JobID)
		require.Equal(t, UCIJobObsolete, old.State)
		require.NotNil(t, old.ErrorCode)
		require.Equal(t, string(ucidomain.EmbeddingFailureTargetObsolete), *old.ErrorCode)
		require.Nil(t, old.LeaseOwner)
		require.Nil(t, old.LeaseExpiry)

		replacement := uciEmbeddingJobsOnlyJobForView(t, fixture.publication, current.Context.ViewID)
		require.Equal(t, UCIJobQueued, replacement.State)
		require.NotNil(t, replacement.TargetViewID)
		require.Equal(t, current.Context.ViewID, *replacement.TargetViewID)
	})
}

func TestUCIEmbeddingNoCandidatesCompletesTruthfully(t *testing.T) {
	fixture := openUCIEmbeddingJobsFixture(t)
	ctx := context.Background()
	published := fixture.publish(t, "embedding-no-candidates", fixture.publication.checkout, nil, ucidomain.IndexJobInitial,
		nil,
		[]ucidomain.IndexMembership{uciPublicationUnreadableMembership("unreadable.go")},
	)
	authorized := uciEmbeddingJobsAuthorize(t, fixture.publication, published.Context)
	claim, claimed, err := fixture.store.ClaimEmbeddingJob(ctx, fixture.profile, "embedding-empty-worker-"+fixture.publication.token, time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)

	batch, err := fixture.store.PrepareEmbeddingBatch(ctx, claim, authorized, 16)
	require.NoError(t, err)
	require.Empty(t, batch.Candidates)
	require.Empty(t, batch.MissingInputIndexes)
	require.True(t, batch.Exhausted)
	require.NoError(t, fixture.store.CompleteEmbeddingJob(ctx, claim, authorized))

	job := uciEmbeddingJobsJob(t, fixture.publication, claim.Ref.JobID)
	require.Equal(t, UCIJobSucceeded, job.State)
	require.NotNil(t, job.ErrorCode)
	require.Equal(t, string(ucidomain.EmbeddingFailureNoCandidates), *job.ErrorCode)
	require.Nil(t, job.RetryAfter)
	require.Nil(t, job.LeaseOwner)
	require.Nil(t, job.LeaseExpiry)

	status := fixture.status(t, authorized)
	require.NotNil(t, status.Embedding.EmbeddingProfileID)
	require.Equal(t, claim.Ref.EmbeddingProfileID, *status.Embedding.EmbeddingProfileID)
	require.NotEqual(t, fixture.profile.ProviderRef, *status.Embedding.EmbeddingProfileID, "status exposes an opaque durable profile ID, not the provider location")
	require.Equal(t, ucidomain.IndexCoverageUnavailable, status.Embedding.Coverage)
	require.Zero(t, status.Embedding.TotalCandidates)
	require.Zero(t, status.Embedding.ReadyCandidates)
	require.Zero(t, status.Embedding.PendingJobs)
	require.NotNil(t, status.Embedding.JobState)
	require.Equal(t, ucidomain.IndexStatusJobSucceeded, *status.Embedding.JobState)
	require.NotNil(t, status.Embedding.ErrorCode)
	require.Equal(t, ucidomain.EmbeddingFailureNoCandidates, *status.Embedding.ErrorCode)
	require.Nil(t, status.Embedding.RetryAfter)
}

func openUCIEmbeddingJobsFixture(t *testing.T) *uciEmbeddingJobsFixture {
	t.Helper()

	publication := openUCIPublicationFixture(t)
	profile := ucidomain.VectorProfile{
		ProviderRef:           "https://embedding.invalid/uci/" + publication.token,
		Model:                 "embedding-model-" + publication.token,
		Dimension:             1536,
		PreprocessingRevision: "embedding-preprocessing-" + publication.token,
		IncludeRelativePath:   true,
	}
	publisher := uciEmbeddingJobsPublisher(t, publication, profile)
	return &uciEmbeddingJobsFixture{
		publication: publication,
		store:       NewUCIProjectionStore(publication.db),
		profile:     profile,
		publisher:   publisher,
	}
}

func uciEmbeddingJobsPublisher(t *testing.T, fixture *uciPublicationFixture, profile ucidomain.VectorProfile) ucidomain.IndexStore {
	t.Helper()

	publisher, err := NewUCIProjectionStore(fixture.db).Publisher(fixture.authorizer, ucidomain.IndexPublicationConfig{
		Limits:           fixture.limits,
		EmbeddingProfile: &profile,
	})
	require.NoError(t, err)
	return publisher
}

func (fixture *uciEmbeddingJobsFixture) publish(t *testing.T, key string, checkout *UCICheckout, parent *ucidomain.ContextRef, kind ucidomain.IndexJobKind, artifacts []uciPublicationArtifact, memberships []ucidomain.IndexMembership) ucidomain.IndexPublishedView {
	t.Helper()

	draft := uciEmbeddingJobsDraft(artifacts, memberships)
	_, published := fixture.publication.publish(
		t,
		fixture.publisher,
		fixture.publication.caller("embedding-"+key),
		key,
		checkout,
		fixture.publication.profile.ProfileID,
		parent,
		ucidomain.IndexManifestFull,
		kind,
		draft,
	)
	return published
}

func (fixture *uciEmbeddingJobsFixture) status(t *testing.T, authorized ucidomain.AuthorizedContext) ucidomain.IndexStatusSnapshot {
	t.Helper()

	service := ucidomain.NewIndexStatusService(fixture.store, &fixture.profile)
	status, err := service.Status(context.Background(), authorized, "")
	require.NoError(t, err)
	return status
}

func uciEmbeddingJobsFactsOnlyArtifact(t *testing.T, fixture *uciPublicationFixture) uciPublicationArtifact {
	t.Helper()

	ctx := context.Background()
	label := "embedding-facts-only-" + fixture.token
	symbol := "EmbeddingFactsOnly" + fixture.token
	body := []byte("package fixture\nfunc " + symbol + "() {}\n")
	blob, err := fixture.projection.UpsertBlob(ctx, UpsertUCIBlobInput{
		SourceID:         fixture.source.SourceID,
		ProtectionDomain: "source-private",
		ContentDigest:    uciPublicationDigestBytes(body),
		ByteLength:       int64(len(body)),
		SafeContent:      append([]byte(nil), body...),
		Encoding:         "utf-8",
		StorageState:     UCIBlobStored,
	})
	require.NoError(t, err)
	artifact, err := fixture.projection.UpsertParseArtifact(ctx, UpsertUCIParseArtifactInput{
		SourceID:                fixture.source.SourceID,
		BlobID:                  blob.BlobID,
		Language:                "go",
		ParserRevision:          "parser-" + label,
		GrammarDigest:           uciPublicationDigest("grammar-" + label),
		ExtractionProfileDigest: fixture.profile.ParserBundleDigest,
		Status:                  UCIParseArtifactComplete,
		Diagnostics:             `{}`,
	})
	require.NoError(t, err)
	definitionStart := int64(len("package fixture\n"))
	definitionEnd := int64(len(body) - 1)
	definition, err := fixture.projection.UpsertDefinition(ctx, UpsertUCIDefinitionInput{
		ArtifactID:         artifact.ArtifactID,
		LocalSymbolKey:     symbol,
		Kind:               "function",
		Name:               symbol,
		QualifiedLocalName: "fixture." + symbol,
		Signature:          "func " + symbol + "()",
		ByteStart:          definitionStart,
		ByteEnd:            definitionEnd,
		LineStart:          2,
		LineEnd:            2,
	})
	require.NoError(t, err)
	spanJSON := fmt.Sprintf(`{"byte_start":%d,"byte_end":%d,"line_start":2,"line_end":2}`, definitionStart, definitionStart+4)
	reference, err := fixture.projection.UpsertReferenceSite(ctx, UpsertUCIReferenceSiteInput{
		ArtifactID:     artifact.ArtifactID,
		SiteKey:        "site-" + label,
		OwnerSymbolKey: &definition.LocalSymbolKey,
		RawTarget:      "fixture.Target",
		Relation:       "calls",
		SyntaxSpan:     spanJSON,
		ResolverHints:  `{}`,
	})
	require.NoError(t, err)
	proof, err := fixture.projection.DescribeIndexArtifact(ctx, fixture.source.SourceID, artifact.ArtifactID)
	require.NoError(t, err)

	return uciPublicationArtifact{
		Body:       body,
		Blob:       blob,
		Artifact:   artifact,
		Definition: definition,
		Reference:  reference,
		Proof:      proof,
	}
}

func uciEmbeddingJobsDraft(artifacts []uciPublicationArtifact, memberships []ucidomain.IndexMembership) uciPublicationDraft {
	replacements := make([]ucidomain.IndexEdgeReplacement, 0, len(memberships))
	for _, membership := range memberships {
		replacements = append(replacements, ucidomain.IndexEdgeReplacement{SourcePath: membership.PathKey})
	}
	return newUCIPublicationDraft(
		[]ucidomain.IndexPart{uciPublicationPart(artifacts, memberships, nil, replacements)},
		memberships,
		replacements,
	)
}

func uciEmbeddingJobsAuthorize(t *testing.T, fixture *uciPublicationFixture, ref ucidomain.ContextRef) ucidomain.AuthorizedContext {
	t.Helper()

	resolver := ucidomain.NewContextResolver(uciEmbeddingJobsCatalog{
		ref.ViewID: {Ref: ref, AuthRealm: fixture.realm},
	}, fixture.authorizer, nil)
	authorized, err := resolver.Authorize(context.Background(), ucidomain.ResolveContextInput{
		ClientSessionID: "embedding-jobs-client-" + fixture.token + "-" + ref.ViewID,
		AuthRealm:       fixture.realm,
		Principal:       fixture.principal,
		Ref:             &ref,
	})
	require.NoError(t, err)
	require.Equal(t, ref, authorized.Ref())
	return authorized
}

func uciEmbeddingJobsOnlyJobForView(t *testing.T, fixture *uciPublicationFixture, viewID string) UCIJob {
	t.Helper()

	jobs := uciEmbeddingJobsJobsForView(t, fixture, viewID)
	require.Len(t, jobs, 1)
	return jobs[0]
}

func uciEmbeddingJobsJobsForView(t *testing.T, fixture *uciPublicationFixture, viewID string) []UCIJob {
	t.Helper()

	var jobs []UCIJob
	require.NoError(t, fixture.db.Where("source_id = ? AND checkout_id = ? AND job_kind = ? AND target_view_id = ?", fixture.source.SourceID, fixture.checkout.CheckoutID, "embed", viewID).Order("created_at ASC, job_id ASC").Find(&jobs).Error)
	return jobs
}

func uciEmbeddingJobsJob(t *testing.T, fixture *uciPublicationFixture, jobID string) UCIJob {
	t.Helper()

	var job UCIJob
	require.NoError(t, fixture.db.Where("job_id = ?", jobID).First(&job).Error)
	return job
}

func uciEmbeddingJobsEmbeddings(t *testing.T, fixture *uciPublicationFixture, embeddingProfileID string) []UCIEmbedding {
	t.Helper()

	var embeddings []UCIEmbedding
	require.NoError(t, fixture.db.Where("embedding_profile_id = ?", embeddingProfileID).Order("embedding_id ASC").Find(&embeddings).Error)
	return embeddings
}

func uciEmbeddingJobsLinks(t *testing.T, fixture *uciPublicationFixture, embeddingProfileID string) []UCIChunkEmbedding {
	t.Helper()

	var links []UCIChunkEmbedding
	require.NoError(t, fixture.db.Where("embedding_profile_id = ?", embeddingProfileID).Order("chunk_embedding_id ASC").Find(&links).Error)
	return links
}

func uciEmbeddingJobsVector(marker float32) []float32 {
	vector := make([]float32, 1536)
	vector[0] = marker
	vector[1] = marker / 10
	return vector
}

func (fixture *uciEmbeddingJobsFixture) publishAdmitted(t *testing.T, publisher ucidomain.IndexStore, key string, parent *ucidomain.ContextRef, kind ucidomain.IndexJobKind, parts []ucidomain.IndexPart) ucidomain.IndexPublishedView {
	t.Helper()
	staged := append([]ucidomain.IndexPart(nil), parts...)
	memberships := make([]ucidomain.IndexMembership, 0)
	replacements := make([]ucidomain.IndexEdgeReplacement, 0)
	for index := range staged {
		part := &staged[index]
		part.EdgeReplacements = append([]ucidomain.IndexEdgeReplacement(nil), part.EdgeReplacements...)
		replacementPaths := make(map[string]struct{}, len(part.EdgeReplacements))
		for _, replacement := range part.EdgeReplacements {
			replacementPaths[replacement.SourcePath] = struct{}{}
		}
		for _, membership := range part.Memberships {
			if _, exists := replacementPaths[membership.PathKey]; !exists {
				part.EdgeReplacements = append(part.EdgeReplacements, ucidomain.IndexEdgeReplacement{SourcePath: membership.PathKey})
			}
		}
		memberships = append(memberships, part.Memberships...)
		replacements = append(replacements, part.EdgeReplacements...)
	}
	draft := newUCIPublicationDraft(staged, memberships, replacements)
	_, published := fixture.publication.publish(t, publisher, fixture.publication.caller("embedding-"+key), key, fixture.publication.checkout, fixture.publication.profile.ProfileID, parent, ucidomain.IndexManifestFull, kind, draft)
	return published
}

func uciEmbeddingJobsCommitBatch(t *testing.T, fixture *uciEmbeddingJobsFixture, claim ucidomain.EmbeddingJobClaim, authorized ucidomain.AuthorizedContext, batch ucidomain.EmbeddingBatch, marker float32) {
	t.Helper()
	vectors := make([][]float32, len(batch.MissingInputIndexes))
	for index := range vectors {
		vectors[index] = uciEmbeddingJobsVector(marker + float32(index))
	}
	require.NoError(t, fixture.store.CommitEmbeddingBatch(context.Background(), claim, authorized, batch, vectors))
	exhausted, err := fixture.store.PrepareEmbeddingBatch(context.Background(), claim, authorized, 16)
	require.NoError(t, err)
	require.True(t, exhausted.Exhausted)
	require.NoError(t, fixture.store.CompleteEmbeddingJob(context.Background(), claim, authorized))
}

func uciEmbeddingJobsCandidateByEntity(t *testing.T, candidates []ucidomain.EmbeddingCandidate, entity string) ucidomain.EmbeddingCandidate {
	t.Helper()
	for _, candidate := range candidates {
		if candidate.Candidate.EntityKey == entity {
			return candidate
		}
	}
	require.Failf(t, "missing embedding candidate", "entity %q not found in %#v", entity, candidates)
	return ucidomain.EmbeddingCandidate{}
}

func uciEmbeddingJobsVersionedGoSource(callee string) string {
	return "package fixture\n\nfunc SharedTarget() string {\n\treturn " + callee + "()\n}\n\nfunc " + callee + "() string {\n\treturn \"" + callee + "\"\n}\n"
}

type uciEmbeddingJobsCatalog map[string]ucidomain.ContextRecord

func (catalog uciEmbeddingJobsCatalog) LoadContext(_ context.Context, ref ucidomain.ContextRef) (ucidomain.ContextRecord, error) {
	record, found := catalog[ref.ViewID]
	if !found {
		return ucidomain.ContextRecord{}, fmt.Errorf("embedding jobs fixture context %q is not registered", ref.ViewID)
	}
	return record, nil
}
