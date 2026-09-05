package grpcserver

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"

	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/uci"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const (
	legacyCodeIndexAliasDomain = "project"
	legacyCodeIndexAliasScheme = "project_id"
	contextAwareHandleBytes    = 32
)

// ContextAwareUCIRuntime is the scoped UCI runtime used after context resolution.
type ContextAwareUCIRuntime interface {
	LegacyCodeIndexNegotiate(context.Context, uci.AuthorizedContext, *pb.CodeIndexNegotiateRequest) (*pb.CodeIndexNegotiateResponse, error)
	BeginCodeIndex(context.Context, uci.AuthorizedContext, *pb.BeginCodeIndexRequest) (*pb.BeginCodeIndexResponse, error)
	StageCodeIndex(context.Context, uci.AuthorizedContext, []*pb.StageCodeIndexFrame) (*pb.StageCodeIndexResponse, error)
	FinalizeCodeIndex(context.Context, uci.AuthorizedContext, *pb.FinalizeCodeIndexRequest) (*pb.FinalizeCodeIndexResponse, error)
	QueryCode(context.Context, uci.AuthorizedContext, *pb.QueryCodeRequest) (*pb.QueryCodeResponse, error)
	ExploreCode(context.Context, uci.AuthorizedContext, *pb.ExploreCodeRequest) (*pb.ExploreCodeResponse, error)
}

// legacyCodeIndexNegotiator is an optional adapter for the legacy negotiate RPC.
type legacyCodeIndexNegotiator interface {
	LegacyCodeIndexNegotiate(context.Context, *pb.CodeIndexNegotiateRequest) (*pb.CodeIndexNegotiateResponse, error)
}

type contextAwareUCITransport struct {
	resolver      *uci.ContextResolver
	aliasResolver *uci.AliasResolver
	runtime       ContextAwareUCIRuntime
}

// NewContextAwareUCITransport creates the UCI adapter that resolves each call's context.
func NewContextAwareUCITransport(resolver *uci.ContextResolver, aliasResolver *uci.AliasResolver, runtime ContextAwareUCIRuntime) UCITransport {
	return &contextAwareUCITransport{
		resolver:      resolver,
		aliasResolver: aliasResolver,
		runtime:       runtime,
	}
}

var (
	_ UCITransport              = (*contextAwareUCITransport)(nil)
	_ legacyCodeIndexNegotiator = (*contextAwareUCITransport)(nil)
)

type contextAwareCaller struct {
	clientSessionID string
	authRealm       string
	principal       string
}

func (transport *contextAwareUCITransport) BindCodeContext(ctx context.Context, request *pb.BindCodeContextRequest) (*pb.BindCodeContextResponse, error) {
	if request == nil {
		return nil, uciTransportInvalidArgument()
	}
	caller, err := contextAwareCallerFrom(ctx)
	if err != nil {
		return nil, err
	}
	if request.GetClientSessionId() != caller.clientSessionID {
		return nil, contextAwareClosedError(uci.ContextMismatch)
	}

	reference := contextAwareContextRefFromProto(request.GetRequestedContext())
	authorized, err := transport.bindExplicit(ctx, caller, reference)
	if err != nil {
		return nil, err
	}
	if err := uciTransportContextError(ctx); err != nil {
		return nil, err
	}

	handle, err := newContextAwareHandle()
	if err != nil {
		return nil, status.Error(codes.Internal, "UCI context handle generation failed")
	}
	return &pb.BindCodeContextResponse{
		ContextHandle: handle,
		Context:       contextAwareProtoContextRef(authorized.Ref()),
	}, nil
}

func (transport *contextAwareUCITransport) BeginCodeIndex(ctx context.Context, request *pb.BeginCodeIndexRequest) (*pb.BeginCodeIndexResponse, error) {
	if request == nil {
		return nil, uciTransportInvalidArgument()
	}
	caller, err := contextAwareCallerFrom(ctx)
	if err != nil {
		return nil, err
	}
	authorized, err := transport.resolveBound(ctx, caller)
	if err != nil {
		return nil, err
	}
	if !contextAwareScopeMatchesRef(request.GetScope(), authorized.Ref()) {
		return nil, contextAwareClosedError(uci.ContextMismatch)
	}
	if parent := request.GetExpectedParent(); parent != nil && !contextAwareProtoRefMatches(authorized.Ref(), parent) {
		return nil, contextAwareClosedError(uci.ContextMismatch)
	}
	runtime, err := transport.contextAwareRuntime()
	if err != nil {
		return nil, err
	}

	response, err := runtime.BeginCodeIndex(ctx, authorized, proto.Clone(request).(*pb.BeginCodeIndexRequest))
	if err != nil {
		return nil, contextAwareRuntimeError(ctx, err)
	}
	if err := uciTransportContextError(ctx); err != nil {
		return nil, err
	}
	return response, nil
}

