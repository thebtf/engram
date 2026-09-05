package grpcserver

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/auth"
	gormstore "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/uci"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	uciRuntimeTestSource      = "11111111-1111-5111-8111-111111111111"
	uciRuntimeTestCheckout    = "22222222-2222-5222-8222-222222222222"
	uciRuntimeTestIncarnation = "33333333-3333-5333-8333-333333333333"
	uciRuntimeTestProfile     = "44444444-4444-5444-8444-444444444444"
	uciRuntimeTestBuild       = "55555555-5555-5555-8555-555555555555"
	uciRuntimeTestOwner       = "durable-runtime-owner"
)

type uciRuntimeProjectionCall struct {
	sourceID  string
	profileID string
	frames    []uci.IndexAdmissionFrame
}

type uciRuntimeBuildLookup struct {
	scope      uci.IndexScope
	profileID  string
	buildID    string
	leaseEpoch int64
}

type uciRuntimeProjectionFake struct {
	build          uci.IndexBuildRef
	returnedBuild  *uci.IndexBuildRef
	parts          []uci.IndexPart
	admissionCalls []uciRuntimeProjectionCall
	lookupCalls    []uciRuntimeBuildLookup
	afterLookup    func(context.Context)
	admitErr       error
	lookupErr      error
}

func (fake *uciRuntimeProjectionFake) AdmitIndexFrames(_ context.Context, sourceID, profileID string, frames []uci.IndexAdmissionFrame) ([]uci.IndexPart, error) {
	cloned := make([]uci.IndexAdmissionFrame, len(frames))
	for index, frame := range frames {
		cloned[index] = frame.Clone()
	}
	fake.admissionCalls = append(fake.admissionCalls, uciRuntimeProjectionCall{
		sourceID:  sourceID,
		profileID: profileID,
		frames:    cloned,
	})
	if fake.admitErr != nil {
		return nil, fake.admitErr
	}
	return append([]uci.IndexPart(nil), fake.parts...), nil
}

func (fake *uciRuntimeProjectionFake) LoadIndexBuildRef(ctx context.Context, scope uci.IndexScope, profileID, buildID string, leaseEpoch int64) (uci.IndexBuildRef, error) {
	fake.lookupCalls = append(fake.lookupCalls, uciRuntimeBuildLookup{
		scope:      scope,
		profileID:  profileID,
		buildID:    buildID,
		leaseEpoch: leaseEpoch,
	})
	if fake.lookupErr != nil {
		return uci.IndexBuildRef{}, fake.lookupErr
	}
	if scope != fake.build.Scope || profileID != uciRuntimeTestProfile || buildID != fake.build.BuildID || leaseEpoch != fake.build.LeaseEpoch {
		return uci.IndexBuildRef{}, errors.New("unexpected durable build lookup")
	}
	if fake.afterLookup != nil {
		fake.afterLookup(ctx)
	}
	if fake.returnedBuild != nil {
		return *fake.returnedBuild, nil
	}
	return fake.build, nil
}

var _ uciRuntimeProjectionStore = (*uciRuntimeProjectionFake)(nil)

type uciRuntimePublisherFake struct {
	beginResult    uci.IndexBeginResult
	published      uci.IndexPublishedView
	beginCalls     []uci.IndexBeginInput
	beginCallers   []uci.IndexCaller
	stageCalls     []uci.IndexStageInput
	stageCallers   []uci.IndexCaller
	finalizeCalls  []uci.IndexFinalizeInput
	finalizeCaller []uci.IndexCaller
	stageCheck     func(uci.IndexStageInput) error
}

func (fake *uciRuntimePublisherFake) Begin(_ context.Context, caller uci.IndexCaller, input uci.IndexBeginInput) (uci.IndexBeginResult, error) {
	fake.beginCallers = append(fake.beginCallers, caller)
	fake.beginCalls = append(fake.beginCalls, input)
	return fake.beginResult, nil
}

