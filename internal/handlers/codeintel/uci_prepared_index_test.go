package codeintel_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/handlers/codeintel"
	"github.com/thebtf/engram/internal/handlers/engramcore"
	"github.com/thebtf/engram/internal/uci"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
	_ "modernc.org/sqlite"
)

const (
	preparedSourceID           = "11111111-1111-4111-8111-111111111111"
	preparedCheckoutID         = "22222222-2222-4222-8222-222222222222"
	preparedIncarnationID      = "33333333-3333-4333-8333-333333333333"
	preparedProfileID          = "44444444-4444-4444-8444-444444444444"
	preparedParentViewID       = "55555555-5555-4555-8555-555555555555"
	preparedPublishedView      = "66666666-6666-4666-8666-666666666666"
	preparedBuildID            = "77777777-7777-4777-8777-777777777777"
	preparedRootID             = "root:prepared-index"
	preparedWorkstationID      = "workstation:prepared-index"
	preparedClientID           = "client:prepared-index"
	preparedParserBundleDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

type preparedIndexScanner struct {
	calls  int
	root   string
	result uci.ScannerResult
	err    error
	onScan func()
}

func (scanner *preparedIndexScanner) Scan(_ context.Context, evidence uci.AuthorizedRootEvidence) (uci.ScannerResult, error) {
	scanner.calls++
	scanner.root = evidence.RootPath
	if scanner.onScan != nil {
		scanner.onScan()
	}
	if scanner.err != nil {
		return uci.ScannerResult{}, scanner.err
	}
	return scanner.result, nil
}

type preparedIndexClient struct {
	binding   uci.IndexBinding
	published uci.ContextRef

	beginRequests    []*pb.BeginCodeIndexRequest
	stagePayloadSets [][][]byte
	finalRequests    []*pb.FinalizeCodeIndexRequest
	stageCalls       int
	finalizeCalls    int
	stageErr         error
	stageNoAck       bool
	finalizeErr      error
	finalizeNoAck    bool
	partDigest       string
	onBegin          func()
	onStage          func()
}

func (client *preparedIndexClient) Begin(_ context.Context, request *pb.BeginCodeIndexRequest) (*pb.BeginCodeIndexResponse, error) {
	client.beginRequests = append(client.beginRequests, request)
	if request.GetOwnerInstance() != preparedClientID {
		return nil, errors.New("unexpected begin owner")
	}
	if request.GetScope().GetSourceId() != client.binding.Scope.SourceID ||
		request.GetScope().GetCheckoutId() != client.binding.Scope.CheckoutID ||
		request.GetScope().GetIncarnationId() != client.binding.Scope.IncarnationID ||
		request.GetScope().GetAnalysisProfileId() != client.binding.ProfileID {
		return nil, errors.New("unexpected begin scope")
	}
	if client.onBegin != nil {
		client.onBegin()
	}
	return &pb.BeginCodeIndexResponse{
		Scope:          request.GetScope(),
		BuildId:        preparedBuildID,
		LeaseEpoch:     1,
		LeaseExpiresAt: timestamppb.New(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)),
	}, nil
}

func (client *preparedIndexClient) Stage(_ context.Context, frames []*pb.StageCodeIndexFrame) (*pb.StageCodeIndexResponse, error) {
	client.stageCalls++
	if client.stageErr != nil {
		return nil, client.stageErr
	}
	if client.stageNoAck {
		return nil, nil
	}
	if len(frames) == 0 {
		return nil, errors.New("empty stage")
	}
	payloads, decodedFrames, err := client.decodeStageFrames(frames)
	if err != nil {
		return nil, err
	}
	if err := uci.ValidateIndexAdmissionFramesForBinding(decodedFrames, client.binding); err != nil {
		return nil, err
	}
	if err := preparedValidateArtifactProfiles(decodedFrames); err != nil {
		return nil, err
	}
	acks, err := preparedStageAcks(decodedFrames)
	if err != nil {
		return nil, err
	}
	partDigest, err := uci.DigestIndexParts(acks)
	if err != nil {
		return nil, err
	}
	client.partDigest = string(partDigest)
	client.stagePayloadSets = append(client.stagePayloadSets, payloads)
	if client.onStage != nil {
		client.onStage()
	}
	return &pb.StageCodeIndexResponse{
		BuildId:           preparedBuildID,
		AcceptedSequence:  uint64(len(frames) - 1),
		AcceptedPartCount: uint64(len(frames)),
		PartDigest:        string(partDigest),
	}, nil
}

func (client *preparedIndexClient) decodeStageFrames(frames []*pb.StageCodeIndexFrame) ([][]byte, []uci.IndexAdmissionFrame, error) {
	payloads := make([][]byte, len(frames))
	decodedFrames := make([]uci.IndexAdmissionFrame, len(frames))
	for index, frame := range frames {
		decoded, err := client.decodeStageFrame(frame, index)
		if err != nil {
			return nil, nil, err
		}
		payloads[index] = append([]byte(nil), frame.GetPayload()...)
		decodedFrames[index] = decoded
	}
	return payloads, decodedFrames, nil
}

func (client *preparedIndexClient) decodeStageFrame(frame *pb.StageCodeIndexFrame, index int) (uci.IndexAdmissionFrame, error) {
	if frame.GetBuildId() != preparedBuildID || frame.GetLeaseEpoch() != 1 || frame.GetSequence() != uint64(index) {
		return uci.IndexAdmissionFrame{}, fmt.Errorf("invalid stage frame %d", index)
	}
	if frame.GetScope().GetSourceId() != client.binding.Scope.SourceID ||
		frame.GetScope().GetCheckoutId() != client.binding.Scope.CheckoutID ||
		frame.GetScope().GetIncarnationId() != client.binding.Scope.IncarnationID ||
		frame.GetScope().GetAnalysisProfileId() != client.binding.ProfileID {
		return uci.IndexAdmissionFrame{}, fmt.Errorf("invalid stage scope %d", index)
	}
	if string(uci.DigestIndexAdmissionPayload(frame.GetPayload())) != frame.GetPayloadDigest() {
		return uci.IndexAdmissionFrame{}, fmt.Errorf("invalid stage digest %d", index)
	}
	return uci.DecodeIndexAdmissionFrame(frame.GetPayload())
}

func preparedValidateArtifactProfiles(frames []uci.IndexAdmissionFrame) error {
	for _, frame := range frames {
		for _, artifact := range frame.Artifacts {
			switch artifact.Profile.Language {
			case uci.IndexAdmissionLanguageGo,
				uci.IndexAdmissionLanguageJavaScript,
				uci.IndexAdmissionLanguageTypeScript,
				uci.IndexAdmissionLanguageTSX,
				uci.IndexAdmissionLanguageMarkdown,
				uci.IndexAdmissionLanguageJSON,
				uci.IndexAdmissionLanguageYAML,
				uci.IndexAdmissionLanguageSQL,
				uci.IndexAdmissionLanguageOpenAPI:
				if artifact.Profile.ExtractionProfileDigest != uci.IndexDigest(preparedParserBundleDigest) {
					return errors.New("artifact profile does not match selected parser bundle")
				}
			}
		}
	}
	return nil
}

func preparedStageAcks(decodedFrames []uci.IndexAdmissionFrame) ([]uci.IndexPartAck, error) {
	acks := make([]uci.IndexPartAck, len(decodedFrames))
	for index, decoded := range decodedFrames {
		part, err := decoded.PublicationPart()
		if err != nil {
			return nil, err
		}
		digest, err := uci.DigestIndexPart(part)
		if err != nil {
			return nil, err
		}
		acks[index] = uci.IndexPartAck{BuildID: preparedBuildID, Sequence: uint32(index), Digest: digest}
	}
	return acks, nil
}

func (client *preparedIndexClient) Finalize(_ context.Context, request *pb.FinalizeCodeIndexRequest) (*pb.FinalizeCodeIndexResponse, error) {
	client.finalizeCalls++
	client.finalRequests = append(client.finalRequests, request)
	if client.finalizeErr != nil {
		return nil, client.finalizeErr
	}
	if client.finalizeNoAck {
		return nil, nil
	}
	if len(client.stagePayloadSets) == 0 {
		return nil, errors.New("finalize before stage")
	}
	if request.GetPartsDigest() != client.partDigest {
		return nil, errors.New("finalize parts digest did not use stage acknowledgement")
	}
	manifestDigest, edgesDigest, entryCount, edgeCount, err := preparedFinalizationDigests(client.stagePayloadSets[len(client.stagePayloadSets)-1])
	if err != nil {
		return nil, err
	}
	if request.GetManifestDigest() != string(manifestDigest) || request.GetEdgesDigest() != string(edgesDigest) ||
		request.GetManifestEntryCount() != entryCount || request.GetEdgeCount() != edgeCount {
		return nil, errors.New("finalize manifest does not match deterministic admission parts")
	}
	return &pb.FinalizeCodeIndexResponse{
		PublishedContext: &pb.ContextRef{
			SourceId:          client.published.SourceID,
			CheckoutId:        client.published.CheckoutID,
			ViewId:            client.published.ViewID,
			Generation:        client.published.Generation,
			AnalysisProfileId: client.published.AnalysisProfileID,
		},
		BuildId:                    preparedBuildID,
		LeaseEpoch:                 1,
		AcceptedFilesystemSequence: request.GetObservedFilesystemSequence(),
	}, nil
}

func preparedFinalizationDigests(payloads [][]byte) (uci.IndexDigest, uci.IndexDigest, uint64, uint64, error) {
	if err := uci.ValidateIndexAdmissionPayloads(payloads); err != nil {
		return "", "", 0, 0, err
	}
	memberships := make([]uci.IndexMembership, 0)
	replacements := make([]uci.IndexEdgeReplacement, 0)
	var edgeCount uint64
	for _, payload := range payloads {
		frame, err := uci.DecodeIndexAdmissionFrame(payload)
		if err != nil {
			return "", "", 0, 0, err
		}
		part, err := frame.PublicationPart()
		if err != nil {
			return "", "", 0, 0, err
		}
		memberships = append(memberships, part.Memberships...)
		for _, replacement := range part.EdgeReplacements {
			edgeCount += uint64(len(replacement.Edges))
		}
		replacements = append(replacements, part.EdgeReplacements...)
	}
	manifestDigest, err := uci.DigestIndexManifest(memberships)
	if err != nil {
		return "", "", 0, 0, err
	}
	edgesDigest, err := uci.DigestIndexEdges(replacements)
	if err != nil {
		return "", "", 0, 0, err
	}
	return manifestDigest, edgesDigest, uint64(len(memberships)), edgeCount, nil
}

