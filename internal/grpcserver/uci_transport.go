package grpcserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/thebtf/engram/internal/uci"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	maxUCITransportIdentifierBytes   = 256
	maxUCITransportDigestBytes       = 128
	maxUCITransportContextBytes      = 2 << 10
	maxUCITransportScopeBytes        = 2 << 10
	maxUCITransportBindBytes         = 4 << 10
	maxUCITransportBeginBytes        = 8 << 10
	maxUCITransportPayloadBytes      = uci.IndexAdmissionMaxEncodedFrameBytes
	maxUCITransportStageBytes        = uci.IndexAdmissionMaxTotalEncodedBytes
	maxUCITransportStageFrames       = uci.IndexAdmissionMaxFrames
	maxUCITransportCoverageBytes     = 1 << 20
	maxUCITransportFinalizeBytes     = maxUCITransportCoverageBytes + 4<<10
	maxUCITransportQueryBytes        = 16 << 10
	maxUCITransportContinuationBytes = 4 << 10
	maxUCITransportExploreBytes      = 8 << 10
	maxUCITransportResponseBytes     = 5 << 10
	maxUCITransportQueryResults      = 1000
	maxUCITransportQueryResultBytes  = 1 << 20
	maxUCITransportDeadlineMS        = 30_000
	maxUCITransportExploreDepth      = 8
	maxUCITransportVisitedNodes      = 5000
	maxUCITransportResultNodes       = 200
	maxUCITransportResultEdges       = 400
	maxUCITransportManifestEntries   = 1_000_000
	maxUCITransportManifestEdges     = 5_000_000
)

// UCITransport is the private adapter from the gRPC boundary to the scoped UCI
// runtime. The runtime owns authorization, lease, publication, and retrieval
// semantics; this boundary only admits bounded, structurally valid requests.
type UCITransport interface {
	BindCodeContext(context.Context, *pb.BindCodeContextRequest) (*pb.BindCodeContextResponse, error)
	PollCodeIndexIntents(context.Context, *pb.PollCodeIndexIntentsRequest) (*pb.PollCodeIndexIntentsResponse, error)
	UpdateCodeIndexIntent(context.Context, *pb.UpdateCodeIndexIntentRequest) (*pb.UpdateCodeIndexIntentResponse, error)
	BeginCodeIndex(context.Context, *pb.BeginCodeIndexRequest) (*pb.BeginCodeIndexResponse, error)
	StageCodeIndex(context.Context, []*pb.StageCodeIndexFrame) (*pb.StageCodeIndexResponse, error)
	FinalizeCodeIndex(context.Context, *pb.FinalizeCodeIndexRequest) (*pb.FinalizeCodeIndexResponse, error)
	QueryCode(context.Context, *pb.QueryCodeRequest) (*pb.QueryCodeResponse, error)
	ExploreCode(context.Context, *pb.ExploreCodeRequest) (*pb.ExploreCodeResponse, error)
}

// BindCodeContext validates one client-scoped context request before handing it
// to the injected UCI runtime. Context authorization remains runtime-owned.
func (s *Server) BindCodeContext(ctx context.Context, request *pb.BindCodeContextRequest) (*pb.BindCodeContextResponse, error) {
	if err := uciTransportContextError(ctx); err != nil {
		return nil, err
	}
	if err := validateUCIBindCodeContextRequest(request); err != nil {
		return nil, err
	}
	transport := s.currentUCITransport()
	if transport == nil {
		return nil, uciTransportUnavailable()
	}
	response, err := transport.BindCodeContext(ctx, request)
	if err != nil {
		return nil, uciTransportHandlerError(ctx, err)
	}
	if err := uciTransportContextError(ctx); err != nil {
		return nil, err
	}
	if response == nil {
		return nil, uciTransportEmptyResponse()
	}
	if !validUCIBindCodeContextResponse(request, response) {
		return nil, uciTransportInvalidResponse()
	}
	return response, nil
}