func (fake *uciRuntimePublisherFake) Stage(_ context.Context, caller uci.IndexCaller, input uci.IndexStageInput) (uci.IndexPartAck, error) {
	if fake.stageCheck != nil {
		if err := fake.stageCheck(input); err != nil {
			return uci.IndexPartAck{}, err
		}
	}
	computed, err := uci.DigestIndexPart(input.Part)
	if err != nil || computed != input.Digest {
		return uci.IndexPartAck{}, errors.New("noncanonical staged part")
	}
	fake.stageCallers = append(fake.stageCallers, caller)
	fake.stageCalls = append(fake.stageCalls, input)
	return uci.IndexPartAck{BuildID: input.Build.BuildID, Sequence: input.Sequence, Digest: input.Digest}, nil
}

func (fake *uciRuntimePublisherFake) Finalize(_ context.Context, caller uci.IndexCaller, input uci.IndexFinalizeInput) (uci.IndexPublishedView, error) {
	fake.finalizeCaller = append(fake.finalizeCaller, caller)
	fake.finalizeCalls = append(fake.finalizeCalls, input)
	return fake.published, nil
}

var _ uci.IndexStore = (*uciRuntimePublisherFake)(nil)

func TestContextAwareUCIRuntimeCompositionRequiresAllStoreHandles(t *testing.T) {
	contexts := &gormstore.UCIContextStore{}
	projections := &gormstore.UCIProjectionStore{}
	publisher := &uciRuntimePublisherFake{}

	for name, configure := range map[string]func(**gormstore.UCIContextStore, **gormstore.UCIProjectionStore, *uci.IndexStore){
		"complete": func(_ **gormstore.UCIContextStore, _ **gormstore.UCIProjectionStore, _ *uci.IndexStore) {},
		"missing context catalog": func(contexts **gormstore.UCIContextStore, _ **gormstore.UCIProjectionStore, _ *uci.IndexStore) {
			*contexts = nil
		},
		"missing projection store": func(_ **gormstore.UCIContextStore, projections **gormstore.UCIProjectionStore, _ *uci.IndexStore) {
			*projections = nil
		},
		"missing publication store": func(_ **gormstore.UCIContextStore, _ **gormstore.UCIProjectionStore, publisher *uci.IndexStore) {
			*publisher = nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			configuredContexts := contexts
			configuredProjections := projections
			var configuredPublisher uci.IndexStore = publisher
			configure(&configuredContexts, &configuredProjections, &configuredPublisher)

			runtime, err := NewContextAwareUCIRuntime(configuredContexts, configuredProjections, configuredPublisher)
			if name == "complete" {
				require.NoError(t, err)
				require.IsType(t, &contextAwareUCIRuntime{}, runtime)
				return
			}
			require.Nil(t, runtime)
			require.ErrorIs(t, err, errUCIContextRuntimeUnavailable)
		})
	}
}

func TestContextAwareUCIRuntimeErrorMappingPreservesOnlyClosedBridgeStatuses(t *testing.T) {
	for _, test := range []struct {
		name    string
		code    codes.Code
		message string
	}{
		{name: "context required", code: codes.FailedPrecondition, message: string(uci.ContextRequired)},
		{name: "context mismatch", code: codes.FailedPrecondition, message: string(uci.ContextMismatch)},
		{name: "permission denied", code: codes.PermissionDenied, message: string(uci.PermissionDenied)},
	} {
		t.Run(test.name, func(t *testing.T) {
			mapped := contextAwareRuntimeError(context.Background(), status.Error(test.code, test.message))
			require.Equal(t, test.code, status.Code(mapped))
			require.Equal(t, test.message, status.Convert(mapped).Message())
		})
	}

	mapped := contextAwareRuntimeError(context.Background(), status.Error(codes.FailedPrecondition, "private bridge detail"))
	require.Equal(t, codes.Internal, status.Code(mapped))
	require.Equal(t, "UCI context runtime failed", status.Convert(mapped).Message())
	require.NotContains(t, mapped.Error(), "private bridge detail")
}

