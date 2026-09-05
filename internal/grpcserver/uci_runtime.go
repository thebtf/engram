package grpcserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"time"

	gormstore "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/uci"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var (
	errUCIContextRuntimeUnavailable = errors.New("UCI context runtime is not configured")
	errUCIContextRuntimeLegacy      = errors.New("UCI legacy index negotiation is unavailable")
	errUCIContextRuntimeExposure    = errors.New("UCI recorder-owned response is unavailable")
	errUCIContextRuntimeInvalid     = errors.New("UCI context runtime request is invalid")
)

// uciRuntimeProjectionStore is the narrow persistence boundary needed by the
// scoped runtime. The concrete store remains the production implementation.
type uciRuntimeProjectionStore interface {
	AdmitIndexFrames(context.Context, string, string, []uci.IndexAdmissionFrame) ([]uci.IndexPart, error)
	LoadIndexBuildRef(context.Context, uci.IndexScope, string, string, int64) (uci.IndexBuildRef, error)
}

// contextAwareUCIRuntime binds server-owned UCI persistence to an already
// authorized IndexBinding. It never accepts a root, project selector, path, or
// client-invented View as authority.
type contextAwareUCIRuntime struct {
	contexts    *gormstore.UCIContextStore
	projections uciRuntimeProjectionStore
	publisher   uci.IndexStore
}

// NewContextAwareUCIRuntime constructs the concrete scoped UCI runtime. Query,
// Explore, and legacy negotiation intentionally remain unavailable until their
// respective recorder-owned response boundaries are wired.
func NewContextAwareUCIRuntime(contexts *gormstore.UCIContextStore, projections *gormstore.UCIProjectionStore, publisher uci.IndexStore) (ContextAwareUCIRuntime, error) {
	if contexts == nil || projections == nil || publisher == nil {
		return nil, errUCIContextRuntimeUnavailable
	}
	return &contextAwareUCIRuntime{
		contexts:    contexts,
		projections: projections,
		publisher:   publisher,
	}, nil
}

var _ ContextAwareUCIRuntime = (*contextAwareUCIRuntime)(nil)

// LoadIndexBinding reloads only a server-owned binding selected by an authorized
// transport caller. It has no path-derived fallback.
func (runtime *contextAwareUCIRuntime) LoadIndexBinding(ctx context.Context, selector uci.IndexBindingSelector) (uci.IndexBinding, error) {
	if err := runtime.require(ctx); err != nil {
		return uci.IndexBinding{}, err
	}
	return runtime.contexts.LoadIndexBinding(ctx, selector.Clone())
}

func (runtime *contextAwareUCIRuntime) LegacyCodeIndexNegotiate(ctx context.Context, _ uci.AuthorizedContext, _ *pb.CodeIndexNegotiateRequest) (*pb.CodeIndexNegotiateResponse, error) {
	if err := runtime.require(ctx); err != nil {
		return nil, err
	}
	return nil, errUCIContextRuntimeLegacy
}

