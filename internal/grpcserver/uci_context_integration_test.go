package grpcserver

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/uci"
	pb "github.com/thebtf/engram/proto/engram/v1"
)

const (
	uciContextIntegrationRealm                  = "client"
	uciContextIntegrationClientA                = "uci-context-client-a"
	uciContextIntegrationClientB                = "uci-context-client-b"
	uciContextIntegrationPrivateClient          = "uci-context-private-client"
	uciContextIntegrationPrincipalA             = "agent/uci-context-a"
	uciContextIntegrationPrincipalB             = "agent/uci-context-b"
	uciContextIntegrationPrivatePrincipal       = "agent/uci-context-private"
	uciContextIntegrationSpaceA                 = "10000000-0000-4000-8000-000000000001"
	uciContextIntegrationSpaceB                 = "10000000-0000-4000-8000-000000000002"
	uciContextIntegrationPrivateSpace           = "10000000-0000-4000-8000-000000000003"
	uciContextIntegrationSourceA                = "20000000-0000-4000-8000-000000000001"
	uciContextIntegrationSourceB                = "20000000-0000-4000-8000-000000000002"
	uciContextIntegrationPrivateSource          = "20000000-0000-4000-8000-000000000003"
	uciContextIntegrationCheckoutA              = "30000000-0000-4000-8000-000000000001"
	uciContextIntegrationCheckoutB              = "30000000-0000-4000-8000-000000000002"
	uciContextIntegrationPrivateCheckout        = "30000000-0000-4000-8000-000000000003"
	uciContextIntegrationViewA                  = "40000000-0000-4000-8000-000000000001"
	uciContextIntegrationViewB                  = "40000000-0000-4000-8000-000000000002"
	uciContextIntegrationPrivateView            = "40000000-0000-4000-8000-000000000003"
	uciContextIntegrationProfile                = "50000000-0000-4000-8000-000000000001"
	uciContextIntegrationPrivateProfile         = "50000000-0000-4000-8000-000000000003"
	uciContextIntegrationIncarnationA           = "60000000-0000-4000-8000-000000000001"
	uciContextIntegrationIncarnationB           = "60000000-0000-4000-8000-000000000002"
	uciContextIntegrationPrivateIncarnation     = "60000000-0000-4000-8000-000000000003"
	uciContextIntegrationLocalRootA             = "70000000-0000-4000-8000-000000000001"
	uciContextIntegrationLocalRootB             = "70000000-0000-4000-8000-000000000002"
	uciContextIntegrationWorkstationA           = "80000000-0000-4000-8000-000000000001"
	uciContextIntegrationWorkstationB           = "80000000-0000-4000-8000-000000000002"
	uciContextIntegrationLegacySelector         = "legacy-project-selector"
	uciContextIntegrationConflictingSelector    = "legacy-project-selector-conflict"
	uciContextIntegrationDigest                 = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	uciContextIntegrationHeadOID                = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	uciContextIntegrationObjectFormat           = "sha1"
	uciContextIntegrationRefLabel               = "main"
	uciContextIntegrationSharedPathAndLabelJSON = `{"path":"same/path.go","label":"same-label"}`
)

func TestUCIContextIntegrationLegacySelectorUsesBoundCheckout(t *testing.T) {
	fixture := newUCIContextIntegrationFixture(t)
	ctx := fixture.clientContext(uciContextIntegrationClientA, uciContextIntegrationPrincipalA)

	bound, err := fixture.server.BindCodeContext(ctx, &pb.BindCodeContextRequest{
		ClientSessionId:  uciContextIntegrationClientA,
		RequestedContext: uciContextIntegrationProtoRef(fixture.refA),
	})
	require.NoError(t, err)
	requireUCIContextIntegrationBinding(t, fixture.bindingA, bound)
	require.Len(t, fixture.catalog.calls, 1)
	require.Len(t, fixture.authorizer.accesses, 1)

	response, err := fixture.server.CodeIndexNegotiate(ctx, &pb.CodeIndexNegotiateRequest{
		ProjectId:      uciContextIntegrationLegacySelector,
		IndexSessionId: "legacy-index-session-a",
	})
	require.NoError(t, err)
	require.Equal(t, []string{"needed-" + uciContextIntegrationCheckoutA[:8]}, response.GetNeedChunks())
	require.Len(t, fixture.aliases.calls, 1)
	require.Equal(t, uciContextIntegrationRealm, fixture.aliases.calls[0].AuthRealm)
	require.Equal(t, uciContextIntegrationLegacySelector, fixture.aliases.calls[0].Value)
	require.Len(t, fixture.catalog.calls, 2, "bound context must be reauthorized before legacy publication routing")
	require.Len(t, fixture.authorizer.accesses, 2)
	require.Len(t, fixture.publication.legacyCalls, 1)
	require.Equal(t, fixture.refA, fixture.publication.legacyCalls[0].ref)
	require.Empty(t, fixture.query.calls)
}

func TestUCIContextIntegrationUnboundBindReusesAuthorizedDefault(t *testing.T) {
	fixture := newUCIContextIntegrationFixture(t)
	ctx := fixture.clientContext(uciContextIntegrationClientA, uciContextIntegrationPrincipalA)

	_, err := fixture.server.BindCodeContext(ctx, &pb.BindCodeContextRequest{
		ClientSessionId:  uciContextIntegrationClientA,
		RequestedContext: uciContextIntegrationProtoRef(fixture.refA),
	})
	require.NoError(t, err)
	catalogCalls := len(fixture.catalog.calls)
	authorizerCalls := len(fixture.authorizer.accesses)
	runtimeCalls := len(fixture.runtime.bindingCalls)
	handleIssues := len(fixture.handles.issues)

	bound, err := fixture.server.BindCodeContext(ctx, &pb.BindCodeContextRequest{ClientSessionId: uciContextIntegrationClientA})
	require.NoError(t, err)
	requireUCIContextIntegrationBinding(t, fixture.bindingA, bound)
	require.NotEmpty(t, bound.GetContextHandle())
	require.Len(t, fixture.catalog.calls, catalogCalls+1)
	require.Equal(t, fixture.refA, fixture.catalog.calls[len(fixture.catalog.calls)-1])
	require.Len(t, fixture.authorizer.accesses, authorizerCalls+1)
	require.Len(t, fixture.runtime.bindingCalls, runtimeCalls+1)
	require.Len(t, fixture.handles.issues, handleIssues+1)
	require.Empty(t, fixture.handles.authorizations, "an empty selector must not be treated as a handle")
}

func TestUCIContextIntegrationUnboundBindRequiresExistingDefault(t *testing.T) {
	fixture := newUCIContextIntegrationFixture(t)
	ctx := fixture.clientContext(uciContextIntegrationClientA, uciContextIntegrationPrincipalA)

	bound, err := fixture.server.BindCodeContext(ctx, &pb.BindCodeContextRequest{ClientSessionId: uciContextIntegrationClientA})
	require.Nil(t, bound)
	requireUCIContextIntegrationClosedStatus(t, err, codes.FailedPrecondition, uci.ContextRequired,
		fixture.refA.SourceID,
		fixture.refA.CheckoutID,
		fixture.refA.ViewID,
	)
	require.Empty(t, fixture.catalog.calls)
	require.Empty(t, fixture.authorizer.accesses)
	require.Empty(t, fixture.runtime.bindingCalls)
	require.Empty(t, fixture.handles.issues)
}