func (s *Server) PollCodeIndexIntents(ctx context.Context, request *pb.PollCodeIndexIntentsRequest) (*pb.PollCodeIndexIntentsResponse, error) {
	if err := uciTransportContextError(ctx); err != nil {
		return nil, err
	}
	if !validUCIPollIndexIntentRequest(request) {
		return nil, uciTransportInvalidArgument()
	}
	transport := s.currentUCITransport()
	if transport == nil {
		return nil, uciTransportUnavailable()
	}
	response, err := transport.PollCodeIndexIntents(ctx, request)
	if err != nil {
		return nil, uciTransportHandlerError(ctx, err)
	}
	if response == nil || !validUCIPollIndexIntentResponse(response) {
		return nil, uciTransportInvalidResponse()
	}
	return response, nil
}

func (s *Server) UpdateCodeIndexIntent(ctx context.Context, request *pb.UpdateCodeIndexIntentRequest) (*pb.UpdateCodeIndexIntentResponse, error) {
	if err := uciTransportContextError(ctx); err != nil {
		return nil, err
	}
	if !validUCIUpdateIndexIntentRequest(request) {
		return nil, uciTransportInvalidArgument()
	}
	transport := s.currentUCITransport()
	if transport == nil {
		return nil, uciTransportUnavailable()
	}
	response, err := transport.UpdateCodeIndexIntent(ctx, request)
	if err != nil {
		return nil, uciTransportHandlerError(ctx, err)
	}
	if response == nil || !validUCIUpdateIndexIntentResponse(response) {
		return nil, uciTransportInvalidResponse()
	}
	return response, nil
}

// BeginCodeIndex accepts a checkout-scoped build request. expected_parent is
// optional so an initial index never needs a fabricated View-pinned ContextRef.
func (s *Server) BeginCodeIndex(ctx context.Context, request *pb.BeginCodeIndexRequest) (*pb.BeginCodeIndexResponse, error) {
	if err := uciTransportContextError(ctx); err != nil {
		return nil, err
	}
	if err := validateUCIBeginCodeIndexRequest(request); err != nil {
		return nil, err
	}
	transport := s.currentUCITransport()
	if transport == nil {
		return nil, uciTransportUnavailable()
	}
	response, err := transport.BeginCodeIndex(ctx, request)
	if err != nil {
		return nil, uciTransportHandlerError(ctx, err)
	}
	if err := uciTransportContextError(ctx); err != nil {
		return nil, err
	}
	if response == nil {
		return nil, uciTransportEmptyResponse()
	}
	if !validUCIBeginCodeIndexResponse(request, response) {
		return nil, uciTransportInvalidResponse()
	}
	return response, nil
}

// StageCodeIndex collects one complete ordered client stream before one runtime
// call. A successful part_digest is the server-canonical aggregate for every
// accepted frame and is passed unchanged to FinalizeCodeIndex.parts_digest.
func (s *Server) StageCodeIndex(stream pb.EngramService_StageCodeIndexServer) error {
	if stream == nil {
		return uciTransportInvalidArgument()
	}
	ctx := stream.Context()
	if err := uciTransportContextError(ctx); err != nil {
		return err
	}
	frames, err := collectUCIStageFrames(ctx, stream)
	if err != nil {
		return err
	}
	transport := s.currentUCITransport()
	if transport == nil {
		return uciTransportUnavailable()
	}
	response, err := transport.StageCodeIndex(ctx, frames)
	if err != nil {
		return uciTransportHandlerError(ctx, err)
	}
	if err := uciTransportContextError(ctx); err != nil {
		return err
	}
	if response == nil {
		return uciTransportEmptyResponse()
	}
	if !validUCIStageCodeIndexResponse(frames, response) {
		return uciTransportInvalidResponse()
	}
	if err := stream.SendAndClose(response); err != nil {
		return uciTransportHandlerError(ctx, err)
	}
	return nil
}

// FinalizeCodeIndex forwards an explicitly complete staged manifest. EOF from
// StageCodeIndex is intentionally not a substitute for this request.
func (s *Server) FinalizeCodeIndex(ctx context.Context, request *pb.FinalizeCodeIndexRequest) (*pb.FinalizeCodeIndexResponse, error) {
	if err := uciTransportContextError(ctx); err != nil {
		return nil, err
	}
	if err := validateUCIFinalizeCodeIndexRequest(request); err != nil {
		return nil, err
	}
	transport := s.currentUCITransport()
	if transport == nil {
		return nil, uciTransportUnavailable()
	}
	response, err := transport.FinalizeCodeIndex(ctx, request)
	if err != nil {
		return nil, uciTransportHandlerError(ctx, err)
	}
	if err := uciTransportContextError(ctx); err != nil {
		return nil, err
	}
	if response == nil {
		return nil, uciTransportEmptyResponse()
	}
	if !validUCIFinalizeCodeIndexResponse(request, response) {
		return nil, uciTransportInvalidResponse()
	}
	return response, nil
}

