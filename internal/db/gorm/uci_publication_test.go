package gorm

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	ucidomain "github.com/thebtf/engram/internal/uci"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const uciPublicationMigrationID = "173_uci_fenced_publication"

type uciPublicationFixture struct {
	db         *gormlib.DB
	schema     string
	context    *UCIContextStore
	projection *UCIProjectionStore
	publisher  ucidomain.IndexStore
	authorizer *uciPublicationAuthorizer
	limits     ucidomain.IndexPublicationLimits

	token     string
	realm     string
	principal string
	source    *UCISource
	foreign   *UCISource
	checkout  *UCICheckout
	sibling   *UCICheckout
	profile   *UCIAnalysisProfile
	seed      uciPublicationArtifact
}

type uciPublicationArtifact struct {
	Body       []byte
	Blob       *UCIBlob
	Artifact   *UCIParseArtifact
	Definition *UCIDefinition
	Reference  *UCIReferenceSite
	Chunk      *UCIChunk
	Proof      ucidomain.IndexArtifactProof
}

type uciPublicationAuthorizer struct {
	realm          string
	principal      string
	allowedSources map[string]bool
}

func (a *uciPublicationAuthorizer) AuthorizeContext(_ context.Context, access ucidomain.ContextAccess) error {
	if access.AuthRealm != a.realm || access.Principal != a.principal || !a.allowedSources[access.SourceID] {
		return fmt.Errorf("publication fixture authorization denied")
	}
	return nil
}

type uciPublicationDraft struct {
	parts        []ucidomain.IndexPart
	memberships  []ucidomain.IndexMembership
	replacements []ucidomain.IndexEdgeReplacement
	scanOutcome  ucidomain.IndexScanOutcome
	census       bool
	coverage     ucidomain.IndexCoverage
	fsSeq        int64
}

func TestUCIPublishInitialCheckoutAndCoherentCurrent(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	main := fixture.admitArtifact(t, fixture.source.SourceID, "initial-main", "func Main() {}\n", UCIParseArtifactComplete)
	target := fixture.admitArtifact(t, fixture.source.SourceID, "initial-target", "func Target() {}\n", UCIParseArtifactComplete)
	memberships := []ucidomain.IndexMembership{
		uciPublicationPresentMembership("main.go", main),
		uciPublicationPresentMembership("target.go", target),
	}
	replacements := []ucidomain.IndexEdgeReplacement{
		{SourcePath: "main.go", Edges: []ucidomain.IndexEdge{uciPublicationResolvedEdge(main, "main.go", target, "target.go")}},
		{SourcePath: "target.go"},
	}
	draft := newUCIPublicationDraft(
		[]ucidomain.IndexPart{uciPublicationPart([]uciPublicationArtifact{main, target}, memberships, nil, replacements)},
		memberships,
		replacements,
	)

	fixture.assertNoCurrentView(t, fixture.checkout)
	build := fixture.begin(t, fixture.publisher, fixture.caller("initial-owner"), "initial", fixture.checkout, fixture.profile.ProfileID, nil, ucidomain.IndexManifestFull, ucidomain.IndexJobInitial)
	acks := fixture.stageDraft(t, fixture.publisher, fixture.caller("initial-owner"), build.Build, draft.parts)
	fixture.assertNoCurrentView(t, fixture.checkout)
	fixture.assertCurrentIntervals(t, fixture.checkout, nil, nil)

	published := fixture.finalizeDraft(t, fixture.publisher, fixture.caller("initial-owner"), build.Build, nil, acks, draft)
	require.Equal(t, int64(1), published.Context.Generation)
	fixture.assertCurrentProjection(t, fixture.checkout, published, memberships, replacements, ucidomain.IndexCoverage{
		Structural: ucidomain.IndexCoverageComplete,
		Lexical:    ucidomain.IndexCoverageComplete,
		Vector:     ucidomain.IndexCoverageUnavailable,
	})
	fixture.assertPublicationJobResult(t, build.Build.BuildID, published.Context.ViewID)
}

func TestUCIPublishStagingInvisibleAndHistoricalIntervals(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	mainV1 := fixture.admitArtifact(t, fixture.source.SourceID, "history-main-v1", "func MainV1() {}\n", UCIParseArtifactComplete)
	targetV1 := fixture.admitArtifact(t, fixture.source.SourceID, "history-target-v1", "func TargetV1() {}\n", UCIParseArtifactComplete)
	keep := fixture.admitArtifact(t, fixture.source.SourceID, "history-keep", "func Keep() {}\n", UCIParseArtifactComplete)
	membersV1 := []ucidomain.IndexMembership{
		uciPublicationPresentMembership("main.go", mainV1),
		uciPublicationPresentMembership("target.go", targetV1),
		uciPublicationPresentMembership("keep.go", keep),
	}
	edgesV1 := []ucidomain.IndexEdgeReplacement{
		{SourcePath: "main.go", Edges: []ucidomain.IndexEdge{uciPublicationResolvedEdge(mainV1, "main.go", targetV1, "target.go")}},
		{SourcePath: "target.go"},
		{SourcePath: "keep.go"},
	}
	firstDraft := newUCIPublicationDraft(
		[]ucidomain.IndexPart{uciPublicationPart([]uciPublicationArtifact{mainV1, targetV1, keep}, membersV1, nil, edgesV1)},
		membersV1,
		edgesV1,
	)
	_, first := fixture.publish(t, fixture.publisher, fixture.caller("history-owner"), "history-v1", fixture.checkout, fixture.profile.ProfileID, nil, ucidomain.IndexManifestFull, ucidomain.IndexJobInitial, firstDraft)

	mainV2 := fixture.admitArtifact(t, fixture.source.SourceID, "history-main-v2", "func MainV2() {}\n", UCIParseArtifactComplete)
	targetV2 := fixture.admitArtifact(t, fixture.source.SourceID, "history-target-v2", "func TargetV2() {}\n", UCIParseArtifactComplete)
	membersV2 := []ucidomain.IndexMembership{
		uciPublicationPresentMembership("main.go", mainV2),
		uciPublicationPresentMembership("target.go", targetV2),
		uciPublicationPresentMembership("keep.go", keep),
	}
	edgesV2 := []ucidomain.IndexEdgeReplacement{
		{SourcePath: "main.go", Edges: []ucidomain.IndexEdge{uciPublicationResolvedEdge(mainV2, "main.go", targetV2, "target.go")}},
		{SourcePath: "target.go"},
		{SourcePath: "keep.go"},
	}
	secondDraft := newUCIPublicationDraft(
		[]ucidomain.IndexPart{uciPublicationPart([]uciPublicationArtifact{mainV2, targetV2, keep}, membersV2, nil, edgesV2)},
		membersV2,
		edgesV2,
	)
	parent := uciPublicationParent(first)
	build := fixture.begin(t, fixture.publisher, fixture.caller("history-owner"), "history-v2", fixture.checkout, fixture.profile.ProfileID, parent, ucidomain.IndexManifestFull, ucidomain.IndexJobReconcile)
	acks := fixture.stageDraft(t, fixture.publisher, fixture.caller("history-owner"), build.Build, secondDraft.parts)

	fixture.assertCurrentProjection(t, fixture.checkout, first, membersV1, edgesV1, ucidomain.IndexCoverage{
		Structural: ucidomain.IndexCoverageComplete,
		Lexical:    ucidomain.IndexCoverageComplete,
		Vector:     ucidomain.IndexCoverageUnavailable,
	})
	second := fixture.finalizeDraft(t, fixture.publisher, fixture.caller("history-owner"), build.Build, parent, acks, secondDraft)
	fixture.assertCurrentProjection(t, fixture.checkout, second, membersV2, edgesV2, ucidomain.IndexCoverage{
		Structural: ucidomain.IndexCoverageComplete,
		Lexical:    ucidomain.IndexCoverageComplete,
		Vector:     ucidomain.IndexCoverageUnavailable,
	})
	fixture.assertProjectionAtGeneration(t, fixture.checkout, first.Context.Generation, membersV1, edgesV1)
	fixture.assertProjectionAtGeneration(t, fixture.checkout, second.Context.Generation, membersV2, edgesV2)

	var unchangedIntervals int64
	require.NoError(t, fixture.db.Model(&UCIMembership{}).Where(
		"checkout_id = ? AND path_key = ? AND artifact_id = ? AND valid_from_generation = ? AND valid_to_generation IS NULL",
		fixture.checkout.CheckoutID, "keep.go", keep.Artifact.ArtifactID, first.Context.Generation,
	).Count(&unchangedIntervals).Error)
	require.Equal(t, int64(1), unchangedIntervals, "unchanged membership must keep its original open interval")
}

func TestUCIPublishExpectedParentAndNoGenerationReservation(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	initial := fixture.admitArtifact(t, fixture.source.SourceID, "parent-initial", "func Initial() {}\n", UCIParseArtifactComplete)
	initialMembers := []ucidomain.IndexMembership{uciPublicationPresentMembership("main.go", initial)}
	initialEdges := []ucidomain.IndexEdgeReplacement{{SourcePath: "main.go"}}
	initialDraft := newUCIPublicationDraft(
		[]ucidomain.IndexPart{uciPublicationPart([]uciPublicationArtifact{initial}, initialMembers, nil, initialEdges)},
		initialMembers,
		initialEdges,
	)
	_, current := fixture.publish(t, fixture.publisher, fixture.caller("parent-owner"), "parent-initial", fixture.checkout, fixture.profile.ProfileID, nil, ucidomain.IndexManifestFull, ucidomain.IndexJobInitial, initialDraft)
	parent := uciPublicationParent(current)

	before, err := fixture.context.GetCheckout(context.Background(), fixture.checkout.CheckoutID)
	require.NoError(t, err)
	wrongParent := *parent
	wrongParent.ViewID = uuid.NewString()
	_, err = fixture.publisher.Begin(context.Background(), fixture.caller("parent-owner"), fixture.beginInput("wrong-parent", fixture.checkout, fixture.profile.ProfileID, &wrongParent, ucidomain.IndexManifestFull, ucidomain.IndexJobReconcile))
	require.Error(t, err)
	after, err := fixture.context.GetCheckout(context.Background(), fixture.checkout.CheckoutID)
	require.NoError(t, err)
	require.Equal(t, before.LeaseEpoch, after.LeaseEpoch, "parent rejection must not acquire a new lease epoch")
	require.Equal(t, before.CurrentViewID, after.CurrentViewID, "parent rejection must not change current")

	candidate := fixture.admitArtifact(t, fixture.source.SourceID, "parent-candidate", "func Candidate() {}\n", UCIParseArtifactComplete)
	candidateMembers := []ucidomain.IndexMembership{uciPublicationPresentMembership("main.go", candidate)}
	candidateEdges := []ucidomain.IndexEdgeReplacement{{SourcePath: "main.go"}}
	candidateDraft := newUCIPublicationDraft(
		[]ucidomain.IndexPart{uciPublicationPart([]uciPublicationArtifact{candidate}, candidateMembers, nil, candidateEdges)},
		candidateMembers,
		candidateEdges,
	)
	first := fixture.begin(t, fixture.publisher, fixture.caller("first-owner"), "first-candidate", fixture.checkout, fixture.profile.ProfileID, parent, ucidomain.IndexManifestFull, ucidomain.IndexJobReconcile)
	firstAcks := fixture.stageDraft(t, fixture.publisher, fixture.caller("first-owner"), first.Build, candidateDraft.parts)
	fixture.assertViewCount(t, fixture.checkout, 1)
	fixture.expireBuild(t, first.Build, fixture.checkout)

	second := fixture.begin(t, fixture.publisher, fixture.caller("second-owner"), "second-candidate", fixture.checkout, fixture.profile.ProfileID, parent, ucidomain.IndexManifestFull, ucidomain.IndexJobReconcile)
	require.Greater(t, second.Build.LeaseEpoch, first.Build.LeaseEpoch)
	fixture.assertViewCount(t, fixture.checkout, 1)
	secondAcks := fixture.stageDraft(t, fixture.publisher, fixture.caller("second-owner"), second.Build, candidateDraft.parts)
	published := fixture.finalizeDraft(t, fixture.publisher, fixture.caller("second-owner"), second.Build, parent, secondAcks, candidateDraft)
	require.Equal(t, int64(2), published.Context.Generation)

	_, err = fixture.publisher.Finalize(context.Background(), fixture.caller("first-owner"), ucidomain.IndexFinalizeInput{
		Build:          first.Build,
		ExpectedParent: parent,
		Manifest:       fixture.manifest(t, firstAcks, candidateDraft),
	})
	require.Error(t, err, "a candidate prepared against an old parent cannot publish after its rival")
	fixture.assertCurrentProjection(t, fixture.checkout, published, candidateMembers, candidateEdges, ucidomain.IndexCoverage{
		Structural: ucidomain.IndexCoverageComplete,
		Lexical:    ucidomain.IndexCoverageComplete,
		Vector:     ucidomain.IndexCoverageUnavailable,
	})
}