func TestContextAwareUCIRuntimeStageAdmitsCompleteBuildBeforeStaging(t *testing.T) {
	binding := uciRuntimeTestBinding()
	frames, parts := uciRuntimeSplitFrames(t, binding)
	projection := &uciRuntimeProjectionFake{
		build: uciRuntimeTestBuildRef(binding),
		parts: parts,
	}
	publisher := &uciRuntimePublisherFake{}
	publisher.stageCheck = func(_ uci.IndexStageInput) error {
		if len(projection.admissionCalls) != 1 || len(projection.admissionCalls[0].frames) != len(frames) {
			return errors.New("stage ran before the complete build was admitted")
		}
		return nil
	}
	runtime := uciRuntimeTestRuntime(projection, publisher)
	requestFrames := uciRuntimeStageFrames(t, binding, projection.build, frames)

	response, err := runtime.StageCodeIndex(uciRuntimeTestContext(), binding, requestFrames)
	require.NoError(t, err)
	require.Len(t, projection.admissionCalls, 1)
	require.Equal(t, binding.Scope.SourceID, projection.admissionCalls[0].sourceID)
	require.Equal(t, binding.ProfileID, projection.admissionCalls[0].profileID)
	require.Len(t, projection.admissionCalls[0].frames, 2)
	require.Len(t, publisher.stageCalls, 2)
	require.Len(t, publisher.stageCallers, 2)
	for index, caller := range publisher.stageCallers {
		require.Equalf(t, uciRuntimeTestOwner, caller.OwnerInstance, "stage %d used a non-durable owner", index)
	}
	require.Len(t, projection.lookupCalls, 1)
	require.Equal(t, binding.Scope, projection.lookupCalls[0].scope)
	require.Equal(t, binding.ProfileID, projection.lookupCalls[0].profileID)
	require.Equal(t, projection.build.BuildID, projection.lookupCalls[0].buildID)
	require.Equal(t, projection.build.LeaseEpoch, projection.lookupCalls[0].leaseEpoch)

	acks := make([]uci.IndexPartAck, len(publisher.stageCalls))
	for index, call := range publisher.stageCalls {
		acks[index] = uci.IndexPartAck{BuildID: call.Build.BuildID, Sequence: call.Sequence, Digest: call.Digest}
	}
	expectedDigest, err := uci.DigestIndexParts(acks)
	require.NoError(t, err)
	require.Equal(t, string(expectedDigest), response.GetPartDigest())
	require.Equal(t, uint64(2), response.GetAcceptedPartCount())
	require.Equal(t, uint64(1), response.GetAcceptedSequence())
}

func TestContextAwareUCIRuntimeRejectsInvalidPackedAdmissionBeforePersistence(t *testing.T) {
	binding := uciRuntimeTestBinding()
	frames, parts := uciRuntimeSplitFrames(t, binding)

	for name, mutate := range map[string]func([]*pb.StageCodeIndexFrame){
		"payload digest": func(requestFrames []*pb.StageCodeIndexFrame) {
			requestFrames[0].PayloadDigest = string(uci.DigestIndexAdmissionPayload([]byte("different payload")))
		},
		"wrong bound source": func(requestFrames []*pb.StageCodeIndexFrame) {
			wrongFrame := uciRuntimeAdmissionFrame(t, uuid.NewString(), binding.ProfileID, "wrong.go", "package wrong\n\nfunc Wrong() {}\n")
			payload, err := uci.EncodeIndexAdmissionFrame(wrongFrame)
			require.NoError(t, err)
			requestFrames[0].Payload = payload
			requestFrames[0].PayloadDigest = string(uci.DigestIndexAdmissionPayload(payload))
		},
		"wrong profile": func(requestFrames []*pb.StageCodeIndexFrame) {
			wrongFrame := frames[0].Clone()
			wrongFrame.Profile.ID = uuid.NewString()
			payload, err := uci.EncodeIndexAdmissionFrame(wrongFrame)
			require.NoError(t, err)
			requestFrames[0].Payload = payload
			requestFrames[0].PayloadDigest = string(uci.DigestIndexAdmissionPayload(payload))
		},
		"duplicate artifact": func(requestFrames []*pb.StageCodeIndexFrame) {
			payload, err := uci.EncodeIndexAdmissionFrame(frames[0])
			require.NoError(t, err)
			requestFrames[1].Payload = payload
			requestFrames[1].PayloadDigest = string(uci.DigestIndexAdmissionPayload(payload))
		},
	} {
		t.Run(name, func(t *testing.T) {
			projection := &uciRuntimeProjectionFake{build: uciRuntimeTestBuildRef(binding), parts: parts}
			publisher := &uciRuntimePublisherFake{}
			runtime := uciRuntimeTestRuntime(projection, publisher)
			requestFrames := uciRuntimeStageFrames(t, binding, projection.build, frames)
			mutate(requestFrames)

			response, err := runtime.StageCodeIndex(uciRuntimeTestContext(), binding, requestFrames)
			require.Nil(t, response)
			require.Error(t, err)
			require.Empty(t, projection.lookupCalls)
			require.Empty(t, projection.admissionCalls)
			require.Empty(t, publisher.stageCalls)
		})
	}
}