func (transport *contextAwareUCITransport) StageCodeIndex(ctx context.Context, frames []*pb.StageCodeIndexFrame) (*pb.StageCodeIndexResponse, error) {
	if len(frames) == 0 || frames[0] == nil {
		return nil, uciTransportInvalidArgument()
	}
	caller, err := contextAwareCallerFrom(ctx)
	if err != nil {
		return nil, err
	}
	authorized, err := transport.resolveBound(ctx, caller)
	if err != nil {
		return nil, err
	}
	if !contextAwareScopeMatchesRef(frames[0].GetScope(), authorized.Ref()) {
		return nil, contextAwareClosedError(uci.ContextMismatch)
	}
	runtime, err := transport.contextAwareRuntime()
	if err != nil {
		return nil, err
	}

	clonedFrames := make([]*pb.StageCodeIndexFrame, len(frames))
	for index, frame := range frames {
		if frame == nil {
			return nil, uciTransportInvalidArgument()
		}
		clonedFrames[index] = proto.Clone(frame).(*pb.StageCodeIndexFrame)
	}
	response, err := runtime.StageCodeIndex(ctx, authorized, clonedFrames)
	if err != nil {
		return nil, contextAwareRuntimeError(ctx, err)
	}
	if err := uciTransportContextError(ctx); err != nil {
		return nil, err
	}
	return response, nil
}

func (transport *contextAwareUCITransport) FinalizeCodeIndex(ctx context.Context, request *pb.FinalizeCodeIndexRequest) (*pb.FinalizeCodeIndexResponse, error) {
	if request == nil {
		return nil, uciTransportInvalidArgument()
	}
	caller, err := contextAwareCallerFrom(ctx)
	if err != nil {
		return nil, err
	}
	authorized, err := transport.resolveBound(ctx, caller)
	if err != nil {
		return nil, err
	}
	resolvedRef := authorized.Ref()
	if !contextAwareScopeMatchesRef(request.GetScope(), resolvedRef) {
		return nil, contextAwareClosedError(uci.ContextMismatch)
	}
	if parent := request.GetExpectedParent(); parent != nil && !contextAwareProtoRefMatches(resolvedRef, parent) {
		return nil, contextAwareClosedError(uci.ContextMismatch)
	}
	runtime, err := transport.contextAwareRuntime()
	if err != nil {
		return nil, err
	}

	response, err := runtime.FinalizeCodeIndex(ctx, authorized, proto.Clone(request).(*pb.FinalizeCodeIndexRequest))
	if err != nil {
		return nil, contextAwareRuntimeError(ctx, err)
	}
	if err := uciTransportContextError(ctx); err != nil {
		return nil, err
	}
	return response, nil
}

func (transport *contextAwareUCITransport) QueryCode(ctx context.Context, request *pb.QueryCodeRequest) (*pb.QueryCodeResponse, error) {
	if request == nil {
		return nil, uciTransportInvalidArgument()
	}
	caller, err := contextAwareCallerFrom(ctx)
	if err != nil {
		return nil, err
	}
	authorized, err := transport.authorizeExplicit(ctx, caller, contextAwareContextRefFromProto(request.GetContext()))
	if err != nil {
		return nil, err
	}
	runtime, err := transport.contextAwareRuntime()
	if err != nil {
		return nil, err
	}

	response, err := runtime.QueryCode(ctx, authorized, proto.Clone(request).(*pb.QueryCodeRequest))
	if err != nil {
		return nil, contextAwareRuntimeError(ctx, err)
	}
	if err := uciTransportContextError(ctx); err != nil {
		return nil, err
	}
	return response, nil
}

func (transport *contextAwareUCITransport) ExploreCode(ctx context.Context, request *pb.ExploreCodeRequest) (*pb.ExploreCodeResponse, error) {
	if request == nil {
		return nil, uciTransportInvalidArgument()
	}
	caller, err := contextAwareCallerFrom(ctx)
	if err != nil {
		return nil, err
	}
	authorized, err := transport.authorizeExplicit(ctx, caller, contextAwareContextRefFromProto(request.GetContext()))
	if err != nil {
		return nil, err
	}
	runtime, err := transport.contextAwareRuntime()
	if err != nil {
		return nil, err
	}

	response, err := runtime.ExploreCode(ctx, authorized, proto.Clone(request).(*pb.ExploreCodeRequest))
	if err != nil {
		return nil, contextAwareRuntimeError(ctx, err)
	}
	if err := uciTransportContextError(ctx); err != nil {
		return nil, err
	}
	return response, nil
}