// QueryCode forwards a bounded request for a View-pinned ContextRef.
func (s *Server) QueryCode(ctx context.Context, request *pb.QueryCodeRequest) (*pb.QueryCodeResponse, error) {
	if err := uciTransportContextError(ctx); err != nil {
		return nil, err
	}
	if err := validateUCIQueryCodeRequest(request); err != nil {
		return nil, err
	}
	transport := s.currentUCITransport()
	if transport == nil {
		return nil, uciTransportUnavailable()
	}
	response, err := transport.QueryCode(ctx, request)
	if err != nil {
		return nil, uciTransportHandlerError(ctx, err)
	}
	if err := uciTransportContextError(ctx); err != nil {
		return nil, err
	}
	if response == nil {
		return nil, uciTransportEmptyResponse()
	}
	if !validUCIQueryCodeResponse(request, response) {
		return nil, uciTransportInvalidResponse()
	}
	return response, nil
}

// ExploreCode forwards bounded graph exploration for a View-pinned ContextRef.
func (s *Server) ExploreCode(ctx context.Context, request *pb.ExploreCodeRequest) (*pb.ExploreCodeResponse, error) {
	if err := uciTransportContextError(ctx); err != nil {
		return nil, err
	}
	if err := validateUCIExploreCodeRequest(request); err != nil {
		return nil, err
	}
	transport := s.currentUCITransport()
	if transport == nil {
		return nil, uciTransportUnavailable()
	}
	response, err := transport.ExploreCode(ctx, request)
	if err != nil {
		return nil, uciTransportHandlerError(ctx, err)
	}
	if err := uciTransportContextError(ctx); err != nil {
		return nil, err
	}
	if response == nil {
		return nil, uciTransportEmptyResponse()
	}
	if !validUCIExploreCodeResponse(request, response) {
		return nil, uciTransportInvalidResponse()
	}
	return response, nil
}

func collectUCIStageFrames(ctx context.Context, stream pb.EngramService_StageCodeIndexServer) ([]*pb.StageCodeIndexFrame, error) {
	frames := make([]*pb.StageCodeIndexFrame, 0, 16)
	totalPayloadBytes := 0
	for {
		frame, finished, err := receiveUCIStageFrame(ctx, stream, len(frames))
		if err != nil {
			return nil, err
		}
		if finished {
			return frames, nil
		}
		if err := validateUCIStageFrame(frames, frame); err != nil {
			return nil, err
		}
		payload := frame.GetPayload()
		if totalPayloadBytes > maxUCITransportStageBytes-len(payload) {
			return nil, uciTransportInvalidArgument()
		}
		totalPayloadBytes += len(payload)
		frames = append(frames, frame)
	}
}

func receiveUCIStageFrame(ctx context.Context, stream pb.EngramService_StageCodeIndexServer, count int) (*pb.StageCodeIndexFrame, bool, error) {
	frame, err := stream.Recv()
	if errors.Is(err, io.EOF) {
		if count == 0 {
			return nil, false, uciTransportInvalidArgument()
		}
		return nil, true, nil
	}
	if err != nil {
		return nil, false, uciTransportHandlerError(ctx, err)
	}
	return frame, false, nil
}

func validateUCIStageFrame(frames []*pb.StageCodeIndexFrame, frame *pb.StageCodeIndexFrame) error {
	if len(frames) >= maxUCITransportStageFrames || !validUCIStageFrame(frame) {
		return uciTransportInvalidArgument()
	}
	if len(frames) > 0 && !sameUCIStageIdentity(frames[0], frame) {
		return uciTransportInvalidArgument()
	}
	if frame.GetSequence() != uint64(len(frames)) {
		return uciTransportInvalidArgument()
	}
	return nil
}