func TestUCIPublishIncompletePartsAndEOF(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	main := fixture.admitArtifact(t, fixture.source.SourceID, "parts-main", "func Main() {}\n", UCIParseArtifactComplete)
	target := fixture.admitArtifact(t, fixture.source.SourceID, "parts-target", "func Target() {}\n", UCIParseArtifactComplete)
	memberships := []ucidomain.IndexMembership{
		uciPublicationPresentMembership("main.go", main),
		uciPublicationPresentMembership("target.go", target),
	}
	replacements := []ucidomain.IndexEdgeReplacement{{SourcePath: "main.go"}, {SourcePath: "target.go"}}
	part0 := uciPublicationPart([]uciPublicationArtifact{main}, memberships[:1], nil, replacements[:1])
	part1 := uciPublicationPart([]uciPublicationArtifact{target}, memberships[1:], nil, replacements[1:])
	build := fixture.begin(t, fixture.publisher, fixture.caller("parts-owner"), "parts", fixture.checkout, fixture.profile.ProfileID, nil, ucidomain.IndexManifestFull, ucidomain.IndexJobInitial)

	_, err := fixture.stagePart(fixture.publisher, fixture.caller("parts-owner"), build.Build, 1, part1)
	require.Error(t, err, "a gapped upload must not create a durable part")
	fixture.assertBuildPartCount(t, build.Build.BuildID, 0)
	ack0 := fixture.stage(t, fixture.publisher, fixture.caller("parts-owner"), build.Build, 0, part0)
	incomplete := newUCIPublicationDraft([]ucidomain.IndexPart{part0, part1}, memberships, replacements)
	badManifest := fixture.manifest(t, []ucidomain.IndexPartAck{ack0}, incomplete)
	badManifest.PartCount = 2
	_, err = fixture.publisher.Finalize(context.Background(), fixture.caller("parts-owner"), ucidomain.IndexFinalizeInput{
		Build:    build.Build,
		Manifest: badManifest,
	})
	require.Error(t, err, "a missing terminal part must preserve the current View")
	fixture.assertNoCurrentView(t, fixture.checkout)

	ack1 := fixture.stage(t, fixture.publisher, fixture.caller("parts-owner"), build.Build, 1, part1)
	published := fixture.finalizeDraft(t, fixture.publisher, fixture.caller("parts-owner"), build.Build, nil, []ucidomain.IndexPartAck{ack0, ack1}, incomplete)
	fixture.assertCurrentProjection(t, fixture.checkout, published, memberships, replacements, ucidomain.IndexCoverage{
		Structural: ucidomain.IndexCoverageComplete,
		Lexical:    ucidomain.IndexCoverageComplete,
		Vector:     ucidomain.IndexCoverageUnavailable,
	})

	eofBuild := fixture.begin(t, fixture.publisher, fixture.caller("eof-owner"), "ordinary-eof", fixture.sibling, fixture.profile.ProfileID, nil, ucidomain.IndexManifestFull, ucidomain.IndexJobInitial)
	eofDraft := newUCIPublicationDraft(nil, nil, nil)
	eofDraft.scanOutcome = ucidomain.IndexScanIncomplete
	eofDraft.census = false
	_, err = fixture.publisher.Finalize(context.Background(), fixture.caller("eof-owner"), ucidomain.IndexFinalizeInput{
		Build:    eofBuild.Build,
		Manifest: fixture.manifest(t, nil, eofDraft),
	})
	require.Error(t, err, "transport EOF without an explicit complete census cannot publish an empty View")
	fixture.assertNoCurrentView(t, fixture.sibling)
}

func TestUCIReplayBeginAndStageBinding(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	artifact := fixture.admitArtifact(t, fixture.source.SourceID, "replay", "func Replay() {}\n", UCIParseArtifactComplete)
	memberships := []ucidomain.IndexMembership{uciPublicationPresentMembership("main.go", artifact)}
	replacements := []ucidomain.IndexEdgeReplacement{{SourcePath: "main.go"}}
	part := uciPublicationPart([]uciPublicationArtifact{artifact}, memberships, nil, replacements)
	caller := fixture.caller("replay-owner")
	first := fixture.begin(t, fixture.publisher, caller, "replay-key", fixture.checkout, fixture.profile.ProfileID, nil, ucidomain.IndexManifestFull, ucidomain.IndexJobInitial)
	retry := fixture.begin(t, fixture.publisher, caller, "replay-key", fixture.checkout, fixture.profile.ProfileID, nil, ucidomain.IndexManifestFull, ucidomain.IndexJobInitial)
	require.Equal(t, first.Build, retry.Build, "an exact Begin retry must retain its original fence")

	otherProfile := fixture.createProfile(t, "replay-other-profile")
	_, err := fixture.publisher.Begin(context.Background(), caller, fixture.beginInput("replay-key", fixture.checkout, otherProfile.ProfileID, nil, ucidomain.IndexManifestFull, ucidomain.IndexJobInitial))
	require.Error(t, err, "a changed Begin binding must not disclose or replace an existing build")
	fixture.assertPublicationJobCount(t, fixture.checkout, "replay-key", 1)

	ack := fixture.stage(t, fixture.publisher, caller, first.Build, 0, part)
	duplicate, err := fixture.stagePart(fixture.publisher, caller, first.Build, 0, part)
	require.NoError(t, err)
	require.Equal(t, ack, duplicate, "an exact frame retry must replay the durable ACK")
	fixture.assertBuildPartCount(t, first.Build.BuildID, 1)

	changed := part
	changed.Memberships = []ucidomain.IndexMembership{uciPublicationPresentMembership("renamed.go", artifact)}
	_, err = fixture.stagePart(fixture.publisher, caller, first.Build, 0, changed)
	require.Error(t, err, "a duplicate sequence with different payload must never overwrite the accepted part")
	fixture.assertBuildPartCount(t, first.Build.BuildID, 1)
}

func TestUCILeaseExpiryTakeoverAndStaleWriter(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	artifact := fixture.admitArtifact(t, fixture.source.SourceID, "lease", "func Lease() {}\n", UCIParseArtifactComplete)
	memberships := []ucidomain.IndexMembership{uciPublicationPresentMembership("main.go", artifact)}
	replacements := []ucidomain.IndexEdgeReplacement{{SourcePath: "main.go"}}
	draft := newUCIPublicationDraft(
		[]ucidomain.IndexPart{uciPublicationPart([]uciPublicationArtifact{artifact}, memberships, nil, replacements)},
		memberships,
		replacements,
	)
	firstCaller := fixture.caller("lease-first")
	first := fixture.begin(t, fixture.publisher, firstCaller, "lease-first", fixture.checkout, fixture.profile.ProfileID, nil, ucidomain.IndexManifestFull, ucidomain.IndexJobInitial)
	firstAcks := fixture.stageDraft(t, fixture.publisher, firstCaller, first.Build, draft.parts)

	peerDB, peerApplicationName := fixture.openPeerDB(t)
	peerStore := fixture.newPublisher(t, peerDB)
	lockTx := fixture.db.Begin()
	require.NoError(t, lockTx.Error)
	var locked UCICheckout
	require.NoError(t, lockTx.Raw(`SELECT * FROM ci_checkouts WHERE checkout_id = ? FOR UPDATE`, fixture.checkout.CheckoutID).Scan(&locked).Error)

	type beginResult struct {
		result ucidomain.IndexBeginResult
		err    error
	}
	started := make(chan struct{})
	finished := make(chan beginResult, 1)
	secondCaller := fixture.caller("lease-second")
	go func() {
		close(started)
		result, err := peerStore.Begin(context.Background(), secondCaller, fixture.beginInput("lease-second", fixture.checkout, fixture.profile.ProfileID, nil, ucidomain.IndexManifestFull, ucidomain.IndexJobInitial))
		finished <- beginResult{result: result, err: err}
	}()
	<-started
	fixture.waitForPeerLock(t, peerApplicationName)
	require.NoError(t, lockTx.Exec(`UPDATE ci_jobs SET lease_expiry = clock_timestamp() - interval '1 microsecond' WHERE job_id = ?`, first.Build.BuildID).Error)
	require.NoError(t, lockTx.Exec(`UPDATE ci_checkouts SET lease_expires_at = clock_timestamp() - interval '1 microsecond' WHERE checkout_id = ?`, fixture.checkout.CheckoutID).Error)
	require.NoError(t, lockTx.Commit().Error)
	secondResult := <-finished
	require.NoError(t, secondResult.err)
	require.Greater(t, secondResult.result.Build.LeaseEpoch, first.Build.LeaseEpoch, "the waiting acquirer must compare expiry with database time after its lock")

	_, err := fixture.stagePart(fixture.publisher, firstCaller, first.Build, 1, draft.parts[0])
	require.Error(t, err, "expired writers cannot add new parts")
	_, err = fixture.publisher.Finalize(context.Background(), firstCaller, ucidomain.IndexFinalizeInput{
		Build:    first.Build,
		Manifest: fixture.manifest(t, firstAcks, draft),
	})
	require.Error(t, err, "expired writers cannot publish")

	secondAcks := fixture.stageDraft(t, peerStore, secondCaller, secondResult.result.Build, draft.parts)
	published := fixture.finalizeDraft(t, peerStore, secondCaller, secondResult.result.Build, nil, secondAcks, draft)
	fixture.assertCurrentProjection(t, fixture.checkout, published, memberships, replacements, ucidomain.IndexCoverage{
		Structural: ucidomain.IndexCoverageComplete,
		Lexical:    ucidomain.IndexCoverageComplete,
		Vector:     ucidomain.IndexCoverageUnavailable,
	})

	siblingArtifact := fixture.admitArtifact(t, fixture.source.SourceID, "lease-sibling", "func Sibling() {}\n", UCIParseArtifactComplete)
	siblingMembers := []ucidomain.IndexMembership{uciPublicationPresentMembership("sibling.go", siblingArtifact)}
	siblingEdges := []ucidomain.IndexEdgeReplacement{{SourcePath: "sibling.go"}}
	siblingDraft := newUCIPublicationDraft(
		[]ucidomain.IndexPart{uciPublicationPart([]uciPublicationArtifact{siblingArtifact}, siblingMembers, nil, siblingEdges)},
		siblingMembers,
		siblingEdges,
	)
	_, siblingPublished := fixture.publish(t, fixture.publisher, fixture.caller("sibling-owner"), "lease-sibling", fixture.sibling, fixture.profile.ProfileID, nil, ucidomain.IndexManifestFull, ucidomain.IndexJobInitial, siblingDraft)
	fixture.assertCurrentProjection(t, fixture.sibling, siblingPublished, siblingMembers, siblingEdges, ucidomain.IndexCoverage{
		Structural: ucidomain.IndexCoverageComplete,
		Lexical:    ucidomain.IndexCoverageComplete,
		Vector:     ucidomain.IndexCoverageUnavailable,
	})
}