func (transport *contextAwareUCITransport) LegacyCodeIndexNegotiate(ctx context.Context, request *pb.CodeIndexNegotiateRequest) (*pb.CodeIndexNegotiateResponse, error) {
	if request == nil {
		return nil, uciTransportInvalidArgument()
	}
	caller, err := contextAwareCallerFrom(ctx)
	if err != nil {
		return nil, err
	}
	if transport == nil || transport.aliasResolver == nil || transport.resolver == nil {
		return nil, contextAwareClosedError(uci.ContextRequired)
	}
	bound, found := transport.resolver.BoundContext(caller.clientSessionID)
	if !found {
		return nil, contextAwareClosedError(uci.ContextRequired)
	}

	target, err := transport.aliasResolver.Resolve(ctx, uci.LegacyAliasKey{
		AuthRealm:       caller.authRealm,
		LegacyDomain:    legacyCodeIndexAliasDomain,
		Scheme:          legacyCodeIndexAliasScheme,
		Value:           request.GetProjectId(),
		ClientNamespace: caller.clientSessionID,
	})
	if err != nil {
		return nil, contextAwareRuntimeError(ctx, err)
	}
	if err := uciTransportContextError(ctx); err != nil {
		return nil, err
	}
	if !contextAwareAliasMatchesRef(target, bound) {
		return nil, contextAwareClosedError(uci.ContextMismatch)
	}

	authorized, err := transport.resolveBound(ctx, caller)
	if err != nil {
		return nil, err
	}
	if !contextAwareAliasMatchesRef(target, authorized.Ref()) {
		return nil, contextAwareClosedError(uci.ContextMismatch)
	}
	runtime, err := transport.contextAwareRuntime()
	if err != nil {
		return nil, err
	}

	runtimeRequest := proto.Clone(request).(*pb.CodeIndexNegotiateRequest)
	runtimeRequest.ProjectId = ""
	response, err := runtime.LegacyCodeIndexNegotiate(ctx, authorized, runtimeRequest)
	if err != nil {
		return nil, contextAwareRuntimeError(ctx, err)
	}
	if err := uciTransportContextError(ctx); err != nil {
		return nil, err
	}
	return response, nil
}

func contextAwareCallerFrom(ctx context.Context) (contextAwareCaller, error) {
	if err := uciTransportContextError(ctx); err != nil {
		return contextAwareCaller{}, err
	}
	identity, found := auth.IdentityFrom(ctx)
	if !found {
		return contextAwareCaller{}, contextAwareClosedError(uci.ContextMismatch)
	}
	caller := contextAwareCaller{
		clientSessionID: auditcontext.SourceSession(ctx),
		authRealm:       string(identity.Source),
		principal:       identity.Principal,
	}
	if caller.clientSessionID == "" || caller.authRealm == "" || caller.principal == "" {
		return contextAwareCaller{}, contextAwareClosedError(uci.ContextMismatch)
	}
	return caller, nil
}

func (transport *contextAwareUCITransport) resolveBound(ctx context.Context, caller contextAwareCaller) (uci.AuthorizedContext, error) {
	return transport.resolve(ctx, caller, nil)
}

func (transport *contextAwareUCITransport) bindExplicit(ctx context.Context, caller contextAwareCaller, reference uci.ContextRef) (uci.AuthorizedContext, error) {
	return transport.resolve(ctx, caller, &reference)
}

func (transport *contextAwareUCITransport) authorizeExplicit(ctx context.Context, caller contextAwareCaller, reference uci.ContextRef) (uci.AuthorizedContext, error) {
	if transport == nil || transport.resolver == nil {
		return uci.AuthorizedContext{}, contextAwareClosedError(uci.ContextRequired)
	}
	resolved, err := transport.resolver.Authorize(ctx, uci.ResolveContextInput{
		ClientSessionID: caller.clientSessionID,
		AuthRealm:       caller.authRealm,
		Principal:       caller.principal,
		Ref:             &reference,
	})
	if err != nil {
		return uci.AuthorizedContext{}, contextAwareRuntimeError(ctx, err)
	}
	if err := uciTransportContextError(ctx); err != nil {
		return uci.AuthorizedContext{}, err
	}
	return resolved, nil
}