func TestUCIPreparedIndexPublishesGoFramesAndReplaysExactInputs(t *testing.T) {
	fixture := newPreparedIndexFixture(t)
	fixture.scanner.result.Files = []uci.ScannerFile{
		{
			Path:  "call.go",
			State: uci.IndexFilePresent,
			Body:  []byte("package sample\n\nfunc Target() {}\n\nfunc Caller() {\n\tTarget()\n}\n"),
		},
		{
			Path:  "notes.txt",
			State: uci.IndexFilePresent,
			Body:  []byte("unsupported source\n"),
		},
		{
			Path:      "protected.go",
			State:     uci.IndexFileExcluded,
			Exclusion: uci.ScannerExclusionProtected,
		},
		{
			Path:  "unreadable.go",
			State: uci.IndexFileUnreadable,
		},
	}
	fixture.scanner.result.Coverage.ExcludedFiles = 1

	first, err := fixture.collaborator.IndexPreparedCodebase(context.Background(), fixture.target, fixture.root, fixture.client)
	require.NoError(t, err)
	require.Equal(t, fixture.published, first.Context)
	require.Equal(t, 1, first.Uploaded)
	require.Zero(t, first.Embedded)
	require.Zero(t, first.Deleted)
	require.Equal(t, []string{
		"notes.txt: source language is unsupported",
		"protected.go: source is excluded",
		"unreadable.go: source is unreadable",
	}, first.Errors)
	require.Equal(t, 1, fixture.scanner.calls)
	require.Equal(t, fixture.root, fixture.scanner.root)
	require.Len(t, fixture.client.beginRequests, 1)
	require.Equal(t, "reconcile", fixture.client.beginRequests[0].GetJobKind())
	require.Equal(t, fixture.parent.ViewID, fixture.client.beginRequests[0].GetExpectedParent().GetViewId())
	require.Len(t, fixture.client.stagePayloadSets, 1)
	require.Len(t, fixture.client.stagePayloadSets[0], 1, "small artifacts must share one bounded frame")

	frame, err := uci.DecodeIndexAdmissionFrame(fixture.client.stagePayloadSets[0][0])
	require.NoError(t, err)
	require.Len(t, frame.Artifacts, 1)
	require.Equal(t, uci.IndexAdmissionLanguageGo, frame.Artifacts[0].Profile.Language)
	require.Equal(t, uci.IndexDigest(preparedParserBundleDigest), frame.Artifacts[0].Profile.ExtractionProfileDigest)
	expectedArtifactID, err := uci.DeriveIndexAdmissionArtifactID(preparedSourceID, frame.Artifacts[0].ContentDigest, frame.Artifacts[0].Profile)
	require.NoError(t, err)
	require.Equal(t, expectedArtifactID, frame.Artifacts[0].ArtifactID)
	require.Len(t, frame.Memberships, 4)
	preparedRequireMembership(t, frame, "call.go", uci.IndexAdmissionMembershipPresent, true)
	preparedRequireMembership(t, frame, "notes.txt", uci.IndexAdmissionMembershipUnsupported, false)
	preparedRequireMembership(t, frame, "protected.go", uci.IndexAdmissionMembershipProtected, false)
	preparedRequireMembership(t, frame, "unreadable.go", uci.IndexAdmissionMembershipUnreadable, false)
	require.Len(t, frame.EdgeReplacements, 4)
	part, err := frame.PublicationPart()
	require.NoError(t, err)
	require.Len(t, part.EdgeReplacements, 4)
	callEdge := preparedRequireResolvedCall(t, part, "call.go")
	require.Equal(t, "func:Caller", *callEdge.SourceSymbolKey)
	require.Equal(t, "func:Target", *callEdge.Target.SymbolKey)
	coverage := preparedCoverage(t, fixture.client.finalRequests[0])
	require.Equal(t, uci.IndexCoveragePartial, coverage.Lexical)
	require.Equal(t, uint64(2), coverage.ExcludedFiles)
	require.Equal(t, uint64(1), coverage.UnreadableFiles)

	state, found, err := fixture.registry.Snapshot(context.Background(), preparedCheckoutID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(4), state.LastReconciledSequence)
	require.Empty(t, state.DirtyPaths)

	second, err := fixture.collaborator.IndexPreparedCodebase(context.Background(), fixture.target, fixture.root, fixture.client)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, 2, fixture.scanner.calls)
	require.Len(t, fixture.client.beginRequests, 2)
	require.Equal(t, fixture.client.beginRequests[0].GetBuildKey(), fixture.client.beginRequests[1].GetBuildKey())
	require.Len(t, fixture.client.stagePayloadSets, 2)
	require.Equal(t, fixture.client.stagePayloadSets[0], fixture.client.stagePayloadSets[1])
}

func TestUCIPreparedIndexPublishesInitialIndexWithoutView(t *testing.T) {
	fixture := newPreparedIndexFixture(t)
	fixture.target.Binding.Context = nil
	fixture.client.binding.Context = nil
	fixture.scanner.result.Files = []uci.ScannerFile{{
		Path:  "initial.go",
		State: uci.IndexFilePresent,
		Body:  []byte("package sample\nfunc Initial() {}\n"),
	}}

	result, err := fixture.collaborator.IndexPreparedCodebase(context.Background(), fixture.target, fixture.root, fixture.client)
	require.NoError(t, err)
	require.Equal(t, fixture.published, result.Context)
	require.Len(t, fixture.client.beginRequests, 1)
	require.Equal(t, "initial_index", fixture.client.beginRequests[0].GetJobKind())
	require.Nil(t, fixture.client.beginRequests[0].GetExpectedParent())
	require.Len(t, fixture.client.finalRequests, 1)
	require.Nil(t, fixture.client.finalRequests[0].GetExpectedParent())
}

func TestUCIPreparedIndexForwardsDirtyAndUnbornWorktreeObservations(t *testing.T) {
	headOID := strings.Repeat("a", 40)
	sha1 := "sha1"
	sha256 := "sha256"
	main := "main"
	testCases := []struct {
		name         string
		headOID      *string
		objectFormat *string
		refLabel     *string
		dirty        bool
	}{
		{
			name:         "dirty detached worktree",
			headOID:      &headOID,
			objectFormat: &sha1,
			dirty:        true,
		},
		{
			name:         "unborn head",
			objectFormat: &sha256,
			refLabel:     &main,
			dirty:        false,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newPreparedIndexFixture(t)
			fixture.scanner.result.Files = []uci.ScannerFile{{
				Path:  "main.go",
				State: uci.IndexFilePresent,
				Body:  []byte("package sample\nfunc Main() {}\n"),
			}}
			observation := fixture.scanner.result.Observation
			observation.HeadOID = testCase.headOID
			observation.ObjectFormat = testCase.objectFormat
			observation.RefLabel = testCase.refLabel
			observation.Dirty = testCase.dirty
			fixture.scanner.result.Observation = observation

			_, err := fixture.collaborator.IndexPreparedCodebase(context.Background(), fixture.target, fixture.root, fixture.client)
			require.NoError(t, err)
			require.Len(t, fixture.client.finalRequests, 1)
			request := fixture.client.finalRequests[0]
			preparedRequireOptionalString(t, request.HeadOid, testCase.headOID, "head_oid")
			preparedRequireOptionalString(t, request.ObjectFormat, testCase.objectFormat, "object_format")
			preparedRequireOptionalString(t, request.RefLabel, testCase.refLabel, "ref_label")
			require.NotNil(t, request.Dirty)
			require.Equal(t, testCase.dirty, *request.Dirty)
		})
	}
}

func TestUCIPreparedIndexRefusesUnprovenLocalEvidenceBeforeScanning(t *testing.T) {
	t.Run("root hint", func(t *testing.T) {
		fixture := newPreparedIndexFixture(t)
		_, err := fixture.collaborator.IndexPreparedCodebase(context.Background(), fixture.target, filepath.Join(fixture.root, "other"), fixture.client)
		require.Error(t, err)
		preparedRequireNoPublication(t, fixture)
	})

	t.Run("unapproved root", func(t *testing.T) {
		fixture := newPreparedIndexFixture(t)
		fixture.target.Binding.LocalRootID = "root:unapproved"
		_, err := fixture.collaborator.IndexPreparedCodebase(context.Background(), fixture.target, fixture.root, fixture.client)
		require.Error(t, err)
		preparedRequireNoPublication(t, fixture)
	})

	t.Run("divergent worktree", func(t *testing.T) {
		fixture := newPreparedIndexFixture(t)
		checkoutID := "99999999-9999-4999-8999-999999999999"
		divergentRootID := "root:divergent"
		require.NoError(t, fixture.registry.RecordApprovedRoot(context.Background(), codeintel.UCILocalApprovedRoot{
			RootID:                  divergentRootID,
			SourceID:                preparedSourceID,
			CommonGitDirFingerprint: "sha256:common-git:divergent",
			RootPath:                t.TempDir(),
		}))
		_, err := fixture.registry.RegisterCheckout(context.Background(), codeintel.UCILocalCheckoutRegistration{
			RootID:                   divergentRootID,
			SourceID:                 preparedSourceID,
			CheckoutID:               checkoutID,
			IncarnationID:            preparedIncarnationID,
			CommonGitDirFingerprint:  "sha256:common-git:divergent",
			PrivateGitDirFingerprint: "sha256:private-git:divergent",
			WorkstationID:            preparedWorkstationID,
			ClientInstanceID:         preparedClientID,
		})
		require.NoError(t, err)
		fixture.target.Binding.Context = nil
		fixture.target.Binding.Scope.CheckoutID = checkoutID
		_, err = fixture.collaborator.IndexPreparedCodebase(context.Background(), fixture.target, fixture.root, fixture.client)
		require.Error(t, err)
		preparedRequireNoPublication(t, fixture)
	})

	t.Run("workstation", func(t *testing.T) {
		fixture := newPreparedIndexFixture(t)
		fixture.target.Binding.WorkstationID = "workstation:other"
		_, err := fixture.collaborator.IndexPreparedCodebase(context.Background(), fixture.target, fixture.root, fixture.client)
		require.Error(t, err)
		preparedRequireNoPublication(t, fixture)
	})

	t.Run("server incarnation", func(t *testing.T) {
		fixture := newPreparedIndexFixture(t)
		fixture.target.Binding.Scope.IncarnationID = "88888888-8888-4888-8888-888888888888"
		_, err := fixture.collaborator.IndexPreparedCodebase(context.Background(), fixture.target, fixture.root, fixture.client)
		require.Error(t, err)
		preparedRequireNoPublication(t, fixture)
	})

	t.Run("client instance", func(t *testing.T) {
		fixture := newPreparedIndexFixture(t)
		wrongClient, err := codeintel.NewUCIPreparedIndexCollaborator(codeintel.UCIPreparedIndexConfig{
			WorkstationID:      preparedWorkstationID,
			ClientInstanceID:   "client:other",
			ParserBundleDigest: preparedParserBundleDigest,
			Registry:           fixture.registry,
			Scanner:            fixture.scanner,
			GoProfile: uci.GoExtractionProfile{
				ProfileKey: "go-structure-v1",
				ParserKey:  "go-parser-v1",
			},
		})
		require.NoError(t, err)
		_, err = wrongClient.IndexPreparedCodebase(context.Background(), fixture.target, fixture.root, fixture.client)
		require.Error(t, err)
		preparedRequireNoPublication(t, fixture)
	})
}

