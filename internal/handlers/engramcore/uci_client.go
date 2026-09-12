package engramcore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/module"
	"github.com/thebtf/engram/internal/uci"
	pb "github.com/thebtf/engram/proto/engram/v1"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
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
	maxUCIClientPayloadBytes      = uci.IndexAdmissionMaxEncodedFrameBytes
	maxUCIClientStageBytes        = uci.IndexAdmissionMaxTotalEncodedBytes
	maxUCIClientStageFrames       = uci.IndexAdmissionMaxFrames
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

// ResolvedIndexTarget is the fully authorized UCI identity for one index run.
// Binding is cloned from the server response and may have no Context only for a
// registered checkout that has no published View. Its connection remains private
// so callers cannot turn a raw project selector into authority after resolution.
type ResolvedIndexTarget struct {
	ClientSessionID string
	ContextHandle   string
	Binding         uci.IndexBinding

	connection  *grpc.ClientConn
	intentClaim *uci.IndexIntentExecutionClaim
}

// Clone returns a target with an independently owned server binding.
func (target ResolvedIndexTarget) Clone() ResolvedIndexTarget {
	clone := target
	clone.Binding = target.Binding.Clone()
	return clone
}

// BindingClone returns the server-authorized binding without exposing its
// mutable Context pointer.
func (target ResolvedIndexTarget) BindingClone() uci.IndexBinding {
	return target.Binding.Clone()
}

// ContextClone returns the resolved View when one exists, or nil for a valid
// registered checkout that has not yet published a View.
func (target ResolvedIndexTarget) ContextClone() *uci.ContextRef {
	return target.Binding.Clone().Context
}

// WithIndexIntentClaim attaches one transient daemon execution token without
// changing the server-authorized target identity.
func (target ResolvedIndexTarget) WithIndexIntentClaim(claim uci.IndexIntentExecutionClaim) (ResolvedIndexTarget, error) {
	if claim.Validate() != nil {
		return ResolvedIndexTarget{}, errUCIClientInvalidRequest
	}
	clone := target.Clone()
	claimCopy := claim
	clone.intentClaim = &claimCopy
	return clone, nil
}

func (target ResolvedIndexTarget) IndexIntentClaim() *uci.IndexIntentExecutionClaim {
	if target.intentClaim == nil {
		return nil
	}
	claim := *target.intentClaim
	return &claim
}

// IndexResult is the prepared indexer's result. Context must be the real,
// newly published ContextRef from successful server-side finalization; this
// adapter never synthesizes a parent or View.
type IndexResult struct {
	Context  uci.ContextRef
	Embedded int
	Deleted  int
	Uploaded int
	Errors   []string
}

type IndexIntentOffer struct {
	IntentID       string
	Kind           uci.IndexIntentKind
	PreviousView   *uci.ContextRef
	State          uci.IndexIntentState
	ClaimEpoch     int64
	LeaseExpiresAt time.Time
}

// UCIIndexClient is the narrow transport surface a prepared indexer uses to
// publish its already-authoritative manifest. It intentionally exposes no
// legacy CodeIndexNegotiate or CodeIndexUpload methods.
type UCIIndexClient interface {
	Begin(context.Context, *pb.BeginCodeIndexRequest) (*pb.BeginCodeIndexResponse, error)
	Stage(context.Context, []*pb.StageCodeIndexFrame) (*pb.StageCodeIndexResponse, error)
	Finalize(context.Context, *pb.FinalizeCodeIndexRequest) (*pb.FinalizeCodeIndexResponse, error)
}

// PreparedIndexCollaborator performs local prepared index work for a target
// already authorized by the server. It must not select Context, Scope, or a
// checkout incarnation.
type PreparedIndexCollaborator interface {
	IndexPreparedCodebase(context.Context, ResolvedIndexTarget, string, UCIIndexClient) (*IndexResult, error)
}

// PreparedIndexConfiguration binds a prepared index collaborator to the
// daemon identities and deterministic parser bundle that constructed it. The
// configuration is write-once: replacing a collaborator after startup could
// mix a checkout's local evidence with a different workstation or profile.
type PreparedIndexConfiguration struct {
	WorkstationID      string
	ClientInstanceID   string
	ParserBundleDigest string
}