func sameUCIStageIdentity(first, next *pb.StageCodeIndexFrame) bool {
	return sameUCIIndexScope(first.GetScope(), next.GetScope()) &&
		first.GetBuildId() == next.GetBuildId() &&
		first.GetLeaseEpoch() == next.GetLeaseEpoch() &&
		sameUCIIndexIntentClaim(first.GetIntentClaim(), next.GetIntentClaim())
}

func sameUCIIndexIntentClaim(left, right *pb.CodeIndexIntentClaim) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.GetIntentRef() == right.GetIntentRef() &&
		left.GetOwnerEpoch() == right.GetOwnerEpoch() &&
		left.GetProcessNonce() == right.GetProcessNonce()
}

func sameUCIIndexScope(left, right *pb.CodeIndexScope) bool {
	return left.GetSourceId() == right.GetSourceId() &&
		left.GetCheckoutId() == right.GetCheckoutId() &&
		left.GetIncarnationId() == right.GetIncarnationId() &&
		left.GetAnalysisProfileId() == right.GetAnalysisProfileId()
}

func sameUCIContextRef(left, right *pb.ContextRef) bool {
	if left == nil || right == nil || (left.SpaceId == nil) != (right.SpaceId == nil) {
		return false
	}
	return left.GetSpaceId() == right.GetSpaceId() &&
		left.GetSourceId() == right.GetSourceId() &&
		left.GetCheckoutId() == right.GetCheckoutId() &&
		left.GetViewId() == right.GetViewId() &&
		left.GetGeneration() == right.GetGeneration() &&
		left.GetAnalysisProfileId() == right.GetAnalysisProfileId()
}

func contextMatchesUCIIndexScope(reference *pb.ContextRef, scope *pb.CodeIndexScope) bool {
	return reference != nil && scope != nil &&
		reference.GetSourceId() == scope.GetSourceId() &&
		reference.GetCheckoutId() == scope.GetCheckoutId() &&
		reference.GetAnalysisProfileId() == scope.GetAnalysisProfileId()
}

func validUCIIndexIntentTarget(target *pb.CodeIndexIntentTarget) bool {
	return target != nil && validUCIIdentifier(target.GetClientSessionId(), maxUCITransportIdentifierBytes) &&
		validUCIIdentifier(target.GetContextHandle(), maxUCITransportIdentifierBytes) && validUCIIndexScope(target.GetScope()) &&
		validUCIIdentifier(target.GetLocalRootId(), maxUCITransportIdentifierBytes) && validUCIIdentifier(target.GetWorkstationId(), maxUCITransportIdentifierBytes)
}

func validUCIPollIndexIntentRequest(request *pb.PollCodeIndexIntentsRequest) bool {
	return request != nil && validUCIMessage(request, maxUCITransportBeginBytes) && validUCIIndexIntentTarget(request.GetTarget()) &&
		validUCIIdentifier(request.GetClientInstanceId(), maxUCITransportIdentifierBytes) && validUCIIdentifier(request.GetProcessNonce(), maxUCITransportIdentifierBytes)
}

func validUCIPollIndexIntentResponse(response *pb.PollCodeIndexIntentsResponse) bool {
	if response == nil || !validUCIMessage(response, maxUCITransportResponseBytes) {
		return false
	}
	if response.GetOffer() == nil {
		return true
	}
	offer := response.GetOffer()
	if !validUCIIdentifier(offer.GetIntentRef(), maxUCITransportIdentifierBytes) ||
		(offer.GetKind() != string(uci.IndexIntentReindex) && offer.GetKind() != string(uci.IndexIntentReconcile)) {
		return false
	}
	if previous := offer.GetPreviousContext(); previous != nil && !validUCIContextRef(previous) {
		return false
	}
	state := uci.IndexIntentState(offer.GetState())
	if state == uci.IndexIntentQueued {
		return offer.GetOwnerEpoch() == 0 && offer.GetLeaseExpiresAt() == nil
	}
	return (state == uci.IndexIntentAcknowledged || state == uci.IndexIntentRunning) && offer.GetOwnerEpoch() > 0 && validUCILeaseExpiry(offer.GetLeaseExpiresAt())
}