func TestUCIContextIntegrationLegacySelectorConflictStopsBeforeCatalogAndProjection(t *testing.T) {
	fixture := newUCIContextIntegrationFixture(t)
	ctx := fixture.clientContext(uciContextIntegrationClientA, uciContextIntegrationPrincipalA)

	_, err := fixture.server.BindCodeContext(ctx, &pb.BindCodeContextRequest{
		ClientSessionId:  uciContextIntegrationClientA,
		RequestedContext: uciContextIntegrationProtoRef(fixture.refA),
	})
	require.NoError(t, err)
	catalogCalls := len(fixture.catalog.calls)
	authorizerCalls := len(fixture.authorizer.accesses)

	response, err := fixture.server.CodeIndexNegotiate(ctx, &pb.CodeIndexNegotiateRequest{
		ProjectId:      uciContextIntegrationConflictingSelector,
		IndexSessionId: "legacy-index-session-conflict",
	})
	require.Nil(t, response)
	requireUCIContextIntegrationClosedStatus(t, err, codes.FailedPrecondition, uci.ContextMismatch,
		fixture.refA.SourceID,
		fixture.refA.CheckoutID,
		fixture.refA.ViewID,
		fixture.refB.SourceID,
		fixture.refB.CheckoutID,
		fixture.refB.ViewID,
		uciContextIntegrationSharedPathAndLabelJSON,
	)
	require.Len(t, fixture.aliases.calls, 1)
	require.Equal(t, uciContextIntegrationConflictingSelector, fixture.aliases.calls[0].Value)
	require.Len(t, fixture.catalog.calls, catalogCalls, "conflicting selector must fail before catalog access")
	require.Len(t, fixture.authorizer.accesses, authorizerCalls, "conflicting selector must fail before authorization reuse")
	require.Empty(t, fixture.publication.legacyCalls, "conflicting selector must not open publication")
	require.Empty(t, fixture.publication.beginCalls)
	require.Empty(t, fixture.publication.stageCalls)
	require.Empty(t, fixture.publication.finalizeCalls)
	require.Empty(t, fixture.query.calls, "conflicting selector must not open query storage")
}

func TestUCIContextIntegrationPrivateCheckoutDeniesWithoutDisclosureOrStorage(t *testing.T) {
	fixture := newUCIContextIntegrationFixture(t)
	ctx := fixture.clientContext(uciContextIntegrationPrivateClient, uciContextIntegrationPrivatePrincipal)
	privateRef := fixture.privateRef

	bound, err := fixture.server.BindCodeContext(ctx, &pb.BindCodeContextRequest{
		ClientSessionId:  uciContextIntegrationPrivateClient,
		RequestedContext: uciContextIntegrationProtoRef(privateRef),
	})
	require.Nil(t, bound)
	requireUCIContextIntegrationClosedStatus(t, err, codes.PermissionDenied, uci.PermissionDenied,
		privateRef.SourceID,
		privateRef.CheckoutID,
		privateRef.ViewID,
		"private-body",
		"private-count",
		`C:\\private\\uci-context`,
	)

	queried, err := fixture.server.QueryCode(ctx, uciContextIntegrationQueryRequest(privateRef))
	require.Nil(t, queried)
	requireUCIContextIntegrationClosedStatus(t, err, codes.PermissionDenied, uci.PermissionDenied,
		privateRef.SourceID,
		privateRef.CheckoutID,
		privateRef.ViewID,
		"private-body",
		"private-count",
		`C:\\private\\uci-context`,
	)
	require.Len(t, fixture.catalog.calls, 2)
	require.Len(t, fixture.authorizer.accesses, 2)
	require.Empty(t, fixture.publication.legacyCalls)
	require.Empty(t, fixture.publication.beginCalls)
	require.Empty(t, fixture.publication.stageCalls)
	require.Empty(t, fixture.publication.finalizeCalls)
	require.Empty(t, fixture.query.calls, "denied query must not open query storage")
}

