package grpcserver

import (
	"context"
	"errors"
	"strings"

	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/uci"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const (
	legacyCodeIndexAliasDomain = "project"
	legacyCodeIndexAliasScheme = "project_id"
)

// ContextAwareUCIRuntime is the scoped UCI runtime used after context resolution.
type ContextAwareUCIRuntime interface {
	uci.IndexBindingCatalog
	LegacyCodeIndexNegotiate(context.Context, uci.AuthorizedContext, *pb.CodeIndexNegotiateRequest) (*pb.CodeIndexNegotiateResponse, error)
	BeginCodeIndex(context.Context, uci.IndexBinding, *pb.BeginCodeIndexRequest) (*pb.BeginCodeIndexResponse, error)
	StageCodeIndex(context.Context, uci.IndexBinding, []*pb.StageCodeIndexFrame) (*pb.StageCodeIndexResponse, error)
	FinalizeCodeIndex(context.Context, uci.IndexBinding, *pb.FinalizeCodeIndexRequest) (*pb.FinalizeCodeIndexResponse, error)
	QueryCode(context.Context, uci.AuthorizedContext, *pb.QueryCodeRequest) (*pb.QueryCodeResponse, error)
	ExploreCode(context.Context, uci.AuthorizedContext, *pb.ExploreCodeRequest) (*pb.ExploreCodeResponse, error)
}

// CodeContextHandlePort owns opaque client-session context handles. It accepts
// no project, CWD, or path authority, and reauthorization must not replace the
// resolver's selected default.
type CodeContextHandlePort interface {
	AuthorizeCodeContextHandle(context.Context, string, string) (uci.IndexBinding, error)
	IssueCodeContextHandle(context.Context, string, uci.IndexBinding) (string, error)
	AuthorizeCodeIndexScope(context.Context, string, uci.IndexScope, string) (uci.IndexBinding, error)
}

// legacyCodeIndexNegotiator is an optional adapter for the legacy negotiate RPC.
type legacyCodeIndexNegotiator interface {
	LegacyCodeIndexNegotiate(context.Context, *pb.CodeIndexNegotiateRequest) (*pb.CodeIndexNegotiateResponse, error)
}

type contextAwareUCITransport struct {
	resolver      *uci.ContextResolver
	aliasResolver *uci.AliasResolver
	runtime       ContextAwareUCIRuntime
	handlePort    CodeContextHandlePort
}