func TestContextAwareUCIRuntimeRejectsMismatchedDurableBuildBeforeAdmission(t *testing.T) {
	binding := uciRuntimeTestBinding()
	frames, parts := uciRuntimeSplitFrames(t, binding)

	for name, mutate := range map[string]func(*uci.IndexBuildRef){
		"build ID":    func(build *uci.IndexBuildRef) { build.BuildID = uuid.NewString() },
		"scope":       func(build *uci.IndexBuildRef) { build.Scope.IncarnationID = uuid.NewString() },
		"lease epoch": func(build *uci.IndexBuildRef) { build.LeaseEpoch++ },
		"lease owner": func(build *uci.IndexBuildRef) { build.OwnerInstance = "" },
	} {
		t.Run(name, func(t *testing.T) {
			projection := &uciRuntimeProjectionFake{build: uciRuntimeTestBuildRef(binding), parts: parts}
			returned := projection.build
			mutate(&returned)
			projection.returnedBuild = &returned
			publisher := &uciRuntimePublisherFake{}
			runtime := uciRuntimeTestRuntime(projection, publisher)

			response, err := runtime.StageCodeIndex(uciRuntimeTestContext(), binding, uciRuntimeStageFrames(t, binding, projection.build, frames))
			require.Nil(t, response)
			require.ErrorIs(t, err, errUCIContextRuntimeInvalid)
			require.Len(t, projection.lookupCalls, 1)
			require.Empty(t, projection.admissionCalls)
			require.Empty(t, publisher.stageCalls)
		})
	}
}

func TestContextAwareUCIRuntimeBeginAndFinalizeFenceOwnerAndInitialParent(t *testing.T) {
	binding := uciRuntimeTestBinding()
	_, parts := uciRuntimeSplitFrames(t, binding)
	build := uciRuntimeTestBuildRef(binding)
	projection := &uciRuntimeProjectionFake{build: build, parts: parts}
	publisher := &uciRuntimePublisherFake{
		beginResult: uci.IndexBeginResult{
			Build:          build,
			LeaseExpiresAt: time.Date(2026, time.September, 5, 12, 0, 0, 0, time.UTC),
		},
		published: uci.IndexPublishedView{
			BuildID: build.BuildID,
			Context: uci.ContextRef{
				SourceID:          binding.Scope.SourceID,
				CheckoutID:        binding.Scope.CheckoutID,
				ViewID:            "66666666-6666-5666-8666-666666666666",
				AnalysisProfileID: binding.ProfileID,
				Generation:        1,
			},
			ManifestDigest: uciRuntimeDigest(t, "published-manifest"),
			AcceptedFSSeq:  7,
			PublishedAt:    time.Date(2026, time.September, 5, 12, 1, 0, 0, time.UTC),
		},
	}
	runtime := uciRuntimeTestRuntime(projection, publisher)
	ctx := uciRuntimeTestContext()

	begin, err := runtime.BeginCodeIndex(ctx, binding, &pb.BeginCodeIndexRequest{
		Scope:         contextAwareProtoIndexScope(binding.Scope, binding.ProfileID),
		OwnerInstance: uciRuntimeTestOwner,
		BuildKey:      "initial-runtime-build",
		ManifestMode:  string(uci.IndexManifestFull),
		JobKind:       string(uci.IndexJobInitial),
	})
	require.NoError(t, err)
	require.Equal(t, build.BuildID, begin.GetBuildId())
	require.Len(t, publisher.beginCalls, 1)
	require.Nil(t, publisher.beginCalls[0].ExpectedParent)
	require.Equal(t, uciRuntimeTestOwner, publisher.beginCallers[0].OwnerInstance)

	request := uciRuntimeFinalizeRequest(t, binding, build, parts)
	publisher.published.ManifestDigest = uci.IndexDigest(request.GetManifestDigest())
	finalized, err := runtime.FinalizeCodeIndex(ctx, binding, request)
	require.NoError(t, err)
	require.Equal(t, build.BuildID, finalized.GetBuildId())
	require.Len(t, projection.lookupCalls, 1)
	require.Equal(t, binding.Scope, projection.lookupCalls[0].scope)
	require.Equal(t, build.LeaseEpoch, projection.lookupCalls[0].leaseEpoch)
	require.Len(t, publisher.finalizeCalls, 1)
	require.Nil(t, publisher.finalizeCalls[0].ExpectedParent)
	require.Equal(t, request.GetPartsDigest(), string(publisher.finalizeCalls[0].Manifest.PartsDigest))
	require.Equal(t, request.GetManifestDigest(), string(publisher.finalizeCalls[0].Manifest.ManifestDigest))
	require.Equal(t, request.GetEdgesDigest(), string(publisher.finalizeCalls[0].Manifest.EdgesDigest))
	require.Equal(t, uciRuntimeTestOwner, publisher.finalizeCaller[0].OwnerInstance)
	require.Equal(t, request.GetHeadOid(), *publisher.finalizeCalls[0].Manifest.Observation.HeadOID)
	require.Equal(t, request.GetObjectFormat(), *publisher.finalizeCalls[0].Manifest.Observation.ObjectFormat)
	require.Equal(t, request.GetRefLabel(), *publisher.finalizeCalls[0].Manifest.Observation.RefLabel)
	require.Equal(t, request.GetDirty(), publisher.finalizeCalls[0].Manifest.Observation.Dirty)
}