func (runtime *contextAwareUCIRuntime) BeginCodeIndex(ctx context.Context, binding uci.IndexBinding, request *pb.BeginCodeIndexRequest) (*pb.BeginCodeIndexResponse, error) {
	if err := runtime.require(ctx); err != nil {
		return nil, err
	}
	binding, err := uciRuntimeBinding(binding)
	if err != nil {
		return nil, err
	}
	if request == nil || !contextAwareIndexScopeMatchesBinding(request.GetScope(), binding) ||
		!uciRuntimeExpectedParentMatchesBinding(request.GetExpectedParent(), binding) {
		return nil, errUCIContextRuntimeInvalid
	}
	caller, err := uciRuntimeCaller(ctx, request.GetOwnerInstance())
	if err != nil {
		return nil, err
	}
	mode, err := uciRuntimeManifestMode(request.GetManifestMode())
	if err != nil {
		return nil, err
	}
	kind, err := uciRuntimeJobKind(request.GetJobKind())
	if err != nil {
		return nil, err
	}
	if (binding.Context == nil && kind != uci.IndexJobInitial) ||
		(binding.Context != nil && kind != uci.IndexJobReconcile) {
		return nil, errUCIContextRuntimeInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	result, err := runtime.publisher.Begin(ctx, caller, uci.IndexBeginInput{
		BuildKey:       request.GetBuildKey(),
		Scope:          binding.Scope,
		ProfileID:      binding.ProfileID,
		ExpectedParent: binding.Context,
		Mode:           mode,
		JobKind:        kind,
	})
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if result.Build.Scope != binding.Scope || result.Build.OwnerInstance != caller.OwnerInstance ||
		!validUCIUUID(result.Build.BuildID) || !validUCIIdentifier(result.Build.OwnerInstance, maxUCITransportIdentifierBytes) ||
		result.Build.LeaseEpoch <= 0 || result.LeaseExpiresAt.IsZero() {
		return nil, errUCIContextRuntimeInvalid
	}
	return &pb.BeginCodeIndexResponse{
		Scope:          contextAwareProtoIndexScope(binding.Scope, binding.ProfileID),
		BuildId:        result.Build.BuildID,
		LeaseEpoch:     uint64(result.Build.LeaseEpoch),
		LeaseExpiresAt: timestamppb.New(result.LeaseExpiresAt.UTC()),
	}, nil
}

func (runtime *contextAwareUCIRuntime) StageCodeIndex(ctx context.Context, binding uci.IndexBinding, frames []*pb.StageCodeIndexFrame) (*pb.StageCodeIndexResponse, error) {
	if err := runtime.require(ctx); err != nil {
		return nil, err
	}
	binding, err := uciRuntimeBinding(binding)
	if err != nil {
		return nil, err
	}
	if len(frames) == 0 || len(frames) > uci.IndexAdmissionMaxFrames || frames[0] == nil {
		return nil, errUCIContextRuntimeInvalid
	}

	first := frames[0]
	if !contextAwareIndexScopeMatchesBinding(first.GetScope(), binding) || !validUCIUUID(first.GetBuildId()) ||
		first.GetLeaseEpoch() == 0 || first.GetLeaseEpoch() > math.MaxInt64 {
		return nil, errUCIContextRuntimeInvalid
	}
	admissions := make([]uci.IndexAdmissionFrame, len(frames))
	totalPayloadBytes := 0
	for index, frame := range frames {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if frame == nil || !contextAwareIndexScopeMatchesBinding(frame.GetScope(), binding) ||
			frame.GetBuildId() != first.GetBuildId() || frame.GetLeaseEpoch() != first.GetLeaseEpoch() ||
			frame.GetSequence() != uint64(index) {
			return nil, errUCIContextRuntimeInvalid
		}
		payload := frame.GetPayload()
		if len(payload) > uci.IndexAdmissionMaxTotalEncodedBytes-totalPayloadBytes {
			return nil, errUCIContextRuntimeInvalid
		}
		totalPayloadBytes += len(payload)
		if uci.DigestIndexAdmissionPayload(payload) != uci.IndexDigest(frame.GetPayloadDigest()) {
			return nil, errUCIContextRuntimeInvalid
		}
		admission, err := uci.DecodeIndexAdmissionFrame(payload)
		if err != nil {
			return nil, fmt.Errorf("uci stage: decode frame: %w", err)
		}
		admissions[index] = admission
	}
	if err := uci.ValidateIndexAdmissionFramesForBinding(admissions, binding); err != nil {
		return nil, fmt.Errorf("uci stage: authorize packed build: %w", err)
	}
	build, caller, err := runtime.loadBuildCaller(ctx, binding, first.GetBuildId(), first.GetLeaseEpoch())
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	parts, err := runtime.projections.AdmitIndexFrames(ctx, binding.Scope.SourceID, binding.ProfileID, admissions)
	if err != nil {
		return nil, err
	}
	if len(parts) != len(frames) {
		return nil, errUCIContextRuntimeInvalid
	}

	acks := make([]uci.IndexPartAck, 0, len(parts))
	for index, part := range parts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		partDigest, err := uci.DigestIndexPart(part)
		if err != nil {
			return nil, fmt.Errorf("uci stage: digest admitted part: %w", err)
		}
		ack, err := runtime.publisher.Stage(ctx, caller, uci.IndexStageInput{
			Build:    build,
			Sequence: uint32(index),
			Digest:   partDigest,
			Part:     part,
		})
		if err != nil {
			return nil, err
		}
		if ack.BuildID != build.BuildID || ack.Sequence != uint32(index) || ack.Digest != partDigest {
			return nil, errUCIContextRuntimeInvalid
		}
		acks = append(acks, ack)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	partsDigest, err := uci.DigestIndexParts(acks)
	if err != nil {
		return nil, fmt.Errorf("uci stage: digest acknowledgements: %w", err)
	}
	return &pb.StageCodeIndexResponse{
		BuildId:           build.BuildID,
		AcceptedSequence:  uint64(acks[len(acks)-1].Sequence),
		AcceptedPartCount: uint64(len(acks)),
		PartDigest:        string(partsDigest),
	}, nil
}

func (runtime *contextAwareUCIRuntime) FinalizeCodeIndex(ctx context.Context, binding uci.IndexBinding, request *pb.FinalizeCodeIndexRequest) (*pb.FinalizeCodeIndexResponse, error) {
	if err := runtime.require(ctx); err != nil {
		return nil, err
	}
	binding, err := uciRuntimeBinding(binding)
	if err != nil {
		return nil, err
	}
	if request == nil || !contextAwareIndexScopeMatchesBinding(request.GetScope(), binding) ||
		!contextAwareFinalizeParentMatchesScope(request.GetExpectedParent(), binding) ||
		!validUCIUUID(request.GetBuildId()) || request.GetLeaseEpoch() == 0 || request.GetLeaseEpoch() > math.MaxInt64 ||
		request.GetManifestPartCount() > uci.IndexAdmissionMaxFrames ||
		request.GetManifestEntryCount() > maxUCITransportManifestEntries || request.GetEdgeCount() > maxUCITransportManifestEdges ||
		!validUCISHA256Digest(request.GetPartsDigest()) || !validUCISHA256Digest(request.GetManifestDigest()) ||
		!validUCISHA256Digest(request.GetEdgesDigest()) || request.GetScanOutcome() != string(uci.IndexScanComplete) ||
		!request.GetCompleteCensus() || !validUCICoverageJSON(request.GetCoverageJson()) {
		return nil, errUCIContextRuntimeInvalid
	}
	coverage, err := uciRuntimeCoverage(request.GetCoverageJson())
	if err != nil {
		return nil, err
	}
	scanStart, err := uciRuntimeTimestamp(request.GetScanStartedAt())
	if err != nil {
		return nil, err
	}
	scanEnd, err := uciRuntimeTimestamp(request.GetScanCompletedAt())
	if err != nil || scanEnd.Before(scanStart) || request.GetObservedFilesystemSequence() > math.MaxInt64 {
		return nil, errUCIContextRuntimeInvalid
	}
	scanOutcome, err := uciRuntimeScanOutcome(request.GetScanOutcome())
	if err != nil {
		return nil, err
	}
	observation, err := uciRuntimeObservation(request)
	if err != nil {
		return nil, err
	}
	observation.ObservedFSSeq = int64(request.GetObservedFilesystemSequence())
	observation.ScanStart = scanStart
	observation.ScanEnd = scanEnd
	build, caller, err := runtime.loadBuildCaller(ctx, binding, request.GetBuildId(), request.GetLeaseEpoch())
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	published, err := runtime.publisher.Finalize(ctx, caller, uci.IndexFinalizeInput{
		Build:          build,
		ExpectedParent: uciRuntimeExpectedParent(request.GetExpectedParent()),
		Manifest: uci.IndexManifestCompletion{
			PartCount:      uint32(request.GetManifestPartCount()),
			PartsDigest:    uci.IndexDigest(request.GetPartsDigest()),
			EntryCount:     request.GetManifestEntryCount(),
			ManifestDigest: uci.IndexDigest(request.GetManifestDigest()),
			EdgeCount:      request.GetEdgeCount(),
			EdgesDigest:    uci.IndexDigest(request.GetEdgesDigest()),
			ScanOutcome:    scanOutcome,
			CensusComplete: request.GetCompleteCensus(),
			Observation:    observation,
			Coverage:       coverage,
		},
	})
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !uciRuntimePublishedMatchesBinding(
		published,
		build,
		binding,
		uci.IndexDigest(request.GetManifestDigest()),
		int64(request.GetObservedFilesystemSequence()),
	) {
		return nil, errUCIContextRuntimeInvalid
	}
	return &pb.FinalizeCodeIndexResponse{
		PublishedContext:           contextAwareProtoContextRef(published.Context),
		BuildId:                    published.BuildID,
		LeaseEpoch:                 uint64(build.LeaseEpoch),
		AcceptedFilesystemSequence: uint64(published.AcceptedFSSeq),
	}, nil
}

func (runtime *contextAwareUCIRuntime) QueryCode(ctx context.Context, _ uci.AuthorizedContext, _ *pb.QueryCodeRequest) (*pb.QueryCodeResponse, error) {
	if err := runtime.require(ctx); err != nil {
		return nil, err
	}
	return nil, errUCIContextRuntimeExposure
}

func (runtime *contextAwareUCIRuntime) ExploreCode(ctx context.Context, _ uci.AuthorizedContext, _ *pb.ExploreCodeRequest) (*pb.ExploreCodeResponse, error) {
	if err := runtime.require(ctx); err != nil {
		return nil, err
	}
	return nil, errUCIContextRuntimeExposure
}

func (runtime *contextAwareUCIRuntime) require(ctx context.Context) error {
	if runtime == nil || runtime.contexts == nil || runtime.projections == nil || runtime.publisher == nil {
		return errUCIContextRuntimeUnavailable
	}
	if ctx == nil {
		return errUCIContextRuntimeInvalid
	}
	return ctx.Err()
}

func (runtime *contextAwareUCIRuntime) loadBuildCaller(ctx context.Context, binding uci.IndexBinding, buildID string, leaseEpoch uint64) (uci.IndexBuildRef, uci.IndexCaller, error) {
	if !validUCIUUID(buildID) || leaseEpoch == 0 || leaseEpoch > math.MaxInt64 {
		return uci.IndexBuildRef{}, uci.IndexCaller{}, errUCIContextRuntimeInvalid
	}
	caller, err := contextAwareCallerFrom(ctx)
	if err != nil {
		return uci.IndexBuildRef{}, uci.IndexCaller{}, err
	}
	build, err := runtime.projections.LoadIndexBuildRef(ctx, binding.Scope, binding.ProfileID, buildID, int64(leaseEpoch))
	if err != nil {
		return uci.IndexBuildRef{}, uci.IndexCaller{}, err
	}
	if err := ctx.Err(); err != nil {
		return uci.IndexBuildRef{}, uci.IndexCaller{}, err
	}
	if !uciRuntimeBuildMatchesRequest(build, binding, buildID, leaseEpoch) {
		return uci.IndexBuildRef{}, uci.IndexCaller{}, errUCIContextRuntimeInvalid
	}
	return build, uci.IndexCaller{
		AuthRealm:     caller.authRealm,
		Principal:     caller.principal,
		OwnerInstance: build.OwnerInstance,
	}, nil
}

func uciRuntimeBuildMatchesRequest(build uci.IndexBuildRef, binding uci.IndexBinding, buildID string, leaseEpoch uint64) bool {
	return build.BuildID == buildID &&
		build.Scope == binding.Scope &&
		build.LeaseEpoch == int64(leaseEpoch) &&
		validUCIIdentifier(build.OwnerInstance, maxUCITransportIdentifierBytes)
}

func uciRuntimePublishedMatchesBinding(published uci.IndexPublishedView, build uci.IndexBuildRef, binding uci.IndexBinding, manifestDigest uci.IndexDigest, observedFSSeq int64) bool {
	reference := contextAwareProtoContextRef(published.Context)
	return published.BuildID == build.BuildID &&
		published.ManifestDigest == manifestDigest &&
		published.AcceptedFSSeq == observedFSSeq &&
		!published.PublishedAt.IsZero() &&
		validUCIContextRef(reference) &&
		reference.GetSourceId() == binding.Scope.SourceID &&
		reference.GetCheckoutId() == binding.Scope.CheckoutID &&
		reference.GetAnalysisProfileId() == binding.ProfileID
}

func uciRuntimeBinding(binding uci.IndexBinding) (uci.IndexBinding, error) {
	binding = binding.Clone()
	if err := binding.Validate(); err != nil {
		return uci.IndexBinding{}, errUCIContextRuntimeInvalid
	}
	return binding, nil
}

func uciRuntimeExpectedParentMatchesBinding(parent *pb.ContextRef, binding uci.IndexBinding) bool {
	if binding.Context == nil {
		return parent == nil
	}
	return contextAwareProtoRefMatches(*binding.Context, parent)
}

func uciRuntimeExpectedParent(parent *pb.ContextRef) *uci.ContextRef {
	if parent == nil {
		return nil
	}
	reference := contextAwareContextRefFromProto(parent)
	return &reference
}

func uciRuntimeCaller(ctx context.Context, ownerInstance string) (uci.IndexCaller, error) {
	caller, err := contextAwareCallerFrom(ctx)
	if err != nil {
		return uci.IndexCaller{}, err
	}
	if !validUCIIdentifier(ownerInstance, maxUCITransportIdentifierBytes) {
		return uci.IndexCaller{}, errUCIContextRuntimeInvalid
	}
	return uci.IndexCaller{
		AuthRealm:     caller.authRealm,
		Principal:     caller.principal,
		OwnerInstance: ownerInstance,
	}, nil
}

func uciRuntimeManifestMode(value string) (uci.IndexManifestMode, error) {
	switch uci.IndexManifestMode(value) {
	case uci.IndexManifestFull:
		return uci.IndexManifestFull, nil
	case uci.IndexManifestDelta:
		return uci.IndexManifestDelta, nil
	default:
		return "", errUCIContextRuntimeInvalid
	}
}

func uciRuntimeJobKind(value string) (uci.IndexJobKind, error) {
	switch uci.IndexJobKind(value) {
	case uci.IndexJobInitial:
		return uci.IndexJobInitial, nil
	case uci.IndexJobReconcile:
		return uci.IndexJobReconcile, nil
	case uci.IndexJobRecovery:
		return uci.IndexJobRecovery, nil
	default:
		return "", errUCIContextRuntimeInvalid
	}
}

func uciRuntimeScanOutcome(value string) (uci.IndexScanOutcome, error) {
	switch uci.IndexScanOutcome(value) {
	case uci.IndexScanComplete:
		return uci.IndexScanComplete, nil
	case uci.IndexScanIncomplete:
		return uci.IndexScanIncomplete, nil
	case uci.IndexScanFailed:
		return uci.IndexScanFailed, nil
	default:
		return "", errUCIContextRuntimeInvalid
	}
}

func uciRuntimeTimestamp(value *timestamppb.Timestamp) (time.Time, error) {
	if value == nil || value.CheckValid() != nil || value.AsTime().IsZero() {
		return time.Time{}, errUCIContextRuntimeInvalid
	}
	return value.AsTime().UTC(), nil
}

func uciRuntimeObservation(request *pb.FinalizeCodeIndexRequest) (uci.IndexObservation, error) {
	if request == nil || request.Dirty == nil {
		return uci.IndexObservation{}, errUCIContextRuntimeInvalid
	}
	if request.ObjectFormat != nil && !validUCIObjectFormat(*request.ObjectFormat) {
		return uci.IndexObservation{}, errUCIContextRuntimeInvalid
	}
	if request.HeadOid != nil && (request.ObjectFormat == nil || !validUCIHeadOID(*request.HeadOid, *request.ObjectFormat)) {
		return uci.IndexObservation{}, errUCIContextRuntimeInvalid
	}
	if request.RefLabel != nil && !validUCIIdentifier(*request.RefLabel, maxUCITransportIdentifierBytes) {
		return uci.IndexObservation{}, errUCIContextRuntimeInvalid
	}
	return uci.IndexObservation{
		HeadOID:      uciRuntimeOptionalString(request.HeadOid),
		ObjectFormat: uciRuntimeOptionalString(request.ObjectFormat),
		RefLabel:     uciRuntimeOptionalString(request.RefLabel),
		Dirty:        *request.Dirty,
	}, nil
}

func uciRuntimeOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func uciRuntimeCoverage(encoded []byte) (uci.IndexCoverage, error) {
	type coverageJSON struct {
		Structural           uci.IndexCoverageState `json:"structural"`
		Lexical              uci.IndexCoverageState `json:"lexical"`
		Vector               uci.IndexCoverageState `json:"vector"`
		ExcludedFiles        uint64                 `json:"excluded_files"`
		UnreadableFiles      uint64                 `json:"unreadable_files"`
		UnresolvedReferences uint64                 `json:"unresolved_references"`
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var decoded coverageJSON
	if err := decoder.Decode(&decoded); err != nil {
		return uci.IndexCoverage{}, errUCIContextRuntimeInvalid
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return uci.IndexCoverage{}, errUCIContextRuntimeInvalid
	}
	coverage := uci.IndexCoverage{
		Structural:           decoded.Structural,
		Lexical:              decoded.Lexical,
		Vector:               decoded.Vector,
		ExcludedFiles:        decoded.ExcludedFiles,
		UnreadableFiles:      decoded.UnreadableFiles,
		UnresolvedReferences: decoded.UnresolvedReferences,
	}
	switch coverage.Structural {
	case uci.IndexCoverageComplete, uci.IndexCoveragePartial, uci.IndexCoverageUnavailable:
	default:
		return uci.IndexCoverage{}, errUCIContextRuntimeInvalid
	}
	switch coverage.Lexical {
	case uci.IndexCoverageComplete, uci.IndexCoveragePartial, uci.IndexCoverageUnavailable:
	default:
		return uci.IndexCoverage{}, errUCIContextRuntimeInvalid
	}
	switch coverage.Vector {
	case uci.IndexCoverageComplete, uci.IndexCoveragePartial, uci.IndexCoverageUnavailable:
	default:
		return uci.IndexCoverage{}, errUCIContextRuntimeInvalid
	}
	return coverage, nil
}