// NewContextAwareUCITransport creates the UCI adapter that resolves each call's context.
func NewContextAwareUCITransport(resolver *uci.ContextResolver, aliasResolver *uci.AliasResolver, runtime ContextAwareUCIRuntime, handlePort CodeContextHandlePort) UCITransport {
	return &contextAwareUCITransport{
		resolver:      resolver,
		aliasResolver: aliasResolver,
		runtime:       runtime,
		handlePort:    handlePort,
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
	workstationID   string
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
	if handle := request.GetContextHandle(); handle != "" {
		return transport.bindCodeContextHandle(ctx, caller, handle)
	}
	if requested := request.GetRequestedContext(); requested != nil {
		return transport.bindRequestedCodeContext(ctx, caller, contextAwareContextRefFromProto(requested))
	}
	authorized, err := transport.resolveBound(ctx, caller)
	if err != nil {
		return nil, err
	}
	return transport.bindAuthorizedCodeContext(ctx, caller, authorized)
}

func (transport *contextAwareUCITransport) bindCodeContextHandle(ctx context.Context, caller contextAwareCaller, handle string) (*pb.BindCodeContextResponse, error) {
	port, err := transport.contextAwareHandlePort()
	if err != nil {
		return nil, err
	}
	binding, err := port.AuthorizeCodeContextHandle(contextAwarePortContext(ctx, caller), caller.clientSessionID, handle)
	if err != nil {
		return nil, contextAwareRuntimeError(ctx, err)
	}
	if err := uciTransportContextError(ctx); err != nil {
		return nil, err
	}
	binding, err = contextAwareValidatedIndexBinding(ctx, binding)
	if err != nil {
		return nil, err
	}
	return contextAwareBindCodeContextResponse(handle, binding), nil
}

func (transport *contextAwareUCITransport) bindRequestedCodeContext(ctx context.Context, caller contextAwareCaller, reference uci.ContextRef) (*pb.BindCodeContextResponse, error) {
	authorized, err := transport.bindExplicit(ctx, caller, reference)
	if err != nil {
		return nil, err
	}
	return transport.bindAuthorizedCodeContext(ctx, caller, authorized)
}

func (transport *contextAwareUCITransport) bindAuthorizedCodeContext(ctx context.Context, caller contextAwareCaller, authorized uci.AuthorizedContext) (*pb.BindCodeContextResponse, error) {
	if err := uciTransportContextError(ctx); err != nil {
		return nil, err
	}
	runtime, err := transport.contextAwareRuntime()
	if err != nil {
		return nil, err
	}
	authorizedRef := authorized.Ref()
	selector, selectorErr := uci.ContextIndexBindingSelector(authorizedRef)
	if selectorErr != nil {
		return nil, contextAwareClosedError(uci.ContextMismatch)
	}
	binding, err := runtime.LoadIndexBinding(ctx, selector)
	if err != nil {
		return nil, contextAwareRuntimeError(ctx, err)
	}
	if err := uciTransportContextError(ctx); err != nil {
		return nil, err
	}
	binding, err = contextAwareValidatedIndexBinding(ctx, binding)
	if err != nil {
		return nil, err
	}
	if binding.Context == nil || !contextAwareContextRefsEqual(authorizedRef, *binding.Context) {
		return nil, status.Error(codes.Internal, "UCI context binding does not match authorized context")
	}
	port, err := transport.contextAwareHandlePort()
	if err != nil {
		return nil, err
	}
	handle, err := port.IssueCodeContextHandle(contextAwarePortContext(ctx, caller), caller.clientSessionID, binding.Clone())
	if err != nil {
		return nil, contextAwareRuntimeError(ctx, err)
	}
	if err := uciTransportContextError(ctx); err != nil {
		return nil, err
	}
	return contextAwareBindCodeContextResponse(handle, binding), nil
}

func (transport *contextAwareUCITransport) BeginCodeIndex(ctx context.Context, request *pb.BeginCodeIndexRequest) (*pb.BeginCodeIndexResponse, error) {
	if request == nil {
		return nil, uciTransportInvalidArgument()
	}
	caller, err := contextAwareCallerFrom(ctx)
	if err != nil {
		return nil, err
	}
	binding, err := transport.authorizeCodeIndexScope(ctx, caller, request.GetScope())
	if err != nil {
		return nil, err
	}
	if !contextAwareExpectedParentMatchesBinding(request.GetExpectedParent(), binding) {
		return nil, contextAwareClosedError(uci.ContextMismatch)
	}
	runtime, err := transport.contextAwareRuntime()
	if err != nil {
		return nil, err
	}

	response, err := runtime.BeginCodeIndex(ctx, binding.Clone(), proto.Clone(request).(*pb.BeginCodeIndexRequest))
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
	binding, err := transport.authorizeCodeIndexScope(ctx, caller, frames[0].GetScope())
	if err != nil {
		return nil, err
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
	response, err := runtime.StageCodeIndex(ctx, binding.Clone(), clonedFrames)
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
	binding, err := transport.authorizeCodeIndexScope(ctx, caller, request.GetScope())
	if err != nil {
		return nil, err
	}
	if !contextAwareFinalizeParentMatchesScope(request.GetExpectedParent(), binding) {
		return nil, contextAwareClosedError(uci.ContextMismatch)
	}
	runtime, err := transport.contextAwareRuntime()
	if err != nil {
		return nil, err
	}

	response, err := runtime.FinalizeCodeIndex(ctx, binding.Clone(), proto.Clone(request).(*pb.FinalizeCodeIndexRequest))
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
	clientSessionID, ok := contextAwareSourceSession(ctx)
	if !ok {
		return contextAwareCaller{}, contextAwareClosedError(uci.ContextMismatch)
	}
	principal, _, owned := identity.MemoryOwner()
	workstationID := identity.WorkstationID()
	if !owned || principal != identity.Principal || !validUCIIdentifier(principal, maxUCITransportIdentifierBytes) || !validUCIIdentifier(workstationID, maxUCITransportIdentifierBytes) {
		return contextAwareCaller{}, contextAwareClosedError(uci.ContextMismatch)
	}
	caller := contextAwareCaller{
		clientSessionID: clientSessionID,
		authRealm:       string(identity.Source),
		principal:       principal,
		workstationID:   workstationID,
	}
	if caller.authRealm == "" {
		return contextAwareCaller{}, contextAwareClosedError(uci.ContextMismatch)
	}
	return caller, nil
}

func contextAwareSourceSession(ctx context.Context) (string, bool) {
	carried := auditcontext.SourceSession(ctx)
	var received string
	if incoming, found := metadata.FromIncomingContext(ctx); found {
		values := incoming.Get(auditcontext.SourceSessionMetadataKey)
		if len(values) > 1 {
			return "", false
		}
		if len(values) == 1 {
			received = strings.TrimSpace(values[0])
			if received == "" || received != values[0] {
				return "", false
			}
		}
	}
	if carried != "" && received != "" && carried != received {
		return "", false
	}
	if carried != "" {
		return carried, true
	}
	return received, received != ""
}

func contextAwarePortContext(ctx context.Context, caller contextAwareCaller) context.Context {
	ctx = auditcontext.WithSourceSession(ctx, caller.clientSessionID)
	return mcp.ContextWithSession(ctx, caller.clientSessionID)
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
		WorkstationID:   caller.workstationID,
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
		WorkstationID:   caller.workstationID,
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

func (transport *contextAwareUCITransport) contextAwareHandlePort() (CodeContextHandlePort, error) {
	if transport == nil || transport.handlePort == nil {
		return nil, uciTransportUnavailable()
	}
	return transport.handlePort, nil
}

func (transport *contextAwareUCITransport) authorizeCodeIndexScope(ctx context.Context, caller contextAwareCaller, scope *pb.CodeIndexScope) (uci.IndexBinding, error) {
	port, err := transport.contextAwareHandlePort()
	if err != nil {
		return uci.IndexBinding{}, err
	}
	binding, err := port.AuthorizeCodeIndexScope(contextAwarePortContext(ctx, caller), caller.clientSessionID, contextAwareIndexScopeFromProto(scope), scope.GetAnalysisProfileId())
	if err != nil {
		return uci.IndexBinding{}, contextAwareRuntimeError(ctx, err)
	}
	if err := uciTransportContextError(ctx); err != nil {
		return uci.IndexBinding{}, err
	}
	binding, err = contextAwareValidatedIndexBinding(ctx, binding)
	if err != nil {
		return uci.IndexBinding{}, err
	}
	if !contextAwareIndexScopeMatchesBinding(scope, binding) {
		return uci.IndexBinding{}, contextAwareClosedError(uci.ContextMismatch)
	}
	return binding, nil
}

func contextAwareValidatedIndexBinding(ctx context.Context, binding uci.IndexBinding) (uci.IndexBinding, error) {
	binding = binding.Clone()
	if err := binding.Validate(); err != nil {
		return uci.IndexBinding{}, contextAwareRuntimeError(ctx, err)
	}
	return binding, nil
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

func contextAwareProtoIndexScope(scope uci.IndexScope, profileID string) *pb.CodeIndexScope {
	return &pb.CodeIndexScope{
		SourceId:          scope.SourceID,
		CheckoutId:        scope.CheckoutID,
		IncarnationId:     scope.IncarnationID,
		AnalysisProfileId: profileID,
	}
}

func contextAwareBindCodeContextResponse(handle string, binding uci.IndexBinding) *pb.BindCodeContextResponse {
	response := &pb.BindCodeContextResponse{
		ContextHandle: handle,
		IndexScope:    contextAwareProtoIndexScope(binding.Scope, binding.ProfileID),
		LocalRootId:   binding.LocalRootID,
		WorkstationId: binding.WorkstationID,
	}
	if binding.Context != nil {
		response.Context = contextAwareProtoContextRef(*binding.Context)
	}
	return response
}

func contextAwareIndexScopeFromProto(scope *pb.CodeIndexScope) uci.IndexScope {
	return uci.IndexScope{
		SourceID:      scope.GetSourceId(),
		CheckoutID:    scope.GetCheckoutId(),
		IncarnationID: scope.GetIncarnationId(),
	}
}

func contextAwareIndexScopeMatchesBinding(scope *pb.CodeIndexScope, binding uci.IndexBinding) bool {
	return scope != nil &&
		scope.GetSourceId() == binding.Scope.SourceID &&
		scope.GetCheckoutId() == binding.Scope.CheckoutID &&
		scope.GetIncarnationId() == binding.Scope.IncarnationID &&
		scope.GetAnalysisProfileId() == binding.ProfileID
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

func contextAwareContextRefsEqual(left, right uci.ContextRef) bool {
	if (left.SpaceID == nil) != (right.SpaceID == nil) {
		return false
	}
	if left.SpaceID != nil && *left.SpaceID != *right.SpaceID {
		return false
	}
	return left.SourceID == right.SourceID &&
		left.CheckoutID == right.CheckoutID &&
		left.ViewID == right.ViewID &&
		left.AnalysisProfileID == right.AnalysisProfileID &&
		left.Generation == right.Generation
}

func contextAwareExpectedParentMatchesBinding(parent *pb.ContextRef, binding uci.IndexBinding) bool {
	return parent == nil || (binding.Context != nil && contextAwareProtoRefMatches(*binding.Context, parent))
}

func contextAwareFinalizeParentMatchesScope(parent *pb.ContextRef, binding uci.IndexBinding) bool {
	return parent == nil || (validUCIContextRef(parent) &&
		parent.GetSourceId() == binding.Scope.SourceID &&
		parent.GetCheckoutId() == binding.Scope.CheckoutID &&
		parent.GetAnalysisProfileId() == binding.ProfileID)
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
	switch {
	case errors.Is(err, uci.ErrPublicationLeaseStale):
		return status.Error(codes.FailedPrecondition, uci.ErrPublicationLeaseStale.Error())
	case errors.Is(err, uci.ErrPublicationBuildIncomplete):
		return status.Error(codes.FailedPrecondition, uci.ErrPublicationBuildIncomplete.Error())
	case errors.Is(err, uci.ErrPublicationIdempotencyMismatch):
		return status.Error(codes.FailedPrecondition, uci.ErrPublicationIdempotencyMismatch.Error())
	}
	if contextAwareTrustedClosedStatus(err) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return status.FromContextError(err).Err()
	}
	return status.Error(codes.Internal, "UCI context runtime failed")
}

func contextAwareTrustedClosedStatus(err error) bool {
	grpcStatus, ok := status.FromError(err)
	if !ok {
		return false
	}
	switch grpcStatus.Code() {
	case codes.FailedPrecondition:
		return grpcStatus.Message() == string(uci.ContextRequired) || grpcStatus.Message() == string(uci.ContextMismatch)
	case codes.PermissionDenied:
		return grpcStatus.Message() == string(uci.PermissionDenied)
	default:
		return false
	}
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