func TestUCIReplayFinalizeAfterLostACKAndLaterPublish(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	firstArtifact := fixture.admitArtifact(t, fixture.source.SourceID, "lost-ack-first", "func First() {}\n", UCIParseArtifactComplete)
	firstMembers := []ucidomain.IndexMembership{uciPublicationPresentMembership("main.go", firstArtifact)}
	firstEdges := []ucidomain.IndexEdgeReplacement{{SourcePath: "main.go"}}
	firstDraft := newUCIPublicationDraft(
		[]ucidomain.IndexPart{uciPublicationPart([]uciPublicationArtifact{firstArtifact}, firstMembers, nil, firstEdges)},
		firstMembers,
		firstEdges,
	)
	caller := fixture.caller("lost-ack-owner")
	firstBuild := fixture.begin(t, fixture.publisher, caller, "lost-ack-first", fixture.checkout, fixture.profile.ProfileID, nil, ucidomain.IndexManifestFull, ucidomain.IndexJobInitial)
	firstAcks := fixture.stageDraft(t, fixture.publisher, caller, firstBuild.Build, firstDraft.parts)
	first := fixture.finalizeDraft(t, fixture.publisher, caller, firstBuild.Build, nil, firstAcks, firstDraft)
	firstManifest := fixture.manifest(t, firstAcks, firstDraft)

	fixture.expireBuild(t, firstBuild.Build, fixture.checkout)
	secondArtifact := fixture.admitArtifact(t, fixture.source.SourceID, "lost-ack-second", "func Second() {}\n", UCIParseArtifactComplete)
	secondMembers := []ucidomain.IndexMembership{uciPublicationPresentMembership("main.go", secondArtifact)}
	secondEdges := []ucidomain.IndexEdgeReplacement{{SourcePath: "main.go"}}
	secondDraft := newUCIPublicationDraft(
		[]ucidomain.IndexPart{uciPublicationPart([]uciPublicationArtifact{secondArtifact}, secondMembers, nil, secondEdges)},
		secondMembers,
		secondEdges,
	)
	parent := uciPublicationParent(first)
	_, second := fixture.publish(t, fixture.newPublisher(t, fixture.db), fixture.caller("later-owner"), "lost-ack-later", fixture.checkout, fixture.profile.ProfileID, parent, ucidomain.IndexManifestFull, ucidomain.IndexJobReconcile, secondDraft)
	intervalsBefore := fixture.intervalCount(t, fixture.checkout)

	replayed, err := fixture.newPublisher(t, fixture.db).Finalize(context.Background(), caller, ucidomain.IndexFinalizeInput{
		Build:    firstBuild.Build,
		Manifest: firstManifest,
	})
	require.NoError(t, err)
	require.Equal(t, first, replayed, "an exact lost-ACK replay must return the original durable result")
	fixture.assertCurrentProjection(t, fixture.checkout, second, secondMembers, secondEdges, ucidomain.IndexCoverage{
		Structural: ucidomain.IndexCoverageComplete,
		Lexical:    ucidomain.IndexCoverageComplete,
		Vector:     ucidomain.IndexCoverageUnavailable,
	})
	require.Equal(t, intervalsBefore, fixture.intervalCount(t, fixture.checkout), "replay must not add intervals or rewind current")

	changed := firstManifest
	changed.ManifestDigest = ucidomain.IndexDigest(uciPublicationDigest("changed-replay"))
	_, err = fixture.newPublisher(t, fixture.db).Finalize(context.Background(), caller, ucidomain.IndexFinalizeInput{
		Build:    firstBuild.Build,
		Manifest: changed,
	})
	require.Error(t, err, "a changed Finalize binding must not replay another result")
}

func TestUCIDeleteAllRequiresCompleteFullCensus(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	artifact := fixture.admitArtifact(t, fixture.source.SourceID, "delete-all", "func Present() {}\n", UCIParseArtifactComplete)
	members := []ucidomain.IndexMembership{uciPublicationPresentMembership("present.go", artifact)}
	edges := []ucidomain.IndexEdgeReplacement{{SourcePath: "present.go"}}
	initialDraft := newUCIPublicationDraft(
		[]ucidomain.IndexPart{uciPublicationPart([]uciPublicationArtifact{artifact}, members, nil, edges)},
		members,
		edges,
	)
	_, first := fixture.publish(t, fixture.publisher, fixture.caller("delete-owner"), "delete-initial", fixture.checkout, fixture.profile.ProfileID, nil, ucidomain.IndexManifestFull, ucidomain.IndexJobInitial, initialDraft)
	parent := uciPublicationParent(first)

	delta := newUCIPublicationDraft(
		[]ucidomain.IndexPart{{
			Deletions:        []ucidomain.IndexDeletion{{PathKey: "present.go", ConfirmedMissing: true}},
			EdgeReplacements: []ucidomain.IndexEdgeReplacement{{SourcePath: "present.go"}},
		}},
		nil,
		nil,
	)
	deltaBuild := fixture.begin(t, fixture.publisher, fixture.caller("delete-owner"), "delete-delta", fixture.checkout, fixture.profile.ProfileID, parent, ucidomain.IndexManifestDelta, ucidomain.IndexJobReconcile)
	deltaAcks := fixture.stageDraft(t, fixture.publisher, fixture.caller("delete-owner"), deltaBuild.Build, delta.parts)
	_, err := fixture.publisher.Finalize(context.Background(), fixture.caller("delete-owner"), ucidomain.IndexFinalizeInput{
		Build:          deltaBuild.Build,
		ExpectedParent: parent,
		Manifest:       fixture.manifest(t, deltaAcks, delta),
	})
	require.Error(t, err, "a delta that happens to empty the candidate cannot authorize delete-all")
	fixture.expireBuild(t, deltaBuild.Build, fixture.checkout)
	fixture.assertCurrentProjection(t, fixture.checkout, first, members, edges, ucidomain.IndexCoverage{
		Structural: ucidomain.IndexCoverageComplete,
		Lexical:    ucidomain.IndexCoverageComplete,
		Vector:     ucidomain.IndexCoverageUnavailable,
	})

	empty := newUCIPublicationDraft(nil, nil, nil)
	_, deleted := fixture.publish(t, fixture.publisher, fixture.caller("delete-owner"), "delete-full", fixture.checkout, fixture.profile.ProfileID, parent, ucidomain.IndexManifestFull, ucidomain.IndexJobReconcile, empty)
	fixture.assertCurrentProjection(t, fixture.checkout, deleted, nil, nil, ucidomain.IndexCoverage{
		Structural: ucidomain.IndexCoverageComplete,
		Lexical:    ucidomain.IndexCoverageComplete,
		Vector:     ucidomain.IndexCoverageUnavailable,
	})
	fixture.assertProjectionAtGeneration(t, fixture.checkout, first.Context.Generation, members, edges)

	incomplete := newUCIPublicationDraft(nil, nil, nil)
	incomplete.scanOutcome = ucidomain.IndexScanIncomplete
	incomplete.census = false
	badBuild := fixture.begin(t, fixture.publisher, fixture.caller("delete-owner"), "delete-no-census", fixture.checkout, fixture.profile.ProfileID, uciPublicationParent(deleted), ucidomain.IndexManifestFull, ucidomain.IndexJobReconcile)
	_, err = fixture.publisher.Finalize(context.Background(), fixture.caller("delete-owner"), ucidomain.IndexFinalizeInput{
		Build:          badBuild.Build,
		ExpectedParent: uciPublicationParent(deleted),
		Manifest:       fixture.manifest(t, nil, incomplete),
	})
	require.Error(t, err, "zero uploads alone must not authorize a second empty publication")
	fixture.assertCurrentProjection(t, fixture.checkout, deleted, nil, nil, ucidomain.IndexCoverage{
		Structural: ucidomain.IndexCoverageComplete,
		Lexical:    ucidomain.IndexCoverageComplete,
		Vector:     ucidomain.IndexCoverageUnavailable,
	})
}

func TestUCIPublishFailedScanPreservesCurrent(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	old := fixture.admitArtifact(t, fixture.source.SourceID, "scan-old", "func Old() {}\n", UCIParseArtifactComplete)
	oldMembers := []ucidomain.IndexMembership{uciPublicationPresentMembership("main.go", old)}
	oldEdges := []ucidomain.IndexEdgeReplacement{{SourcePath: "main.go"}}
	oldDraft := newUCIPublicationDraft(
		[]ucidomain.IndexPart{uciPublicationPart([]uciPublicationArtifact{old}, oldMembers, nil, oldEdges)},
		oldMembers,
		oldEdges,
	)
	_, published := fixture.publish(t, fixture.publisher, fixture.caller("scan-owner"), "scan-old", fixture.checkout, fixture.profile.ProfileID, nil, ucidomain.IndexManifestFull, ucidomain.IndexJobInitial, oldDraft)
	parent := uciPublicationParent(published)

	for _, outcome := range []ucidomain.IndexScanOutcome{ucidomain.IndexScanIncomplete, ucidomain.IndexScanFailed} {
		t.Run(string(outcome), func(t *testing.T) {
			fresh := fixture.admitArtifact(t, fixture.source.SourceID, "scan-"+string(outcome), "func Fresh() {}\n", UCIParseArtifactComplete)
			members := []ucidomain.IndexMembership{uciPublicationPresentMembership("main.go", fresh)}
			edges := []ucidomain.IndexEdgeReplacement{{SourcePath: "main.go"}}
			draft := newUCIPublicationDraft(
				[]ucidomain.IndexPart{uciPublicationPart([]uciPublicationArtifact{fresh}, members, nil, edges)},
				members,
				edges,
			)
			draft.scanOutcome = outcome
			draft.census = false
			build := fixture.begin(t, fixture.publisher, fixture.caller("scan-owner"), "scan-"+string(outcome), fixture.checkout, fixture.profile.ProfileID, parent, ucidomain.IndexManifestFull, ucidomain.IndexJobReconcile)
			acks := fixture.stageDraft(t, fixture.publisher, fixture.caller("scan-owner"), build.Build, draft.parts)
			_, err := fixture.publisher.Finalize(context.Background(), fixture.caller("scan-owner"), ucidomain.IndexFinalizeInput{
				Build:          build.Build,
				ExpectedParent: parent,
				Manifest:       fixture.manifest(t, acks, draft),
			})
			require.Error(t, err, "failed or incomplete scans must preserve the current projection")
			fixture.expireBuild(t, build.Build, fixture.checkout)
			fixture.assertCurrentProjection(t, fixture.checkout, published, oldMembers, oldEdges, ucidomain.IndexCoverage{
				Structural: ucidomain.IndexCoverageComplete,
				Lexical:    ucidomain.IndexCoverageComplete,
				Vector:     ucidomain.IndexCoverageUnavailable,
			})
		})
	}

	unreadableMembers := []ucidomain.IndexMembership{uciPublicationUnreadableMembership("main.go")}
	unreadableEdges := []ucidomain.IndexEdgeReplacement{{SourcePath: "main.go"}}
	unreadableDraft := newUCIPublicationDraft(
		[]ucidomain.IndexPart{{Memberships: unreadableMembers, EdgeReplacements: unreadableEdges}},
		unreadableMembers,
		unreadableEdges,
	)
	unreadableDraft.coverage = ucidomain.IndexCoverage{
		Structural:      ucidomain.IndexCoveragePartial,
		Lexical:         ucidomain.IndexCoverageUnavailable,
		Vector:          ucidomain.IndexCoverageUnavailable,
		UnreadableFiles: 1,
	}
	_, unreadable := fixture.publish(t, fixture.publisher, fixture.caller("scan-owner"), "scan-unreadable", fixture.checkout, fixture.profile.ProfileID, parent, ucidomain.IndexManifestFull, ucidomain.IndexJobReconcile, unreadableDraft)
	fixture.assertCurrentProjection(t, fixture.checkout, unreadable, unreadableMembers, unreadableEdges, unreadableDraft.coverage)
}