func (configuration PreparedIndexConfiguration) valid() bool {
	return validUCIClientIdentifier(configuration.WorkstationID, maxUCIClientIdentifierBytes) &&
		validUCIClientIdentifier(configuration.ClientInstanceID, maxUCIClientIdentifierBytes) &&
		validUCIClientSHA256Digest(configuration.ParserBundleDigest)
}

// ConfigurePreparedIndexCollaborator installs the daemon-local prepared-index
// collaborator before the first index request. It never replaces an existing
// collaborator, including one supplied by the existing injection constructor.
func (m *Module) ConfigurePreparedIndexCollaborator(configuration PreparedIndexConfiguration, collaborator PreparedIndexCollaborator) error {
	if m == nil {
		return errors.New("uci prepared index: module is unavailable")
	}
	if !configuration.valid() {
		return errors.New("uci prepared index: collaborator configuration is invalid")
	}
	if uciClientIsNil(collaborator) {
		return errors.New("uci prepared index: collaborator is required")
	}

	m.preparedIndexMu.Lock()
	defer m.preparedIndexMu.Unlock()
	if m.shuttingDown {
		return errors.New("uci prepared index: module is shutting down")
	}
	if !uciClientIsNil(m.preparedIndex) || m.preparedIndexConfiguration != nil {
		return errors.New("uci prepared index: collaborator is already configured")
	}
	configured := configuration
	m.preparedIndex = collaborator
	m.preparedIndexConfiguration = &configured
	return nil
}

func (m *Module) preparedIndexCollaborator() PreparedIndexCollaborator {
	if m == nil {
		return nil
	}
	m.preparedIndexMu.RLock()
	defer m.preparedIndexMu.RUnlock()
	if m.shuttingDown || uciClientIsNil(m.preparedIndex) {
		return nil
	}
	return m.preparedIndex
}