func TestUCIPreparedIndexRequiresSelectedParserBundleDigest(t *testing.T) {
	fixture := newPreparedIndexFixture(t)
	_, err := codeintel.NewUCIPreparedIndexCollaborator(codeintel.UCIPreparedIndexConfig{
		WorkstationID:    preparedWorkstationID,
		ClientInstanceID: preparedClientID,
		Registry:         fixture.registry,
		Scanner:          fixture.scanner,
		GoProfile: uci.GoExtractionProfile{
			ProfileKey: "go-structure-v1",
			ParserKey:  "go-parser-v1",
		},
	})
	require.Error(t, err)

	wrongBundle, err := codeintel.NewUCIPreparedIndexCollaborator(codeintel.UCIPreparedIndexConfig{
		WorkstationID:      preparedWorkstationID,
		ClientInstanceID:   preparedClientID,
		ParserBundleDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Registry:           fixture.registry,
		Scanner:            fixture.scanner,
		GoProfile: uci.GoExtractionProfile{
			ProfileKey: "go-structure-v1",
			ParserKey:  "go-parser-v1",
		},
	})
	require.NoError(t, err)
	fixture.scanner.result.Files = []uci.ScannerFile{{
		Path:  "main.go",
		State: uci.IndexFilePresent,
		Body:  []byte("package sample\nfunc Main() {}\n"),
	}}
	_, err = wrongBundle.IndexPreparedCodebase(context.Background(), fixture.target, fixture.root, fixture.client)
	require.Error(t, err)
	require.Equal(t, 1, fixture.scanner.calls)
	require.Len(t, fixture.client.beginRequests, 1)
	require.Equal(t, 1, fixture.client.stageCalls)
	require.Zero(t, fixture.client.finalizeCalls)
	state, found, snapshotErr := fixture.registry.Snapshot(context.Background(), preparedCheckoutID)
	require.NoError(t, snapshotErr)
	require.True(t, found)
	require.Zero(t, state.LastReconciledSequence)
	require.NotEmpty(t, state.DirtyPaths)
}

func TestUCIPreparedIndexLeavesLocalStateDirtyWithoutDurableAcknowledgement(t *testing.T) {
	testCases := []struct {
		name              string
		configure         func(*preparedIndexClient)
		expectsFinalizing bool
	}{
		{
			name: "stage error",
			configure: func(client *preparedIndexClient) {
				client.stageErr = errors.New("stage unavailable")
			},
		},
		{
			name: "stage no acknowledgement",
			configure: func(client *preparedIndexClient) {
				client.stageNoAck = true
			},
		},
		{
			name: "finalize error",
			configure: func(client *preparedIndexClient) {
				client.finalizeErr = errors.New("finalize unavailable")
			},
			expectsFinalizing: true,
		},
		{
			name: "finalize no acknowledgement",
			configure: func(client *preparedIndexClient) {
				client.finalizeNoAck = true
			},
			expectsFinalizing: true,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newPreparedIndexFixture(t)
			fixture.scanner.result.Files = []uci.ScannerFile{{
				Path:  "main.go",
				State: uci.IndexFilePresent,
				Body:  []byte("package sample\nfunc Main() {}\n"),
			}}
			testCase.configure(fixture.client)

			_, err := fixture.collaborator.IndexPreparedCodebase(context.Background(), fixture.target, fixture.root, fixture.client)
			require.Error(t, err)
			require.Equal(t, 1, fixture.scanner.calls)
			require.Len(t, fixture.client.beginRequests, 1)
			require.Equal(t, 1, fixture.client.stageCalls)
			if testCase.expectsFinalizing {
				require.Equal(t, 1, fixture.client.finalizeCalls)
				require.Len(t, fixture.client.stagePayloadSets, 1)
			} else {
				require.Zero(t, fixture.client.finalizeCalls)
				require.Empty(t, fixture.client.stagePayloadSets)
			}

			state, found, snapshotErr := fixture.registry.Snapshot(context.Background(), preparedCheckoutID)
			require.NoError(t, snapshotErr)
			require.True(t, found)
			require.Zero(t, state.LastReconciledSequence)
			require.NotEmpty(t, state.DirtyPaths)
		})
	}
}

func TestUCIPreparedIndexStopsAtCanceledTransition(t *testing.T) {
	tests := []struct {
		name          string
		configure     func(*preparedIndexFixture, context.CancelFunc)
		beginCalls    int
		stageCalls    int
		finalizeCalls int
	}{
		{
			name: "after scan",
			configure: func(fixture *preparedIndexFixture, cancel context.CancelFunc) {
				fixture.scanner.result.Files = nil
				fixture.scanner.onScan = cancel
			},
		},
		{
			name: "after begin",
			configure: func(fixture *preparedIndexFixture, cancel context.CancelFunc) {
				fixture.client.onBegin = cancel
			},
			beginCalls: 1,
		},
		{
			name: "after stage",
			configure: func(fixture *preparedIndexFixture, cancel context.CancelFunc) {
				fixture.client.onStage = cancel
			},
			beginCalls: 1,
			stageCalls: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPreparedIndexFixture(t)
			fixture.scanner.result.Files = []uci.ScannerFile{{
				Path:  "main.go",
				State: uci.IndexFilePresent,
				Body:  []byte("package sample\nfunc Main() {}\n"),
			}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			test.configure(&fixture, cancel)

			result, err := fixture.collaborator.IndexPreparedCodebase(ctx, fixture.target, fixture.root, fixture.client)
			require.ErrorIs(t, err, context.Canceled)
			require.Nil(t, result)
			require.Equal(t, test.beginCalls, len(fixture.client.beginRequests))
			require.Equal(t, test.stageCalls, fixture.client.stageCalls)
			require.Equal(t, test.finalizeCalls, fixture.client.finalizeCalls)

			state, found, snapshotErr := fixture.registry.Snapshot(context.Background(), preparedCheckoutID)
			require.NoError(t, snapshotErr)
			require.True(t, found)
			require.Zero(t, state.LastReconciledSequence)
			require.NotEmpty(t, state.DirtyPaths)
		})
	}
}

func TestUCIPreparedIndexPacksGloballyValidCrossFrameGoCall(t *testing.T) {
	fixture := newPreparedIndexFixture(t)
	padding := strings.Repeat("x", 900_000)
	fixture.scanner.result.Files = []uci.ScannerFile{
		{
			Path:  "call.go",
			State: uci.IndexFilePresent,
			Body:  []byte("package sample\nfunc Caller() { Target() }\n//" + padding + "\n"),
		},
		{
			Path:  "target.go",
			State: uci.IndexFilePresent,
			Body:  []byte("package sample\nfunc Target() {}\n//" + padding + "\n"),
		},
	}

	result, err := fixture.collaborator.IndexPreparedCodebase(context.Background(), fixture.target, fixture.root, fixture.client)
	require.NoError(t, err)
	require.Equal(t, 2, result.Uploaded)
	require.Len(t, fixture.client.stagePayloadSets, 1)
	frames := preparedFrames(t, fixture.client.stagePayloadSets[0])
	require.Len(t, frames, 2)
	require.NoError(t, uci.ValidateIndexAdmissionFramesForBinding(frames, fixture.target.Binding))

	sourceArtifactFrame := preparedArtifactFrameIndex(t, frames, "call.go")
	targetArtifactFrame := preparedArtifactFrameIndex(t, frames, "target.go")
	require.NotEqual(t, sourceArtifactFrame, targetArtifactFrame)
	sourceEdgeFrame := preparedEdgeReplacementFrameIndex(t, frames, "call.go")
	require.NotEqual(t, sourceArtifactFrame, sourceEdgeFrame)
	rawEdge := preparedResolvedAdmissionCall(t, frames[sourceEdgeFrame], "call.go")
	require.NotNil(t, rawEdge.SourceSymbolKey)
	require.Equal(t, "func:Caller", *rawEdge.SourceSymbolKey)
	require.NotNil(t, rawEdge.Target)
	require.Equal(t, "target.go", rawEdge.Target.PathKey)
	publicationPart, err := frames[sourceEdgeFrame].PublicationPart()
	require.NoError(t, err)
	publishedEdge := preparedPublicationEdge(t, publicationPart, "call.go", rawEdge.EdgeKey)
	expectedReferenceID, err := uci.DeriveIndexAdmissionReferenceSiteID(rawEdge.SourceArtifactID, rawEdge.Evidence.ReferenceSiteKey)
	require.NoError(t, err)
	require.Equal(t, expectedReferenceID, *publishedEdge.Evidence.ReferenceSiteID)
	require.NotNil(t, publishedEdge.SourceSymbolKey)
	require.Equal(t, "func:Caller", *publishedEdge.SourceSymbolKey)

	coverage := preparedCoverage(t, fixture.client.finalRequests[0])
	require.Zero(t, coverage.UnresolvedReferences)
}

func TestUCIPreparedIndexLeavesAmbiguousGoCallsUnresolved(t *testing.T) {
	fixture := newPreparedIndexFixture(t)
	fixture.scanner.result.Files = []uci.ScannerFile{
		{Path: "caller.go", State: uci.IndexFilePresent, Body: []byte("package sample\nfunc Caller() { Target() }\n")},
		{Path: "left.go", State: uci.IndexFilePresent, Body: []byte("package sample\nfunc Target() {}\n")},
		{Path: "right.go", State: uci.IndexFilePresent, Body: []byte("package sample\nfunc Target() {}\n")},
	}

	_, err := fixture.collaborator.IndexPreparedCodebase(context.Background(), fixture.target, fixture.root, fixture.client)
	require.NoError(t, err)
	frames := preparedFrames(t, fixture.client.stagePayloadSets[0])
	callerFrame := preparedEdgeReplacementFrameIndex(t, frames, "caller.go")
	for _, replacement := range frames[callerFrame].EdgeReplacements {
		if replacement.SourcePath == "caller.go" {
			require.Empty(t, replacement.Edges)
		}
	}
	coverage := preparedCoverage(t, fixture.client.finalRequests[0])
	require.Equal(t, uint64(1), coverage.UnresolvedReferences)
}

func TestUCIPreparedIndexLeavesOwnerlessGoCallsUnresolved(t *testing.T) {
	fixture := newPreparedIndexFixture(t)
	fixture.scanner.result.Files = []uci.ScannerFile{
		{Path: "initializer.go", State: uci.IndexFilePresent, Body: []byte("package sample\nvar _ = Target()\n")},
		{Path: "target.go", State: uci.IndexFilePresent, Body: []byte("package sample\nfunc Target() {}\n")},
	}

	_, err := fixture.collaborator.IndexPreparedCodebase(context.Background(), fixture.target, fixture.root, fixture.client)
	require.NoError(t, err)
	frames := preparedFrames(t, fixture.client.stagePayloadSets[0])
	initializerFrame := preparedEdgeReplacementFrameIndex(t, frames, "initializer.go")
	for _, replacement := range frames[initializerFrame].EdgeReplacements {
		if replacement.SourcePath == "initializer.go" {
			require.Empty(t, replacement.Edges)
		}
	}
	coverage := preparedCoverage(t, fixture.client.finalRequests[0])
	require.Equal(t, uint64(1), coverage.UnresolvedReferences)
}