func TestUCIPublishFaultRollsBackEveryVisibleSurface(t *testing.T) {
	faults := []struct {
		name  string
		table string
		time  string
		event string
		when  string
	}{
		{
			name:  "interval-closure",
			table: "ci_memberships",
			time:  "BEFORE",
			event: "UPDATE OF valid_to_generation",
			when:  "WHEN (OLD.valid_to_generation IS NULL AND NEW.valid_to_generation IS NOT NULL)",
		},
		{
			name:  "view-insert",
			table: "ci_views",
			time:  "AFTER",
			event: "INSERT",
		},
		{
			name:  "pointer-switch",
			table: "ci_checkouts",
			time:  "AFTER",
			event: "UPDATE OF current_view_id",
			when:  "WHEN (NEW.current_view_id IS DISTINCT FROM OLD.current_view_id)",
		},
	}

	for _, fault := range faults {
		t.Run(fault.name, func(t *testing.T) {
			fixture := openUCIPublicationFixture(t)
			old := fixture.admitArtifact(t, fixture.source.SourceID, "fault-old-"+fault.name, "func Old() {}\n", UCIParseArtifactComplete)
			oldMembers := []ucidomain.IndexMembership{uciPublicationPresentMembership("main.go", old)}
			oldEdges := []ucidomain.IndexEdgeReplacement{{SourcePath: "main.go"}}
			oldDraft := newUCIPublicationDraft(
				[]ucidomain.IndexPart{uciPublicationPart([]uciPublicationArtifact{old}, oldMembers, nil, oldEdges)},
				oldMembers,
				oldEdges,
			)
			_, oldPublished := fixture.publish(t, fixture.publisher, fixture.caller("fault-owner"), "fault-old-"+fault.name, fixture.checkout, fixture.profile.ProfileID, nil, ucidomain.IndexManifestFull, ucidomain.IndexJobInitial, oldDraft)

			fresh := fixture.admitArtifact(t, fixture.source.SourceID, "fault-new-"+fault.name, "func Fresh() {}\n", UCIParseArtifactComplete)
			freshMembers := []ucidomain.IndexMembership{uciPublicationPresentMembership("main.go", fresh)}
			freshEdges := []ucidomain.IndexEdgeReplacement{{SourcePath: "main.go"}}
			freshDraft := newUCIPublicationDraft(
				[]ucidomain.IndexPart{uciPublicationPart([]uciPublicationArtifact{fresh}, freshMembers, nil, freshEdges)},
				freshMembers,
				freshEdges,
			)
			parent := uciPublicationParent(oldPublished)
			build := fixture.begin(t, fixture.publisher, fixture.caller("fault-owner"), "fault-new-"+fault.name, fixture.checkout, fixture.profile.ProfileID, parent, ucidomain.IndexManifestFull, ucidomain.IndexJobReconcile)
			acks := fixture.stageDraft(t, fixture.publisher, fixture.caller("fault-owner"), build.Build, freshDraft.parts)
			beforeIntervals := fixture.intervalCount(t, fixture.checkout)
			removeFault := installUCIPublicationFault(t, fixture.db, fault.table, fault.time, fault.event, fault.when)
			_, err := fixture.publisher.Finalize(context.Background(), fixture.caller("fault-owner"), ucidomain.IndexFinalizeInput{
				Build:          build.Build,
				ExpectedParent: parent,
				Manifest:       fixture.manifest(t, acks, freshDraft),
			})
			require.Error(t, err, "the injected PostgreSQL error must abort publication")
			removeFault()

			fixture.assertCurrentProjection(t, fixture.checkout, oldPublished, oldMembers, oldEdges, ucidomain.IndexCoverage{
				Structural: ucidomain.IndexCoverageComplete,
				Lexical:    ucidomain.IndexCoverageComplete,
				Vector:     ucidomain.IndexCoverageUnavailable,
			})
			require.Equal(t, beforeIntervals, fixture.intervalCount(t, fixture.checkout), "a failed transaction cannot leave interval residue")
			fixture.assertNoPublicationJobResult(t, build.Build.BuildID)
			fixture.assertViewCount(t, fixture.checkout, 1)

			retried := fixture.finalizeDraft(t, fixture.publisher, fixture.caller("fault-owner"), build.Build, parent, acks, freshDraft)
			fixture.assertCurrentProjection(t, fixture.checkout, retried, freshMembers, freshEdges, ucidomain.IndexCoverage{
				Structural: ucidomain.IndexCoverageComplete,
				Lexical:    ucidomain.IndexCoverageComplete,
				Vector:     ucidomain.IndexCoverageUnavailable,
			})
		})
	}
}

func TestUCIPublishRejectsIncompleteOrMutableArtifact(t *testing.T) {
	t.Run("forged-content-and-missing-facts", func(t *testing.T) {
		fixture := openUCIPublicationFixture(t)
		artifact := fixture.admitArtifact(t, fixture.source.SourceID, "artifact-forged", "func Artifact() {}\n", UCIParseArtifactComplete)
		memberships := []ucidomain.IndexMembership{uciPublicationPresentMembership("main.go", artifact)}
		edges := []ucidomain.IndexEdgeReplacement{{SourcePath: "main.go"}}
		part := uciPublicationPart([]uciPublicationArtifact{artifact}, memberships, nil, edges)
		part.Artifacts[0].ContentDigest = ucidomain.IndexDigest(uciPublicationDigest("forged-content"))
		build := fixture.begin(t, fixture.publisher, fixture.caller("artifact-owner"), "artifact-forged", fixture.checkout, fixture.profile.ProfileID, nil, ucidomain.IndexManifestFull, ucidomain.IndexJobInitial)
		_, err := fixture.stagePart(fixture.publisher, fixture.caller("artifact-owner"), build.Build, 0, part)
		require.Error(t, err, "the publisher must recompute the admitted blob hash")
		fixture.assertBuildPartCount(t, build.Build.BuildID, 0)
		fixture.expireBuild(t, build.Build, fixture.checkout)

		completeProof := artifact.Proof
		require.NoError(t, fixture.db.Where("chunk_id = ?", artifact.Chunk.ChunkID).Delete(&UCIChunk{}).Error)
		missingPart := uciPublicationPart([]uciPublicationArtifact{artifact}, memberships, nil, edges)
		missingPart.Artifacts[0] = completeProof
		missingBuild := fixture.begin(t, fixture.publisher, fixture.caller("artifact-owner"), "artifact-missing", fixture.checkout, fixture.profile.ProfileID, nil, ucidomain.IndexManifestFull, ucidomain.IndexJobInitial)
		fixture.requireStageOrFinalizeError(t, fixture.publisher, fixture.caller("artifact-owner"), missingBuild.Build, nil, newUCIPublicationDraft([]ucidomain.IndexPart{missingPart}, memberships, edges))
	})

	t.Run("span-and-profile", func(t *testing.T) {
		fixture := openUCIPublicationFixture(t)
		artifact := fixture.admitArtifact(t, fixture.source.SourceID, "artifact-span", "func Span() {}\n", UCIParseArtifactComplete)
		memberships := []ucidomain.IndexMembership{uciPublicationPresentMembership("main.go", artifact)}
		edges := []ucidomain.IndexEdgeReplacement{{
			SourcePath: "main.go",
			Edges: []ucidomain.IndexEdge{{
				EdgeKey:          "bad-span",
				SourceArtifactID: artifact.Artifact.ArtifactID,
				Relation:         ucidomain.IndexRelation("calls"),
				EvidenceKind:     ucidomain.IndexEvidenceKind("resolved"),
				ResolutionState:  ucidomain.IndexResolutionState("resolved"),
				ResolverRevision: "fixture",
				Evidence:         ucidomain.IndexEdgeEvidence{Span: ucidomain.IndexSpan{ByteStart: 0, ByteEnd: int64(len(artifact.Body)) + 1, LineStart: 1, LineEnd: 1}, RuleKey: "fixture", Explanation: "bad span"},
			}},
		}}
		spanDraft := newUCIPublicationDraft(
			[]ucidomain.IndexPart{uciPublicationPart([]uciPublicationArtifact{artifact}, memberships, nil, edges)},
			memberships,
			edges,
		)
		spanBuild := fixture.begin(t, fixture.publisher, fixture.caller("artifact-owner"), "artifact-span", fixture.checkout, fixture.profile.ProfileID, nil, ucidomain.IndexManifestFull, ucidomain.IndexJobInitial)
		fixture.requireStageOrFinalizeError(t, fixture.publisher, fixture.caller("artifact-owner"), spanBuild.Build, nil, spanDraft)
		fixture.expireBuild(t, spanBuild.Build, fixture.checkout)

		otherProfile := fixture.createProfile(t, "artifact-profile-mismatch")
		profileDraft := newUCIPublicationDraft(
			[]ucidomain.IndexPart{uciPublicationPart([]uciPublicationArtifact{artifact}, memberships, nil, []ucidomain.IndexEdgeReplacement{{SourcePath: "main.go"}})},
			memberships,
			[]ucidomain.IndexEdgeReplacement{{SourcePath: "main.go"}},
		)
		profileBuild := fixture.begin(t, fixture.publisher, fixture.caller("artifact-owner"), "artifact-profile", fixture.checkout, otherProfile.ProfileID, nil, ucidomain.IndexManifestFull, ucidomain.IndexJobInitial)
		fixture.requireStageOrFinalizeError(t, fixture.publisher, fixture.caller("artifact-owner"), profileBuild.Build, nil, profileDraft)
	})

	t.Run("sealed-artifact-is-immutable", func(t *testing.T) {
		fixture := openUCIPublicationFixture(t)
		artifact := fixture.admitArtifact(t, fixture.source.SourceID, "artifact-sealed", "func Sealed() {}\n", UCIParseArtifactComplete)
		memberships := []ucidomain.IndexMembership{uciPublicationPresentMembership("main.go", artifact)}
		edges := []ucidomain.IndexEdgeReplacement{{SourcePath: "main.go"}}
		draft := newUCIPublicationDraft(
			[]ucidomain.IndexPart{uciPublicationPart([]uciPublicationArtifact{artifact}, memberships, nil, edges)},
			memberships,
			edges,
		)
		fixture.publish(t, fixture.publisher, fixture.caller("artifact-owner"), "artifact-sealed", fixture.checkout, fixture.profile.ProfileID, nil, ucidomain.IndexManifestFull, ucidomain.IndexJobInitial, draft)

		_, err := fixture.projection.UpsertBlob(context.Background(), UpsertUCIBlobInput{
			SourceID:         fixture.source.SourceID,
			ProtectionDomain: "source-private",
			ContentDigest:    artifact.Blob.ContentDigest,
			ByteLength:       int64(len("forged bytes")),
			SafeContent:      []byte("forged bytes"),
			Encoding:         "utf-8",
			StorageState:     UCIBlobStored,
		})
		require.Error(t, err, "a cache retry with matching declared digest but different bytes must fail")
		_, err = fixture.projection.UpsertChunk(context.Background(), UpsertUCIChunkInput{
			SourceID:      fixture.source.SourceID,
			ArtifactID:    artifact.Artifact.ArtifactID,
			ChunkKind:     "definition",
			Ordinal:       1,
			ByteStart:     0,
			ByteEnd:       1,
			ContentDigest: uciPublicationDigestBytes(artifact.Body[:1]),
			TextForSearch: string(artifact.Body[:1]),
		})
		require.Error(t, err, "a sealed artifact cannot gain a later fact")
		require.Error(t, fixture.db.Model(&UCIParseArtifact{}).
			Where("artifact_id = ?", artifact.Artifact.ArtifactID).
			Update("diagnostics", `{"changed":true}`).Error, "sealed artifact metadata must remain immutable")
		require.Error(t, fixture.db.Model(&UCIChunk{}).
			Where("chunk_id = ?", artifact.Chunk.ChunkID).
			Update("text_for_search", "changed").Error, "sealed artifact facts must remain immutable")
	})
}

