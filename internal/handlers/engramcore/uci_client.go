package engramcore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	maxUCIClientIdentifierBytes   = 256
	maxUCIClientDigestBytes       = 128
	maxUCIClientContextBytes      = 2 << 10
	maxUCIClientScopeBytes        = 2 << 10
	maxUCIClientBindBytes         = 4 << 10
	maxUCIClientBeginBytes        = 8 << 10
	maxUCIClientPayloadBytes      = 1 << 20
	maxUCIClientStageBytes        = 16 << 20
	maxUCIClientStageFrames       = 1024
	maxUCIClientCoverageBytes     = 1 << 20
	maxUCIClientFinalizeBytes     = maxUCIClientCoverageBytes + 4<<10
	maxUCIClientQueryBytes        = 16 << 10
	maxUCIClientContinuationBytes = 4 << 10
	maxUCIClientExploreBytes      = 8 << 10
	maxUCIClientResponseBytes     = 4 << 10
	maxUCIClientQueryResults      = 1000
	maxUCIClientQueryResultBytes  = 1 << 20
	maxUCIClientDeadlineMS        = 30_000
	maxUCIClientExploreDepth      = 8
	maxUCIClientVisitedNodes      = 5000
	maxUCIClientResultNodes       = 200
	maxUCIClientResultEdges       = 400
	maxUCIClientManifestEntries   = 1_000_000
	maxUCIClientManifestEdges     = 5_000_000
)

var (
	errUCIClientUnavailable     = errors.New("client is unavailable")
	errUCIClientInvalidContext  = errors.New("context is invalid")
	errUCIClientInvalidRequest  = errors.New("request is invalid")
	errUCIClientEmptyResponse   = errors.New("response is empty")
	errUCIClientInvalidResponse = errors.New("response is invalid")
)

// uciClientRPC is the generated EngramService surface used by the UCI adapter.
type uciClientRPC interface {
	BindCodeContext(context.Context, *pb.BindCodeContextRequest, ...grpc.CallOption) (*pb.BindCodeContextResponse, error)
	BeginCodeIndex(context.Context, *pb.BeginCodeIndexRequest, ...grpc.CallOption) (*pb.BeginCodeIndexResponse, error)
	StageCodeIndex(context.Context, ...grpc.CallOption) (grpc.ClientStreamingClient[pb.StageCodeIndexFrame, pb.StageCodeIndexResponse], error)
	FinalizeCodeIndex(context.Context, *pb.FinalizeCodeIndexRequest, ...grpc.CallOption) (*pb.FinalizeCodeIndexResponse, error)
	QueryCode(context.Context, *pb.QueryCodeRequest, ...grpc.CallOption) (*pb.QueryCodeResponse, error)
	ExploreCode(context.Context, *pb.ExploreCodeRequest, ...grpc.CallOption) (*pb.ExploreCodeResponse, error)
}

var _ uciClientRPC = (pb.EngramServiceClient)(nil)

type uciClient struct {
	rpc uciClientRPC
}

func newUCIClient(rpc uciClientRPC) *uciClient {
	return &uciClient{rpc: rpc}
}

func (client *uciClient) Bind(ctx context.Context, request *pb.BindCodeContextRequest) (*pb.BindCodeContextResponse, error) {
	const operation = "Bind"
	if err := uciClientContextError(operation, ctx); err != nil {
		return nil, err
	}
	if err := client.available(operation); err != nil {
		return nil, err
	}
	if !validUCIClientBindRequest(request) {
		return nil, uciClientError(operation, errUCIClientInvalidRequest)
	}

	outbound := proto.Clone(request).(*pb.BindCodeContextRequest)
	if err := uciClientContextError(operation, ctx); err != nil {
		return nil, err
	}
	response, err := client.rpc.BindCodeContext(ctx, outbound)
	if err != nil {
		return nil, uciClientCallError(operation, err)
	}
	if err := uciClientContextError(operation, ctx); err != nil {
		return nil, err
	}
	if response == nil {
		return nil, uciClientError(operation, errUCIClientEmptyResponse)
	}
	if !validUCIClientBindResponse(outbound, response) {
		return nil, uciClientError(operation, errUCIClientInvalidResponse)
	}
	return response, nil
}