func TestUCIContextIntegrationRoutesIndependentBindingsToDistinctPublicationAndQueryTargets(t *testing.T) {
	fixture := newUCIContextIntegrationFixture(t)
	ctxA := fixture.clientContext(uciContextIntegrationClientA, uciContextIntegrationPrincipalA)
	ctxB := fixture.clientContext(uciContextIntegrationClientB, uciContextIntegrationPrincipalB)
	scopeA := uciContextIntegrationScope(fixture.refA, uciContextIntegrationIncarnationA)
	scopeB := uciContextIntegrationScope(fixture.refB, uciContextIntegrationIncarnationB)

	boundA, err := fixture.server.BindCodeContext(ctxA, &pb.BindCodeContextRequest{
		ClientSessionId:  uciContextIntegrationClientA,
		RequestedContext: uciContextIntegrationProtoRef(fixture.refA),
	})
	require.NoError(t, err)
	boundB, err := fixture.server.BindCodeContext(ctxB, &pb.BindCodeContextRequest{
		ClientSessionId:  uciContextIntegrationClientB,
		RequestedContext: uciContextIntegrationProtoRef(fixture.refB),
	})
	require.NoError(t, err)

	beginA, err := fixture.server.BeginCodeIndex(ctxA, uciContextIntegrationBeginRequest(scopeA))
	require.NoError(t, err)
	beginB, err := fixture.server.BeginCodeIndex(ctxB, uciContextIntegrationBeginRequest(scopeB))
	require.NoError(t, err)

	stageA := &uciContextIntegrationStageStream{
		ctx:    ctxA,
		frames: []*pb.StageCodeIndexFrame{uciContextIntegrationStageFrame(scopeA, beginA.GetBuildId(), beginA.GetLeaseEpoch())},
	}
	require.NoError(t, fixture.server.StageCodeIndex(stageA))
	require.NotNil(t, stageA.response)
	stageB := &uciContextIntegrationStageStream{
		ctx:    ctxB,
		frames: []*pb.StageCodeIndexFrame{uciContextIntegrationStageFrame(scopeB, beginB.GetBuildId(), beginB.GetLeaseEpoch())},
	}
	require.NoError(t, fixture.server.StageCodeIndex(stageB))
	require.NotNil(t, stageB.response)

	finalizedA, err := fixture.server.FinalizeCodeIndex(ctxA, uciContextIntegrationFinalizeRequest(scopeA, beginA, boundA.GetContext(), stageA.response.GetPartDigest()))
	require.NoError(t, err)
	finalizedB, err := fixture.server.FinalizeCodeIndex(ctxB, uciContextIntegrationFinalizeRequest(scopeB, beginB, boundB.GetContext(), stageB.response.GetPartDigest()))
	require.NoError(t, err)
	queriedA, err := fixture.server.QueryCode(ctxA, uciContextIntegrationProtoQueryRequest(finalizedA.GetPublishedContext()))
	require.NoError(t, err)
	queriedB, err := fixture.server.QueryCode(ctxB, uciContextIntegrationProtoQueryRequest(finalizedB.GetPublishedContext()))
	require.NoError(t, err)

	requireUCIContextIntegrationProtoRef(t, fixture.refA, finalizedA.GetPublishedContext())
	requireUCIContextIntegrationProtoRef(t, fixture.refB, finalizedB.GetPublishedContext())
	requireUCIContextIntegrationProtoRef(t, fixture.refA, queriedA.GetContext())
	requireUCIContextIntegrationProtoRef(t, fixture.refB, queriedB.GetContext())

	requirePublicationTarget(t, fixture.publication.beginCalls, fixture.refA, scopeA, fixture.refB, scopeB)
	requirePublicationTarget(t, fixture.publication.stageCalls, fixture.refA, scopeA, fixture.refB, scopeB)
	requirePublicationTarget(t, fixture.publication.finalizeCalls, fixture.refA, scopeA, fixture.refB, scopeB)
	require.Equal(t, stageA.response.GetPartDigest(), fixture.publication.finalizeCalls[0].partsDigest)
	require.Equal(t, stageB.response.GetPartDigest(), fixture.publication.finalizeCalls[1].partsDigest)
	requireUCIContextIntegrationObservedViewFacts(t, fixture.publication.finalizeCalls[0].finalize)
	requireUCIContextIntegrationObservedViewFacts(t, fixture.publication.finalizeCalls[1].finalize)
	require.Len(t, fixture.publication.stageCalls, 2)
	require.Equal(t, []byte(uciContextIntegrationSharedPathAndLabelJSON), fixture.publication.stageCalls[0].payload)
	require.Equal(t, fixture.publication.stageCalls[0].payload, fixture.publication.stageCalls[1].payload, "same path and label must not collapse distinct checkout routes")
	require.Len(t, fixture.query.calls, 2)
	require.Equal(t, fixture.refA, fixture.query.calls[0].ref)
	require.Equal(t, fixture.refB, fixture.query.calls[1].ref)
	require.Equal(t, "SharedSymbol", fixture.query.calls[0].query)
	require.Equal(t, fixture.query.calls[0].query, fixture.query.calls[1].query)
	require.Len(t, fixture.catalog.calls, 4, "requested bindings and explicit queries reload canonical Views")
	require.Len(t, fixture.authorizer.accesses, 4, "requested bindings and explicit queries authorize canonical Views")
	for index, access := range fixture.authorizer.accesses {
		require.Equal(t, fixture.catalog.calls[index].SourceID, access.SourceID)
		require.Equal(t, fixture.catalog.calls[index].CheckoutID, access.CheckoutID)
	}
	require.Len(t, fixture.handles.scopeAuthorizations, 6, "begin, stage, and finalize reauthorize each exact client scope through the handle owner")
	for index, authorization := range fixture.handles.scopeAuthorizations {
		wantBinding := fixture.bindingA
		wantClient := uciContextIntegrationClientA
		if index%2 == 1 {
			wantBinding = fixture.bindingB
			wantClient = uciContextIntegrationClientB
		}
		require.Equal(t, wantClient, authorization.clientSessionID)
		require.Equal(t, wantBinding.Scope, authorization.scope)
		require.Equal(t, wantBinding.ProfileID, authorization.profileID)
	}
}

func TestUCIContextIntegrationExplicitQueryDoesNotReplaceSelectedDefault(t *testing.T) {
	fixture := newUCIContextIntegrationFixture(t)
	ctx := fixture.clientContext(uciContextIntegrationClientA, uciContextIntegrationPrincipalA)
	_, err := fixture.server.BindCodeContext(ctx, &pb.BindCodeContextRequest{
		ClientSessionId:  uciContextIntegrationClientA,
		RequestedContext: uciContextIntegrationProtoRef(fixture.refA),
	})
	require.NoError(t, err)

	queried, err := fixture.server.QueryCode(ctx, uciContextIntegrationQueryRequest(fixture.refB))
	require.NoError(t, err)
	requireUCIContextIntegrationProtoRef(t, fixture.refB, queried.GetContext())

	scopeA := uciContextIntegrationScope(fixture.refA, uciContextIntegrationIncarnationA)
	begin, err := fixture.server.BeginCodeIndex(ctx, uciContextIntegrationBeginRequest(scopeA))
	require.NoError(t, err)
	require.Equal(t, uciContextIntegrationIncarnationA, begin.GetBuildId())
	require.Len(t, fixture.query.calls, 1)
	require.Equal(t, fixture.refB, fixture.query.calls[0].ref)
	require.Len(t, fixture.publication.beginCalls, 1)
	require.Equal(t, fixture.refA, fixture.publication.beginCalls[0].ref)
}

func TestUCIContextIntegrationHandlePreservesSelectedDefault(t *testing.T) {
	fixture := newUCIContextIntegrationFixture(t)
	ctx := fixture.clientContext(uciContextIntegrationClientA, uciContextIntegrationPrincipalA)

	_, err := fixture.server.BindCodeContext(ctx, &pb.BindCodeContextRequest{
		ClientSessionId:  uciContextIntegrationClientA,
		RequestedContext: uciContextIntegrationProtoRef(fixture.refA),
	})
	require.NoError(t, err)
	fixture.handles.register(uciContextIntegrationClientA, "context-handle-b", fixture.bindingB)
	catalogCalls := len(fixture.catalog.calls)
	authorizerCalls := len(fixture.authorizer.accesses)

	bound, err := fixture.server.BindCodeContext(ctx, &pb.BindCodeContextRequest{
		ClientSessionId: uciContextIntegrationClientA,
		ContextHandle:   "context-handle-b",
	})
	require.NoError(t, err)
	requireUCIContextIntegrationBinding(t, fixture.bindingB, bound)
	require.Len(t, fixture.handles.authorizations, 1)
	require.Equal(t, uciContextIntegrationClientA, fixture.handles.authorizations[0].clientSessionID)
	require.Equal(t, "context-handle-b", fixture.handles.authorizations[0].handle)
	require.Len(t, fixture.handles.issues, 1, "only the handle owner may issue requested-context handles")
	require.Len(t, fixture.runtime.bindingCalls, 1, "a handle branch must not ask the binding catalog to resolve another context")
	require.Len(t, fixture.catalog.calls, catalogCalls, "a handle branch must not replace the selected resolver binding")
	require.Len(t, fixture.authorizer.accesses, authorizerCalls, "a handle branch must reauthorize through the handle owner")

	_, err = fixture.server.BeginCodeIndex(ctx, uciContextIntegrationBeginRequest(uciContextIntegrationScope(fixture.refA, uciContextIntegrationIncarnationA)))
	require.NoError(t, err)
	require.Len(t, fixture.publication.beginCalls, 1)
	require.Equal(t, fixture.refA, fixture.publication.beginCalls[0].ref, "the selected default must remain the requested A context")
}