type uciClientRPC interface {
	BindCodeContext(context.Context, *pb.BindCodeContextRequest, ...grpc.CallOption) (*pb.BindCodeContextResponse, error)
	PollCodeIndexIntents(context.Context, *pb.PollCodeIndexIntentsRequest, ...grpc.CallOption) (*pb.PollCodeIndexIntentsResponse, error)
	UpdateCodeIndexIntent(context.Context, *pb.UpdateCodeIndexIntentRequest, ...grpc.CallOption) (*pb.UpdateCodeIndexIntentResponse, error)
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

var _ UCIIndexClient = (*uciClient)(nil)

// UCIIndexAdapter owns typed code-index operations over one engramcore module.
// It sits beside the legacy raw-project proxy because the two incompatible
// ProxyHandleTool signatures must not share a receiver.
type UCIIndexAdapter struct {
	module *Module
}

// NewUCIIndexAdapter returns the typed UCI adapter for one existing module.
func NewUCIIndexAdapter(module *Module) *UCIIndexAdapter {
	return &UCIIndexAdapter{module: module}
}

// ResolveIndexTarget binds an optional opaque client-owned handle at the
// configured server. An empty handle leaves selection to the server's existing
// client-scoped default and retains only server-authorized target authority.
func (a *UCIIndexAdapter) ResolveIndexTarget(ctx context.Context, project muxcore.ProjectContext, contextHandle string) (ResolvedIndexTarget, error) {
	if err := uciClientContextError("ResolveIndexTarget", ctx); err != nil {
		return ResolvedIndexTarget{}, err
	}
	clientSessionID, err := requireUCITransportSession(ctx)
	if err != nil {
		return ResolvedIndexTarget{}, err
	}
	if contextHandle != "" && !validUCIClientIdentifier(contextHandle, 128) {
		return ResolvedIndexTarget{}, uciIndexSourceUnavailable("resolved context handle is unavailable")
	}
	if a == nil || a.module == nil {
		return ResolvedIndexTarget{}, uciIndexSourceUnavailable("UCI server is unavailable")
	}
	m := a.module

	serverURL, err := m.requireServerURL(project)
	if err != nil {
		return ResolvedIndexTarget{}, uciIndexSourceUnavailable("UCI server is unavailable")
	}
	token := m.envFor(project, config.EnvWorkstationToken)
	conn, err := m.pool.getOrDialGRPC(serverURL, token)
	if err != nil {
		return ResolvedIndexTarget{}, uciIndexSourceUnavailable("UCI server is unavailable")
	}

	bound, err := newUCIClient(pb.NewEngramServiceClient(conn)).Bind(ctx,
		&pb.BindCodeContextRequest{ClientSessionId: clientSessionID, ContextHandle: contextHandle},
	)
	if err != nil {
		return ResolvedIndexTarget{}, uciIndexContextResolutionError(err)
	}
	binding, err := uciClientIndexBindingFromBindResponse(bound)
	if err != nil {
		return ResolvedIndexTarget{}, uciIndexSourceUnavailable("UCI binding is invalid")
	}

	return ResolvedIndexTarget{
		ClientSessionID: clientSessionID,
		ContextHandle:   bound.GetContextHandle(),
		Binding:         binding,
		connection:      conn,
	}, nil
}

// RebindIndexTarget refreshes the server binding for a previously resolved
// target through its retained authenticated connection. It never reloads a
// project, path, environment, or raw selector: the original client session and
// opaque handle remain the entire server authority input. A newer View is
// allowed, but the checkout identity and workstation must remain unchanged.
func (a *UCIIndexAdapter) RebindIndexTarget(ctx context.Context, target ResolvedIndexTarget) (ResolvedIndexTarget, error) {
	if err := uciClientContextError("RebindIndexTarget", ctx); err != nil {
		return ResolvedIndexTarget{}, err
	}
	clientSessionID, err := requireUCITransportSession(ctx)
	if err != nil {
		return ResolvedIndexTarget{}, err
	}
	if a == nil || a.module == nil || !validResolvedIndexTarget(target) || target.ClientSessionID != clientSessionID {
		return ResolvedIndexTarget{}, uciIndexSourceUnavailable("resolved target is unavailable")
	}
	conn, err := a.connectionForResolvedIndexTarget(target)
	if err != nil {
		return ResolvedIndexTarget{}, err
	}
	bound, err := newUCIClient(pb.NewEngramServiceClient(conn)).Bind(ctx, &pb.BindCodeContextRequest{
		ClientSessionId: target.ClientSessionID,
		ContextHandle:   target.ContextHandle,
	})
	if err != nil {
		return ResolvedIndexTarget{}, err
	}
	binding, err := uciClientIndexBindingFromBindResponse(bound)
	if err != nil {
		return ResolvedIndexTarget{}, uciIndexSourceUnavailable("UCI binding is invalid")
	}
	if !sameUCIResolvedIndexIdentity(target.BindingClone(), binding) {
		return ResolvedIndexTarget{}, uciIndexSourceUnavailable("UCI binding changed during rebind")
	}
	rebound := target.Clone()
	rebound.Binding = binding.Clone()
	return rebound, nil
}

func (a *UCIIndexAdapter) PollIndexIntent(ctx context.Context, target ResolvedIndexTarget, clientInstanceID, processNonce string) (*IndexIntentOffer, error) {
	if err := uciClientContextError("PollIndexIntent", ctx); err != nil {
		return nil, err
	}
	if !validResolvedIndexTarget(target) || !validUCIClientIdentifier(clientInstanceID, maxUCIClientIdentifierBytes) || !validUCIClientIdentifier(processNonce, maxUCIClientIdentifierBytes) {
		return nil, errUCIClientInvalidRequest
	}
	conn, err := a.connectionForResolvedIndexTarget(target)
	if err != nil {
		return nil, err
	}
	response, err := pb.NewEngramServiceClient(conn).PollCodeIndexIntents(uciClientOutgoingContext(ctx), &pb.PollCodeIndexIntentsRequest{
		Target: uciClientIntentTarget(target), ClientInstanceId: clientInstanceID, ProcessNonce: processNonce,
	})
	if err != nil {
		return nil, uciClientCallError("PollIndexIntent", err)
	}
	if response == nil || response.GetOffer() == nil {
		return nil, nil
	}
	offer := response.GetOffer()
	var previous *uci.ContextRef
	if wirePrevious := offer.GetPreviousContext(); wirePrevious != nil {
		value := uciClientContextRefFromProto(wirePrevious)
		if value.SourceID != target.Binding.Scope.SourceID || value.CheckoutID != target.Binding.Scope.CheckoutID || value.AnalysisProfileID != target.Binding.ProfileID {
			return nil, errUCIClientInvalidResponse
		}
		previous = &value
	}
	state := uci.IndexIntentState(offer.GetState())
	if !validUCIClientIdentifier(offer.GetIntentRef(), maxUCIClientIdentifierBytes) ||
		(offer.GetKind() != string(uci.IndexIntentReindex) && offer.GetKind() != string(uci.IndexIntentReconcile)) ||
		(state != uci.IndexIntentQueued && state != uci.IndexIntentAcknowledged && state != uci.IndexIntentRunning) {
		return nil, errUCIClientInvalidResponse
	}
	result := &IndexIntentOffer{IntentID: offer.GetIntentRef(), Kind: uci.IndexIntentKind(offer.GetKind()), PreviousView: previous, State: state, ClaimEpoch: int64(offer.GetOwnerEpoch())}
	if offer.GetLeaseExpiresAt() != nil {
		result.LeaseExpiresAt = offer.GetLeaseExpiresAt().AsTime().UTC()
	}
	if (state == uci.IndexIntentQueued && (result.ClaimEpoch != 0 || !result.LeaseExpiresAt.IsZero())) || (state != uci.IndexIntentQueued && (result.ClaimEpoch < 1 || result.LeaseExpiresAt.IsZero())) {
		return nil, errUCIClientInvalidResponse
	}
	return result, nil
}

func (a *UCIIndexAdapter) UpdateIndexIntent(ctx context.Context, target ResolvedIndexTarget, clientInstanceID, processNonce, intentID string, update uci.IndexIntentUpdate) (uci.IndexIntentUpdateResult, error) {
	if err := uciClientContextError("UpdateIndexIntent", ctx); err != nil {
		return uci.IndexIntentUpdateResult{}, err
	}
	if !validResolvedIndexTarget(target) || !validUCIClientIdentifier(clientInstanceID, maxUCIClientIdentifierBytes) || !validUCIClientIdentifier(processNonce, maxUCIClientIdentifierBytes) || update.Validate() != nil {
		return uci.IndexIntentUpdateResult{}, errUCIClientInvalidRequest
	}
	conn, err := a.connectionForResolvedIndexTarget(target)
	if err != nil {
		return uci.IndexIntentUpdateResult{}, err
	}
	response, err := pb.NewEngramServiceClient(conn).UpdateCodeIndexIntent(uciClientOutgoingContext(ctx), &pb.UpdateCodeIndexIntentRequest{
		Target: uciClientIntentTarget(target), ClientInstanceId: clientInstanceID, ProcessNonce: processNonce,
		IntentRef: intentID, Operation: string(update.Operation), OperationRef: update.OperationRef, OwnerEpoch: uint64(update.ClaimEpoch),
	})
	if err != nil {
		return uci.IndexIntentUpdateResult{}, uciClientCallError("UpdateIndexIntent", err)
	}
	result := uci.IndexIntentUpdateResult{
		IntentID: response.GetIntentRef(), State: uci.IndexIntentState(response.GetState()), Attempt: int(response.GetAttempt()), ClaimEpoch: int64(response.GetOwnerEpoch()),
	}
	if response.GetLeaseExpiresAt() != nil {
		result.LeaseExpiresAt = response.GetLeaseExpiresAt().AsTime().UTC()
	}
	if result.IntentID != intentID || result.Validate() != nil {
		return uci.IndexIntentUpdateResult{}, errUCIClientInvalidResponse
	}
	return result, nil
}

func uciClientIntentTarget(target ResolvedIndexTarget) *pb.CodeIndexIntentTarget {
	binding := target.BindingClone()
	return &pb.CodeIndexIntentTarget{
		ClientSessionId: target.ClientSessionID, ContextHandle: target.ContextHandle,
		Scope: uciClientProtoScope(binding), LocalRootId: binding.LocalRootID, WorkstationId: binding.WorkstationID,
	}
}

func uciClientProtoScope(binding uci.IndexBinding) *pb.CodeIndexScope {
	return &pb.CodeIndexScope{
		SourceId: binding.Scope.SourceID, CheckoutId: binding.Scope.CheckoutID,
		IncarnationId: binding.Scope.IncarnationID, AnalysisProfileId: binding.ProfileID,
	}
}

func uciClientContextRefFromProto(reference *pb.ContextRef) uci.ContextRef {
	if reference == nil {
		return uci.ContextRef{}
	}
	result := uci.ContextRef{
		SourceID: reference.GetSourceId(), CheckoutID: reference.GetCheckoutId(), ViewID: reference.GetViewId(),
		Generation: reference.GetGeneration(), AnalysisProfileID: reference.GetAnalysisProfileId(),
	}
	if reference.SpaceId != nil {
		spaceID := reference.GetSpaceId()
		result.SpaceID = &spaceID
	}
	return result
}

// IndexCodebase executes only prepared UCI index work. Without the injected
// collaborator it fails closed rather than scanning raw project bytes or
// publishing an invented empty census.
func (a *UCIIndexAdapter) IndexCodebase(ctx context.Context, target ResolvedIndexTarget, rootHint string) (*IndexResult, error) {
	if err := uciClientContextError("IndexCodebase", ctx); err != nil {
		return nil, err
	}
	clientSessionID, err := requireUCITransportSession(ctx)
	if err != nil {
		return nil, err
	}
	if a == nil || a.module == nil || rootHint == "" || !validResolvedIndexTarget(target) || target.ClientSessionID != clientSessionID {
		return nil, uciIndexSourceUnavailable("prepared code index is unavailable")
	}
	collaborator := a.module.preparedIndexCollaborator()
	if collaborator == nil {
		return nil, uciIndexSourceUnavailable("prepared code index is unavailable")
	}
	conn, err := a.connectionForResolvedIndexTarget(target)
	if err != nil {
		return nil, err
	}
	result, err := collaborator.IndexPreparedCodebase(
		ctx,
		target.Clone(),
		rootHint,
		newUCIClient(pb.NewEngramServiceClient(conn)),
	)
	if err != nil {
		return nil, err
	}
	if !validUCIIndexResult(result, target) {
		return nil, uciIndexSourceUnavailable("prepared indexer returned invalid result")
	}
	return result, nil
}

// ProxyHandleTool forwards a typed target call without a raw project field.
// The caller owns the typed context arguments; this method supplies only the
// per-session provenance and connection established during resolution.
func (a *UCIIndexAdapter) ProxyHandleTool(ctx context.Context, target ResolvedIndexTarget, name string, args json.RawMessage) (json.RawMessage, error) {
	if err := uciClientContextError("ProxyHandleTool", ctx); err != nil {
		return nil, err
	}
	clientSessionID, err := requireUCITransportSession(ctx)
	if err != nil {
		return nil, err
	}
	if a == nil || a.module == nil || name == "" || !validResolvedIndexTarget(target) || target.ClientSessionID != clientSessionID {
		return nil, uciIndexSourceUnavailable("resolved target is unavailable")
	}
	conn, err := a.connectionForResolvedIndexTarget(target)
	if err != nil {
		return nil, err
	}
	ctx = uciClientOutgoingContext(ctx)
	response, err := pb.NewEngramServiceClient(conn).CallTool(ctx, &pb.CallToolRequest{
		ToolName:      name,
		ArgumentsJson: args,
		SessionId:     clientSessionID,
	})
	if err != nil {
		return nil, fmt.Errorf("gRPC CallTool: %w", err)
	}
	if response == nil {
		return nil, uciIndexSourceUnavailable("UCI status proxy returned no response")
	}
	block, moduleErr := buildInnerBlock(response.ContentJson)
	if moduleErr != nil {
		return nil, moduleErr
	}
	if response.IsError {
		return nil, &module.ProxyIsError{RawContent: block}
	}
	return block, nil
}

func (a *UCIIndexAdapter) connectionForResolvedIndexTarget(target ResolvedIndexTarget) (*grpc.ClientConn, error) {
	if a == nil || a.module == nil || target.connection == nil || !validResolvedIndexTarget(target) {
		return nil, uciIndexSourceUnavailable("resolved target is unavailable")
	}
	return target.connection, nil
}

func sameUCIResolvedIndexIdentity(previous, current uci.IndexBinding) bool {
	return previous.Scope.SourceID == current.Scope.SourceID &&
		previous.Scope.CheckoutID == current.Scope.CheckoutID &&
		previous.Scope.IncarnationID == current.Scope.IncarnationID &&
		previous.ProfileID == current.ProfileID &&
		previous.LocalRootID == current.LocalRootID &&
		previous.WorkstationID == current.WorkstationID
}

func validResolvedIndexTarget(target ResolvedIndexTarget) bool {
	return validUCIClientIdentifier(target.ClientSessionID, maxUCIClientIdentifierBytes) &&
		validUCIClientIdentifier(target.ContextHandle, 128) &&
		target.Binding.Validate() == nil
}

func validUCIIndexResult(result *IndexResult, target ResolvedIndexTarget) bool {
	if result == nil {
		return false
	}
	binding := target.Binding
	context := result.Context
	binding.Context = &context
	return binding.Validate() == nil
}

func uciClientIndexBindingFromBindResponse(response *pb.BindCodeContextResponse) (uci.IndexBinding, error) {
	if response == nil {
		return uci.IndexBinding{}, errUCIClientEmptyResponse
	}
	scope := response.GetIndexScope()
	binding := uci.IndexBinding{
		Scope: uci.IndexScope{
			SourceID:      scope.GetSourceId(),
			CheckoutID:    scope.GetCheckoutId(),
			IncarnationID: scope.GetIncarnationId(),
		},
		ProfileID:     scope.GetAnalysisProfileId(),
		LocalRootID:   response.GetLocalRootId(),
		WorkstationID: response.GetWorkstationId(),
	}
	if context := response.GetContext(); context != nil {
		contextRef := uci.ContextRef{
			SourceID:          context.GetSourceId(),
			CheckoutID:        context.GetCheckoutId(),
			ViewID:            context.GetViewId(),
			AnalysisProfileID: context.GetAnalysisProfileId(),
			Generation:        context.GetGeneration(),
		}
		if context.SpaceId != nil {
			spaceID := context.GetSpaceId()
			contextRef.SpaceID = &spaceID
		}
		binding.Context = &contextRef
	}
	if err := binding.Validate(); err != nil {
		return uci.IndexBinding{}, err
	}
	return binding.Clone(), nil
}

func uciIndexSourceUnavailable(message string) error {
	return &module.ModuleError{Code: string(uci.QueryErrorSourceUnavailable), Message: message}
}

func uciIndexContextResolutionError(err error) error {
	for current := err; current != nil; current = errors.Unwrap(current) {
		grpcStatus, ok := status.FromError(current)
		if ok && grpcStatus.Code() == codes.FailedPrecondition && grpcStatus.Message() == string(uci.ContextRequired) {
			return &module.ModuleError{Code: string(uci.ContextRequired), Message: "context is required"}
		}
	}
	return err
}

func (client *uciClient) Bind(ctx context.Context, request *pb.BindCodeContextRequest) (*pb.BindCodeContextResponse, error) {
	const operation = "Bind"
	if err := uciClientContextError(operation, ctx); err != nil {
		return nil, err
	}
	ctx = uciClientOutgoingContext(ctx)
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
	ctx = uciClientOutgoingContext(ctx)
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

// Stage sends one contiguous full stream and returns the server's aggregate
// digest of canonical per-frame acknowledgements. Frame PayloadDigest values
// remain raw-frame digests and are not used to derive the aggregate.
func (client *uciClient) Stage(ctx context.Context, frames []*pb.StageCodeIndexFrame) (*pb.StageCodeIndexResponse, error) {
	const operation = "Stage"
	if err := uciClientContextError(operation, ctx); err != nil {
		return nil, err
	}
	ctx = uciClientOutgoingContext(ctx)
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

// Finalize forwards PartsDigest unchanged. Callers must use the aggregate
// returned by Stage, never derive it from raw frame payload digests. Optional
// checkout observations retain their wire presence; Dirty must be explicit.
func (client *uciClient) Finalize(ctx context.Context, request *pb.FinalizeCodeIndexRequest) (*pb.FinalizeCodeIndexResponse, error) {
	const operation = "Finalize"
	if err := uciClientContextError(operation, ctx); err != nil {
		return nil, err
	}
	ctx = uciClientOutgoingContext(ctx)
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
	ctx = uciClientOutgoingContext(ctx)
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
	ctx = uciClientOutgoingContext(ctx)
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

func requireUCITransportSession(ctx context.Context) (string, error) {
	sessionID := auditcontext.UCITransportSession(ctx)
	if !auditcontext.ValidUCITransportSession(sessionID) {
		return "", &module.ModuleError{Code: "UCI_TRANSPORT_SESSION_REQUIRED", Message: "UCI transport session is unavailable"}
	}
	return sessionID, nil
}

func uciClientOutgoingContext(ctx context.Context) context.Context {
	outgoing, _ := metadata.FromOutgoingContext(ctx)
	outgoing = outgoing.Copy()
	if outgoing == nil {
		outgoing = metadata.MD{}
	}
	delete(outgoing, auditcontext.SourceSessionMetadataKey)
	delete(outgoing, auditcontext.UCIRequestCorrelationMetadataKey)
	if sessionID := auditcontext.UCITransportSession(ctx); auditcontext.ValidUCITransportSession(sessionID) {
		outgoing.Set(auditcontext.SourceSessionMetadataKey, sessionID)
	}
	if correlation, found := auditcontext.UCIRequestCorrelationFromContext(ctx); found {
		if value := correlation.MetadataValue(); value != "" {
			outgoing.Set(auditcontext.UCIRequestCorrelationMetadataKey, value)
		}
	}
	return metadata.NewOutgoingContext(ctx, outgoing)
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
	if request == nil || !validUCIClientMessage(request, maxUCIClientBindBytes) ||
		!validUCIClientIdentifier(request.GetClientSessionId(), maxUCIClientIdentifierBytes) {
		return false
	}
	switch {
	case request.GetRequestedContext() != nil && request.GetContextHandle() == "":
		return validUCIClientContextRef(request.GetRequestedContext())
	case request.GetRequestedContext() == nil && request.GetContextHandle() != "":
		return validUCIClientIdentifier(request.GetContextHandle(), 128)
	case request.GetRequestedContext() == nil && request.GetContextHandle() == "":
		return true
	default:
		return false
	}
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
		!validUCIClientFinalizeObservation(request) ||
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

func validUCIClientFinalizeObservation(request *pb.FinalizeCodeIndexRequest) bool {
	if request == nil || request.Dirty == nil {
		return false
	}
	if request.ObjectFormat != nil && *request.ObjectFormat != "sha1" && *request.ObjectFormat != "sha256" {
		return false
	}
	if request.HeadOid != nil {
		if request.ObjectFormat == nil {
			return false
		}
		length := 40
		if *request.ObjectFormat == "sha256" {
			length = 64
		}
		if !validUCIClientLowerHex(*request.HeadOid, length) {
			return false
		}
	}
	return request.RefLabel == nil || validUCIClientIdentifier(*request.RefLabel, maxUCIClientFinalizeBytes)
}

func validUCIClientLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
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
	if request == nil || response == nil || !validUCIClientMessage(response, maxUCIClientResponseBytes) ||
		!validUCIClientIdentifier(response.GetContextHandle(), maxUCIClientIdentifierBytes) ||
		!validUCIClientIndexScope(response.GetIndexScope()) ||
		!validUCIClientIdentifier(response.GetLocalRootId(), maxUCIClientIdentifierBytes) ||
		!validUCIClientIdentifier(response.GetWorkstationId(), maxUCIClientIdentifierBytes) {
		return false
	}
	context := response.GetContext()
	if context != nil && (!validUCIClientContextRef(context) || !contextMatchesUCIClientIndexScope(context, response.GetIndexScope())) {
		return false
	}
	switch {
	case request.GetRequestedContext() != nil && request.GetContextHandle() == "":
		return context != nil && sameUCIClientContextRef(request.GetRequestedContext(), context)
	case request.GetRequestedContext() == nil && request.GetContextHandle() != "":
		return response.GetContextHandle() == request.GetContextHandle()
	case request.GetRequestedContext() == nil && request.GetContextHandle() == "":
		return context != nil
	default:
		return false
	}
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