func TestUCIPreparedIndexRecomputesUnresolvedReferenceCoverage(t *testing.T) {
	fixture := newPreparedIndexFixture(t)
	fixture.scanner.result.Coverage.UnresolvedReferences = 99
	fixture.scanner.result.Files = []uci.ScannerFile{
		{Path: "caller.go", State: uci.IndexFilePresent, Body: []byte("package sample\nfunc Caller() { Target() }\n")},
		{Path: "left.go", State: uci.IndexFilePresent, Body: []byte("package sample\nfunc Target() {}\n")},
		{Path: "right.go", State: uci.IndexFilePresent, Body: []byte("package sample\nfunc Target() {}\n")},
	}

	_, err := fixture.collaborator.IndexPreparedCodebase(context.Background(), fixture.target, fixture.root, fixture.client)
	require.NoError(t, err)
	coverage := preparedCoverage(t, fixture.client.finalRequests[0])
	require.Equal(t, uint64(1), coverage.UnresolvedReferences)
}

func TestUCIPreparedIndexClonesBindingBeforeScanning(t *testing.T) {
	fixture := newPreparedIndexFixture(t)
	fixture.scanner.result.Files = []uci.ScannerFile{{
		Path:  "main.go",
		State: uci.IndexFilePresent,
		Body:  []byte("package sample\nfunc Main() {}\n"),
	}}
	originalParent := *fixture.target.Binding.Context
	fixture.scanner.onScan = func() {
		fixture.target.Binding.Context.ViewID = preparedPublishedView
	}

	_, err := fixture.collaborator.IndexPreparedCodebase(context.Background(), fixture.target, fixture.root, fixture.client)
	require.NoError(t, err)
	require.Equal(t, preparedPublishedView, fixture.target.Binding.Context.ViewID)
	require.Len(t, fixture.client.beginRequests, 1)
	require.Equal(t, originalParent.ViewID, fixture.client.beginRequests[0].GetExpectedParent().GetViewId())
}

func TestUCIPreparedIndexLogsOneCorrelatedScannerAggregate(t *testing.T) {
	fixture := newPreparedIndexFixture(t)
	var logs bytes.Buffer
	collaborator, err := codeintel.NewUCIPreparedIndexCollaborator(codeintel.UCIPreparedIndexConfig{
		WorkstationID:      preparedWorkstationID,
		ClientInstanceID:   preparedClientID,
		ParserBundleDigest: preparedParserBundleDigest,
		Registry:           fixture.registry,
		Scanner:            fixture.scanner,
		GoProfile: uci.GoExtractionProfile{
			ProfileKey: "go-structure-v1",
			ParserKey:  "go-parser-v1",
		},
		Logger: slog.New(slog.NewJSONHandler(&logs, nil)),
	})
	require.NoError(t, err)
	fixture.collaborator = collaborator
	fixture.scanner.result.Files = []uci.ScannerFile{{
		Path:  "sensitive/path.go",
		State: uci.IndexFilePresent,
		Body:  []byte("package sample\nconst hiddenBody = \"secret-body-do-not-log\"\n"),
	}}
	fixture.scanner.result.Diagnostics = uci.ScannerDiagnostics{
		GitTopologyDuration:   11 * time.Nanosecond,
		GitStatusDuration:     13 * time.Nanosecond,
		GitCandidatesDuration: 15 * time.Nanosecond,
		GitStagedDuration:     17 * time.Nanosecond,
		GitUntrackedDuration:  19 * time.Nanosecond,
		CandidateLoopDuration: 23 * time.Nanosecond,
		TotalDuration:         101 * time.Nanosecond,
		ResidualDuration:      18 * time.Nanosecond,
		CandidateCount:        29,
		AdmittedCount:         31,
		ExcludedCount:         37,
		UnreadableCount:       41,
		BytesRead:             43,
	}

	_, err = fixture.collaborator.IndexPreparedCodebase(context.Background(), fixture.target, fixture.root, fixture.client)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	require.Len(t, lines, 1)
	var entry map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &entry))
	require.Equal(t, "codeintel: prepared scanner phase aggregate", entry["msg"])
	require.Equal(t, preparedSourceID, entry["source_id"])
	require.Equal(t, preparedCheckoutID, entry["checkout_id"])
	require.Equal(t, preparedProfileID, entry["profile_id"])
	require.Equal(t, float64(4), entry["observed_fs_seq"])
	require.Equal(t, "2026-09-05T12:00:00Z", entry["scan_started_at"])
	require.Equal(t, "2026-09-05T12:00:01Z", entry["scan_completed_at"])
	for field, want := range map[string]float64{
		"git_topology_duration_ns":   11,
		"git_status_duration_ns":     13,
		"git_candidates_duration_ns": 15,
		"git_staged_duration_ns":     17,
		"git_untracked_duration_ns":  19,
		"candidate_loop_duration_ns": 23,
		"scan_total_duration_ns":     101,
		"residual_duration_ns":       18,
		"candidate_count":            29,
		"admitted_count":             31,
		"excluded_count":             37,
		"unreadable_count":           41,
		"bytes_read":                 43,
	} {
		require.Equalf(t, want, entry[field], "log field %q", field)
	}
	require.NotContains(t, logs.String(), fixture.root)
	require.NotContains(t, logs.String(), "sensitive/path.go")
	require.NotContains(t, logs.String(), "secret-body-do-not-log")
}

type preparedIndexFixture struct {
	collaborator *codeintel.UCIPreparedIndexCollaborator
	registry     *codeintel.UCILocalRegistry
	scanner      *preparedIndexScanner
	client       *preparedIndexClient
	target       engramcore.ResolvedIndexTarget
	root         string
	parent       uci.ContextRef
	published    uci.ContextRef
}

func newPreparedIndexFixture(t *testing.T) preparedIndexFixture {
	return newPreparedIndexFixtureWithTreeSitter(t, nil)
}

func newPreparedIndexFixtureWithTreeSitter(t *testing.T, parser codeintel.UCIPreparedTreeSitterParser) preparedIndexFixture {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	registry, err := codeintel.NewUCILocalRegistry(db)
	require.NoError(t, err)
	root := t.TempDir()
	require.NoError(t, registry.RecordApprovedRoot(context.Background(), codeintel.UCILocalApprovedRoot{
		RootID:                  preparedRootID,
		SourceID:                preparedSourceID,
		CommonGitDirFingerprint: "sha256:common-git:prepared",
		RootPath:                root,
	}))
	_, err = registry.RegisterCheckout(context.Background(), codeintel.UCILocalCheckoutRegistration{
		RootID:                   preparedRootID,
		SourceID:                 preparedSourceID,
		CheckoutID:               preparedCheckoutID,
		IncarnationID:            preparedIncarnationID,
		CommonGitDirFingerprint:  "sha256:common-git:prepared",
		PrivateGitDirFingerprint: "sha256:private-git:prepared",
		WorkstationID:            preparedWorkstationID,
		ClientInstanceID:         preparedClientID,
	})
	require.NoError(t, err)
	_, err = registry.RecordDirty(context.Background(), codeintel.UCILocalDirtyChange{
		CheckoutID:   preparedCheckoutID,
		RelativePath: "call.go",
		Sequence:     4,
	})
	require.NoError(t, err)

	parent := uci.ContextRef{
		SourceID:          preparedSourceID,
		CheckoutID:        preparedCheckoutID,
		ViewID:            preparedParentViewID,
		AnalysisProfileID: preparedProfileID,
		Generation:        1,
	}
	binding := uci.IndexBinding{
		Context: &parent,
		Scope: uci.IndexScope{
			SourceID:      preparedSourceID,
			CheckoutID:    preparedCheckoutID,
			IncarnationID: preparedIncarnationID,
		},
		ProfileID:     preparedProfileID,
		LocalRootID:   preparedRootID,
		WorkstationID: preparedWorkstationID,
	}
	published := uci.ContextRef{
		SourceID:          preparedSourceID,
		CheckoutID:        preparedCheckoutID,
		ViewID:            preparedPublishedView,
		AnalysisProfileID: preparedProfileID,
		Generation:        2,
	}
	scanner := &preparedIndexScanner{result: uci.ScannerResult{
		Census: uci.ScannerCensus{Outcome: uci.IndexScanComplete, Complete: true, CanDeleteAll: true},
		Observation: uci.IndexObservation{
			ObservedFSSeq: 4,
			ScanStart:     time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC),
			ScanEnd:       time.Date(2026, 9, 5, 12, 0, 1, 0, time.UTC),
		},
		Coverage: uci.IndexCoverage{Structural: uci.IndexCoverageComplete},
	}}
	collaborator, err := codeintel.NewUCIPreparedIndexCollaborator(codeintel.UCIPreparedIndexConfig{
		WorkstationID:      preparedWorkstationID,
		ClientInstanceID:   preparedClientID,
		ParserBundleDigest: preparedParserBundleDigest,
		Registry:           registry,
		Scanner:            scanner,
		TreeSitterParser:   parser,
		GoProfile: uci.GoExtractionProfile{
			ProfileKey: "go-structure-v1",
			ParserKey:  "go-parser-v1",
		},
	})
	require.NoError(t, err)
	return preparedIndexFixture{
		collaborator: collaborator,
		registry:     registry,
		scanner:      scanner,
		client:       &preparedIndexClient{binding: binding, published: published},
		target: engramcore.ResolvedIndexTarget{
			ClientSessionID: "session:prepared-index",
			ContextHandle:   "handle:prepared-index",
			Binding:         binding,
		},
		root:      root,
		parent:    parent,
		published: published,
	}
}

func preparedRequireMembership(t *testing.T, frame uci.IndexAdmissionFrame, path string, state uci.IndexAdmissionMembershipState, hasArtifact bool) {
	t.Helper()
	for _, membership := range frame.Memberships {
		if membership.PathKey != path {
			continue
		}
		require.Equal(t, state, membership.State)
		if hasArtifact {
			require.NotNil(t, membership.ArtifactID)
		} else {
			require.Nil(t, membership.ArtifactID)
		}
		return
	}
	require.Failf(t, "membership missing", "path %q not found", path)
}

func preparedRequireResolvedCall(t *testing.T, part uci.IndexPart, sourcePath string) uci.IndexEdge {
	t.Helper()
	for _, replacement := range part.EdgeReplacements {
		if replacement.SourcePath != sourcePath {
			continue
		}
		for _, edge := range replacement.Edges {
			if edge.Relation == uci.IndexRelation("calls") && edge.ResolutionState == uci.IndexResolutionState("resolved") {
				require.NotNil(t, edge.SourceSymbolKey)
				require.NotNil(t, edge.Target)
				require.NotNil(t, edge.Target.SymbolKey)
				require.NotNil(t, edge.Evidence.ReferenceSiteID)
				return edge
			}
		}
	}
	require.Fail(t, "expected a resolved same-source Go call edge")
	return uci.IndexEdge{}
}