func TestUCIPublishRequiresProvenArtifactsAndWorkingTree(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	artifact := fixture.admitArtifact(t, fixture.source.SourceID, "proof-required", "func Proven() {}\n", UCIParseArtifactComplete)
	memberships := []ucidomain.IndexMembership{uciPublicationPresentMembership("main.go", artifact)}
	replacements := []ucidomain.IndexEdgeReplacement{{SourcePath: "main.go"}}

	unprovenPart := uciPublicationPart(nil, memberships, nil, replacements)
	unprovenDraft := newUCIPublicationDraft([]ucidomain.IndexPart{unprovenPart}, memberships, replacements)
	caller := fixture.caller("proof-required")
	unprovenBuild := fixture.begin(t, fixture.publisher, caller, "proof-required", fixture.checkout, fixture.profile.ProfileID, nil, ucidomain.IndexManifestFull, ucidomain.IndexJobInitial)
	unprovenAck := fixture.stage(t, fixture.publisher, caller, unprovenBuild.Build, 0, unprovenPart)
	_, err := fixture.publisher.Finalize(context.Background(), caller, ucidomain.IndexFinalizeInput{
		Build:    unprovenBuild.Build,
		Manifest: fixture.manifest(t, []ucidomain.IndexPartAck{unprovenAck}, unprovenDraft),
	})
	require.Error(t, err, "an unsealed membership artifact requires an admitted proof")
	fixture.assertNoCurrentView(t, fixture.checkout)
	fixture.expireBuild(t, unprovenBuild.Build, fixture.checkout)

	target := fixture.admitArtifact(t, fixture.source.SourceID, "symbol-target", "func Target() {}\n", UCIParseArtifactComplete)
	symbolMembers := []ucidomain.IndexMembership{
		uciPublicationPresentMembership("main.go", artifact),
		uciPublicationPresentMembership("target.go", target),
	}
	badEdge := uciPublicationResolvedEdge(artifact, "main.go", target, "target.go")
	missingSymbol := "missing-symbol"
	badEdge.Target.SymbolKey = &missingSymbol
	symbolReplacements := []ucidomain.IndexEdgeReplacement{
		{SourcePath: "main.go", Edges: []ucidomain.IndexEdge{badEdge}},
		{SourcePath: "target.go"},
	}
	badPart := uciPublicationPart([]uciPublicationArtifact{artifact, target}, symbolMembers, nil, symbolReplacements)
	symbolBuild := fixture.begin(t, fixture.publisher, caller, "symbol-required", fixture.checkout, fixture.profile.ProfileID, nil, ucidomain.IndexManifestFull, ucidomain.IndexJobInitial)
	_, err = fixture.stagePart(fixture.publisher, caller, symbolBuild.Build, 0, badPart)
	require.Error(t, err, "graph symbol endpoints must name admitted artifact facts")
	fixture.assertBuildPartCount(t, symbolBuild.Build.BuildID, 0)
	fixture.expireBuild(t, symbolBuild.Build, fixture.checkout)

	require.NoError(t, fixture.db.Model(&UCICheckout{}).Where("checkout_id = ?", fixture.checkout.CheckoutID).
		Update("kind", UCICheckoutCommitReader).Error)
	var before UCICheckout
	require.NoError(t, fixture.db.Where("checkout_id = ?", fixture.checkout.CheckoutID).First(&before).Error)
	beforeEpoch := before.LeaseEpoch
	_, err = fixture.publisher.Begin(context.Background(), caller, fixture.beginInput("commit-reader", fixture.checkout, fixture.profile.ProfileID, nil, ucidomain.IndexManifestFull, ucidomain.IndexJobInitial))
	require.Error(t, err, "the first release must not publish through read-only commit_reader checkouts")
	var after UCICheckout
	require.NoError(t, fixture.db.Where("checkout_id = ?", fixture.checkout.CheckoutID).First(&after).Error)
	require.Equal(t, beforeEpoch, after.LeaseEpoch)
	fixture.assertNoCurrentView(t, fixture.checkout)
}

func TestUCIPublishSealingRevalidatesAfterConcurrentFactWrite(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	artifact := fixture.admitArtifact(t, fixture.source.SourceID, "concurrent-seal", "func Concurrent() {}\n", UCIParseArtifactComplete)
	memberships := []ucidomain.IndexMembership{uciPublicationPresentMembership("main.go", artifact)}
	replacements := []ucidomain.IndexEdgeReplacement{{SourcePath: "main.go"}}
	part := uciPublicationPart([]uciPublicationArtifact{artifact}, memberships, nil, replacements)
	draft := newUCIPublicationDraft([]ucidomain.IndexPart{part}, memberships, replacements)
	caller := fixture.caller("concurrent-seal")
	build := fixture.begin(t, fixture.publisher, caller, "concurrent-seal", fixture.checkout, fixture.profile.ProfileID, nil, ucidomain.IndexManifestFull, ucidomain.IndexJobInitial)
	ack := fixture.stage(t, fixture.publisher, caller, build.Build, 0, part)

	lockTx := fixture.db.Begin()
	require.NoError(t, lockTx.Error)
	t.Cleanup(func() { _ = lockTx.Rollback().Error })
	concurrentDefinition := UCIDefinition{
		DefinitionID:       uuid.NewString(),
		ArtifactID:         artifact.Artifact.ArtifactID,
		LocalSymbolKey:     "ConcurrentLateFact",
		Kind:               "function",
		Name:               "ConcurrentLateFact",
		QualifiedLocalName: "fixture.ConcurrentLateFact",
		Signature:          "func ConcurrentLateFact()",
		ByteStart:          0,
		ByteEnd:            1,
		LineStart:          1,
		LineEnd:            1,
		CreatedAt:          time.Now().UTC(),
	}
	require.NoError(t, lockTx.Create(&concurrentDefinition).Error)

	peerDB, peerApplicationName := fixture.openPeerDB(t)
	peerPublisher := fixture.newPublisher(t, peerDB)
	type finalizeResult struct {
		published ucidomain.IndexPublishedView
		err       error
	}
	finished := make(chan finalizeResult, 1)
	go func() {
		published, err := peerPublisher.Finalize(context.Background(), caller, ucidomain.IndexFinalizeInput{
			Build:    build.Build,
			Manifest: fixture.manifest(t, []ucidomain.IndexPartAck{ack}, draft),
		})
		finished <- finalizeResult{published: published, err: err}
	}()
	fixture.waitForPeerLock(t, peerApplicationName)
	require.NoError(t, lockTx.Commit().Error)
	result := <-finished
	require.Error(t, result.err, "Finalize must revalidate facts after acquiring the artifact seal lock")
	require.Empty(t, result.published.Context.ViewID)
	fixture.assertNoCurrentView(t, fixture.checkout)
}

func TestUCIPublishScopedEndpointsAndChangedCallee(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	foreignArtifact := fixture.admitArtifact(t, fixture.foreign.SourceID, "foreign", "func Foreign() {}\n", UCIParseArtifactComplete)
	foreignMembers := []ucidomain.IndexMembership{uciPublicationPresentMembership("foreign.go", foreignArtifact)}
	foreignEdges := []ucidomain.IndexEdgeReplacement{{SourcePath: "foreign.go"}}
	foreignBuild := fixture.begin(t, fixture.publisher, fixture.caller("scope-owner"), "foreign-artifact", fixture.checkout, fixture.profile.ProfileID, nil, ucidomain.IndexManifestFull, ucidomain.IndexJobInitial)
	fixture.requireStageOrFinalizeError(t, fixture.publisher, fixture.caller("scope-owner"), foreignBuild.Build, nil, newUCIPublicationDraft(
		[]ucidomain.IndexPart{uciPublicationPart([]uciPublicationArtifact{foreignArtifact}, foreignMembers, nil, foreignEdges)},
		foreignMembers,
		foreignEdges,
	))
	fixture.expireBuild(t, foreignBuild.Build, fixture.checkout)
	fixture.assertNoCurrentView(t, fixture.checkout)

	siblingArtifact := fixture.admitArtifact(t, fixture.source.SourceID, "sibling-target", "func SiblingTarget() {}\n", UCIParseArtifactComplete)
	siblingMembers := []ucidomain.IndexMembership{uciPublicationPresentMembership("sibling.go", siblingArtifact)}
	siblingEdges := []ucidomain.IndexEdgeReplacement{{SourcePath: "sibling.go"}}
	_, siblingPublished := fixture.publish(t, fixture.publisher, fixture.caller("sibling-owner"), "scope-sibling", fixture.sibling, fixture.profile.ProfileID, nil, ucidomain.IndexManifestFull, ucidomain.IndexJobInitial, newUCIPublicationDraft(
		[]ucidomain.IndexPart{uciPublicationPart([]uciPublicationArtifact{siblingArtifact}, siblingMembers, nil, siblingEdges)},
		siblingMembers,
		siblingEdges,
	))
	fixture.assertCurrentProjection(t, fixture.sibling, siblingPublished, siblingMembers, siblingEdges, ucidomain.IndexCoverage{
		Structural: ucidomain.IndexCoverageComplete,
		Lexical:    ucidomain.IndexCoverageComplete,
		Vector:     ucidomain.IndexCoverageUnavailable,
	})

	callerArtifact := fixture.admitArtifact(t, fixture.source.SourceID, "nonmember-caller", "func Caller() {}\n", UCIParseArtifactComplete)
	nonmemberMembers := []ucidomain.IndexMembership{uciPublicationPresentMembership("caller.go", callerArtifact)}
	nonmemberEdges := []ucidomain.IndexEdgeReplacement{{
		SourcePath: "caller.go",
		Edges:      []ucidomain.IndexEdge{uciPublicationResolvedEdge(callerArtifact, "caller.go", siblingArtifact, "sibling.go")},
	}}
	nonmemberBuild := fixture.begin(t, fixture.publisher, fixture.caller("scope-owner"), "same-source-nonmember", fixture.checkout, fixture.profile.ProfileID, nil, ucidomain.IndexManifestFull, ucidomain.IndexJobInitial)
	fixture.requireStageOrFinalizeError(t, fixture.publisher, fixture.caller("scope-owner"), nonmemberBuild.Build, nil, newUCIPublicationDraft(
		[]ucidomain.IndexPart{uciPublicationPart([]uciPublicationArtifact{callerArtifact}, nonmemberMembers, nil, nonmemberEdges)},
		nonmemberMembers,
		nonmemberEdges,
	))
	fixture.expireBuild(t, nonmemberBuild.Build, fixture.checkout)
	fixture.assertNoCurrentView(t, fixture.checkout)

	callerV1 := fixture.admitArtifact(t, fixture.source.SourceID, "callee-caller-v1", "func CallerV1() {}\n", UCIParseArtifactComplete)
	calleeV1 := fixture.admitArtifact(t, fixture.source.SourceID, "callee-v1", "func CalleeV1() {}\n", UCIParseArtifactComplete)
	membersV1 := []ucidomain.IndexMembership{
		uciPublicationPresentMembership("caller.go", callerV1),
		uciPublicationPresentMembership("callee.go", calleeV1),
	}
	edgesV1 := []ucidomain.IndexEdgeReplacement{
		{SourcePath: "caller.go", Edges: []ucidomain.IndexEdge{uciPublicationResolvedEdge(callerV1, "caller.go", calleeV1, "callee.go")}},
		{SourcePath: "callee.go"},
	}
	_, first := fixture.publish(t, fixture.publisher, fixture.caller("scope-owner"), "callee-v1", fixture.checkout, fixture.profile.ProfileID, nil, ucidomain.IndexManifestFull, ucidomain.IndexJobInitial, newUCIPublicationDraft(
		[]ucidomain.IndexPart{uciPublicationPart([]uciPublicationArtifact{callerV1, calleeV1}, membersV1, nil, edgesV1)},
		membersV1,
		edgesV1,
	))
	parent := uciPublicationParent(first)
	deletedMembers := []ucidomain.IndexMembership{uciPublicationPresentMembership("caller.go", callerV1)}
	missingCallerReplacement := uciPublicationPart(nil, nil, []ucidomain.IndexDeletion{{PathKey: "callee.go", ConfirmedMissing: true}}, []ucidomain.IndexEdgeReplacement{{SourcePath: "callee.go"}})
	deltaBuild := fixture.begin(t, fixture.publisher, fixture.caller("scope-owner"), "callee-delta", fixture.checkout, fixture.profile.ProfileID, parent, ucidomain.IndexManifestDelta, ucidomain.IndexJobReconcile)
	ack0 := fixture.stage(t, fixture.publisher, fixture.caller("scope-owner"), deltaBuild.Build, 0, missingCallerReplacement)
	deltaDraft := newUCIPublicationDraft([]ucidomain.IndexPart{missingCallerReplacement}, deletedMembers, nil)
	_, err := fixture.publisher.Finalize(context.Background(), fixture.caller("scope-owner"), ucidomain.IndexFinalizeInput{
		Build:          deltaBuild.Build,
		ExpectedParent: parent,
		Manifest:       fixture.manifest(t, []ucidomain.IndexPartAck{ack0}, deltaDraft),
	})
	require.Error(t, err, "deleting a callee requires an explicit replacement for unchanged callers")
	fixture.assertCurrentProjection(t, fixture.checkout, first, membersV1, edgesV1, ucidomain.IndexCoverage{
		Structural: ucidomain.IndexCoverageComplete,
		Lexical:    ucidomain.IndexCoverageComplete,
		Vector:     ucidomain.IndexCoverageUnavailable,
	})

	callerReplacement := uciPublicationPart(nil, nil, nil, []ucidomain.IndexEdgeReplacement{{SourcePath: "caller.go"}})
	ack1 := fixture.stage(t, fixture.publisher, fixture.caller("scope-owner"), deltaBuild.Build, 1, callerReplacement)
	deltaDraft.parts = append(deltaDraft.parts, callerReplacement)
	deltaDraft.replacements = []ucidomain.IndexEdgeReplacement{{SourcePath: "caller.go"}, {SourcePath: "callee.go"}}
	deleted := fixture.finalizeDraft(t, fixture.publisher, fixture.caller("scope-owner"), deltaBuild.Build, parent, []ucidomain.IndexPartAck{ack0, ack1}, deltaDraft)
	fixture.assertCurrentProjection(t, fixture.checkout, deleted, deletedMembers, nil, ucidomain.IndexCoverage{
		Structural: ucidomain.IndexCoverageComplete,
		Lexical:    ucidomain.IndexCoverageComplete,
		Vector:     ucidomain.IndexCoverageUnavailable,
	})

	partial := fixture.admitArtifact(t, fixture.source.SourceID, "partial-caller", "func CallerPartial() {}\n", UCIParseArtifactPartial)
	partialMembers := []ucidomain.IndexMembership{uciPublicationPresentMembership("caller.go", partial)}
	partialEdges := []ucidomain.IndexEdgeReplacement{{SourcePath: "caller.go"}}
	partialDraft := newUCIPublicationDraft(
		[]ucidomain.IndexPart{uciPublicationPart([]uciPublicationArtifact{partial}, partialMembers, nil, partialEdges)},
		partialMembers,
		partialEdges,
	)
	partialDraft.coverage = ucidomain.IndexCoverage{
		Structural: ucidomain.IndexCoveragePartial,
		Lexical:    ucidomain.IndexCoveragePartial,
		Vector:     ucidomain.IndexCoverageUnavailable,
	}
	_, partialPublished := fixture.publish(t, fixture.publisher, fixture.caller("scope-owner"), "partial-caller", fixture.checkout, fixture.profile.ProfileID, uciPublicationParent(deleted), ucidomain.IndexManifestFull, ucidomain.IndexJobReconcile, partialDraft)
	fixture.assertCurrentProjection(t, fixture.checkout, partialPublished, partialMembers, partialEdges, partialDraft.coverage)
}