func (client *uciClient) Begin(ctx context.Context, request *pb.BeginCodeIndexRequest) (*pb.BeginCodeIndexResponse, error) {
	const operation = "Begin"
	if err := uciClientContextError(operation, ctx); err != nil {
		return nil, err
	}
	if err := client.available(operation); err != nil {
		return nil, err
	}
	if !validUCIClientBeginRequest(request) {
		return nil, uciClientError(operation, errUCIClientInvalidRequest)
	}

	outbound := proto.Clone(request).(*pb.BeginCodeIndexRequest)
	if err := uciClientContextError(operation, ctx); err != nil {
		return nil, err
	}
	response, err := client.rpc.BeginCodeIndex(ctx, outbound)
	if err != nil {
		return nil, uciClientCallError(operation, err)
	}
	if err := uciClientContextError(operation, ctx); err != nil {
		return nil, err
	}
	if response == nil {
		return nil, uciClientError(operation, errUCIClientEmptyResponse)
	}
	if !validUCIClientBeginResponse(outbound, response) {
		return nil, uciClientError(operation, errUCIClientInvalidResponse)
	}
	return response, nil
}

func (client *uciClient) Stage(ctx context.Context, frames []*pb.StageCodeIndexFrame) (*pb.StageCodeIndexResponse, error) {
	const operation = "Stage"
	if err := uciClientContextError(operation, ctx); err != nil {
		return nil, err
	}
	if err := client.available(operation); err != nil {
		return nil, err
	}
	if !validUCIClientStageFrames(frames) {
		return nil, uciClientError(operation, errUCIClientInvalidRequest)
	}

	outbound := make([]*pb.StageCodeIndexFrame, len(frames))
	for index, frame := range frames {
		outbound[index] = proto.Clone(frame).(*pb.StageCodeIndexFrame)
	}
	if err := uciClientContextError(operation, ctx); err != nil {
		return nil, err
	}
	stream, err := client.rpc.StageCodeIndex(ctx)
	if err != nil {
		return nil, uciClientCallError(operation+" open", err)
	}
	if uciClientIsNil(stream) {
		return nil, uciClientError(operation+" open", errUCIClientUnavailable)
	}

	for _, frame := range outbound {
		if err := uciClientContextError(operation, ctx); err != nil {
			return nil, err
		}
		if err := stream.Send(frame); err != nil {
			return nil, uciClientCallError(operation+" send", err)
		}
	}
	if err := uciClientContextError(operation, ctx); err != nil {
		return nil, err
	}
	response, err := stream.CloseAndRecv()
	if err != nil {
		return nil, uciClientCallError(operation+" close", err)
	}
	if err := uciClientContextError(operation, ctx); err != nil {
		return nil, err
	}
	if response == nil {
		return nil, uciClientError(operation, errUCIClientEmptyResponse)
	}
	if !validUCIClientStageResponse(outbound, response) {
		return nil, uciClientError(operation, errUCIClientInvalidResponse)
	}
	return response, nil
}

func (client *uciClient) Finalize(ctx context.Context, request *pb.FinalizeCodeIndexRequest) (*pb.FinalizeCodeIndexResponse, error) {
	const operation = "Finalize"
	if err := uciClientContextError(operation, ctx); err != nil {
		return nil, err
	}
	if err := client.available(operation); err != nil {
		return nil, err
	}
	if !validUCIClientFinalizeRequest(request) {
		return nil, uciClientError(operation, errUCIClientInvalidRequest)
	}

	outbound := proto.Clone(request).(*pb.FinalizeCodeIndexRequest)
	if err := uciClientContextError(operation, ctx); err != nil {
		return nil, err
	}
	response, err := client.rpc.FinalizeCodeIndex(ctx, outbound)
	if err != nil {
		return nil, uciClientCallError(operation, err)
	}
	if err := uciClientContextError(operation, ctx); err != nil {
		return nil, err
	}
	if response == nil {
		return nil, uciClientError(operation, errUCIClientEmptyResponse)
	}
	if !validUCIClientFinalizeResponse(outbound, response) {
		return nil, uciClientError(operation, errUCIClientInvalidResponse)
	}
	return response, nil
}