func TestContextAwareUCIRuntimeFinalizeKeepsPersistedParentAcrossBindingRefresh(t *testing.T) {
	binding := uciRuntimeTestBinding()
	parent := uci.ContextRef{
		SourceID:          binding.Scope.SourceID,
		CheckoutID:        binding.Scope.CheckoutID,
		ViewID:            "66666666-6666-5666-8666-666666666666",
		AnalysisProfileID: binding.ProfileID,
		Generation:        1,
	}
	binding.Context = &parent
	refreshedBinding := binding.Clone()
	refreshedBinding.Context = &uci.ContextRef{
		SourceID:          binding.Scope.SourceID,
		CheckoutID:        binding.Scope.CheckoutID,
		ViewID:            "77777777-7777-5777-8777-777777777777",
		AnalysisProfileID: binding.ProfileID,
		Generation:        2,
	}
	build := uciRuntimeTestBuildRef(binding)
	_, parts := uciRuntimeSplitFrames(t, binding)
	request := uciRuntimeFinalizeRequest(t, binding, build, parts)
	request.ExpectedParent = contextAwareProtoContextRef(parent)
	published := *refreshedBinding.Context
	published.ViewID = uuid.NewString()
	published.Generation = 3
	projection := &uciRuntimeProjectionFake{build: build, parts: parts}
	publisher := &uciRuntimePublisherFake{published: uci.IndexPublishedView{
		BuildID:        build.BuildID,
		Context:        published,
		ManifestDigest: uci.IndexDigest(request.GetManifestDigest()),
		AcceptedFSSeq:  int64(request.GetObservedFilesystemSequence()),
		PublishedAt:    time.Date(2026, time.September, 5, 12, 1, 0, 0, time.UTC),
	}}
	runtime := uciRuntimeTestRuntime(projection, publisher)

	response, err := runtime.FinalizeCodeIndex(uciRuntimeTestContext(), refreshedBinding, request)
	require.NoError(t, err)
	require.Equal(t, published.ViewID, response.GetPublishedContext().GetViewId())
	require.Len(t, publisher.finalizeCalls, 1)
	require.Equal(t, parent, *publisher.finalizeCalls[0].ExpectedParent)
}