func TestUCIContextIntegrationHandleAcceptsCompleteNoViewBinding(t *testing.T) {
	fixture := newUCIContextIntegrationFixture(t)
	ctx := fixture.clientContext(uciContextIntegrationClientA, uciContextIntegrationPrincipalA)
	binding := uciContextIntegrationNoViewBinding(
		fixture.refA,
		uciContextIntegrationIncarnationA,
		uciContextIntegrationLocalRootA,
		uciContextIntegrationWorkstationA,
	)
	fixture.handles.register(uciContextIntegrationClientA, "registered-no-view", binding)

	bound, err := fixture.server.BindCodeContext(ctx, &pb.BindCodeContextRequest{
		ClientSessionId: uciContextIntegrationClientA,
		ContextHandle:   "registered-no-view",
	})
	require.NoError(t, err)
	requireUCIContextIntegrationBinding(t, binding, bound)
	require.Len(t, fixture.handles.authorizations, 1)
	require.Empty(t, fixture.catalog.calls, "a server-authorized no-View handle must not resolve raw context input")
	require.Empty(t, fixture.authorizer.accesses)
	require.Empty(t, fixture.runtime.bindingCalls)
}

func TestUCIContextIntegrationNoViewHandleBeginsInitialIndex(t *testing.T) {
	fixture := newUCIContextIntegrationFixture(t)
	ctx := fixture.clientContext(uciContextIntegrationClientA, uciContextIntegrationPrincipalA)
	binding := uciContextIntegrationNoViewBinding(
		fixture.refA,
		uciContextIntegrationIncarnationA,
		uciContextIntegrationLocalRootA,
		uciContextIntegrationWorkstationA,
	)
	fixture.handles.register(uciContextIntegrationClientA, "registered-no-view", binding)

	_, err := fixture.server.BindCodeContext(ctx, &pb.BindCodeContextRequest{
		ClientSessionId: uciContextIntegrationClientA,
		ContextHandle:   "registered-no-view",
	})
	require.NoError(t, err)
	scope := uciContextIntegrationScope(fixture.refA, uciContextIntegrationIncarnationA)
	begin, err := fixture.server.BeginCodeIndex(ctx, uciContextIntegrationBeginRequest(scope))
	require.NoError(t, err)
	require.True(t, proto.Equal(scope, begin.GetScope()))
	require.Len(t, fixture.handles.scopeAuthorizations, 1)
	require.Equal(t, uciContextIntegrationClientA, fixture.handles.scopeAuthorizations[0].clientSessionID)
	require.Equal(t, binding.Scope, fixture.handles.scopeAuthorizations[0].scope)
	require.Equal(t, binding.ProfileID, fixture.handles.scopeAuthorizations[0].profileID)
	require.Len(t, fixture.publication.beginCalls, 1)
	require.Nil(t, fixture.publication.beginCalls[0].binding.Context)
	require.Equal(t, binding.Scope, fixture.publication.beginCalls[0].binding.Scope)
	require.Equal(t, binding.ProfileID, fixture.publication.beginCalls[0].binding.ProfileID)

	withParent := uciContextIntegrationBeginRequest(uciContextIntegrationScope(fixture.refA, uciContextIntegrationIncarnationA))
	withParent.ExpectedParent = uciContextIntegrationProtoRef(fixture.refA)
	begin, err = fixture.server.BeginCodeIndex(ctx, withParent)
	require.Nil(t, begin)
	requireUCIContextIntegrationClosedStatus(t, err, codes.FailedPrecondition, uci.ContextMismatch)
	require.Len(t, fixture.publication.beginCalls, 1, "a no-View binding must reject expected_parent before the runtime")
}

func TestUCIContextIntegrationUnownedIndexScopeStopsBeforeRuntime(t *testing.T) {
	fixture := newUCIContextIntegrationFixture(t)
	ctx := fixture.clientContext(uciContextIntegrationClientA, uciContextIntegrationPrincipalA)
	_, err := fixture.server.BindCodeContext(ctx, &pb.BindCodeContextRequest{
		ClientSessionId:  uciContextIntegrationClientA,
		RequestedContext: uciContextIntegrationProtoRef(fixture.refA),
	})
	require.NoError(t, err)

	begin, err := fixture.server.BeginCodeIndex(ctx, uciContextIntegrationBeginRequest(uciContextIntegrationScope(fixture.refB, uciContextIntegrationIncarnationB)))
	require.Nil(t, begin)
	require.Error(t, err)
	require.Len(t, fixture.handles.scopeAuthorizations, 1)
	require.Equal(t, uciContextIntegrationClientA, fixture.handles.scopeAuthorizations[0].clientSessionID)
	require.Empty(t, fixture.publication.beginCalls, "an unowned scope must not reach the publication runtime")
}

func TestUCIContextIntegrationHandleRejectsIncompleteNoViewBinding(t *testing.T) {
	fixture := newUCIContextIntegrationFixture(t)
	ctx := fixture.clientContext(uciContextIntegrationClientA, uciContextIntegrationPrincipalA)
	binding := uciContextIntegrationNoViewBinding(
		fixture.refA,
		uciContextIntegrationIncarnationA,
		uciContextIntegrationLocalRootA,
		uciContextIntegrationWorkstationA,
	)
	binding.LocalRootID = ""
	fixture.handles.register(uciContextIntegrationClientA, "incomplete-no-view", binding)

	bound, err := fixture.server.BindCodeContext(ctx, &pb.BindCodeContextRequest{
		ClientSessionId: uciContextIntegrationClientA,
		ContextHandle:   "incomplete-no-view",
	})
	require.Nil(t, bound)
	require.Equal(t, codes.Internal, status.Code(err))
	require.Len(t, fixture.handles.authorizations, 1)
	require.Empty(t, fixture.catalog.calls)
	require.Empty(t, fixture.authorizer.accesses)
	require.Empty(t, fixture.runtime.bindingCalls)
}

func TestUCIContextIntegrationBeginParentConflictStopsBeforePublication(t *testing.T) {
	fixture := newUCIContextIntegrationFixture(t)
	ctx := fixture.clientContext(uciContextIntegrationClientA, uciContextIntegrationPrincipalA)
	_, err := fixture.server.BindCodeContext(ctx, &pb.BindCodeContextRequest{
		ClientSessionId:  uciContextIntegrationClientA,
		RequestedContext: uciContextIntegrationProtoRef(fixture.refA),
	})
	require.NoError(t, err)

	request := uciContextIntegrationBeginRequest(uciContextIntegrationScope(fixture.refA, uciContextIntegrationIncarnationA))
	request.ExpectedParent = uciContextIntegrationProtoRef(fixture.refB)
	response, err := fixture.server.BeginCodeIndex(ctx, request)
	require.Nil(t, response)
	requireUCIContextIntegrationClosedStatus(t, err, codes.FailedPrecondition, uci.ContextMismatch,
		fixture.refA.SourceID,
		fixture.refA.CheckoutID,
		fixture.refA.ViewID,
		fixture.refB.SourceID,
		fixture.refB.CheckoutID,
		fixture.refB.ViewID,
	)
	require.Empty(t, fixture.publication.beginCalls)
}