func (client *uciClient) Query(ctx context.Context, request *pb.QueryCodeRequest) (*pb.QueryCodeResponse, error) {
	const operation = "Query"
	if err := uciClientContextError(operation, ctx); err != nil {
		return nil, err
	}
	if err := client.available(operation); err != nil {
		return nil, err
	}
	if !validUCIClientQueryRequest(request) {
		return nil, uciClientError(operation, errUCIClientInvalidRequest)
	}

	outbound := proto.Clone(request).(*pb.QueryCodeRequest)
	if err := uciClientContextError(operation, ctx); err != nil {
		return nil, err
	}
	response, err := client.rpc.QueryCode(ctx, outbound)
	if err != nil {
		return nil, uciClientCallError(operation, err)
	}
	if err := uciClientContextError(operation, ctx); err != nil {
		return nil, err
	}
	if response == nil {
		return nil, uciClientError(operation, errUCIClientEmptyResponse)
	}
	if !validUCIClientQueryResponse(outbound, response) {
		return nil, uciClientError(operation, errUCIClientInvalidResponse)
	}
	return response, nil
}

func (client *uciClient) Explore(ctx context.Context, request *pb.ExploreCodeRequest) (*pb.ExploreCodeResponse, error) {
	const operation = "Explore"
	if err := uciClientContextError(operation, ctx); err != nil {
		return nil, err
	}
	if err := client.available(operation); err != nil {
		return nil, err
	}
	if !validUCIClientExploreRequest(request) {
		return nil, uciClientError(operation, errUCIClientInvalidRequest)
	}

	outbound := proto.Clone(request).(*pb.ExploreCodeRequest)
	if err := uciClientContextError(operation, ctx); err != nil {
		return nil, err
	}
	response, err := client.rpc.ExploreCode(ctx, outbound)
	if err != nil {
		return nil, uciClientCallError(operation, err)
	}
	if err := uciClientContextError(operation, ctx); err != nil {
		return nil, err
	}
	if response == nil {
		return nil, uciClientError(operation, errUCIClientEmptyResponse)
	}
	if !validUCIClientExploreResponse(outbound, response) {
		return nil, uciClientError(operation, errUCIClientInvalidResponse)
	}
	return response, nil
}

func (client *uciClient) available(operation string) error {
	if client == nil || uciClientIsNil(client.rpc) {
		return uciClientError(operation, errUCIClientUnavailable)
	}
	return nil
}

func uciClientContextError(operation string, ctx context.Context) error {
	if ctx == nil {
		return uciClientError(operation, errUCIClientInvalidContext)
	}
	if err := ctx.Err(); err != nil {
		return uciClientCallError(operation, err)
	}
	return nil
}

func uciClientError(operation string, err error) error {
	return fmt.Errorf("UCI %s: %w", operation, err)
}

func uciClientCallError(operation string, err error) error {
	return fmt.Errorf("UCI %s: %w", operation, err)
}

func uciClientIsNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func validUCIClientBindRequest(request *pb.BindCodeContextRequest) bool {
	return request != nil && validUCIClientMessage(request, maxUCIClientBindBytes) &&
		validUCIClientIdentifier(request.GetClientSessionId(), maxUCIClientIdentifierBytes) &&
		validUCIClientContextRef(request.GetRequestedContext())
}

func validUCIClientBeginRequest(request *pb.BeginCodeIndexRequest) bool {
	if request == nil || !validUCIClientMessage(request, maxUCIClientBeginBytes) ||
		!validUCIClientIndexScope(request.GetScope()) ||
		!validUCIClientIdentifier(request.GetOwnerInstance(), maxUCIClientIdentifierBytes) ||
		!validUCIClientIdentifier(request.GetBuildKey(), maxUCIClientIdentifierBytes) ||
		!validUCIClientManifestMode(request.GetManifestMode()) ||
		!validUCIClientJobKind(request.GetJobKind()) {
		return false
	}
	return request.GetExpectedParent() == nil || validUCIClientContextRef(request.GetExpectedParent())
}