func preparedCoverage(t *testing.T, request *pb.FinalizeCodeIndexRequest) uci.IndexCoverage {
	t.Helper()
	require.NotNil(t, request)
	var wire struct {
		Structural           uci.IndexCoverageState `json:"structural"`
		Lexical              uci.IndexCoverageState `json:"lexical"`
		Vector               uci.IndexCoverageState `json:"vector"`
		ExcludedFiles        uint64                 `json:"excluded_files"`
		UnreadableFiles      uint64                 `json:"unreadable_files"`
		UnresolvedReferences uint64                 `json:"unresolved_references"`
	}
	require.NoError(t, json.Unmarshal(request.GetCoverageJson(), &wire))
	return uci.IndexCoverage{
		Structural:           wire.Structural,
		Lexical:              wire.Lexical,
		Vector:               wire.Vector,
		ExcludedFiles:        wire.ExcludedFiles,
		UnreadableFiles:      wire.UnreadableFiles,
		UnresolvedReferences: wire.UnresolvedReferences,
	}
}

func preparedRequireOptionalString(t *testing.T, got, want *string, field string) {
	t.Helper()
	if want == nil {
		require.Nil(t, got, field)
		return
	}
	require.NotNil(t, got, field)
	require.Equal(t, *want, *got, field)
}

func preparedFrames(t *testing.T, payloads [][]byte) []uci.IndexAdmissionFrame {
	t.Helper()
	frames := make([]uci.IndexAdmissionFrame, len(payloads))
	for index, payload := range payloads {
		frame, err := uci.DecodeIndexAdmissionFrame(payload)
		require.NoError(t, err)
		frames[index] = frame
	}
	return frames
}

func preparedFrameIndex(t *testing.T, frames []uci.IndexAdmissionFrame, path string) int {
	t.Helper()
	for frameIndex, frame := range frames {
		for _, membership := range frame.Memberships {
			if membership.PathKey == path {
				return frameIndex
			}
		}
	}
	require.Failf(t, "membership missing", "path %q not found", path)
	return -1
}

func preparedArtifactFrameIndex(t *testing.T, frames []uci.IndexAdmissionFrame, path string) int {
	t.Helper()
	artifact := preparedArtifactForPath(t, frames, path)
	for frameIndex, frame := range frames {
		for _, candidate := range frame.Artifacts {
			if candidate.ArtifactID == artifact.ArtifactID {
				return frameIndex
			}
		}
	}
	require.Failf(t, "artifact missing", "path %q has no admitted artifact frame", path)
	return -1
}

func preparedEdgeReplacementFrameIndex(t *testing.T, frames []uci.IndexAdmissionFrame, path string) int {
	t.Helper()
	for frameIndex, frame := range frames {
		for _, replacement := range frame.EdgeReplacements {
			if replacement.SourcePath == path {
				return frameIndex
			}
		}
	}
	require.Failf(t, "edge replacement missing", "path %q has no edge replacement", path)
	return -1
}

func preparedResolvedAdmissionCall(t *testing.T, frame uci.IndexAdmissionFrame, sourcePath string) uci.IndexAdmissionEdge {
	t.Helper()
	for _, replacement := range frame.EdgeReplacements {
		if replacement.SourcePath != sourcePath {
			continue
		}
		for _, edge := range replacement.Edges {
			if edge.Relation == uci.IndexRelation("calls") && edge.ResolutionState == uci.IndexResolutionState("resolved") {
				return edge
			}
		}
	}
	require.Fail(t, "expected a resolved same-source Go call edge")
	return uci.IndexAdmissionEdge{}
}

func preparedPublicationEdge(t *testing.T, part uci.IndexPart, sourcePath, edgeKey string) uci.IndexEdge {
	t.Helper()
	for _, replacement := range part.EdgeReplacements {
		if replacement.SourcePath != sourcePath {
			continue
		}
		for _, edge := range replacement.Edges {
			if edge.EdgeKey == edgeKey {
				return edge
			}
		}
	}
	require.Failf(t, "published edge missing", "edge %q from %q not found", edgeKey, sourcePath)
	return uci.IndexEdge{}
}

func preparedRequireNoPublication(t *testing.T, fixture preparedIndexFixture) {
	t.Helper()
	require.Zero(t, fixture.scanner.calls)
	require.Empty(t, fixture.client.beginRequests)
	require.Zero(t, fixture.client.stageCalls)
	require.Zero(t, fixture.client.finalizeCalls)
}

func preparedRequireCapacityFailureBeforeBegin(t *testing.T, fixture preparedIndexFixture, result *engramcore.IndexResult, err error, scope uci.IndexCapacityScope, resource uci.IndexCapacityResource) *uci.IndexCapacityError {
	t.Helper()
	require.Nil(t, result)
	var capacity *uci.IndexCapacityError
	require.ErrorAs(t, err, &capacity)
	require.Equal(t, uci.IndexCapacityExceeded, capacity.Code())
	require.Equal(t, string(uci.IndexCapacityExceeded), err.Error())
	require.Equal(t, scope, capacity.Scope())
	require.Equal(t, resource, capacity.Resource())
	require.Greater(t, capacity.Required(), capacity.Limit())
	require.Equal(t, 1, fixture.scanner.calls)
	require.Empty(t, fixture.client.beginRequests)
	require.Zero(t, fixture.client.stageCalls)
	require.Zero(t, fixture.client.finalizeCalls)
	require.Equal(t, fixture.parent, *fixture.target.Binding.Context)
	state, found, snapshotErr := fixture.registry.Snapshot(context.Background(), preparedCheckoutID)
	require.NoError(t, snapshotErr)
	require.True(t, found)
	require.Zero(t, state.LastReconciledSequence)
	require.NotEmpty(t, state.DirtyPaths)
	return capacity
}

func TestUCIPreparedIndexAcceptsGoArtifactNearSourceLimit(t *testing.T) {
	fixture := newPreparedIndexFixture(t)
	fixture.scanner.result.Files = []uci.ScannerFile{{
		Path:  "large.go",
		State: uci.IndexFilePresent,
		Body:  []byte("package sample\n//" + strings.Repeat("x", uci.IndexAdmissionMaxArtifactBodyBytes-32)),
	}}

	result, err := fixture.collaborator.IndexPreparedCodebase(context.Background(), fixture.target, fixture.root, fixture.client)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, result.Uploaded)
	require.Len(t, fixture.client.beginRequests, 1)
	require.Equal(t, 1, fixture.client.stageCalls)
	require.Equal(t, 1, fixture.client.finalizeCalls)
}

func TestUCIPreparedIndexAcceptsBoundedAggregateCorpus(t *testing.T) {
	fixture := newPreparedIndexFixture(t)
	padding := strings.Repeat("x", 400_000)
	files := make([]uci.ScannerFile, 0, 32)
	for index := range 32 {
		files = append(files, uci.ScannerFile{
			Path:  fmt.Sprintf("large-%02d.go", index),
			State: uci.IndexFilePresent,
			Body:  []byte(fmt.Sprintf("package sample\nfunc F%d() {}\n//%s\n", index, padding)),
		})
	}
	fixture.scanner.result.Files = files

	result, err := fixture.collaborator.IndexPreparedCodebase(context.Background(), fixture.target, fixture.root, fixture.client)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, len(files), result.Uploaded)
	require.Len(t, fixture.client.stagePayloadSets, 1)
	payloadBytes := 0
	for _, frame := range fixture.client.stagePayloadSets[0] {
		payloadBytes += len(frame)
	}
	require.LessOrEqual(t, payloadBytes, uci.IndexAdmissionMaxTotalEncodedBytes)
	require.Greater(t, len(fixture.client.stagePayloadSets[0]), 1)
	require.Equal(t, 1, fixture.client.finalizeCalls)
}

func TestUCIPreparedIndexRefusesOversizedCallCorpusBeforePublishing(t *testing.T) {
	fixture := newPreparedIndexFixture(t)
	const calls = 4_097
	fixture.scanner.result.Files = []uci.ScannerFile{
		{
			Path:  "caller.go",
			State: uci.IndexFilePresent,
			Body:  []byte("package sample\nfunc Caller() {\n" + strings.Repeat("\tTarget()\n", calls) + "}\n"),
		},
		{
			Path:  "target.go",
			State: uci.IndexFilePresent,
			Body:  []byte("package sample\nfunc Target() {}\n"),
		},
	}

	result, err := fixture.collaborator.IndexPreparedCodebase(context.Background(), fixture.target, fixture.root, fixture.client)
	preparedRequireCapacityFailureBeforeBegin(t, fixture, result, err, uci.IndexCapacityScopeEdgeReplacement, uci.IndexCapacityResourceEdges)
}

func TestUCIPreparedIndexPacksCompleteRecordsWithoutLoss(t *testing.T) {
	fixture := newPreparedIndexFixture(t)
	const calls = 3_000
	caller := "package sample\nfunc Caller() {\n" + strings.Repeat("\tTarget()\n", calls) + "}\n//" + strings.Repeat("x", 700_000) + "\n"
	alias := "package sample\nfunc Alias() {}\n"
	fixture.scanner.result.Files = []uci.ScannerFile{
		{Path: "caller.go", State: uci.IndexFilePresent, Body: []byte(caller)},
		{Path: "target.go", State: uci.IndexFilePresent, Body: []byte("package sample\nfunc Target() {}\n")},
		{Path: "alias-a.go", State: uci.IndexFilePresent, Body: []byte(alias)},
		{Path: "alias-b.go", State: uci.IndexFilePresent, Body: []byte(alias)},
	}

	result, err := fixture.collaborator.IndexPreparedCodebase(context.Background(), fixture.target, fixture.root, fixture.client)
	require.NoError(t, err)
	require.Equal(t, fixture.published, result.Context)
	require.Equal(t, 3, result.Uploaded)
	require.Empty(t, result.Errors)
	require.Len(t, fixture.client.stagePayloadSets, 1)
	require.Len(t, fixture.client.finalRequests, 1)
	frames := preparedFrames(t, fixture.client.stagePayloadSets[0])
	require.NoError(t, uci.ValidateIndexAdmissionFramesForBinding(frames, fixture.target.Binding))

	memberships := make(map[string]uci.IndexAdmissionMembership)
	artifacts := make(map[string]struct{})
	edgeKeys := make(map[string]struct{})
	replacements := 0
	for _, frame := range frames {
		for _, artifact := range frame.Artifacts {
			artifacts[artifact.ArtifactID] = struct{}{}
		}
		for _, membership := range frame.Memberships {
			memberships[membership.PathKey] = membership
		}
		for _, replacement := range frame.EdgeReplacements {
			if replacement.SourcePath != "caller.go" {
				continue
			}
			replacements++
			for _, edge := range replacement.Edges {
				require.NotNil(t, edge.Target)
				require.Equal(t, "target.go", edge.Target.PathKey)
				edgeKeys[edge.EdgeKey] = struct{}{}
			}
		}
	}
	require.Len(t, memberships, 4)
	for _, path := range []string{"caller.go", "target.go", "alias-a.go", "alias-b.go"} {
		membership, found := memberships[path]
		require.True(t, found)
		require.Equal(t, uci.IndexAdmissionMembershipPresent, membership.State)
		require.NotNil(t, membership.ArtifactID)
	}
	require.Equal(t, *memberships["alias-a.go"].ArtifactID, *memberships["alias-b.go"].ArtifactID)
	require.Len(t, artifacts, 3)
	require.Equal(t, 1, replacements)
	require.Len(t, edgeKeys, calls)
	callerArtifactFrame := preparedArtifactFrameIndex(t, frames, "caller.go")
	callerEdgeFrame := preparedEdgeReplacementFrameIndex(t, frames, "caller.go")
	require.NotEqual(t, callerArtifactFrame, callerEdgeFrame)
	coverage := preparedCoverage(t, fixture.client.finalRequests[0])
	require.Zero(t, coverage.UnresolvedReferences)
}