func validUCIUpdateIndexIntentRequest(request *pb.UpdateCodeIndexIntentRequest) bool {
	if request == nil || !validUCIMessage(request, maxUCITransportBeginBytes) || !validUCIIndexIntentTarget(request.GetTarget()) ||
		!validUCIIdentifier(request.GetClientInstanceId(), maxUCITransportIdentifierBytes) || !validUCIIdentifier(request.GetProcessNonce(), maxUCITransportIdentifierBytes) ||
		!validUCIIdentifier(request.GetIntentRef(), maxUCITransportIdentifierBytes) || !validUCIIdentifier(request.GetOperationRef(), maxUCITransportIdentifierBytes) {
		return false
	}
	operation := uci.IndexIntentOperation(request.GetOperation())
	return operation.Valid() && ((operation == uci.IndexIntentAcknowledge && request.GetOwnerEpoch() == 0) || (operation != uci.IndexIntentAcknowledge && request.GetOwnerEpoch() > 0))
}

func validUCIUpdateIndexIntentResponse(response *pb.UpdateCodeIndexIntentResponse) bool {
	if response == nil || !validUCIMessage(response, maxUCITransportResponseBytes) || !validUCIIdentifier(response.GetIntentRef(), maxUCITransportIdentifierBytes) || response.GetAttempt() == 0 || response.GetOwnerEpoch() == 0 {
		return false
	}
	state := uci.IndexIntentState(response.GetState())
	return (state == uci.IndexIntentAcknowledged || state == uci.IndexIntentRunning || state == uci.IndexIntentCompleted || state == uci.IndexIntentFailed) && validUCILeaseExpiry(response.GetLeaseExpiresAt())
}

func validUCIBindCodeContextResponse(request *pb.BindCodeContextRequest, response *pb.BindCodeContextResponse) bool {
	if request == nil || response == nil ||
		!validUCIMessage(response, maxUCITransportResponseBytes) ||
		!validUCIIdentifier(response.GetContextHandle(), maxUCITransportIdentifierBytes) ||
		!validUCIIndexScope(response.GetIndexScope()) ||
		!validUCIIdentifier(response.GetLocalRootId(), maxUCITransportIdentifierBytes) ||
		!validUCIIdentifier(response.GetWorkstationId(), maxUCITransportIdentifierBytes) {
		return false
	}
	if response.GetContext() != nil &&
		(!validUCIContextRef(response.GetContext()) || !contextMatchesUCIIndexScope(response.GetContext(), response.GetIndexScope())) {
		return false
	}
	switch {
	case request.GetRequestedContext() != nil && request.GetContextHandle() == "":
		return response.GetContext() != nil && sameUCIContextRef(request.GetRequestedContext(), response.GetContext())
	case request.GetRequestedContext() == nil && request.GetContextHandle() != "":
		return response.GetContextHandle() == request.GetContextHandle()
	case request.GetRequestedContext() == nil && request.GetContextHandle() == "":
		return response.GetContext() != nil
	default:
		return false
	}
}

func validUCIBeginCodeIndexResponse(request *pb.BeginCodeIndexRequest, response *pb.BeginCodeIndexResponse) bool {
	return response != nil && validUCIMessage(response, maxUCITransportResponseBytes) &&
		validUCIIndexScope(response.GetScope()) &&
		sameUCIIndexScope(request.GetScope(), response.GetScope()) &&
		validUCIIdentifier(response.GetBuildId(), maxUCITransportIdentifierBytes) &&
		response.GetLeaseEpoch() != 0 && validUCILeaseExpiry(response.GetLeaseExpiresAt())
}

func validUCIStageCodeIndexResponse(frames []*pb.StageCodeIndexFrame, response *pb.StageCodeIndexResponse) bool {
	return len(frames) > 0 && response != nil && validUCIMessage(response, maxUCITransportResponseBytes) &&
		response.GetBuildId() == frames[0].GetBuildId() &&
		response.GetAcceptedSequence() == frames[len(frames)-1].GetSequence() &&
		response.GetAcceptedPartCount() == uint64(len(frames)) &&
		validUCISHA256Digest(response.GetPartDigest())
}

func validUCIFinalizeCodeIndexResponse(request *pb.FinalizeCodeIndexRequest, response *pb.FinalizeCodeIndexResponse) bool {
	return response != nil && validUCIMessage(response, maxUCITransportResponseBytes) &&
		validUCIContextRef(response.GetPublishedContext()) &&
		contextMatchesUCIIndexScope(response.GetPublishedContext(), request.GetScope()) &&
		response.GetBuildId() == request.GetBuildId() &&
		response.GetLeaseEpoch() == request.GetLeaseEpoch() &&
		response.GetAcceptedFilesystemSequence() == request.GetObservedFilesystemSequence()
}