func validUCIClientStageFrames(frames []*pb.StageCodeIndexFrame) bool {
	if len(frames) == 0 || len(frames) > maxUCIClientStageFrames {
		return false
	}

	var payloadBytes int
	for index, frame := range frames {
		if !validUCIClientStageFrame(frame) || frame.GetSequence() != uint64(index) {
			return false
		}
		if index > 0 && !sameUCIClientStageIdentity(frames[0], frame) {
			return false
		}
		payload := frame.GetPayload()
		if payloadBytes > maxUCIClientStageBytes-len(payload) {
			return false
		}
		payloadBytes += len(payload)
	}
	return true
}

func validUCIClientFinalizeRequest(request *pb.FinalizeCodeIndexRequest) bool {
	if request == nil || !validUCIClientMessage(request, maxUCIClientFinalizeBytes) ||
		!validUCIClientIndexScope(request.GetScope()) ||
		!validUCIClientIdentifier(request.GetBuildId(), maxUCIClientIdentifierBytes) ||
		request.GetLeaseEpoch() == 0 ||
		request.GetManifestPartCount() > maxUCIClientStageFrames ||
		request.GetManifestEntryCount() > maxUCIClientManifestEntries ||
		request.GetEdgeCount() > maxUCIClientManifestEdges ||
		!validUCIClientSHA256Digest(request.GetPartsDigest()) ||
		!validUCIClientSHA256Digest(request.GetManifestDigest()) ||
		!validUCIClientSHA256Digest(request.GetEdgesDigest()) ||
		!validUCIClientScanOutcome(request.GetScanOutcome()) ||
		!validUCIClientJSONObject(request.GetCoverageJson(), maxUCIClientCoverageBytes) ||
		!validUCIClientTimestamp(request.GetScanStartedAt()) ||
		!validUCIClientTimestamp(request.GetScanCompletedAt()) {
		return false
	}
	if request.GetScanCompletedAt().AsTime().Before(request.GetScanStartedAt().AsTime()) {
		return false
	}
	return request.GetExpectedParent() == nil || validUCIClientContextRef(request.GetExpectedParent())
}

func validUCIClientQueryRequest(request *pb.QueryCodeRequest) bool {
	return request != nil && validUCIClientMessage(request, maxUCIClientQueryBytes) &&
		validUCIClientContextRef(request.GetContext()) &&
		validUCIClientText(request.GetQuery(), maxUCIClientQueryBytes) &&
		request.GetMaxResults() != 0 && request.GetMaxResults() <= maxUCIClientQueryResults &&
		request.GetMaxBytes() != 0 && request.GetMaxBytes() <= maxUCIClientQueryResultBytes &&
		request.GetDeadlineMs() != 0 && request.GetDeadlineMs() <= maxUCIClientDeadlineMS &&
		validUCIClientOptionalIdentifier(request.GetContinuationToken(), maxUCIClientContinuationBytes)
}

func validUCIClientExploreRequest(request *pb.ExploreCodeRequest) bool {
	return request != nil && validUCIClientMessage(request, maxUCIClientExploreBytes) &&
		validUCIClientContextRef(request.GetContext()) &&
		validUCIClientExploreOperation(request.GetOperation()) &&
		validUCIClientText(request.GetSubject(), maxUCIClientQueryBytes) &&
		request.GetMaxDepth() != 0 && request.GetMaxDepth() <= maxUCIClientExploreDepth &&
		request.GetMaxVisitedNodes() != 0 && request.GetMaxVisitedNodes() <= maxUCIClientVisitedNodes &&
		request.GetMaxResultNodes() != 0 && request.GetMaxResultNodes() <= maxUCIClientResultNodes &&
		request.GetMaxResultEdges() != 0 && request.GetMaxResultEdges() <= maxUCIClientResultEdges &&
		request.GetDeadlineMs() != 0 && request.GetDeadlineMs() <= maxUCIClientDeadlineMS &&
		validUCIClientOptionalIdentifier(request.GetContinuationToken(), maxUCIClientContinuationBytes)
}