func openUCIPublicationFixture(t *testing.T) *uciPublicationFixture {
	t.Helper()

	db, schema := openInterventionReceiptMigrationTestDB(t)
	assertUCIProjectionMigration171Prerequisites(t, db)
	require.Equal(t, int64(1), uciProjectionMigrationAppliedCount(t, db, uciProjectionMigrationID), "migration 172 must be applied before publication tests")
	for _, tableName := range uciProjectionMigrationTableNames {
		var count int64
		require.NoError(t, db.Raw(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = ?`, tableName).Scan(&count).Error)
		require.Equalf(t, int64(1), count, "migration 172 prerequisite table %q must remain available", tableName)
	}

	token := strings.ReplaceAll(uuid.NewString(), "-", "")
	fixture := &uciPublicationFixture{
		db:         db,
		schema:     schema,
		context:    NewUCIContextStore(db),
		projection: NewUCIProjectionStore(db),
		token:      token,
		realm:      "uci-publication-realm-" + token,
		principal:  "principal-" + token,
		limits: ucidomain.IndexPublicationLimits{
			LeaseTTL:           5 * time.Minute,
			MaxPartBytes:       1 << 20,
			MaxParts:           128,
			MaxBuildBytes:      8 << 20,
			MaxManifestEntries: 1 << 12,
			MaxEdges:           1 << 12,
			MaxArtifactBytes:   1 << 20,
		},
	}
	ctx := context.Background()
	var err error
	fixture.source, err = fixture.context.CreateSource(ctx, CreateSourceInput{
		AuthRealm:   fixture.realm,
		Kind:        UCISourceGit,
		DisplayName: "source-" + token,
	})
	require.NoError(t, err)
	fixture.foreign, err = fixture.context.CreateSource(ctx, CreateSourceInput{
		AuthRealm:   fixture.realm,
		Kind:        UCISourceGit,
		DisplayName: "foreign-source-" + token,
	})
	require.NoError(t, err)
	fixture.authorizer = &uciPublicationAuthorizer{
		realm:     fixture.realm,
		principal: fixture.principal,
		allowedSources: map[string]bool{
			fixture.source.SourceID: true,
		},
	}
	fixture.checkout = fixture.registerCheckout(t, fixture.source, "primary")
	fixture.sibling = fixture.registerCheckout(t, fixture.source, "sibling")
	fixture.profile = fixture.createProfile(t, "primary")
	require.Nil(t, fixture.checkout.CurrentViewID, "a fresh working-tree checkout must have no fabricated View")
	require.NotEmpty(t, fixture.profile.ProfileID, "a fresh publication profile is required")

	fixture.seed = fixture.insertArtifact(t, fixture.source.SourceID, "fixture-seed", "func FixtureSeed() {}\n", UCIParseArtifactComplete)
	require.Equal(t, uciPublicationDigestBytes(fixture.seed.Body), fixture.seed.Blob.ContentDigest, "fixture blobs use the SHA-256 of actual bytes")
	require.Equal(t, int64(len(fixture.seed.Body)), fixture.seed.Blob.ByteLength)

	assertUCIPublicationMigration173(t, db)
	fixture.seed.Proof = fixture.describeArtifact(t, fixture.source.SourceID, fixture.seed)
	fixture.publisher = fixture.newPublisher(t, db)
	return fixture
}

func assertUCIPublicationMigration173(t *testing.T, db *gormlib.DB) {
	t.Helper()

	require.Equal(t, int64(1), uciProjectionMigrationAppliedCount(t, db, uciProjectionMigrationID), "migration 172 remains a forward prerequisite")
	require.Equal(t, int64(1), uciProjectionMigrationAppliedCount(t, db, uciPublicationMigrationID), "migration 173 must be registered exactly once as the additive publication amendment")
	assertUCIProjectionRequiredColumns(t, db, "ci_index_build_parts", "build_id", "sequence", "part_digest", "payload", "payload_bytes", "created_at")
	assertUCIProjectionRequiredColumns(t, db, "ci_jobs", "publication_key", "requested_by", "incarnation_id", "profile_id", "expected_parent_view_id", "manifest_mode", "sealed_manifest", "finalize_binding_digest", "result_view_id")
	assertUCIProjectionRequiredColumns(t, db, "ci_parse_artifacts", "facts_digest", "sealed_at")
	assertUCIProjectionIndex(t, db, "ci_index_build_parts", "build_id", "sequence")
	assertUCIProjectionIndex(t, db, "ci_jobs", "unique", "publication_key")
	assertUCIProjectionIndex(t, db, "ci_resolved_edges", "unique", "checkout_id", "edge_key", "where", "valid_to_generation is null")
}

func (fixture *uciPublicationFixture) registerCheckout(t *testing.T, source *UCISource, label string) *UCICheckout {
	t.Helper()

	checkout, err := fixture.context.RegisterCheckout(context.Background(), RegisterCheckoutInput{
		SourceID:       source.SourceID,
		WorkstationID:  "workstation-" + label + "-" + fixture.token,
		Kind:           UCICheckoutWorkingTree,
		OwnerPrincipal: fixture.principal,
		LocatorRef:     "file:///uci-publication/" + label + "/" + fixture.token,
	})
	require.NoError(t, err)
	return checkout
}

func (fixture *uciPublicationFixture) createProfile(t *testing.T, label string) *UCIAnalysisProfile {
	t.Helper()

	profile, err := fixture.context.CreateProfile(context.Background(), CreateProfileInput{
		ParserBundleDigest:   uciPublicationDigest("parser-bundle-" + label + "-" + fixture.token),
		ResolverRevision:     "resolver-" + label + "-" + fixture.token,
		ChunkerRevision:      "chunker-" + label + "-" + fixture.token,
		IgnorePolicyDigest:   uciPublicationDigest("ignore-policy-" + label + "-" + fixture.token),
		BuildContextJSON:     `{"fixture":"` + label + "-" + fixture.token + `"}`,
		SecretPolicyRevision: "secret-policy-" + label + "-" + fixture.token,
	})
	require.NoError(t, err)
	return profile
}

func (fixture *uciPublicationFixture) insertArtifact(t *testing.T, sourceID, label, source string, status UCIParseArtifactStatus) uciPublicationArtifact {
	t.Helper()

	body := []byte("package fixture\n" + source)
	blob, err := fixture.projection.UpsertBlob(context.Background(), UpsertUCIBlobInput{
		SourceID:         sourceID,
		ProtectionDomain: "source-private",
		ContentDigest:    uciPublicationDigestBytes(body),
		ByteLength:       int64(len(body)),
		SafeContent:      append([]byte(nil), body...),
		Encoding:         "utf-8",
		StorageState:     UCIBlobStored,
	})
	require.NoError(t, err)
	artifact, err := fixture.projection.UpsertParseArtifact(context.Background(), UpsertUCIParseArtifactInput{
		SourceID:                sourceID,
		BlobID:                  blob.BlobID,
		Language:                "go",
		ParserRevision:          "parser-" + label,
		GrammarDigest:           uciPublicationDigest("grammar-" + label),
		ExtractionProfileDigest: fixture.profile.ParserBundleDigest,
		Status:                  status,
		Diagnostics:             `{}`,
	})
	require.NoError(t, err)

	definitionStart := int64(strings.Index(string(body), "func "))
	require.GreaterOrEqual(t, definitionStart, int64(0))
	definitionEnd := int64(len(body) - 1)
	symbol := "Symbol" + strings.ReplaceAll(label, "-", "")
	definition, err := fixture.projection.UpsertDefinition(context.Background(), UpsertUCIDefinitionInput{
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
	reference, err := fixture.projection.UpsertReferenceSite(context.Background(), UpsertUCIReferenceSiteInput{
		ArtifactID:     artifact.ArtifactID,
		SiteKey:        "site-" + label,
		OwnerSymbolKey: &definition.LocalSymbolKey,
		RawTarget:      "fixture.Target",
		Relation:       "calls",
		SyntaxSpan:     spanJSON,
		ResolverHints:  `{}`,
	})
	require.NoError(t, err)
	chunk, err := fixture.projection.UpsertChunk(context.Background(), UpsertUCIChunkInput{
		SourceID:      sourceID,
		ArtifactID:    artifact.ArtifactID,
		SymbolKey:     &definition.LocalSymbolKey,
		ChunkKind:     "definition",
		Ordinal:       0,
		ByteStart:     0,
		ByteEnd:       int64(len(body)),
		ContentDigest: uciPublicationDigestBytes(body),
		TextForSearch: string(body),
	})
	require.NoError(t, err)
	return uciPublicationArtifact{
		Body:       body,
		Blob:       blob,
		Artifact:   artifact,
		Definition: definition,
		Reference:  reference,
		Chunk:      chunk,
	}
}

func (fixture *uciPublicationFixture) admitArtifact(t *testing.T, sourceID, label, source string, status UCIParseArtifactStatus) uciPublicationArtifact {
	t.Helper()

	artifact := fixture.insertArtifact(t, sourceID, label, source, status)
	artifact.Proof = fixture.describeArtifact(t, sourceID, artifact)
	return artifact
}

func (fixture *uciPublicationFixture) describeArtifact(t *testing.T, sourceID string, artifact uciPublicationArtifact) ucidomain.IndexArtifactProof {
	t.Helper()

	proof, err := fixture.projection.DescribeIndexArtifact(context.Background(), sourceID, artifact.Artifact.ArtifactID)
	require.NoError(t, err)
	require.Equal(t, artifact.Artifact.ArtifactID, proof.ArtifactID)
	require.Equal(t, ucidomain.IndexDigest(artifact.Blob.ContentDigest), proof.ContentDigest)
	require.Equal(t, uint64(1), proof.DefinitionCount)
	require.Equal(t, uint64(1), proof.ReferenceSiteCount)
	require.Equal(t, uint64(1), proof.ChunkCount)
	return proof
}

func (fixture *uciPublicationFixture) newPublisher(t *testing.T, db *gormlib.DB) ucidomain.IndexStore {
	t.Helper()

	publisher, err := NewUCIProjectionStore(db).Publisher(fixture.authorizer, fixture.limits)
	require.NoError(t, err)
	return publisher
}

func (fixture *uciPublicationFixture) caller(owner string) ucidomain.IndexCaller {
	return ucidomain.IndexCaller{
		AuthRealm:     fixture.realm,
		Principal:     fixture.principal,
		OwnerInstance: owner + "-" + fixture.token,
	}
}

func (fixture *uciPublicationFixture) beginInput(key string, checkout *UCICheckout, profileID string, parent *ucidomain.ContextRef, mode ucidomain.IndexManifestMode, kind ucidomain.IndexJobKind) ucidomain.IndexBeginInput {
	return ucidomain.IndexBeginInput{
		BuildKey: key,
		Scope: ucidomain.IndexScope{
			SourceID:      checkout.SourceID,
			CheckoutID:    checkout.CheckoutID,
			IncarnationID: checkout.IncarnationID,
		},
		ProfileID:      profileID,
		ExpectedParent: parent,
		Mode:           mode,
		JobKind:        kind,
	}
}

func (fixture *uciPublicationFixture) begin(t *testing.T, publisher ucidomain.IndexStore, caller ucidomain.IndexCaller, key string, checkout *UCICheckout, profileID string, parent *ucidomain.ContextRef, mode ucidomain.IndexManifestMode, kind ucidomain.IndexJobKind) ucidomain.IndexBeginResult {
	t.Helper()

	result, err := publisher.Begin(context.Background(), caller, fixture.beginInput(key, checkout, profileID, parent, mode, kind))
	require.NoError(t, err)
	return result
}

func (fixture *uciPublicationFixture) publish(t *testing.T, publisher ucidomain.IndexStore, caller ucidomain.IndexCaller, key string, checkout *UCICheckout, profileID string, parent *ucidomain.ContextRef, mode ucidomain.IndexManifestMode, kind ucidomain.IndexJobKind, draft uciPublicationDraft) (ucidomain.IndexBeginResult, ucidomain.IndexPublishedView) {
	t.Helper()

	build := fixture.begin(t, publisher, caller, key, checkout, profileID, parent, mode, kind)
	acks := fixture.stageDraft(t, publisher, caller, build.Build, draft.parts)
	return build, fixture.finalizeDraft(t, publisher, caller, build.Build, parent, acks, draft)
}

func (fixture *uciPublicationFixture) stageDraft(t *testing.T, publisher ucidomain.IndexStore, caller ucidomain.IndexCaller, build ucidomain.IndexBuildRef, parts []ucidomain.IndexPart) []ucidomain.IndexPartAck {
	t.Helper()

	acks := make([]ucidomain.IndexPartAck, 0, len(parts))
	for sequence, part := range parts {
		acks = append(acks, fixture.stage(t, publisher, caller, build, uint32(sequence), part))
	}
	return acks
}

func (fixture *uciPublicationFixture) stage(t *testing.T, publisher ucidomain.IndexStore, caller ucidomain.IndexCaller, build ucidomain.IndexBuildRef, sequence uint32, part ucidomain.IndexPart) ucidomain.IndexPartAck {
	t.Helper()

	ack, err := fixture.stagePart(publisher, caller, build, sequence, part)
	require.NoError(t, err)
	return ack
}

func (fixture *uciPublicationFixture) stagePart(publisher ucidomain.IndexStore, caller ucidomain.IndexCaller, build ucidomain.IndexBuildRef, sequence uint32, part ucidomain.IndexPart) (ucidomain.IndexPartAck, error) {
	digest, err := ucidomain.DigestIndexPart(part)
	if err != nil {
		return ucidomain.IndexPartAck{}, err
	}
	return publisher.Stage(context.Background(), caller, ucidomain.IndexStageInput{
		Build:    build,
		Sequence: sequence,
		Digest:   digest,
		Part:     part,
	})
}

func (fixture *uciPublicationFixture) finalizeDraft(t *testing.T, publisher ucidomain.IndexStore, caller ucidomain.IndexCaller, build ucidomain.IndexBuildRef, parent *ucidomain.ContextRef, acks []ucidomain.IndexPartAck, draft uciPublicationDraft) ucidomain.IndexPublishedView {
	t.Helper()

	published, err := publisher.Finalize(context.Background(), caller, ucidomain.IndexFinalizeInput{
		Build:          build,
		ExpectedParent: parent,
		Manifest:       fixture.manifest(t, acks, draft),
	})
	require.NoError(t, err)
	return published
}

func (fixture *uciPublicationFixture) requireStageOrFinalizeError(t *testing.T, publisher ucidomain.IndexStore, caller ucidomain.IndexCaller, build ucidomain.IndexBuildRef, parent *ucidomain.ContextRef, draft uciPublicationDraft) {
	t.Helper()

	acks := make([]ucidomain.IndexPartAck, 0, len(draft.parts))
	for sequence, part := range draft.parts {
		ack, err := fixture.stagePart(publisher, caller, build, uint32(sequence), part)
		if err != nil {
			return
		}
		acks = append(acks, ack)
	}
	_, err := publisher.Finalize(context.Background(), caller, ucidomain.IndexFinalizeInput{
		Build:          build,
		ExpectedParent: parent,
		Manifest:       fixture.manifest(t, acks, draft),
	})
	require.Error(t, err, "the invalid staged candidate must not publish")
}

func (fixture *uciPublicationFixture) manifest(t *testing.T, acks []ucidomain.IndexPartAck, draft uciPublicationDraft) ucidomain.IndexManifestCompletion {
	t.Helper()

	partsDigest, err := ucidomain.DigestIndexParts(acks)
	require.NoError(t, err)
	manifestDigest, err := ucidomain.DigestIndexManifest(draft.memberships)
	require.NoError(t, err)
	edgesDigest, err := ucidomain.DigestIndexEdges(draft.replacements)
	require.NoError(t, err)
	fsSeq := draft.fsSeq
	if fsSeq == 0 {
		fsSeq = 1
	}
	coverage := draft.coverage
	if coverage.Structural == "" {
		coverage = ucidomain.IndexCoverage{
			Structural: ucidomain.IndexCoverageComplete,
			Lexical:    ucidomain.IndexCoverageComplete,
			Vector:     ucidomain.IndexCoverageUnavailable,
		}
	}
	scanOutcome := draft.scanOutcome
	if scanOutcome == "" {
		scanOutcome = ucidomain.IndexScanComplete
	}
	census := draft.census
	if !census && scanOutcome == ucidomain.IndexScanComplete {
		census = true
	}
	headOID := strings.Repeat("a", 40)
	objectFormat := "sha1"
	refLabel := "refs/heads/publication-" + fixture.token
	scanStart := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC).Add(time.Duration(fsSeq) * time.Second)
	return ucidomain.IndexManifestCompletion{
		PartCount:      uint32(len(acks)),
		PartsDigest:    partsDigest,
		EntryCount:     uint64(len(draft.memberships)),
		ManifestDigest: manifestDigest,
		EdgeCount:      uint64(uciPublicationEdgeCount(draft.replacements)),
		EdgesDigest:    edgesDigest,
		ScanOutcome:    scanOutcome,
		CensusComplete: census,
		Observation: ucidomain.IndexObservation{
			HeadOID:       &headOID,
			ObjectFormat:  &objectFormat,
			RefLabel:      &refLabel,
			ObservedFSSeq: fsSeq,
			ScanStart:     scanStart,
			ScanEnd:       scanStart.Add(time.Second),
		},
		Coverage: coverage,
	}
}

func newUCIPublicationDraft(parts []ucidomain.IndexPart, memberships []ucidomain.IndexMembership, replacements []ucidomain.IndexEdgeReplacement) uciPublicationDraft {
	return uciPublicationDraft{
		parts:        parts,
		memberships:  memberships,
		replacements: replacements,
		scanOutcome:  ucidomain.IndexScanComplete,
		census:       true,
		coverage: ucidomain.IndexCoverage{
			Structural: ucidomain.IndexCoverageComplete,
			Lexical:    ucidomain.IndexCoverageComplete,
			Vector:     ucidomain.IndexCoverageUnavailable,
		},
	}
}

func uciPublicationPart(artifacts []uciPublicationArtifact, memberships []ucidomain.IndexMembership, deletions []ucidomain.IndexDeletion, replacements []ucidomain.IndexEdgeReplacement) ucidomain.IndexPart {
	proofs := make([]ucidomain.IndexArtifactProof, 0, len(artifacts))
	for _, artifact := range artifacts {
		proofs = append(proofs, artifact.Proof)
	}
	return ucidomain.IndexPart{
		Artifacts:        proofs,
		Memberships:      memberships,
		Deletions:        deletions,
		EdgeReplacements: replacements,
	}
}

func uciPublicationPresentMembership(path string, artifact uciPublicationArtifact) ucidomain.IndexMembership {
	artifactID := artifact.Artifact.ArtifactID
	return ucidomain.IndexMembership{
		PathKey:     path,
		DisplayPath: path,
		Mode:        "100644",
		State:       ucidomain.IndexFilePresent,
		ArtifactID:  &artifactID,
	}
}

func uciPublicationUnreadableMembership(path string) ucidomain.IndexMembership {
	return ucidomain.IndexMembership{
		PathKey:     path,
		DisplayPath: path,
		Mode:        "100644",
		State:       ucidomain.IndexFileUnreadable,
	}
}

func uciPublicationResolvedEdge(source uciPublicationArtifact, sourcePath string, target uciPublicationArtifact, targetPath string) ucidomain.IndexEdge {
	sourceSymbol := source.Definition.LocalSymbolKey
	targetSymbol := target.Definition.LocalSymbolKey
	referenceSiteID := source.Reference.ReferenceSiteID
	return ucidomain.IndexEdge{
		EdgeKey:          sourcePath + "->" + targetPath,
		SourceArtifactID: source.Artifact.ArtifactID,
		SourceSymbolKey:  &sourceSymbol,
		Target: &ucidomain.IndexEdgeTarget{
			PathKey:    targetPath,
			ArtifactID: target.Artifact.ArtifactID,
			SymbolKey:  &targetSymbol,
		},
		Relation:         ucidomain.IndexRelation("calls"),
		EvidenceKind:     ucidomain.IndexEvidenceKind("resolved"),
		ResolutionState:  ucidomain.IndexResolutionState("resolved"),
		ResolverRevision: "fixture-resolver",
		Evidence: ucidomain.IndexEdgeEvidence{
			ReferenceSiteID: &referenceSiteID,
			Span:            ucidomain.IndexSpan{ByteStart: 0, ByteEnd: 1, LineStart: 1, LineEnd: 1},
			RuleKey:         "fixture-call",
			Explanation:     "fixture resolved call",
		},
	}
}

func uciPublicationParent(published ucidomain.IndexPublishedView) *ucidomain.ContextRef {
	parent := published.Context
	return &parent
}

func uciPublicationEdgeCount(groups []ucidomain.IndexEdgeReplacement) int {
	count := 0
	for _, group := range groups {
		count += len(group.Edges)
	}
	return count
}

func uciPublicationDigest(value string) string {
	return uciPublicationDigestBytes([]byte(value))
}

func uciPublicationDigestBytes(value []byte) string {
	return fmt.Sprintf("sha256:%x", sha256Sum(value))
}

func sha256Sum(value []byte) [32]byte {
	return sha256.Sum256(value)
}

func (fixture *uciPublicationFixture) assertNoCurrentView(t *testing.T, checkout *UCICheckout) {
	t.Helper()

	_, err := fixture.context.GetCurrentView(context.Background(), checkout.CheckoutID)
	require.Error(t, err)
	require.True(t, errors.Is(err, gormlib.ErrRecordNotFound), "fresh or unpublished checkout must have no current View")
}

func (fixture *uciPublicationFixture) assertCurrentProjection(t *testing.T, checkout *UCICheckout, published ucidomain.IndexPublishedView, memberships []ucidomain.IndexMembership, replacements []ucidomain.IndexEdgeReplacement, coverage ucidomain.IndexCoverage) {
	t.Helper()

	current, err := fixture.context.GetCurrentView(context.Background(), checkout.CheckoutID)
	require.NoError(t, err)
	require.Equal(t, published.Context.ViewID, current.ViewID)
	require.Equal(t, published.Context.Generation, current.Generation)
	fixture.assertMembershipsAtGeneration(t, checkout, current.Generation, memberships)
	fixture.assertEdgesAtGeneration(t, checkout, current.Generation, replacements)
	fixture.assertCoverage(t, current, coverage)
}

func (fixture *uciPublicationFixture) assertProjectionAtGeneration(t *testing.T, checkout *UCICheckout, generation int64, memberships []ucidomain.IndexMembership, replacements []ucidomain.IndexEdgeReplacement) {
	t.Helper()

	fixture.assertMembershipsAtGeneration(t, checkout, generation, memberships)
	fixture.assertEdgesAtGeneration(t, checkout, generation, replacements)
}

func (fixture *uciPublicationFixture) assertCurrentIntervals(t *testing.T, checkout *UCICheckout, memberships []ucidomain.IndexMembership, replacements []ucidomain.IndexEdgeReplacement) {
	t.Helper()

	var memberCount, edgeCount int64
	require.NoError(t, fixture.db.Model(&UCIMembership{}).Where("checkout_id = ? AND valid_to_generation IS NULL", checkout.CheckoutID).Count(&memberCount).Error)
	require.NoError(t, fixture.db.Model(&UCIResolvedEdge{}).Where("checkout_id = ? AND valid_to_generation IS NULL", checkout.CheckoutID).Count(&edgeCount).Error)
	require.Equal(t, int64(len(memberships)), memberCount)
	require.Equal(t, int64(uciPublicationEdgeCount(replacements)), edgeCount)
}

func (fixture *uciPublicationFixture) assertMembershipsAtGeneration(t *testing.T, checkout *UCICheckout, generation int64, expected []ucidomain.IndexMembership) {
	t.Helper()

	rows := make([]UCIMembership, 0)
	require.NoError(t, fixture.db.Where(
		"checkout_id = ? AND valid_from_generation <= ? AND (valid_to_generation IS NULL OR valid_to_generation > ?)",
		checkout.CheckoutID, generation, generation,
	).Order("path_key ASC").Find(&rows).Error)
	require.Len(t, rows, len(expected))
	byPath := make(map[string]UCIMembership, len(rows))
	for _, row := range rows {
		byPath[row.PathKey] = row
	}
	for _, want := range expected {
		got, ok := byPath[want.PathKey]
		require.Truef(t, ok, "missing membership for %q", want.PathKey)
		require.Equal(t, want.DisplayPath, got.DisplayPath)
		require.Equal(t, string(want.State), string(got.FileState))
		require.Equal(t, want.Mode, got.Mode)
		if want.ArtifactID == nil {
			require.Nil(t, got.ArtifactID)
		} else {
			require.NotNil(t, got.ArtifactID)
			require.Equal(t, *want.ArtifactID, *got.ArtifactID)
		}
	}
}

func (fixture *uciPublicationFixture) assertEdgesAtGeneration(t *testing.T, checkout *UCICheckout, generation int64, groups []ucidomain.IndexEdgeReplacement) {
	t.Helper()

	rows := make([]UCIResolvedEdge, 0)
	require.NoError(t, fixture.db.Where(
		"checkout_id = ? AND valid_from_generation <= ? AND (valid_to_generation IS NULL OR valid_to_generation > ?)",
		checkout.CheckoutID, generation, generation,
	).Order("edge_key ASC").Find(&rows).Error)
	expected := make(map[string]ucidomain.IndexEdge)
	for _, group := range groups {
		for _, edge := range group.Edges {
			expected[edge.EdgeKey] = edge
		}
	}
	require.Len(t, rows, len(expected))
	for _, row := range rows {
		want, ok := expected[row.EdgeKey]
		require.Truef(t, ok, "unexpected current edge %q", row.EdgeKey)
		require.Equal(t, want.SourceArtifactID, row.SourceArtifact)
		require.Equal(t, string(want.Relation), row.Relation)
		require.Equal(t, string(want.EvidenceKind), string(row.EvidenceKind))
		require.Equal(t, string(want.ResolutionState), string(row.ResolutionState))
		if want.Target == nil {
			require.Nil(t, row.TargetPath)
			require.Nil(t, row.TargetArtifact)
		} else {
			require.NotNil(t, row.TargetPath)
			require.NotNil(t, row.TargetArtifact)
			require.Equal(t, want.Target.PathKey, *row.TargetPath)
			require.Equal(t, want.Target.ArtifactID, *row.TargetArtifact)
		}
	}
}

func (fixture *uciPublicationFixture) assertCoverage(t *testing.T, view *UCIView, expected ucidomain.IndexCoverage) {
	t.Helper()

	var coverage map[string]any
	require.NoError(t, json.Unmarshal([]byte(view.CoverageJSON), &coverage))
	require.Equal(t, string(expected.Structural), coverage["structural"])
	require.Equal(t, string(expected.Lexical), coverage["lexical"])
	require.Equal(t, string(expected.Vector), coverage["vector"])
	require.Equal(t, float64(expected.ExcludedFiles), coverage["excluded_files"])
	require.Equal(t, float64(expected.UnreadableFiles), coverage["unreadable_files"])
	require.Equal(t, float64(expected.UnresolvedReferences), coverage["unresolved_references"])
}

func (fixture *uciPublicationFixture) assertPublicationJobResult(t *testing.T, buildID, viewID string) {
	t.Helper()

	var row struct {
		State        string  `gorm:"column:state"`
		ResultViewID *string `gorm:"column:result_view_id"`
	}
	require.NoError(t, fixture.db.Raw(`SELECT state, result_view_id FROM ci_jobs WHERE job_id = ?`, buildID).Scan(&row).Error)
	require.Equal(t, string(UCIJobSucceeded), row.State)
	require.NotNil(t, row.ResultViewID)
	require.Equal(t, viewID, *row.ResultViewID)
}

func (fixture *uciPublicationFixture) assertNoPublicationJobResult(t *testing.T, buildID string) {
	t.Helper()

	var row struct {
		ResultViewID *string `gorm:"column:result_view_id"`
	}
	require.NoError(t, fixture.db.Raw(`SELECT result_view_id FROM ci_jobs WHERE job_id = ?`, buildID).Scan(&row).Error)
	require.Nil(t, row.ResultViewID)
}

func (fixture *uciPublicationFixture) assertPublicationJobCount(t *testing.T, checkout *UCICheckout, key string, expected int64) {
	t.Helper()

	var count int64
	require.NoError(t, fixture.db.Raw(`SELECT COUNT(*) FROM ci_jobs WHERE source_id = ? AND checkout_id = ? AND publication_key = ?`, checkout.SourceID, checkout.CheckoutID, key).Scan(&count).Error)
	require.Equal(t, expected, count)
}

func (fixture *uciPublicationFixture) assertBuildPartCount(t *testing.T, buildID string, expected int64) {
	t.Helper()

	var count int64
	require.NoError(t, fixture.db.Raw(`SELECT COUNT(*) FROM ci_index_build_parts WHERE build_id = ?`, buildID).Scan(&count).Error)
	require.Equal(t, expected, count)
}

func (fixture *uciPublicationFixture) assertViewCount(t *testing.T, checkout *UCICheckout, expected int64) {
	t.Helper()

	var count int64
	require.NoError(t, fixture.db.Model(&UCIView{}).Where("checkout_id = ?", checkout.CheckoutID).Count(&count).Error)
	require.Equal(t, expected, count)
}

func (fixture *uciPublicationFixture) intervalCount(t *testing.T, checkout *UCICheckout) int64 {
	t.Helper()

	var memberships, edges int64
	require.NoError(t, fixture.db.Model(&UCIMembership{}).Where("checkout_id = ?", checkout.CheckoutID).Count(&memberships).Error)
	require.NoError(t, fixture.db.Model(&UCIResolvedEdge{}).Where("checkout_id = ?", checkout.CheckoutID).Count(&edges).Error)
	return memberships + edges
}

func (fixture *uciPublicationFixture) expireBuild(t *testing.T, build ucidomain.IndexBuildRef, checkout *UCICheckout) {
	t.Helper()

	require.NoError(t, fixture.db.Exec(`UPDATE ci_jobs SET lease_expiry = clock_timestamp() - interval '1 microsecond' WHERE job_id = ?`, build.BuildID).Error)
	require.NoError(t, fixture.db.Exec(`UPDATE ci_checkouts SET lease_expires_at = clock_timestamp() - interval '1 microsecond' WHERE checkout_id = ? AND owner_instance IS NOT NULL`, checkout.CheckoutID).Error)
}

func (fixture *uciPublicationFixture) openPeerDB(t *testing.T) (*gormlib.DB, string) {
	t.Helper()

	dsn := os.Getenv("DATABASE_DSN")
	require.NotEmpty(t, dsn)
	config, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	if config.RuntimeParams == nil {
		config.RuntimeParams = make(map[string]string)
	}
	applicationName := "engram_uci_publication_peer_" + fixture.token
	config.RuntimeParams["application_name"] = applicationName
	config.RuntimeParams["search_path"] = fixture.schema + ", public"
	sqlDB := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = sqlDB.Close() })
	peer, err := gormlib.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, sqlDB.Ping())
	return peer, applicationName
}

func (fixture *uciPublicationFixture) waitForPeerLock(t *testing.T, applicationName string) {
	t.Helper()

	for attempt := 0; attempt < 10000; attempt++ {
		var waiting bool
		require.NoError(t, fixture.db.Raw(`SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE application_name = ? AND wait_event_type = 'Lock')`, applicationName).Scan(&waiting).Error)
		if waiting {
			return
		}
		runtime.Gosched()
	}
	require.Fail(t, "peer publication attempt never reached the checkout lock")
}

func installUCIPublicationFault(t *testing.T, db *gormlib.DB, tableName, timing, event, when string) func() {
	t.Helper()

	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	functionName := "uci_publication_fault_fn_" + suffix
	triggerName := "uci_publication_fault_trigger_" + suffix
	require.NoError(t, db.Exec(fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'uci publication injected fault'; END; $$`, functionName)).Error)
	require.NoError(t, db.Exec(fmt.Sprintf(`CREATE TRIGGER %s %s %s ON %s FOR EACH ROW %s EXECUTE FUNCTION %s()`, triggerName, timing, event, tableName, when, functionName)).Error)
	cleanup := func() {
		_ = db.Exec(fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON %s", triggerName, tableName)).Error
		_ = db.Exec(fmt.Sprintf("DROP FUNCTION IF EXISTS %s()", functionName)).Error
	}
	t.Cleanup(cleanup)
	return cleanup
}