func (transport *contextAwareUCITransport) resolve(ctx context.Context, caller contextAwareCaller, reference *uci.ContextRef) (uci.AuthorizedContext, error) {
	if transport == nil || transport.resolver == nil {
		return uci.AuthorizedContext{}, contextAwareClosedError(uci.ContextRequired)
	}
	resolved, err := transport.resolver.Resolve(ctx, uci.ResolveContextInput{
		ClientSessionID: caller.clientSessionID,
		AuthRealm:       caller.authRealm,
		Principal:       caller.principal,
		Ref:             reference,
	})
	if err != nil {
		return uci.AuthorizedContext{}, contextAwareRuntimeError(ctx, err)
	}
	if err := uciTransportContextError(ctx); err != nil {
		return uci.AuthorizedContext{}, err
	}
	return resolved, nil
}

func (transport *contextAwareUCITransport) contextAwareRuntime() (ContextAwareUCIRuntime, error) {
	if transport == nil || transport.runtime == nil {
		return nil, uciTransportUnavailable()
	}
	return transport.runtime, nil
}

func contextAwareContextRefFromProto(reference *pb.ContextRef) uci.ContextRef {
	if reference == nil {
		return uci.ContextRef{}
	}
	result := uci.ContextRef{
		SourceID:          reference.GetSourceId(),
		CheckoutID:        reference.GetCheckoutId(),
		ViewID:            reference.GetViewId(),
		AnalysisProfileID: reference.GetAnalysisProfileId(),
		Generation:        reference.GetGeneration(),
	}
	if reference.SpaceId != nil {
		spaceID := reference.GetSpaceId()
		result.SpaceID = &spaceID
	}
	return result
}

func contextAwareProtoContextRef(reference uci.ContextRef) *pb.ContextRef {
	result := &pb.ContextRef{
		SourceId:          reference.SourceID,
		CheckoutId:        reference.CheckoutID,
		ViewId:            reference.ViewID,
		AnalysisProfileId: reference.AnalysisProfileID,
		Generation:        reference.Generation,
	}
	if reference.SpaceID != nil {
		spaceID := *reference.SpaceID
		result.SpaceId = &spaceID
	}
	return result
}

func contextAwareScopeMatchesRef(scope *pb.CodeIndexScope, reference uci.ContextRef) bool {
	return scope != nil &&
		scope.GetSourceId() == reference.SourceID &&
		scope.GetCheckoutId() == reference.CheckoutID &&
		scope.GetAnalysisProfileId() == reference.AnalysisProfileID
}

func contextAwareProtoRefMatches(reference uci.ContextRef, candidate *pb.ContextRef) bool {
	if candidate == nil || (reference.SpaceID == nil) != (candidate.SpaceId == nil) {
		return false
	}
	if reference.SpaceID != nil && *reference.SpaceID != candidate.GetSpaceId() {
		return false
	}
	return reference.SourceID == candidate.GetSourceId() &&
		reference.CheckoutID == candidate.GetCheckoutId() &&
		reference.ViewID == candidate.GetViewId() &&
		reference.AnalysisProfileID == candidate.GetAnalysisProfileId() &&
		reference.Generation == candidate.GetGeneration()
}

func contextAwareAliasMatchesRef(target uci.AliasTarget, reference uci.ContextRef) bool {
	if target.SpaceID != nil && (reference.SpaceID == nil || *target.SpaceID != *reference.SpaceID) {
		return false
	}
	return target.SourceID == nil || *target.SourceID == reference.SourceID
}

func contextAwareRuntimeError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if contextErr := uciTransportContextError(ctx); contextErr != nil {
		return contextErr
	}
	var closed *uci.ContextError
	if errors.As(err, &closed) {
		return contextAwareClosedError(closed.Code())
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return status.FromContextError(err).Err()
	}
	return status.Error(codes.Internal, "UCI context runtime failed")
}

func contextAwareClosedError(code uci.ContextErrorCode) error {
	switch code {
	case uci.ContextRequired, uci.ContextMismatch:
		return status.Error(codes.FailedPrecondition, string(code))
	case uci.PermissionDenied:
		return status.Error(codes.PermissionDenied, string(code))
	default:
		return status.Error(codes.Internal, "UCI context resolution failed")
	}
}

func newContextAwareHandle() (string, error) {
	bytes := make([]byte, contextAwareHandleBytes)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}