func validUCIClientBindResponse(request *pb.BindCodeContextRequest, response *pb.BindCodeContextResponse) bool {
	return response != nil && validUCIClientMessage(response, maxUCIClientResponseBytes) &&
		validUCIClientIdentifier(response.GetContextHandle(), maxUCIClientIdentifierBytes) &&
		validUCIClientContextRef(response.GetContext()) &&
		sameUCIClientContextRef(request.GetRequestedContext(), response.GetContext())
}

func validUCIClientBeginResponse(request *pb.BeginCodeIndexRequest, response *pb.BeginCodeIndexResponse) bool {
	return response != nil && validUCIClientMessage(response, maxUCIClientResponseBytes) &&
		validUCIClientIndexScope(response.GetScope()) &&
		sameUCIClientIndexScope(request.GetScope(), response.GetScope()) &&
		validUCIClientIdentifier(response.GetBuildId(), maxUCIClientIdentifierBytes) &&
		response.GetLeaseEpoch() != 0 && validUCIClientLeaseExpiry(response.GetLeaseExpiresAt())
}

func validUCIClientStageResponse(frames []*pb.StageCodeIndexFrame, response *pb.StageCodeIndexResponse) bool {
	return len(frames) > 0 && response != nil && validUCIClientMessage(response, maxUCIClientResponseBytes) &&
		response.GetBuildId() == frames[0].GetBuildId() &&
		response.GetAcceptedSequence() == frames[len(frames)-1].GetSequence() &&
		response.GetAcceptedPartCount() == uint64(len(frames)) &&
		validUCIClientSHA256Digest(response.GetPartDigest())
}

func validUCIClientFinalizeResponse(request *pb.FinalizeCodeIndexRequest, response *pb.FinalizeCodeIndexResponse) bool {
	return response != nil && validUCIClientMessage(response, maxUCIClientResponseBytes) &&
		validUCIClientContextRef(response.GetPublishedContext()) &&
		contextMatchesUCIClientIndexScope(response.GetPublishedContext(), request.GetScope()) &&
		response.GetBuildId() == request.GetBuildId() &&
		response.GetLeaseEpoch() == request.GetLeaseEpoch() &&
		response.GetAcceptedFilesystemSequence() == request.GetObservedFilesystemSequence()
}

func validUCIClientQueryResponse(request *pb.QueryCodeRequest, response *pb.QueryCodeResponse) bool {
	return response != nil && validUCIClientMessage(response, int(request.GetMaxBytes())+maxUCIClientContextBytes) &&
		validUCIClientContextRef(response.GetContext()) &&
		sameUCIClientContextRef(request.GetContext(), response.GetContext()) &&
		validUCIClientJSONObject(response.GetResponseJson(), int(request.GetMaxBytes()))
}

func validUCIClientExploreResponse(request *pb.ExploreCodeRequest, response *pb.ExploreCodeResponse) bool {
	return response != nil && validUCIClientMessage(response, maxUCIClientQueryResultBytes+maxUCIClientContextBytes) &&
		validUCIClientContextRef(response.GetContext()) &&
		sameUCIClientContextRef(request.GetContext(), response.GetContext()) &&
		validUCIClientJSONObject(response.GetResponseJson(), maxUCIClientQueryResultBytes)
}

func validUCIClientStageFrame(frame *pb.StageCodeIndexFrame) bool {
	return frame != nil &&
		validUCIClientMessage(frame, maxUCIClientPayloadBytes+maxUCIClientScopeBytes+maxUCIClientIdentifierBytes+maxUCIClientDigestBytes) &&
		validUCIClientIndexScope(frame.GetScope()) &&
		validUCIClientIdentifier(frame.GetBuildId(), maxUCIClientIdentifierBytes) &&
		frame.GetLeaseEpoch() != 0 &&
		validUCIClientSHA256Digest(frame.GetPayloadDigest()) &&
		len(frame.GetPayload()) > 0 && len(frame.GetPayload()) <= maxUCIClientPayloadBytes
}