type uciContextIntegrationFixture struct {
	server      *Server
	catalog     *uciContextIntegrationCatalog
	authorizer  *uciContextIntegrationAuthorizer
	aliases     *uciContextIntegrationAliases
	publication *uciContextIntegrationPublication
	query       *uciContextIntegrationQuery
	runtime     *uciContextIntegrationRuntime
	handles     *uciContextIntegrationHandlePort
	bindingA    uci.IndexBinding
	bindingB    uci.IndexBinding
	refA        uci.ContextRef
	refB        uci.ContextRef
	privateRef  uci.ContextRef
}

func newUCIContextIntegrationFixture(t *testing.T) *uciContextIntegrationFixture {
	t.Helper()

	refA := uciContextIntegrationRef(
		uciContextIntegrationSpaceA,
		uciContextIntegrationSourceA,
		uciContextIntegrationCheckoutA,
		uciContextIntegrationViewA,
		uciContextIntegrationProfile,
		1,
	)
	refB := uciContextIntegrationRef(
		uciContextIntegrationSpaceB,
		uciContextIntegrationSourceB,
		uciContextIntegrationCheckoutB,
		uciContextIntegrationViewB,
		uciContextIntegrationProfile,
		2,
	)
	privateRef := uciContextIntegrationRef(
		uciContextIntegrationPrivateSpace,
		uciContextIntegrationPrivateSource,
		uciContextIntegrationPrivateCheckout,
		uciContextIntegrationPrivateView,
		uciContextIntegrationPrivateProfile,
		3,
	)
	bindingA := uciContextIntegrationBinding(
		refA,
		uciContextIntegrationIncarnationA,
		uciContextIntegrationLocalRootA,
		uciContextIntegrationWorkstationA,
	)
	bindingB := uciContextIntegrationBinding(
		refB,
		uciContextIntegrationIncarnationB,
		uciContextIntegrationLocalRootB,
		uciContextIntegrationWorkstationB,
	)
	handles := &uciContextIntegrationHandlePort{bindings: make(map[uciContextIntegrationHandleKey]uci.IndexBinding)}
	fixture := &uciContextIntegrationFixture{
		catalog: &uciContextIntegrationCatalog{records: map[uciContextIntegrationRefKey]uci.ContextRecord{
			uciContextIntegrationKey(refA):       {Ref: refA, AuthRealm: uciContextIntegrationRealm},
			uciContextIntegrationKey(refB):       {Ref: refB, AuthRealm: uciContextIntegrationRealm},
			uciContextIntegrationKey(privateRef): {Ref: privateRef, AuthRealm: uciContextIntegrationRealm},
		}},
		authorizer: &uciContextIntegrationAuthorizer{denied: map[string]error{
			privateRef.CheckoutID: errors.New("private source " + privateRef.SourceID + " checkout " + privateRef.CheckoutID + " view " + privateRef.ViewID + " body=private-body count=private-count at C:\\private\\uci-context"),
		}},
		aliases: &uciContextIntegrationAliases{targets: map[string]uci.AliasTarget{
			uciContextIntegrationLegacySelector: {
				SpaceID:  uciContextIntegrationStringPointer(uciContextIntegrationSpaceA),
				SourceID: uciContextIntegrationStringPointer(uciContextIntegrationSourceA),
			},
			uciContextIntegrationConflictingSelector: {
				SpaceID:  uciContextIntegrationStringPointer(uciContextIntegrationSpaceB),
				SourceID: uciContextIntegrationStringPointer(uciContextIntegrationSourceB),
			},
		}},
		publication: &uciContextIntegrationPublication{},
		query:       &uciContextIntegrationQuery{},
		refA:        refA,
		refB:        refB,
		privateRef:  privateRef,
		handles:     handles,
		bindingA:    bindingA,
		bindingB:    bindingB,
	}
	fixture.runtime = &uciContextIntegrationRuntime{
		publication: fixture.publication,
		query:       fixture.query,
		bindings: map[uciContextIntegrationRefKey]uci.IndexBinding{
			uciContextIntegrationKey(refA): bindingA,
			uciContextIntegrationKey(refB): bindingB,
		},
	}
	_, fixture.server = New(nil, nil)
	fixture.server.SetUCITransport(NewContextAwareUCITransport(
		uci.NewContextResolver(fixture.catalog, fixture.authorizer, fixture.runtime),
		uci.NewAliasResolver(fixture.aliases.Lookup),
		fixture.runtime,
		fixture.handles,
	))
	return fixture
}

func (fixture *uciContextIntegrationFixture) clientContext(sessionID, principal string) context.Context {
	identity := auth.ClientWithPrincipal("read-write", uciContextIntegrationRealm, principal, auth.PrincipalKindAgent)
	ctx := auth.WithIdentity(context.Background(), identity)
	return metadata.NewIncomingContext(ctx, metadata.Pairs(auditcontext.SourceSessionMetadataKey, sessionID))
}

type uciContextIntegrationCatalog struct {
	records map[uciContextIntegrationRefKey]uci.ContextRecord
	calls   []uci.ContextRef
}

func (catalog *uciContextIntegrationCatalog) LoadContext(_ context.Context, ref uci.ContextRef) (uci.ContextRecord, error) {
	catalog.calls = append(catalog.calls, ref)
	record, found := catalog.records[uciContextIntegrationKey(ref)]
	if !found {
		return uci.ContextRecord{}, errors.New("unknown UCI context")
	}
	return record, nil
}

type uciContextIntegrationAuthorizer struct {
	denied   map[string]error
	accesses []uci.ContextAccess
}

func (authorizer *uciContextIntegrationAuthorizer) AuthorizeContext(_ context.Context, access uci.ContextAccess) error {
	authorizer.accesses = append(authorizer.accesses, access)
	return authorizer.denied[access.CheckoutID]
}

type uciContextIntegrationAliases struct {
	targets map[string]uci.AliasTarget
	calls   []uci.LegacyAliasKey
}

func (aliases *uciContextIntegrationAliases) Lookup(_ context.Context, key uci.LegacyAliasKey) ([]uci.LegacyAliasRecord, error) {
	aliases.calls = append(aliases.calls, key)
	target, found := aliases.targets[key.Value]
	if !found {
		return nil, nil
	}
	return []uci.LegacyAliasRecord{{
		Key:      key,
		State:    "resolved",
		SpaceID:  target.SpaceID,
		SourceID: target.SourceID,
	}}, nil
}

type uciContextIntegrationRuntime struct {
	publication  *uciContextIntegrationPublication
	query        *uciContextIntegrationQuery
	bindings     map[uciContextIntegrationRefKey]uci.IndexBinding
	bindingCalls []uci.IndexBindingSelector
}