func TestUCIPreparedIndexPublishesTreeSitterFactsWithPinnedProfile(t *testing.T) {
	parser := &preparedTreeSitterParser{responses: map[uci.TreeSitterLanguage]preparedTreeSitterResponse{
		uci.TreeSitterLanguageJavaScript: {},
		uci.TreeSitterLanguageTypeScript: {},
		uci.TreeSitterLanguageTSX:        {},
	}}
	fixture := newPreparedIndexFixtureWithTreeSitter(t, parser)
	fixture.scanner.result.Files = []uci.ScannerFile{
		{Path: "client.js", State: uci.IndexFilePresent, Body: []byte("export const client = true;\n")},
		{Path: "client.ts", State: uci.IndexFilePresent, Body: []byte("export const typed: string = \"ok\";\n")},
		{Path: "legacy.jsx", State: uci.IndexFilePresent, Body: []byte("export default <div />;\n")},
		{Path: "view.tsx", State: uci.IndexFilePresent, Body: []byte("export const View = () => <main />;\n")},
	}

	result, err := fixture.collaborator.IndexPreparedCodebase(context.Background(), fixture.target, fixture.root, fixture.client)
	require.NoError(t, err)
	require.Equal(t, fixture.published, result.Context)
	require.Equal(t, 3, result.Uploaded)
	require.Equal(t, []string{"legacy.jsx: source language is unsupported"}, result.Errors)
	require.Len(t, parser.requests, 3)

	expected := []struct {
		path     string
		language uci.TreeSitterLanguage
	}{
		{path: "client.js", language: uci.TreeSitterLanguageJavaScript},
		{path: "client.ts", language: uci.TreeSitterLanguageTypeScript},
		{path: "view.tsx", language: uci.TreeSitterLanguageTSX},
	}
	for index, want := range expected {
		request := parser.requests[index]
		require.Equal(t, want.language, request.Language)
		require.Equal(t, "uci-prepared-tree-sitter/v1:"+preparedProfileID+":"+string(want.language)+":"+preparedParserBundleDigest, request.ProfileKey)
		require.Equal(t, map[string][]byte{
			"client.js": fixture.scanner.result.Files[0].Body,
			"client.ts": fixture.scanner.result.Files[1].Body,
			"view.tsx":  fixture.scanner.result.Files[3].Body,
		}[want.path], request.Source)
	}

	frames := preparedFrames(t, fixture.client.stagePayloadSets[0])
	preparedRequireMembership(t, frames[preparedFrameIndex(t, frames, "client.js")], "client.js", uci.IndexAdmissionMembershipPresent, true)
	preparedRequireMembership(t, frames[preparedFrameIndex(t, frames, "client.ts")], "client.ts", uci.IndexAdmissionMembershipPresent, true)
	preparedRequireMembership(t, frames[preparedFrameIndex(t, frames, "view.tsx")], "view.tsx", uci.IndexAdmissionMembershipPresent, true)
	preparedRequireMembership(t, frames[preparedFrameIndex(t, frames, "legacy.jsx")], "legacy.jsx", uci.IndexAdmissionMembershipUnsupported, false)
	for _, want := range expected {
		artifact := preparedArtifactForPath(t, frames, want.path)
		require.Equal(t, uci.IndexDigest(preparedParserBundleDigest), artifact.Profile.GrammarDigest)
		require.Equal(t, uci.IndexDigest(preparedParserBundleDigest), artifact.Profile.ExtractionProfileDigest)
		expectedID, err := uci.DeriveIndexAdmissionArtifactID(preparedSourceID, artifact.ContentDigest, artifact.Profile)
		require.NoError(t, err)
		require.Equal(t, expectedID, artifact.ArtifactID)
		require.Len(t, artifact.Chunks, 1)
		require.Equal(t, string(fixture.scanner.result.Files[map[string]int{"client.js": 0, "client.ts": 1, "view.tsx": 3}[want.path]].Body), artifact.Chunks[0].Text)
	}
	for _, frame := range frames {
		for _, replacement := range frame.EdgeReplacements {
			if replacement.SourcePath == "client.js" || replacement.SourcePath == "client.ts" || replacement.SourcePath == "view.tsx" {
				require.Empty(t, replacement.Edges)
			}
		}
	}
	coverage := preparedCoverage(t, fixture.client.finalRequests[0])
	require.Equal(t, uci.IndexCoveragePartial, coverage.Lexical)
	require.Equal(t, uint64(1), coverage.ExcludedFiles)
}

func TestUCIPreparedIndexRetainsPartialTreeSitterFacts(t *testing.T) {
	parser := &preparedTreeSitterParser{responses: map[uci.TreeSitterLanguage]preparedTreeSitterResponse{
		uci.TreeSitterLanguageTypeScript: {
			coverage: uci.IndexCoveragePartial,
			diagnostics: []uci.TreeSitterDiagnostic{{
				Code:    "PARSE_ERROR",
				Message: "source could not be parsed completely",
			}},
		},
	}}
	fixture := newPreparedIndexFixtureWithTreeSitter(t, parser)
	fixture.scanner.result.Files = []uci.ScannerFile{{
		Path: "broken.ts", State: uci.IndexFilePresent, Body: []byte("export function broken(\n"),
	}}

	result, err := fixture.collaborator.IndexPreparedCodebase(context.Background(), fixture.target, fixture.root, fixture.client)
	require.NoError(t, err)
	require.Equal(t, 1, result.Uploaded)
	require.Equal(t, []string{"broken.ts: Tree-sitter parser coverage is partial"}, result.Errors)
	frames := preparedFrames(t, fixture.client.stagePayloadSets[0])
	broken := preparedArtifactForPath(t, frames, "broken.ts")
	require.Equal(t, uci.IndexAdmissionArtifactPartial, broken.Status)
	require.True(t, preparedArtifactHasDiagnostic(broken, "PARSE_ERROR"))
	require.True(t, preparedArtifactHasDiagnostic(broken, "TREE_SITTER_PARTIAL_COVERAGE"))
	coverage := preparedCoverage(t, fixture.client.finalRequests[0])
	require.Equal(t, uci.IndexCoveragePartial, coverage.Lexical)
	require.Zero(t, coverage.ExcludedFiles)
}