func TestContextAwareUCIRuntimeRejectsUnclosedFinalizeBeforePersistence(t *testing.T) {
	binding := uciRuntimeTestBinding()
	_, parts := uciRuntimeSplitFrames(t, binding)
	build := uciRuntimeTestBuildRef(binding)

	for name, mutate := range map[string]func(*pb.FinalizeCodeIndexRequest){
		"incomplete census": func(request *pb.FinalizeCodeIndexRequest) { request.CompleteCensus = false },
		"failed scan":       func(request *pb.FinalizeCodeIndexRequest) { request.ScanOutcome = string(uci.IndexScanFailed) },
		"unknown coverage": func(request *pb.FinalizeCodeIndexRequest) {
			request.CoverageJson = []byte(`{"structural":"complete","lexical":"complete","vector":"unavailable","unexpected":true}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			projection := &uciRuntimeProjectionFake{build: build, parts: parts}
			publisher := &uciRuntimePublisherFake{}
			runtime := uciRuntimeTestRuntime(projection, publisher)
			request := uciRuntimeFinalizeRequest(t, binding, build, parts)
			mutate(request)

			response, err := runtime.FinalizeCodeIndex(uciRuntimeTestContext(), binding, request)
			require.Nil(t, response)
			require.Error(t, err)
			require.Empty(t, projection.lookupCalls)
			require.Empty(t, publisher.finalizeCalls)
		})
	}
}

func TestContextAwareUCIRuntimeRejectsMismatchedPublishedContext(t *testing.T) {
	binding := uciRuntimeTestBinding()
	_, parts := uciRuntimeSplitFrames(t, binding)
	build := uciRuntimeTestBuildRef(binding)
	request := uciRuntimeFinalizeRequest(t, binding, build, parts)
	projection := &uciRuntimeProjectionFake{build: build, parts: parts}
	publisher := &uciRuntimePublisherFake{published: uci.IndexPublishedView{
		BuildID: build.BuildID,
		Context: uci.ContextRef{
			SourceID:          uuid.NewString(),
			CheckoutID:        binding.Scope.CheckoutID,
			ViewID:            "66666666-6666-5666-8666-666666666666",
			AnalysisProfileID: binding.ProfileID,
			Generation:        1,
		},
		ManifestDigest: uci.IndexDigest(request.GetManifestDigest()),
		AcceptedFSSeq:  int64(request.GetObservedFilesystemSequence()),
		PublishedAt:    time.Date(2026, time.September, 5, 12, 1, 0, 0, time.UTC),
	}}
	runtime := uciRuntimeTestRuntime(projection, publisher)

	response, err := runtime.FinalizeCodeIndex(uciRuntimeTestContext(), binding, request)
	require.Nil(t, response)
	require.ErrorIs(t, err, errUCIContextRuntimeInvalid)
	require.Len(t, projection.lookupCalls, 1)
	require.Len(t, publisher.finalizeCalls, 1)
}

func TestContextAwareUCIRuntimeCancellationAndExposureStayClosed(t *testing.T) {
	binding := uciRuntimeTestBinding()
	frames, parts := uciRuntimeSplitFrames(t, binding)
	projection := &uciRuntimeProjectionFake{build: uciRuntimeTestBuildRef(binding), parts: parts}
	publisher := &uciRuntimePublisherFake{}
	runtime := uciRuntimeTestRuntime(projection, publisher)
	ctx, cancel := context.WithCancel(uciRuntimeTestContext())
	cancel()

	response, err := runtime.StageCodeIndex(ctx, binding, uciRuntimeStageFrames(t, binding, projection.build, frames))
	require.Nil(t, response)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, projection.lookupCalls)
	require.Empty(t, projection.admissionCalls)
	require.Empty(t, publisher.stageCalls)

	query, err := runtime.QueryCode(context.Background(), uci.AuthorizedContext{}, nil)
	require.Nil(t, query)
	require.ErrorIs(t, err, errUCIContextRuntimeExposure)
	explore, err := runtime.ExploreCode(context.Background(), uci.AuthorizedContext{}, nil)
	require.Nil(t, explore)
	require.ErrorIs(t, err, errUCIContextRuntimeExposure)
	legacy, err := runtime.LegacyCodeIndexNegotiate(context.Background(), uci.AuthorizedContext{}, nil)
	require.Nil(t, legacy)
	require.ErrorIs(t, err, errUCIContextRuntimeLegacy)
}

func uciRuntimeTestRuntime(projection uciRuntimeProjectionStore, publisher uci.IndexStore) *contextAwareUCIRuntime {
	return &contextAwareUCIRuntime{
		contexts:    &gormstore.UCIContextStore{},
		projections: projection,
		publisher:   publisher,
	}
}

func uciRuntimeTestBinding() uci.IndexBinding {
	return uci.IndexBinding{
		Scope: uci.IndexScope{
			SourceID:      uciRuntimeTestSource,
			CheckoutID:    uciRuntimeTestCheckout,
			IncarnationID: uciRuntimeTestIncarnation,
		},
		ProfileID:     uciRuntimeTestProfile,
		LocalRootID:   "runtime-test-root",
		WorkstationID: "runtime-test-workstation",
	}
}

func uciRuntimeTestBuildRef(binding uci.IndexBinding) uci.IndexBuildRef {
	return uci.IndexBuildRef{
		BuildID:       uciRuntimeTestBuild,
		Scope:         binding.Scope,
		OwnerInstance: uciRuntimeTestOwner,
		LeaseEpoch:    9,
	}
}

func uciRuntimeTestContext() context.Context {
	identity := auth.ClientWithPrincipal("read-write", "runtime-test-workstation", "agent/runtime-test", auth.PrincipalKindAgent)
	ctx := auth.WithIdentity(context.Background(), identity)
	return metadata.NewIncomingContext(ctx, metadata.Pairs(auditcontext.SourceSessionMetadataKey, "runtime-test-session"))
}

func uciRuntimeFinalizeRequest(t *testing.T, binding uci.IndexBinding, build uci.IndexBuildRef, parts []uci.IndexPart) *pb.FinalizeCodeIndexRequest {
	t.Helper()
	acks := make([]uci.IndexPartAck, len(parts))
	memberships := make([]uci.IndexMembership, 0)
	replacements := make([]uci.IndexEdgeReplacement, 0)
	var edgeCount uint64
	for index, part := range parts {
		digest, err := uci.DigestIndexPart(part)
		require.NoError(t, err)
		acks[index] = uci.IndexPartAck{BuildID: build.BuildID, Sequence: uint32(index), Digest: digest}
		memberships = append(memberships, part.Memberships...)
		replacements = append(replacements, part.EdgeReplacements...)
		for _, replacement := range part.EdgeReplacements {
			edgeCount += uint64(len(replacement.Edges))
		}
	}
	partsDigest, err := uci.DigestIndexParts(acks)
	require.NoError(t, err)
	manifestDigest, err := uci.DigestIndexManifest(memberships)
	require.NoError(t, err)
	edgesDigest, err := uci.DigestIndexEdges(replacements)
	require.NoError(t, err)
	scanStart := time.Date(2026, time.September, 5, 12, 0, 0, 0, time.UTC)
	headOID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	objectFormat := "sha1"
	refLabel := "refs/heads/runtime-test"
	dirty := true
	return &pb.FinalizeCodeIndexRequest{
		Scope:                      contextAwareProtoIndexScope(binding.Scope, binding.ProfileID),
		BuildId:                    build.BuildID,
		LeaseEpoch:                 uint64(build.LeaseEpoch),
		ManifestPartCount:          uint64(len(parts)),
		PartsDigest:                string(partsDigest),
		ManifestEntryCount:         uint64(len(memberships)),
		ManifestDigest:             string(manifestDigest),
		EdgeCount:                  edgeCount,
		EdgesDigest:                string(edgesDigest),
		ScanOutcome:                string(uci.IndexScanComplete),
		CompleteCensus:             true,
		ObservedFilesystemSequence: 7,
		ScanStartedAt:              timestamppb.New(scanStart),
		ScanCompletedAt:            timestamppb.New(scanStart.Add(time.Second)),
		CoverageJson:               []byte(`{"structural":"complete","lexical":"complete","vector":"unavailable","excluded_files":0,"unreadable_files":0,"unresolved_references":0}`),
		HeadOid:                    &headOID,
		ObjectFormat:               &objectFormat,
		RefLabel:                   &refLabel,
		Dirty:                      &dirty,
	}
}

func uciRuntimeStageFrames(t *testing.T, binding uci.IndexBinding, build uci.IndexBuildRef, admissions []uci.IndexAdmissionFrame) []*pb.StageCodeIndexFrame {
	t.Helper()
	frames := make([]*pb.StageCodeIndexFrame, len(admissions))
	for index, admission := range admissions {
		payload, err := uci.EncodeIndexAdmissionFrame(admission)
		require.NoError(t, err)
		frames[index] = &pb.StageCodeIndexFrame{
			Scope:         contextAwareProtoIndexScope(binding.Scope, binding.ProfileID),
			BuildId:       build.BuildID,
			LeaseEpoch:    uint64(build.LeaseEpoch),
			Sequence:      uint64(index),
			Payload:       payload,
			PayloadDigest: string(uci.DigestIndexAdmissionPayload(payload)),
		}
	}
	return frames
}

func uciRuntimeSplitFrames(t *testing.T, binding uci.IndexBinding) ([]uci.IndexAdmissionFrame, []uci.IndexPart) {
	t.Helper()
	source := uciRuntimeAdmissionFrame(t, binding.Scope.SourceID, binding.ProfileID, "source.go", "package source\n\nfunc Caller() {\n\tTarget()\n}\n")
	target := uciRuntimeAdmissionFrame(t, binding.Scope.SourceID, binding.ProfileID, "target.go", "package target\n\nfunc Target() {}\n")
	require.NotEmpty(t, source.Artifacts[0].References)
	reference := source.Artifacts[0].References[0]
	require.NotNil(t, reference.OwnerSymbolKey)
	sourceSymbol := *reference.OwnerSymbolKey
	targetID := target.Artifacts[0].ArtifactID
	targetSymbol := "func:Target"
	source.EdgeReplacements = []uci.IndexAdmissionEdgeReplacement{{
		SourcePath: "source.go",
		Edges: []uci.IndexAdmissionEdge{{
			EdgeKey:          "source-to-target",
			SourceArtifactID: source.Artifacts[0].ArtifactID,
			SourceSymbolKey:  &sourceSymbol,
			Target: &uci.IndexAdmissionEdgeTarget{
				PathKey:    "target.go",
				ArtifactID: targetID,
				SymbolKey:  &targetSymbol,
			},
			Relation:         reference.Relation,
			EvidenceKind:     uci.IndexEvidenceKind("extracted"),
			ResolutionState:  uci.IndexResolutionState("resolved"),
			ResolverRevision: "runtime-test-resolver/v1",
			Evidence: uci.IndexAdmissionEdgeEvidence{
				ReferenceSiteKey: reference.SiteKey,
				Span:             reference.Span,
				RuleKey:          "runtime-source-fact",
				Explanation:      "packed admission preserves a cross-frame target",
			},
		}},
	}}
	target.EdgeReplacements = []uci.IndexAdmissionEdgeReplacement{{SourcePath: "target.go"}}

	sourcePart, err := source.PublicationPart()
	require.NoError(t, err)
	targetPart, err := target.PublicationPart()
	require.NoError(t, err)
	return []uci.IndexAdmissionFrame{source, target}, []uci.IndexPart{sourcePart, targetPart}
}

func uciRuntimeAdmissionFrame(t *testing.T, sourceID, profileID, path, source string) uci.IndexAdmissionFrame {
	t.Helper()
	goProfile := uci.GoExtractionProfile{ProfileKey: "runtime-admission", ParserKey: "runtime-go-parser"}
	profile, err := uci.GoIndexAdmissionArtifactProfile(goProfile)
	require.NoError(t, err)
	body := []byte(source)
	artifact, err := uci.NewIndexAdmissionArtifactFromGo(sourceID, profile, body, uci.ExtractGo(body, goProfile))
	require.NoError(t, err)
	artifactID := artifact.ArtifactID
	return uci.IndexAdmissionFrame{
		Version:   uci.IndexAdmissionFrameVersion,
		Profile:   uci.IndexAdmissionProfile{ID: profileID},
		Artifacts: []uci.IndexAdmissionArtifact{artifact},
		Memberships: []uci.IndexAdmissionMembership{{
			PathKey:     path,
			DisplayPath: path,
			Mode:        "100644",
			State:       uci.IndexAdmissionMembershipPresent,
			ArtifactID:  &artifactID,
		}},
	}
}

func uciRuntimeDigest(t *testing.T, value string) uci.IndexDigest {
	t.Helper()
	part, err := uci.DigestIndexPart(uci.IndexPart{
		Memberships: []uci.IndexMembership{{
			PathKey:     value,
			DisplayPath: value,
			Mode:        "100644",
			State:       uci.IndexFileExcluded,
		}},
	})
	require.NoError(t, err)
	return part
}