func (runtime *uciContextIntegrationRuntime) LoadIndexBinding(_ context.Context, selector uci.IndexBindingSelector) (uci.IndexBinding, error) {
	selector = selector.Clone()
	runtime.bindingCalls = append(runtime.bindingCalls, selector)
	if err := selector.Validate(); err != nil {
		return uci.IndexBinding{}, err
	}
	ref, pinned := selector.Context()
	if !pinned {
		return uci.IndexBinding{}, errors.New("no-View binding is handle-owned")
	}
	binding, found := runtime.bindings[uciContextIntegrationKey(ref)]
	if !found {
		return uci.IndexBinding{}, errors.New("unknown UCI index binding")
	}
	return binding.Clone(), nil
}

func (runtime *uciContextIntegrationRuntime) LegacyCodeIndexNegotiate(_ context.Context, authorized uci.AuthorizedContext, request *pb.CodeIndexNegotiateRequest) (*pb.CodeIndexNegotiateResponse, error) {
	return runtime.publication.LegacyNegotiate(authorized, request), nil
}

func (runtime *uciContextIntegrationRuntime) BeginCodeIndex(_ context.Context, binding uci.IndexBinding, request *pb.BeginCodeIndexRequest) (*pb.BeginCodeIndexResponse, error) {
	return runtime.publication.Begin(binding, request), nil
}

func (runtime *uciContextIntegrationRuntime) StageCodeIndex(_ context.Context, binding uci.IndexBinding, frames []*pb.StageCodeIndexFrame) (*pb.StageCodeIndexResponse, error) {
	return runtime.publication.Stage(binding, frames), nil
}

func (runtime *uciContextIntegrationRuntime) FinalizeCodeIndex(_ context.Context, binding uci.IndexBinding, request *pb.FinalizeCodeIndexRequest) (*pb.FinalizeCodeIndexResponse, error) {
	return runtime.publication.Finalize(binding, request), nil
}

func (runtime *uciContextIntegrationRuntime) QueryCode(_ context.Context, authorized uci.AuthorizedContext, request *pb.QueryCodeRequest) (*pb.QueryCodeResponse, error) {
	return runtime.query.Query(authorized, request), nil
}

func (runtime *uciContextIntegrationRuntime) ExploreCode(_ context.Context, authorized uci.AuthorizedContext, request *pb.ExploreCodeRequest) (*pb.ExploreCodeResponse, error) {
	return runtime.query.Explore(authorized, request), nil
}

type uciContextIntegrationHandleKey struct {
	clientSessionID string
	handle          string
}

type uciContextIntegrationHandleAuthorization struct {
	clientSessionID string
	handle          string
}

type uciContextIntegrationScopeAuthorization struct {
	clientSessionID string
	scope           uci.IndexScope
	profileID       string
}

type uciContextIntegrationHandleIssue struct {
	clientSessionID string
	binding         uci.IndexBinding
}

type uciContextIntegrationHandlePort struct {
	bindings            map[uciContextIntegrationHandleKey]uci.IndexBinding
	authorizations      []uciContextIntegrationHandleAuthorization
	issues              []uciContextIntegrationHandleIssue
	scopeAuthorizations []uciContextIntegrationScopeAuthorization
}

func (port *uciContextIntegrationHandlePort) AuthorizeCodeContextHandle(_ context.Context, clientSessionID, handle string) (uci.IndexBinding, error) {
	port.authorizations = append(port.authorizations, uciContextIntegrationHandleAuthorization{clientSessionID: clientSessionID, handle: handle})
	binding, found := port.bindings[uciContextIntegrationHandleKey{clientSessionID: clientSessionID, handle: handle}]
	if !found {
		return uci.IndexBinding{}, errors.New("unknown context handle")
	}
	return binding.Clone(), nil
}

func (port *uciContextIntegrationHandlePort) AuthorizeCodeIndexScope(_ context.Context, clientSessionID string, scope uci.IndexScope, profileID string) (uci.IndexBinding, error) {
	port.scopeAuthorizations = append(port.scopeAuthorizations, uciContextIntegrationScopeAuthorization{
		clientSessionID: clientSessionID,
		scope:           scope,
		profileID:       profileID,
	})
	for key, binding := range port.bindings {
		if key.clientSessionID == clientSessionID && binding.Scope == scope && binding.ProfileID == profileID {
			return binding.Clone(), nil
		}
	}
	return uci.IndexBinding{}, errors.New("unowned index scope")
}

func (port *uciContextIntegrationHandlePort) IssueCodeContextHandle(_ context.Context, clientSessionID string, binding uci.IndexBinding) (string, error) {
	binding = binding.Clone()
	port.issues = append(port.issues, uciContextIntegrationHandleIssue{clientSessionID: clientSessionID, binding: binding})
	handle := "context-handle-" + binding.Scope.CheckoutID[:8]
	port.register(clientSessionID, handle, binding)
	return handle, nil
}

func (port *uciContextIntegrationHandlePort) register(clientSessionID, handle string, binding uci.IndexBinding) {
	if port.bindings == nil {
		port.bindings = make(map[uciContextIntegrationHandleKey]uci.IndexBinding)
	}
	port.bindings[uciContextIntegrationHandleKey{clientSessionID: clientSessionID, handle: handle}] = binding.Clone()
}

var _ CodeContextHandlePort = (*uciContextIntegrationHandlePort)(nil)

type uciContextIntegrationPublicationCall struct {
	ref         uci.ContextRef
	scope       *pb.CodeIndexScope
	payload     []byte
	partsDigest string
	binding     uci.IndexBinding
	finalize    *pb.FinalizeCodeIndexRequest
}

type uciContextIntegrationPublication struct {
	legacyCalls   []uciContextIntegrationPublicationCall
	beginCalls    []uciContextIntegrationPublicationCall
	stageCalls    []uciContextIntegrationPublicationCall
	finalizeCalls []uciContextIntegrationPublicationCall
}

func (publication *uciContextIntegrationPublication) LegacyNegotiate(authorized uci.AuthorizedContext, _ *pb.CodeIndexNegotiateRequest) *pb.CodeIndexNegotiateResponse {
	ref := authorized.Ref()
	publication.legacyCalls = append(publication.legacyCalls, uciContextIntegrationPublicationCall{ref: ref})
	return &pb.CodeIndexNegotiateResponse{NeedChunks: []string{"needed-" + ref.CheckoutID[:8]}}
}

func uciContextIntegrationPublicationRef(binding uci.IndexBinding) uci.ContextRef {
	if binding.Context == nil {
		return uci.ContextRef{}
	}
	return *binding.Context
}

func (publication *uciContextIntegrationPublication) Begin(binding uci.IndexBinding, request *pb.BeginCodeIndexRequest) *pb.BeginCodeIndexResponse {
	binding = binding.Clone()
	ref := uciContextIntegrationPublicationRef(binding)
	publication.beginCalls = append(publication.beginCalls, uciContextIntegrationPublicationCall{
		ref:     ref,
		binding: binding,
		scope:   proto.Clone(request.GetScope()).(*pb.CodeIndexScope),
	})
	return &pb.BeginCodeIndexResponse{
		Scope:          request.GetScope(),
		BuildId:        binding.Scope.IncarnationID,
		LeaseEpoch:     7,
		LeaseExpiresAt: timestamppb.New(time.Date(2026, time.September, 5, 12, 0, 0, 0, time.UTC)),
	}
}