func TestUCIPreparedIndexRejectsTreeSitterBundleMismatchBeforePublishing(t *testing.T) {
	parser := &preparedTreeSitterParser{responses: map[uci.TreeSitterLanguage]preparedTreeSitterResponse{
		uci.TreeSitterLanguageTSX: {bundleDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	}}
	fixture := newPreparedIndexFixtureWithTreeSitter(t, parser)
	fixture.scanner.result.Files = []uci.ScannerFile{{
		Path: "missing.tsx", State: uci.IndexFilePresent, Body: []byte("export const Missing = () => <main />;\n"),
	}}

	result, err := fixture.collaborator.IndexPreparedCodebase(context.Background(), fixture.target, fixture.root, fixture.client)
	require.Nil(t, result)
	require.ErrorContains(t, err, "Tree-sitter parser bundle mismatch")
	require.Equal(t, 1, fixture.scanner.calls)
	require.Empty(t, fixture.client.beginRequests)
	require.Zero(t, fixture.client.stageCalls)
	require.Zero(t, fixture.client.finalizeCalls)
}

func TestUCIPreparedIndexRejectsMissingTreeSitterBeforePublishingAnyFacts(t *testing.T) {
	fixture := newPreparedIndexFixture(t)
	fixture.scanner.result.Files = []uci.ScannerFile{
		{Path: "caller.go", State: uci.IndexFilePresent, Body: []byte("package sample\nfunc Caller() { Target() }\n")},
		{Path: "client.js", State: uci.IndexFilePresent, Body: []byte("export const client = true;\n")},
		{Path: "target.go", State: uci.IndexFilePresent, Body: []byte("package sample\nfunc Target() {}\n")},
	}

	result, err := fixture.collaborator.IndexPreparedCodebase(context.Background(), fixture.target, fixture.root, fixture.client)
	require.Nil(t, result)
	require.ErrorContains(t, err, "Tree-sitter parser is unavailable")
	require.Equal(t, 1, fixture.scanner.calls)
	require.Empty(t, fixture.client.beginRequests)
	require.Zero(t, fixture.client.stageCalls)
	require.Zero(t, fixture.client.finalizeCalls)
}

func TestUCIPreparedIndexPublishesMixedCaseCapabilityCorpus(t *testing.T) {
	parser := &preparedTreeSitterParser{responses: map[uci.TreeSitterLanguage]preparedTreeSitterResponse{
		uci.TreeSitterLanguageJavaScript: {},
		uci.TreeSitterLanguageTypeScript: {},
		uci.TreeSitterLanguageTSX:        {},
	}}
	fixture := newPreparedIndexFixtureWithTreeSitter(t, parser)

	goSource := []byte("package sample\nfunc Main() {}\n")
	javascriptSource := []byte("export const client = true;\n")
	typescriptSource := []byte("export const answer: number = 42;\n")
	tsxSource := []byte("export const View = () => <main />;\n")
	legacy := []struct {
		path     string
		body     []byte
		language uci.IndexAdmissionLanguage
	}{
		{path: "code.GO", body: goSource, language: uci.IndexAdmissionLanguageGo},
		{path: "client.JS", body: javascriptSource, language: uci.IndexAdmissionLanguageJavaScript},
		{path: "typed.TS", body: typescriptSource, language: uci.IndexAdmissionLanguageTypeScript},
		{path: "view.TSX", body: tsxSource, language: uci.IndexAdmissionLanguageTSX},
	}
	type structuredCase struct {
		path          string
		body          []byte
		capability    string
		language      uci.IndexAdmissionLanguage
		buildExpected func(*testing.T, string, []byte) uci.IndexAdmissionArtifact
	}
	structured := []structuredCase{
		{
			path:          "README.MD",
			body:          []byte("# Intro\n\n[Docs](docs/guide.md)\n"),
			capability:    "markdown",
			language:      uci.IndexAdmissionLanguageMarkdown,
			buildExpected: preparedMarkdownAdmissionArtifact,
		},
		{
			path:          "guide.MARKDOWN",
			body:          []byte("Overview\n========\n\nSee [API](api.md).\n"),
			capability:    "markdown",
			language:      uci.IndexAdmissionLanguageMarkdown,
			buildExpected: preparedMarkdownAdmissionArtifact,
		},
		{
			path:          "contracts.JSON",
			body:          preparedOpenAPIJSONSource("Ordinary JSON"),
			capability:    "json",
			language:      uci.IndexAdmissionLanguageJSON,
			buildExpected: preparedJSONYAMLAdmissionArtifact,
		},
		{
			path:          "settings.YAML",
			body:          []byte("service: engram\nfeatures:\n  enabled: true\n"),
			capability:    "yaml",
			language:      uci.IndexAdmissionLanguageYAML,
			buildExpected: preparedJSONYAMLAdmissionArtifact,
		},
		{
			path:          "values.YML",
			body:          []byte("items:\n  - name: first\n"),
			capability:    "yaml",
			language:      uci.IndexAdmissionLanguageYAML,
			buildExpected: preparedJSONYAMLAdmissionArtifact,
		},
		{
			path:          "schema.SQL",
			body:          []byte("CREATE TABLE accounts (id INTEGER PRIMARY KEY, name TEXT);\n"),
			capability:    "sql",
			language:      uci.IndexAdmissionLanguageSQL,
			buildExpected: preparedSQLAdmissionArtifact,
		},
		{
			path:          "openapi.JSON",
			body:          preparedOpenAPIJSONSource("Exact JSON"),
			capability:    "openapi-json",
			language:      uci.IndexAdmissionLanguageOpenAPI,
			buildExpected: preparedOpenAPIAdmissionArtifact,
		},
		{
			path:          "service.openapi.JSON",
			body:          preparedOpenAPIJSONSource("Suffix JSON"),
			capability:    "openapi-json",
			language:      uci.IndexAdmissionLanguageOpenAPI,
			buildExpected: preparedOpenAPIAdmissionArtifact,
		},
		{
			path:          "OPENAPI.YAML",
			body:          preparedOpenAPIYAMLSource("Exact YAML"),
			capability:    "openapi-yaml",
			language:      uci.IndexAdmissionLanguageOpenAPI,
			buildExpected: preparedOpenAPIAdmissionArtifact,
		},
		{
			path:          "service.openapi.YAML",
			body:          preparedOpenAPIYAMLSource("Suffix YAML"),
			capability:    "openapi-yaml",
			language:      uci.IndexAdmissionLanguageOpenAPI,
			buildExpected: preparedOpenAPIAdmissionArtifact,
		},
		{
			path:          "OpenAPI.YML",
			body:          preparedOpenAPIYAMLSource("Exact YML"),
			capability:    "openapi-yaml",
			language:      uci.IndexAdmissionLanguageOpenAPI,
			buildExpected: preparedOpenAPIAdmissionArtifact,
		},
		{
			path:          "service.openapi.YML",
			body:          preparedOpenAPIYAMLSource("Suffix YML"),
			capability:    "openapi-yaml",
			language:      uci.IndexAdmissionLanguageOpenAPI,
			buildExpected: preparedOpenAPIAdmissionArtifact,
		},
	}
	files := make([]uci.ScannerFile, 0, len(legacy)+len(structured)+1)
	for _, source := range legacy {
		files = append(files, uci.ScannerFile{Path: source.path, State: uci.IndexFilePresent, Body: source.body})
	}
	for _, source := range structured {
		files = append(files, uci.ScannerFile{Path: source.path, State: uci.IndexFilePresent, Body: source.body})
	}
	files = append(files, uci.ScannerFile{Path: "ignored.IPYNB", State: uci.IndexFilePresent, Body: []byte("{}")})
	fixture.scanner.result.Files = files

	result, err := fixture.collaborator.IndexPreparedCodebase(context.Background(), fixture.target, fixture.root, fixture.client)
	require.NoError(t, err)
	require.Equal(t, fixture.published, result.Context)
	require.Equal(t, len(legacy)+len(structured), result.Uploaded)
	require.Equal(t, []string{"ignored.IPYNB: source language is unsupported"}, result.Errors)
	require.Len(t, fixture.client.beginRequests, 1)
	require.Len(t, fixture.client.stagePayloadSets, 1)
	require.Len(t, fixture.client.finalRequests, 1)

	frames := preparedFrames(t, fixture.client.stagePayloadSets[0])
	require.NoError(t, uci.ValidateIndexAdmissionFramesForBinding(frames, fixture.target.Binding))
	for _, source := range legacy {
		frame := frames[preparedFrameIndex(t, frames, source.path)]
		preparedRequireMembership(t, frame, source.path, uci.IndexAdmissionMembershipPresent, true)
		artifact := preparedArtifactForPath(t, frames, source.path)
		require.Equal(t, source.language, artifact.Profile.Language)
		require.Equal(t, source.body, artifact.Body)
	}
	for _, source := range structured {
		frame := frames[preparedFrameIndex(t, frames, source.path)]
		preparedRequireMembership(t, frame, source.path, uci.IndexAdmissionMembershipPresent, true)
		artifact := preparedArtifactForPath(t, frames, source.path)
		require.Equal(t, source.language, artifact.Profile.Language)
		preparedRequireExactAdmissionArtifact(t, artifact, source.buildExpected(t, preparedStructuredProfileKey(source.capability), source.body))
		preparedRequireSourceGroundedStructuredFacts(t, artifact)
	}
	require.NotEmpty(t, preparedArtifactForPath(t, frames, "README.MD").References)
	unsupportedFrame := frames[preparedFrameIndex(t, frames, "ignored.IPYNB")]
	preparedRequireMembership(t, unsupportedFrame, "ignored.IPYNB", uci.IndexAdmissionMembershipUnsupported, false)

	require.Len(t, parser.requests, 3)
	requestedSources := map[uci.TreeSitterLanguage][]byte{
		uci.TreeSitterLanguageJavaScript: javascriptSource,
		uci.TreeSitterLanguageTypeScript: typescriptSource,
		uci.TreeSitterLanguageTSX:        tsxSource,
	}
	for _, request := range parser.requests {
		require.Equal(t, requestedSources[request.Language], request.Source)
		delete(requestedSources, request.Language)
	}
	require.Empty(t, requestedSources)
	coverage := preparedCoverage(t, fixture.client.finalRequests[0])
	require.Equal(t, uci.IndexCoveragePartial, coverage.Lexical)
	require.Equal(t, uint64(1), coverage.ExcludedFiles)
}

func TestUCIPreparedIndexPublishesPartialStructuredFactsWithPathReason(t *testing.T) {
	fixture := newPreparedIndexFixture(t)
	source := []byte("{\"kept\":{\"name\":\"value\"},\"broken\":[1,}")
	fixture.scanner.result.Files = []uci.ScannerFile{{
		Path:  "broken.JSON",
		State: uci.IndexFilePresent,
		Body:  source,
	}}

	result, err := fixture.collaborator.IndexPreparedCodebase(context.Background(), fixture.target, fixture.root, fixture.client)
	require.NoError(t, err)
	require.Equal(t, 1, result.Uploaded)
	require.Equal(t, []string{"broken.JSON: JSON extraction is partial"}, result.Errors)
	require.Len(t, fixture.client.beginRequests, 1)
	require.Len(t, fixture.client.finalRequests, 1)

	frames := preparedFrames(t, fixture.client.stagePayloadSets[0])
	frame := frames[preparedFrameIndex(t, frames, "broken.JSON")]
	preparedRequireMembership(t, frame, "broken.JSON", uci.IndexAdmissionMembershipPresent, true)
	artifact := preparedArtifactForPath(t, frames, "broken.JSON")
	require.Equal(t, uci.IndexAdmissionLanguageJSON, artifact.Profile.Language)
	require.Equal(t, uci.IndexAdmissionArtifactPartial, artifact.Status)
	preparedRequireExactAdmissionArtifact(t, artifact, preparedJSONYAMLAdmissionArtifact(t, preparedStructuredProfileKey("json"), source))
	require.NotEmpty(t, artifact.Definitions)
	require.NotEmpty(t, artifact.Chunks)
	require.True(t, preparedArtifactHasDiagnostic(artifact, "PARSE_ERROR"))
	preparedRequireSourceGroundedStructuredFacts(t, artifact)
	coverage := preparedCoverage(t, fixture.client.finalRequests[0])
	require.Equal(t, uci.IndexCoveragePartial, coverage.Lexical)
	require.Zero(t, coverage.ExcludedFiles)
}

func TestUCIPreparedIndexPublishesPartialOpenAPIFactsWithPathReason(t *testing.T) {
	fixture := newPreparedIndexFixture(t)
	source := []byte("{\"openapi\":\"3.0.3\",\"info\":{\"title\":\"Café\",\"version\":\"1\"},\"paths\":{\"/pets\":{\"get\":{\"responses\":{\"200\":{\"description\":\"OK\"}}}}},\"components\":")
	fixture.scanner.result.Files = []uci.ScannerFile{{
		Path:  "openapi.JSON",
		State: uci.IndexFilePresent,
		Body:  source,
	}}

	result, err := fixture.collaborator.IndexPreparedCodebase(context.Background(), fixture.target, fixture.root, fixture.client)
	require.NoError(t, err)
	require.Equal(t, 1, result.Uploaded)
	require.Equal(t, []string{"openapi.JSON: OpenAPI extraction is partial"}, result.Errors)
	require.Len(t, fixture.client.beginRequests, 1)
	require.Len(t, fixture.client.finalRequests, 1)

	frames := preparedFrames(t, fixture.client.stagePayloadSets[0])
	frame := frames[preparedFrameIndex(t, frames, "openapi.JSON")]
	preparedRequireMembership(t, frame, "openapi.JSON", uci.IndexAdmissionMembershipPresent, true)
	artifact := preparedArtifactForPath(t, frames, "openapi.JSON")
	require.Equal(t, uci.IndexAdmissionLanguageOpenAPI, artifact.Profile.Language)
	require.Equal(t, uci.IndexAdmissionArtifactPartial, artifact.Status)
	preparedRequireExactAdmissionArtifact(t, artifact, preparedOpenAPIAdmissionArtifact(t, preparedStructuredProfileKey("openapi-json"), source))
	require.NotEmpty(t, artifact.Definitions)
	require.NotEmpty(t, artifact.Chunks)
	require.True(t, preparedArtifactHasDiagnostic(artifact, "PARSE_ERROR"))
	preparedRequireSourceGroundedStructuredFacts(t, artifact)
	coverage := preparedCoverage(t, fixture.client.finalRequests[0])
	require.Equal(t, uci.IndexCoveragePartial, coverage.Lexical)
	require.Zero(t, coverage.ExcludedFiles)
}

func TestUCIPreparedIndexRejectsUnavailableOpenAPIBeforeBegin(t *testing.T) {
	fixture := newPreparedIndexFixture(t)
	fixture.scanner.result.Files = []uci.ScannerFile{{
		Path:  "openapi.JSON",
		State: uci.IndexFilePresent,
		Body:  []byte("{\"openapi\":\"2.0\",\"info\":{\"title\":\"Legacy\",\"version\":\"1\"},\"paths\":{}}"),
	}}

	result, err := fixture.collaborator.IndexPreparedCodebase(context.Background(), fixture.target, fixture.root, fixture.client)
	require.Nil(t, result)
	require.ErrorContains(t, err, "OpenAPI extraction is unavailable")
	require.Equal(t, 1, fixture.scanner.calls)
	require.Empty(t, fixture.client.beginRequests)
	require.Zero(t, fixture.client.stageCalls)
	require.Zero(t, fixture.client.finalizeCalls)
}

func TestUCIPreparedIndexRefusesStructuredArtifactCapacityBeforeBegin(t *testing.T) {
	fixture := newPreparedIndexFixture(t)
	fixture.scanner.result.Files = []uci.ScannerFile{{
		Path:  "too-large.MD",
		State: uci.IndexFilePresent,
		Body:  []byte(strings.Repeat("x", uci.IndexAdmissionMaxArtifactBodyBytes+1)),
	}}

	result, err := fixture.collaborator.IndexPreparedCodebase(context.Background(), fixture.target, fixture.root, fixture.client)
	preparedRequireCapacityFailureBeforeBegin(t, fixture, result, err, uci.IndexCapacityScopeArtifact, uci.IndexCapacityResourceArtifactBodyBytes)
}

func preparedStructuredProfileKey(capability string) string {
	return "uci-prepared-structured/v1:" + preparedProfileID + ":" + capability + ":v1"
}

func preparedMarkdownAdmissionArtifact(t *testing.T, profileKey string, source []byte) uci.IndexAdmissionArtifact {
	t.Helper()
	profile := uci.DefaultMarkdownExtractionProfile(profileKey)
	admissionProfile, err := uci.MarkdownIndexAdmissionArtifactProfile(profile)
	require.NoError(t, err)
	admissionProfile.ExtractionProfileDigest = uci.IndexDigest(preparedParserBundleDigest)
	artifact, err := uci.NewIndexAdmissionArtifactFromMarkdown(preparedSourceID, admissionProfile, profile, source, uci.ExtractMarkdown(source, profile))
	require.NoError(t, err)
	return artifact
}

func preparedJSONYAMLAdmissionArtifact(t *testing.T, profileKey string, source []byte) uci.IndexAdmissionArtifact {
	t.Helper()
	format := uci.JSONYAMLFormatJSON
	if strings.Contains(profileKey, ":yaml:") {
		format = uci.JSONYAMLFormatYAML
	}
	profile := uci.DefaultJSONYAMLExtractionProfile(profileKey, format)
	admissionProfile, err := uci.JSONYAMLIndexAdmissionArtifactProfile(profile)
	require.NoError(t, err)
	admissionProfile.ExtractionProfileDigest = uci.IndexDigest(preparedParserBundleDigest)
	artifact, err := uci.NewIndexAdmissionArtifactFromJSONYAML(preparedSourceID, admissionProfile, profile, source, uci.ExtractJSONYAML(source, profile))
	require.NoError(t, err)
	return artifact
}

func preparedSQLAdmissionArtifact(t *testing.T, profileKey string, source []byte) uci.IndexAdmissionArtifact {
	t.Helper()
	profile := uci.DefaultSQLExtractionProfile(profileKey)
	admissionProfile, err := uci.SQLIndexAdmissionArtifactProfile(profile)
	require.NoError(t, err)
	admissionProfile.ExtractionProfileDigest = uci.IndexDigest(preparedParserBundleDigest)
	artifact, err := uci.NewIndexAdmissionArtifactFromSQL(preparedSourceID, admissionProfile, profile, source, uci.ExtractSQL(source, profile))
	require.NoError(t, err)
	return artifact
}

func preparedOpenAPIAdmissionArtifact(t *testing.T, profileKey string, source []byte) uci.IndexAdmissionArtifact {
	t.Helper()
	format := uci.OpenAPIFormatJSON
	if strings.Contains(profileKey, ":openapi-yaml:") {
		format = uci.OpenAPIFormatYAML
	}
	profile := uci.DefaultOpenAPIExtractionProfile(profileKey, format)
	admissionProfile, err := uci.OpenAPIIndexAdmissionArtifactProfile(profile)
	require.NoError(t, err)
	admissionProfile.ExtractionProfileDigest = uci.IndexDigest(preparedParserBundleDigest)
	artifact, err := uci.NewIndexAdmissionArtifactFromOpenAPI(preparedSourceID, admissionProfile, profile, source, uci.ExtractOpenAPI(source, profile))
	require.NoError(t, err)
	return artifact
}

func preparedRequireExactAdmissionArtifact(t *testing.T, got, want uci.IndexAdmissionArtifact) {
	t.Helper()
	require.Equal(t, want.ArtifactID, got.ArtifactID)
	require.Equal(t, want.ContentDigest, got.ContentDigest)
	require.Equal(t, want.FactsDigest, got.FactsDigest)
	require.Equal(t, want.Profile, got.Profile)
	require.Equal(t, want.Status, got.Status)
	require.Equal(t, want.Body, got.Body)
	require.Equal(t, want.Definitions, got.Definitions)
	require.Equal(t, want.References, got.References)
	require.Equal(t, want.Chunks, got.Chunks)
	require.Equal(t, want.Diagnostics, got.Diagnostics)
}

func preparedRequireSourceGroundedStructuredFacts(t *testing.T, artifact uci.IndexAdmissionArtifact) {
	t.Helper()
	require.NotEmpty(t, artifact.Chunks)
	for _, chunk := range artifact.Chunks {
		preparedRequireExactBodySpan(t, artifact.Body, chunk.Span.ByteStart, chunk.Span.ByteEnd, chunk.Text)
	}
	for _, reference := range artifact.References {
		require.Equal(t, uci.IndexRelation("references"), reference.Relation)
		preparedRequireExactBodySpan(t, artifact.Body, reference.Span.ByteStart, reference.Span.ByteEnd, reference.RawTarget)
	}
}

func preparedRequireExactBodySpan(t *testing.T, body []byte, start, end int64, want string) {
	t.Helper()
	require.GreaterOrEqual(t, start, int64(0))
	require.GreaterOrEqual(t, end, start)
	require.LessOrEqual(t, end, int64(len(body)))
	require.Equal(t, want, string(body[start:end]))
}

func preparedOpenAPIJSONSource(title string) []byte {
	return []byte(fmt.Sprintf(`{"openapi":"3.0.3","info":{"title":%q,"version":"1.0.0"},"paths":{"/health":{"get":{"responses":{"200":{"description":"ok"}}}}}}`, title))
}

func preparedOpenAPIYAMLSource(title string) []byte {
	return []byte(fmt.Sprintf("openapi: 3.0.3\ninfo:\n  title: %s\n  version: 1.0.0\npaths:\n  /health:\n    get:\n      responses:\n        '200':\n          description: ok\n", title))
}

type preparedTreeSitterResponse struct {
	coverage     uci.IndexCoverageState
	bundleDigest uci.IndexDigest
	diagnostics  []uci.TreeSitterDiagnostic
	err          error
}

type preparedTreeSitterParser struct {
	responses map[uci.TreeSitterLanguage]preparedTreeSitterResponse
	requests  []uci.TreeSitterParseRequest
}

func (parser *preparedTreeSitterParser) Parse(ctx context.Context, request uci.TreeSitterParseRequest) (uci.TreeSitterArtifact, error) {
	if err := ctx.Err(); err != nil {
		return uci.TreeSitterArtifact{}, err
	}
	copy := request
	copy.Source = append([]byte(nil), request.Source...)
	parser.requests = append(parser.requests, copy)
	response, found := parser.responses[request.Language]
	if !found {
		return uci.TreeSitterArtifact{}, fmt.Errorf("unconfigured Tree-sitter language %q", request.Language)
	}
	if response.err != nil {
		return uci.TreeSitterArtifact{}, response.err
	}
	return preparedTreeSitterArtifact(request.Source, request.Language, response.coverage, response.bundleDigest, response.diagnostics), nil
}

func preparedTreeSitterArtifact(source []byte, language uci.TreeSitterLanguage, coverage uci.IndexCoverageState, bundleDigest uci.IndexDigest, diagnostics []uci.TreeSitterDiagnostic) uci.TreeSitterArtifact {
	if coverage == "" {
		coverage = uci.IndexCoverageComplete
	}
	if bundleDigest == "" {
		bundleDigest = preparedParserBundleDigest
	}
	chunks := []uci.TreeSitterChunk{}
	if len(source) != 0 {
		chunks = append(chunks, uci.TreeSitterChunk{
			Span:          preparedTreeSitterSpan(source, 0, len(source)),
			Text:          string(source),
			ContentDigest: preparedTreeSitterDigest(source),
		})
	}
	return uci.TreeSitterArtifact{
		Proof: uci.IndexArtifactProof{
			ArtifactID:         "88888888-8888-4888-8888-888888888888",
			ContentDigest:      preparedTreeSitterDigest(source),
			FactsDigest:        preparedTreeSitterDigest(append([]byte("facts:"), source...)),
			ChunkCount:         uint64(len(chunks)),
			DefinitionCount:    0,
			ReferenceSiteCount: 0,
		},
		Coverage:     coverage,
		Language:     language,
		BundleDigest: bundleDigest,
		Text:         string(source),
		Chunks:       chunks,
		Diagnostics:  append([]uci.TreeSitterDiagnostic(nil), diagnostics...),
	}
}

func preparedTreeSitterDigest(value []byte) uci.IndexDigest {
	sum := sha256.Sum256(value)
	return uci.IndexDigest("sha256:" + hex.EncodeToString(sum[:]))
}

func preparedTreeSitterSpan(source []byte, start, end int) uci.IndexSpan {
	lineEndOffset := start
	if end > start {
		lineEndOffset = end - 1
	}
	return uci.IndexSpan{
		ByteStart: int64(start),
		ByteEnd:   int64(end),
		LineStart: strings.Count(string(source[:start]), "\n") + 1,
		LineEnd:   strings.Count(string(source[:lineEndOffset]), "\n") + 1,
	}
}

func preparedArtifactForPath(t *testing.T, frames []uci.IndexAdmissionFrame, path string) uci.IndexAdmissionArtifact {
	t.Helper()
	artifactID := preparedArtifactIDForPath(frames, path)
	if artifactID == "" {
		require.Failf(t, "artifact missing", "path %q has no admitted artifact", path)
		return uci.IndexAdmissionArtifact{}
	}
	if artifact, found := preparedArtifactWithID(frames, artifactID); found {
		return artifact
	}
	require.Failf(t, "artifact missing", "path %q has no admitted artifact", path)
	return uci.IndexAdmissionArtifact{}
}

func preparedArtifactIDForPath(frames []uci.IndexAdmissionFrame, path string) string {
	for _, frame := range frames {
		for _, membership := range frame.Memberships {
			if membership.PathKey == path && membership.ArtifactID != nil {
				return *membership.ArtifactID
			}
		}
	}
	return ""
}

func preparedArtifactWithID(frames []uci.IndexAdmissionFrame, artifactID string) (uci.IndexAdmissionArtifact, bool) {
	for _, frame := range frames {
		for _, artifact := range frame.Artifacts {
			if artifact.ArtifactID == artifactID {
				return artifact, true
			}
		}
	}
	return uci.IndexAdmissionArtifact{}, false
}

func preparedArtifactHasDiagnostic(artifact uci.IndexAdmissionArtifact, code string) bool {
	for _, diagnostic := range artifact.Diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}