func validUCIClientContextRef(reference *pb.ContextRef) bool {
	if reference == nil || !validUCIClientMessage(reference, maxUCIClientContextBytes) ||
		!validUCIClientUUID(reference.GetSourceId()) ||
		!validUCIClientUUID(reference.GetCheckoutId()) ||
		!validUCIClientUUID(reference.GetViewId()) ||
		!validUCIClientUUID(reference.GetAnalysisProfileId()) ||
		reference.GetGeneration() <= 0 {
		return false
	}
	return reference.SpaceId == nil || validUCIClientUUID(reference.GetSpaceId())
}

func validUCIClientIndexScope(scope *pb.CodeIndexScope) bool {
	return scope != nil && validUCIClientMessage(scope, maxUCIClientScopeBytes) &&
		validUCIClientUUID(scope.GetSourceId()) &&
		validUCIClientUUID(scope.GetCheckoutId()) &&
		validUCIClientUUID(scope.GetIncarnationId()) &&
		validUCIClientUUID(scope.GetAnalysisProfileId())
}

func validUCIClientTimestamp(timestamp *timestamppb.Timestamp) bool {
	return timestamp != nil && validUCIClientMessage(timestamp, 64) && timestamp.CheckValid() == nil
}

func validUCIClientLeaseExpiry(timestamp *timestamppb.Timestamp) bool {
	return validUCIClientTimestamp(timestamp) && !timestamp.AsTime().IsZero()
}

func validUCIClientMessage(message proto.Message, maxBytes int) bool {
	return message != nil && len(message.ProtoReflect().GetUnknown()) == 0 && proto.Size(message) <= maxBytes
}

func validUCIClientUUID(value string) bool {
	if !validUCIClientIdentifier(value, 36) {
		return false
	}
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

func validUCIClientOptionalIdentifier(value string, maxBytes int) bool {
	return value == "" || validUCIClientIdentifier(value, maxBytes)
}

func validUCIClientManifestMode(value string) bool {
	return value == "full" || value == "delta"
}

func validUCIClientJobKind(value string) bool {
	switch value {
	case "initial_index", "reconcile", "recovery":
		return true
	default:
		return false
	}
}

func validUCIClientScanOutcome(value string) bool {
	switch value {
	case "complete", "incomplete", "failed":
		return true
	default:
		return false
	}
}

func validUCIClientExploreOperation(value string) bool {
	switch value {
	case "explain", "neighbors", "path", "impact", "flow", "cycles":
		return true
	default:
		return false
	}
}

func validUCIClientSHA256Digest(value string) bool {
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

func validUCIClientJSONObject(payload []byte, maxBytes int) bool {
	if len(payload) == 0 || len(payload) > maxBytes || !utf8.Valid(payload) {
		return false
	}
	trimmed := bytes.TrimSpace(payload)
	return len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(trimmed)
}

func validUCIClientIdentifier(value string, maxBytes int) bool {
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

func validUCIClientText(value string, maxBytes int) bool {
	return len(value) > 0 && len(value) <= maxBytes && utf8.ValidString(value) && strings.TrimSpace(value) != ""
}

func sameUCIClientStageIdentity(first, next *pb.StageCodeIndexFrame) bool {
	return sameUCIClientIndexScope(first.GetScope(), next.GetScope()) &&
		first.GetBuildId() == next.GetBuildId() &&
		first.GetLeaseEpoch() == next.GetLeaseEpoch()
}

func sameUCIClientIndexScope(left, right *pb.CodeIndexScope) bool {
	return left.GetSourceId() == right.GetSourceId() &&
		left.GetCheckoutId() == right.GetCheckoutId() &&
		left.GetIncarnationId() == right.GetIncarnationId() &&
		left.GetAnalysisProfileId() == right.GetAnalysisProfileId()
}

func sameUCIClientContextRef(left, right *pb.ContextRef) bool {
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

func contextMatchesUCIClientIndexScope(reference *pb.ContextRef, scope *pb.CodeIndexScope) bool {
	return reference != nil && scope != nil &&
		reference.GetSourceId() == scope.GetSourceId() &&
		reference.GetCheckoutId() == scope.GetCheckoutId() &&
		reference.GetAnalysisProfileId() == scope.GetAnalysisProfileId()
}