func (publication *uciContextIntegrationPublication) Stage(binding uci.IndexBinding, frames []*pb.StageCodeIndexFrame) *pb.StageCodeIndexResponse {
	binding = binding.Clone()
	ref := uciContextIntegrationPublicationRef(binding)
	first := frames[0]
	publication.stageCalls = append(publication.stageCalls, uciContextIntegrationPublicationCall{
		ref:     ref,
		binding: binding,
		scope:   proto.Clone(first.GetScope()).(*pb.CodeIndexScope),
		payload: append([]byte(nil), first.GetPayload()...),
	})
	return &pb.StageCodeIndexResponse{
		BuildId:           first.GetBuildId(),
		AcceptedSequence:  frames[len(frames)-1].GetSequence(),
		AcceptedPartCount: uint64(len(frames)),
		PartDigest:        uciContextIntegrationStagedPartsDigest(frames),
	}
}

// uciContextIntegrationStagedPartsDigest mirrors the runtime's canonical
// complete-stream acknowledgement aggregate.
func uciContextIntegrationStagedPartsDigest(frames []*pb.StageCodeIndexFrame) string {
	acks := make([]uci.IndexPartAck, len(frames))
	for index, frame := range frames {
		acks[index] = uci.IndexPartAck{
			BuildID:  frame.GetBuildId(),
			Sequence: uint32(frame.GetSequence()),
			Digest:   uci.IndexDigest(frame.GetPayloadDigest()),
		}
	}
	digest, err := uci.DigestIndexParts(acks)
	if err != nil {
		return ""
	}
	return string(digest)
}

func (publication *uciContextIntegrationPublication) Finalize(binding uci.IndexBinding, request *pb.FinalizeCodeIndexRequest) *pb.FinalizeCodeIndexResponse {
	binding = binding.Clone()
	ref := uciContextIntegrationPublicationRef(binding)
	publication.finalizeCalls = append(publication.finalizeCalls, uciContextIntegrationPublicationCall{
		ref:         ref,
		binding:     binding,
		scope:       proto.Clone(request.GetScope()).(*pb.CodeIndexScope),
		partsDigest: request.GetPartsDigest(),
		finalize:    proto.Clone(request).(*pb.FinalizeCodeIndexRequest),
	})
	response := &pb.FinalizeCodeIndexResponse{
		BuildId:                    request.GetBuildId(),
		LeaseEpoch:                 request.GetLeaseEpoch(),
		AcceptedFilesystemSequence: request.GetObservedFilesystemSequence(),
	}
	if binding.Context != nil {
		response.PublishedContext = uciContextIntegrationProtoRef(*binding.Context)
	}
	return response
}

type uciContextIntegrationQueryCall struct {
	ref   uci.ContextRef
	query string
}

type uciContextIntegrationQuery struct {
	calls []uciContextIntegrationQueryCall
}

func (query *uciContextIntegrationQuery) Query(authorized uci.AuthorizedContext, request *pb.QueryCodeRequest) *pb.QueryCodeResponse {
	query.calls = append(query.calls, uciContextIntegrationQueryCall{ref: authorized.Ref(), query: request.GetQuery()})
	return &pb.QueryCodeResponse{Context: request.GetContext(), ResponseJson: []byte(`{"status":"ok"}`)}
}

func (query *uciContextIntegrationQuery) Explore(authorized uci.AuthorizedContext, request *pb.ExploreCodeRequest) *pb.ExploreCodeResponse {
	query.calls = append(query.calls, uciContextIntegrationQueryCall{ref: authorized.Ref(), query: request.GetSubject()})
	return &pb.ExploreCodeResponse{Context: request.GetContext(), ResponseJson: []byte(`{"status":"ok"}`)}
}

type uciContextIntegrationStageStream struct {
	ctx      context.Context
	frames   []*pb.StageCodeIndexFrame
	position int
	response *pb.StageCodeIndexResponse
}

func (stream *uciContextIntegrationStageStream) SetHeader(metadata.MD) error  { return nil }
func (stream *uciContextIntegrationStageStream) SendHeader(metadata.MD) error { return nil }
func (stream *uciContextIntegrationStageStream) SetTrailer(metadata.MD)       {}
func (stream *uciContextIntegrationStageStream) Context() context.Context     { return stream.ctx }
func (stream *uciContextIntegrationStageStream) SendMsg(any) error            { return nil }
func (stream *uciContextIntegrationStageStream) RecvMsg(any) error            { return nil }

func (stream *uciContextIntegrationStageStream) Recv() (*pb.StageCodeIndexFrame, error) {
	if stream.position >= len(stream.frames) {
		return nil, io.EOF
	}
	frame := stream.frames[stream.position]
	stream.position++
	return frame, nil
}

func (stream *uciContextIntegrationStageStream) SendAndClose(response *pb.StageCodeIndexResponse) error {
	stream.response = response
	return nil
}

var _ grpc.ClientStreamingServer[pb.StageCodeIndexFrame, pb.StageCodeIndexResponse] = (*uciContextIntegrationStageStream)(nil)

type uciContextIntegrationRefKey struct {
	spaceID    string
	sourceID   string
	checkout   string
	viewID     string
	profileID  string
	generation int64
}

func uciContextIntegrationKey(ref uci.ContextRef) uciContextIntegrationRefKey {
	spaceID := ""
	if ref.SpaceID != nil {
		spaceID = *ref.SpaceID
	}
	return uciContextIntegrationRefKey{
		spaceID:    spaceID,
		sourceID:   ref.SourceID,
		checkout:   ref.CheckoutID,
		viewID:     ref.ViewID,
		profileID:  ref.AnalysisProfileID,
		generation: ref.Generation,
	}
}

func uciContextIntegrationRef(spaceID, sourceID, checkoutID, viewID, profileID string, generation int64) uci.ContextRef {
	return uci.ContextRef{
		SpaceID:           uciContextIntegrationStringPointer(spaceID),
		SourceID:          sourceID,
		CheckoutID:        checkoutID,
		ViewID:            viewID,
		AnalysisProfileID: profileID,
		Generation:        generation,
	}
}

func uciContextIntegrationStringPointer(value string) *string {
	copy := value
	return &copy
}

func uciContextIntegrationBoolPointer(value bool) *bool {
	copy := value
	return &copy
}

func uciContextIntegrationProtoRef(ref uci.ContextRef) *pb.ContextRef {
	result := &pb.ContextRef{
		SourceId:          ref.SourceID,
		CheckoutId:        ref.CheckoutID,
		ViewId:            ref.ViewID,
		Generation:        ref.Generation,
		AnalysisProfileId: ref.AnalysisProfileID,
	}
	if ref.SpaceID != nil {
		result.SpaceId = uciContextIntegrationStringPointer(*ref.SpaceID)
	}
	return result
}