func validUCIQueryCodeResponse(request *pb.QueryCodeRequest, response *pb.QueryCodeResponse) bool {
	return response != nil && validUCIMessage(response, int(request.GetMaxBytes())+maxUCITransportContextBytes) &&
		validUCIContextRef(response.GetContext()) &&
		sameUCIContextRef(request.GetContext(), response.GetContext()) &&
		validUCIJSONObject(response.GetResponseJson(), int(request.GetMaxBytes()))
}

func validUCIExploreCodeResponse(request *pb.ExploreCodeRequest, response *pb.ExploreCodeResponse) bool {
	return response != nil && validUCIMessage(response, maxUCITransportQueryResultBytes+maxUCITransportContextBytes) &&
		validUCIContextRef(response.GetContext()) &&
		sameUCIContextRef(request.GetContext(), response.GetContext()) &&
		validUCIJSONObject(response.GetResponseJson(), maxUCITransportQueryResultBytes)
}

func validateUCIBindCodeContextRequest(request *pb.BindCodeContextRequest) error {
	if request == nil || !validUCIMessage(request, maxUCITransportBindBytes) ||
		!validUCIIdentifier(request.GetClientSessionId(), maxUCITransportIdentifierBytes) {
		return uciTransportInvalidArgument()
	}
	switch {
	case request.GetRequestedContext() != nil && request.GetContextHandle() == "":
		if !validUCIContextRef(request.GetRequestedContext()) {
			return uciTransportInvalidArgument()
		}
	case request.GetRequestedContext() == nil && validUCIIdentifier(request.GetContextHandle(), maxUCITransportIdentifierBytes):
	case request.GetRequestedContext() == nil && request.GetContextHandle() == "":
	default:
		return uciTransportInvalidArgument()
	}
	return nil
}

func validateUCIBeginCodeIndexRequest(request *pb.BeginCodeIndexRequest) error {
	if request == nil || !validUCIMessage(request, maxUCITransportBeginBytes) ||
		!validUCIIndexScope(request.GetScope()) ||
		!validUCIIdentifier(request.GetOwnerInstance(), maxUCITransportIdentifierBytes) ||
		!validUCIIdentifier(request.GetBuildKey(), maxUCITransportIdentifierBytes) ||
		!validUCIManifestMode(request.GetManifestMode()) ||
		!validUCIJobKind(request.GetJobKind()) ||
		!validUCIIndexIntentClaim(request.GetIntentClaim()) {
		return uciTransportInvalidArgument()
	}
	if parent := request.GetExpectedParent(); parent != nil && !validUCIContextRef(parent) {
		return uciTransportInvalidArgument()
	}
	return nil
}

func validateUCIFinalizeCodeIndexRequest(request *pb.FinalizeCodeIndexRequest) error {
	if request == nil || !validUCIMessage(request, maxUCITransportFinalizeBytes) ||
		!validUCIIndexScope(request.GetScope()) ||
		!validUCIIdentifier(request.GetBuildId(), maxUCITransportIdentifierBytes) ||
		request.GetLeaseEpoch() == 0 ||
		request.GetManifestPartCount() > maxUCITransportStageFrames ||
		request.GetManifestEntryCount() > maxUCITransportManifestEntries ||
		request.GetEdgeCount() > maxUCITransportManifestEdges ||
		!validUCISHA256Digest(request.GetPartsDigest()) ||
		!validUCISHA256Digest(request.GetManifestDigest()) ||
		!validUCISHA256Digest(request.GetEdgesDigest()) ||
		!validUCIScanOutcome(request.GetScanOutcome()) ||
		!validUCICoverageJSON(request.GetCoverageJson()) ||
		!validUCITimestamp(request.GetScanStartedAt()) ||
		!validUCITimestamp(request.GetScanCompletedAt()) ||
		!validUCIFinalizeObservation(request) ||
		!validUCIIndexIntentClaim(request.GetIntentClaim()) {
		return uciTransportInvalidArgument()
	}
	if request.GetScanCompletedAt().AsTime().Before(request.GetScanStartedAt().AsTime()) {
		return uciTransportInvalidArgument()
	}
	if parent := request.GetExpectedParent(); parent != nil && !validUCIContextRef(parent) {
		return uciTransportInvalidArgument()
	}
	return nil
}