func uciContextIntegrationScope(ref uci.ContextRef, incarnationID string) *pb.CodeIndexScope {
	return &pb.CodeIndexScope{
		SourceId:          ref.SourceID,
		CheckoutId:        ref.CheckoutID,
		IncarnationId:     incarnationID,
		AnalysisProfileId: ref.AnalysisProfileID,
	}
}

func uciContextIntegrationBinding(ref uci.ContextRef, incarnationID, localRootID, workstationID string) uci.IndexBinding {
	contextRef := ref
	return uci.IndexBinding{
		Context: &contextRef,
		Scope: uci.IndexScope{
			SourceID:      ref.SourceID,
			CheckoutID:    ref.CheckoutID,
			IncarnationID: incarnationID,
		},
		ProfileID:     ref.AnalysisProfileID,
		LocalRootID:   localRootID,
		WorkstationID: workstationID,
	}
}

func uciContextIntegrationNoViewBinding(ref uci.ContextRef, incarnationID, localRootID, workstationID string) uci.IndexBinding {
	binding := uciContextIntegrationBinding(ref, incarnationID, localRootID, workstationID)
	binding.Context = nil
	return binding
}

func uciContextIntegrationBeginRequest(scope *pb.CodeIndexScope) *pb.BeginCodeIndexRequest {
	return &pb.BeginCodeIndexRequest{
		Scope:         scope,
		OwnerInstance: "shared-daemon",
		BuildKey:      "shared-build-key",
		ManifestMode:  "full",
		JobKind:       "initial_index",
	}
}

func uciContextIntegrationStageFrame(scope *pb.CodeIndexScope, buildID string, leaseEpoch uint64) *pb.StageCodeIndexFrame {
	return &pb.StageCodeIndexFrame{
		Scope:         scope,
		BuildId:       buildID,
		LeaseEpoch:    leaseEpoch,
		Sequence:      0,
		PayloadDigest: uciContextIntegrationDigest,
		Payload:       []byte(uciContextIntegrationSharedPathAndLabelJSON),
	}
}

func uciContextIntegrationFinalizeRequest(scope *pb.CodeIndexScope, begin *pb.BeginCodeIndexResponse, expectedParent *pb.ContextRef, partsDigest string) *pb.FinalizeCodeIndexRequest {
	startedAt := time.Date(2026, time.September, 5, 11, 59, 0, 0, time.UTC)
	return &pb.FinalizeCodeIndexRequest{
		Scope:                      scope,
		BuildId:                    begin.GetBuildId(),
		LeaseEpoch:                 begin.GetLeaseEpoch(),
		ExpectedParent:             expectedParent,
		ManifestPartCount:          1,
		PartsDigest:                partsDigest,
		ManifestEntryCount:         1,
		ManifestDigest:             uciContextIntegrationDigest,
		EdgeCount:                  0,
		EdgesDigest:                uciContextIntegrationDigest,
		ObservedFilesystemSequence: 9,
		ScanStartedAt:              timestamppb.New(startedAt),
		ScanCompletedAt:            timestamppb.New(startedAt.Add(time.Second)),
		ScanOutcome:                "complete",
		CompleteCensus:             true,
		CoverageJson:               []byte(`{"structural":"complete"}`),
		HeadOid:                    uciContextIntegrationStringPointer(uciContextIntegrationHeadOID),
		ObjectFormat:               uciContextIntegrationStringPointer(uciContextIntegrationObjectFormat),
		RefLabel:                   uciContextIntegrationStringPointer(uciContextIntegrationRefLabel),
		Dirty:                      uciContextIntegrationBoolPointer(false),
	}
}

func uciContextIntegrationQueryRequest(ref uci.ContextRef) *pb.QueryCodeRequest {
	return &pb.QueryCodeRequest{
		Context:    uciContextIntegrationProtoRef(ref),
		Query:      "SharedSymbol",
		MaxResults: 10,
		MaxBytes:   1024,
		DeadlineMs: 1_000,
	}
}

func uciContextIntegrationProtoQueryRequest(ref *pb.ContextRef) *pb.QueryCodeRequest {
	return &pb.QueryCodeRequest{
		Context:    proto.Clone(ref).(*pb.ContextRef),
		Query:      "SharedSymbol",
		MaxResults: 10,
		MaxBytes:   1024,
		DeadlineMs: 1_000,
	}
}

func requireUCIContextIntegrationClosedStatus(t *testing.T, err error, grpcCode codes.Code, contextCode uci.ContextErrorCode, forbidden ...string) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, grpcCode, status.Code(err))
	require.Equal(t, string(contextCode), status.Convert(err).Message())
	for _, value := range forbidden {
		require.NotContains(t, err.Error(), value)
	}
}

func requireUCIContextIntegrationProtoRef(t *testing.T, want uci.ContextRef, got *pb.ContextRef) {
	t.Helper()
	require.True(t, proto.Equal(uciContextIntegrationProtoRef(want), got), "context got=%v want=%v", got, want)
}

func requireUCIContextIntegrationBinding(t *testing.T, want uci.IndexBinding, got *pb.BindCodeContextResponse) {
	t.Helper()
	require.NoError(t, want.Validate())
	require.NotNil(t, got)
	if want.Context == nil {
		require.Nil(t, got.GetContext())
	} else {
		requireUCIContextIntegrationProtoRef(t, *want.Context, got.GetContext())
	}
	require.True(t, proto.Equal(&pb.CodeIndexScope{
		SourceId:          want.Scope.SourceID,
		CheckoutId:        want.Scope.CheckoutID,
		IncarnationId:     want.Scope.IncarnationID,
		AnalysisProfileId: want.ProfileID,
	}, got.GetIndexScope()), "scope got=%v want=%v", got.GetIndexScope(), want.Scope)
	require.Equal(t, want.LocalRootID, got.GetLocalRootId())
	require.Equal(t, want.WorkstationID, got.GetWorkstationId())
}

func requireUCIContextIntegrationObservedViewFacts(t *testing.T, request *pb.FinalizeCodeIndexRequest) {
	t.Helper()
	require.NotNil(t, request)
	require.Equal(t, uciContextIntegrationHeadOID, request.GetHeadOid())
	require.Equal(t, uciContextIntegrationObjectFormat, request.GetObjectFormat())
	require.Equal(t, uciContextIntegrationRefLabel, request.GetRefLabel())
	require.NotNil(t, request.Dirty, "dirty=false must retain protobuf presence")
	require.False(t, request.GetDirty())
}

func requirePublicationTarget(t *testing.T, calls []uciContextIntegrationPublicationCall, firstRef uci.ContextRef, firstScope *pb.CodeIndexScope, secondRef uci.ContextRef, secondScope *pb.CodeIndexScope) {
	t.Helper()
	require.Len(t, calls, 2)
	require.Equal(t, firstRef, calls[0].ref)
	require.True(t, proto.Equal(firstScope, calls[0].scope), "first scope got=%v want=%v", calls[0].scope, firstScope)
	require.Equal(t, secondRef, calls[1].ref)
	require.True(t, proto.Equal(secondScope, calls[1].scope), "second scope got=%v want=%v", calls[1].scope, secondScope)
}