func validUCIFinalizeObservation(request *pb.FinalizeCodeIndexRequest) bool {
	if request == nil || request.Dirty == nil {
		return false
	}
	if request.ObjectFormat != nil && !validUCIObjectFormat(*request.ObjectFormat) {
		return false
	}
	if request.HeadOid != nil &&
		(request.ObjectFormat == nil || !validUCIHeadOID(*request.HeadOid, *request.ObjectFormat)) {
		return false
	}
	return request.RefLabel == nil || validUCIIdentifier(*request.RefLabel, maxUCITransportIdentifierBytes)
}

func validateUCIQueryCodeRequest(request *pb.QueryCodeRequest) error {
	if request == nil || !validUCIMessage(request, maxUCITransportQueryBytes) ||
		!validUCIContextRef(request.GetContext()) ||
		!validUCIText(request.GetQuery(), maxUCITransportQueryBytes) ||
		request.GetMaxResults() == 0 || request.GetMaxResults() > maxUCITransportQueryResults ||
		request.GetMaxBytes() == 0 || request.GetMaxBytes() > maxUCITransportQueryResultBytes ||
		request.GetDeadlineMs() == 0 || request.GetDeadlineMs() > maxUCITransportDeadlineMS ||
		!validUCIOptionalIdentifier(request.GetContinuationToken(), maxUCITransportContinuationBytes) {
		return uciTransportInvalidArgument()
	}
	return nil
}

func validateUCIExploreCodeRequest(request *pb.ExploreCodeRequest) error {
	if request == nil || !validUCIMessage(request, maxUCITransportExploreBytes) ||
		!validUCIContextRef(request.GetContext()) ||
		!validUCIExploreOperation(request.GetOperation()) ||
		!validUCIText(request.GetSubject(), maxUCITransportQueryBytes) ||
		request.GetMaxDepth() == 0 || request.GetMaxDepth() > maxUCITransportExploreDepth ||
		request.GetMaxVisitedNodes() == 0 || request.GetMaxVisitedNodes() > maxUCITransportVisitedNodes ||
		request.GetMaxResultNodes() == 0 || request.GetMaxResultNodes() > maxUCITransportResultNodes ||
		request.GetMaxResultEdges() == 0 || request.GetMaxResultEdges() > maxUCITransportResultEdges ||
		request.GetDeadlineMs() == 0 || request.GetDeadlineMs() > maxUCITransportDeadlineMS ||
		!validUCIOptionalIdentifier(request.GetContinuationToken(), maxUCITransportContinuationBytes) {
		return uciTransportInvalidArgument()
	}
	return nil
}

func validUCIStageFrame(frame *pb.StageCodeIndexFrame) bool {
	return frame != nil &&
		validUCIMessage(frame, maxUCITransportPayloadBytes+maxUCITransportScopeBytes+maxUCITransportIdentifierBytes+maxUCITransportDigestBytes) &&
		validUCIIndexScope(frame.GetScope()) &&
		validUCIIdentifier(frame.GetBuildId(), maxUCITransportIdentifierBytes) &&
		frame.GetLeaseEpoch() != 0 &&
		validUCISHA256Digest(frame.GetPayloadDigest()) &&
		len(frame.GetPayload()) > 0 && len(frame.GetPayload()) <= maxUCITransportPayloadBytes &&
		validUCIIndexIntentClaim(frame.GetIntentClaim())
}

func validUCIContextRef(reference *pb.ContextRef) bool {
	if reference == nil || !validUCIMessage(reference, maxUCITransportContextBytes) ||
		!validUCIUUID(reference.GetSourceId()) ||
		!validUCIUUID(reference.GetCheckoutId()) ||
		!validUCIUUID(reference.GetViewId()) ||
		!validUCIUUID(reference.GetAnalysisProfileId()) ||
		reference.GetGeneration() <= 0 {
		return false
	}
	return reference.SpaceId == nil || validUCIUUID(reference.GetSpaceId())
}

func validUCIIndexScope(scope *pb.CodeIndexScope) bool {
	return scope != nil &&
		validUCIMessage(scope, maxUCITransportScopeBytes) &&
		validUCIUUID(scope.GetSourceId()) &&
		validUCIUUID(scope.GetCheckoutId()) &&
		validUCIUUID(scope.GetIncarnationId()) &&
		validUCIUUID(scope.GetAnalysisProfileId())
}

func validUCIIndexIntentClaim(claim *pb.CodeIndexIntentClaim) bool {
	return claim == nil || (validUCIUUID(claim.GetIntentRef()) && claim.GetOwnerEpoch() > 0 &&
		validUCIIdentifier(claim.GetProcessNonce(), maxUCITransportIdentifierBytes))
}

func validUCIObjectFormat(value string) bool {
	return value == "sha1" || value == "sha256"
}

func validUCIHeadOID(value, objectFormat string) bool {
	wantLength := 40
	if objectFormat == "sha256" {
		wantLength = 64
	}
	if len(value) != wantLength {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validUCITimestamp(timestamp *timestamppb.Timestamp) bool {
	return timestamp != nil && validUCIMessage(timestamp, 64) && timestamp.CheckValid() == nil
}

func validUCILeaseExpiry(timestamp *timestamppb.Timestamp) bool {
	return validUCITimestamp(timestamp) && !timestamp.AsTime().IsZero()
}

func validUCICoverageJSON(payload []byte) bool {
	return validUCIJSONObject(payload, maxUCITransportCoverageBytes)
}

func validUCIMessage(message proto.Message, maxBytes int) bool {
	return message != nil && len(message.ProtoReflect().GetUnknown()) == 0 && proto.Size(message) <= maxBytes
}

func validUCIUUID(value string) bool {
	if !validUCIIdentifier(value, 36) {
		return false
	}
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

func validUCIOptionalIdentifier(value string, maxBytes int) bool {
	return value == "" || validUCIIdentifier(value, maxBytes)
}

func validUCIManifestMode(value string) bool {
	return value == "full" || value == "delta"
}

func validUCIJobKind(value string) bool {
	switch value {
	case "initial_index", "reconcile", "recovery":
		return true
	default:
		return false
	}
}

func validUCIScanOutcome(value string) bool {
	switch value {
	case "complete", "incomplete", "failed":
		return true
	default:
		return false
	}
}

func validUCIExploreOperation(value string) bool {
	switch value {
	case "explain", "neighbors", "path", "impact", "flow", "cycles":
		return true
	default:
		return false
	}
}

func validUCISHA256Digest(value string) bool {
	const prefix = "sha256:"
	if len(value) != len(prefix)+64 || !strings.HasPrefix(value, prefix) {
		return false
	}
	for _, character := range value[len(prefix):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validUCIJSONObject(payload []byte, maxBytes int) bool {
	if len(payload) == 0 || len(payload) > maxBytes || !utf8.Valid(payload) {
		return false
	}
	trimmed := bytes.TrimSpace(payload)
	return len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(trimmed)
}

func validUCIIdentifier(value string, maxBytes int) bool {
	if len(value) == 0 || len(value) > maxBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validUCIText(value string, maxBytes int) bool {
	return len(value) > 0 && len(value) <= maxBytes && utf8.ValidString(value) && strings.TrimSpace(value) != ""
}

func uciTransportContextError(ctx context.Context) error {
	if ctx == nil {
		return uciTransportInvalidArgument()
	}
	if err := ctx.Err(); err != nil {
		return status.FromContextError(err).Err()
	}
	return nil
}

func uciTransportHandlerError(ctx context.Context, err error) error {
	if _, isStatus := status.FromError(err); isStatus {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return status.FromContextError(err).Err()
	}
	if ctx != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return status.FromContextError(contextErr).Err()
		}
	}
	return status.Error(codes.Internal, "UCI transport handler failed")
}

func uciTransportInvalidArgument() error {
	return status.Error(codes.InvalidArgument, "UCI transport request is invalid")
}

func uciTransportUnavailable() error {
	return status.Error(codes.Unavailable, "UCI transport unavailable")
}

func uciTransportEmptyResponse() error {
	return status.Error(codes.Internal, "UCI transport returned an empty response")
}

func uciTransportInvalidResponse() error {
	return status.Error(codes.Internal, "UCI transport returned an invalid response")
}
