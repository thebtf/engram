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
	uciContextIntegrationLegacySelector         = "legacy-project-selector"
	uciContextIntegrationConflictingSelector    = "legacy-project-selector-conflict"
	uciContextIntegrationDigest                 = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
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
	requireUCIContextIntegrationProtoRef(t, fixture.refA, bound.GetContext())
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
	stageB := &uciContextIntegrationStageStream{
		ctx:    ctxB,
		frames: []*pb.StageCodeIndexFrame{uciContextIntegrationStageFrame(scopeB, beginB.GetBuildId(), beginB.GetLeaseEpoch())},
	}
	require.NoError(t, fixture.server.StageCodeIndex(stageB))

	finalizedA, err := fixture.server.FinalizeCodeIndex(ctxA, uciContextIntegrationFinalizeRequest(scopeA, beginA, boundA.GetContext()))
	require.NoError(t, err)
	finalizedB, err := fixture.server.FinalizeCodeIndex(ctxB, uciContextIntegrationFinalizeRequest(scopeB, beginB, boundB.GetContext()))
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
	require.Len(t, fixture.publication.stageCalls, 2)
	require.Equal(t, []byte(uciContextIntegrationSharedPathAndLabelJSON), fixture.publication.stageCalls[0].payload)
	require.Equal(t, fixture.publication.stageCalls[0].payload, fixture.publication.stageCalls[1].payload, "same path and label must not collapse distinct checkout routes")
	require.Len(t, fixture.query.calls, 2)
	require.Equal(t, fixture.refA, fixture.query.calls[0].ref)
	require.Equal(t, fixture.refB, fixture.query.calls[1].ref)
	require.Equal(t, "SharedSymbol", fixture.query.calls[0].query)
	require.Equal(t, fixture.query.calls[0].query, fixture.query.calls[1].query)
	require.Len(t, fixture.catalog.calls, 10, "each binding and publication/query access must resolve the caller context")
	require.Len(t, fixture.authorizer.accesses, 10, "each resolved caller context must be authorized")
	for index, access := range fixture.authorizer.accesses {
		require.Equal(t, fixture.catalog.calls[index].SourceID, access.SourceID)
		require.Equal(t, fixture.catalog.calls[index].CheckoutID, access.CheckoutID)
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
	require.Equal(t, "build-"+fixture.refA.CheckoutID[:8], begin.GetBuildId())
	require.Len(t, fixture.query.calls, 1)
	require.Equal(t, fixture.refB, fixture.query.calls[0].ref)
	require.Len(t, fixture.publication.beginCalls, 1)
	require.Equal(t, fixture.refA, fixture.publication.beginCalls[0].ref)
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
	}
	fixture.runtime = &uciContextIntegrationRuntime{
		publication: fixture.publication,
		query:       fixture.query,
	}
	_, fixture.server = New(nil, nil)
	fixture.server.SetUCITransport(NewContextAwareUCITransport(
		uci.NewContextResolver(fixture.catalog, fixture.authorizer),
		uci.NewAliasResolver(fixture.aliases.Lookup),
		fixture.runtime,
	))
	return fixture
}

func (fixture *uciContextIntegrationFixture) clientContext(sessionID, principal string) context.Context {
	identity := auth.ClientWithPrincipal("read-write", uciContextIntegrationRealm, principal, auth.PrincipalKindAgent)
	return auditcontext.WithSourceSession(auth.WithIdentity(context.Background(), identity), sessionID)
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
	publication *uciContextIntegrationPublication
	query       *uciContextIntegrationQuery
}

func (runtime *uciContextIntegrationRuntime) LegacyCodeIndexNegotiate(_ context.Context, authorized uci.AuthorizedContext, request *pb.CodeIndexNegotiateRequest) (*pb.CodeIndexNegotiateResponse, error) {
	return runtime.publication.LegacyNegotiate(authorized, request), nil
}

func (runtime *uciContextIntegrationRuntime) BeginCodeIndex(_ context.Context, authorized uci.AuthorizedContext, request *pb.BeginCodeIndexRequest) (*pb.BeginCodeIndexResponse, error) {
	return runtime.publication.Begin(authorized, request), nil
}

func (runtime *uciContextIntegrationRuntime) StageCodeIndex(_ context.Context, authorized uci.AuthorizedContext, frames []*pb.StageCodeIndexFrame) (*pb.StageCodeIndexResponse, error) {
	return runtime.publication.Stage(authorized, frames), nil
}

func (runtime *uciContextIntegrationRuntime) FinalizeCodeIndex(_ context.Context, authorized uci.AuthorizedContext, request *pb.FinalizeCodeIndexRequest) (*pb.FinalizeCodeIndexResponse, error) {
	return runtime.publication.Finalize(authorized, request), nil
}

func (runtime *uciContextIntegrationRuntime) QueryCode(_ context.Context, authorized uci.AuthorizedContext, request *pb.QueryCodeRequest) (*pb.QueryCodeResponse, error) {
	return runtime.query.Query(authorized, request), nil
}

func (runtime *uciContextIntegrationRuntime) ExploreCode(_ context.Context, authorized uci.AuthorizedContext, request *pb.ExploreCodeRequest) (*pb.ExploreCodeResponse, error) {
	return runtime.query.Explore(authorized, request), nil
}

type uciContextIntegrationPublicationCall struct {
	ref     uci.ContextRef
	scope   *pb.CodeIndexScope
	payload []byte
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

func (publication *uciContextIntegrationPublication) Begin(authorized uci.AuthorizedContext, request *pb.BeginCodeIndexRequest) *pb.BeginCodeIndexResponse {
	ref := authorized.Ref()
	publication.beginCalls = append(publication.beginCalls, uciContextIntegrationPublicationCall{
		ref:   ref,
		scope: proto.Clone(request.GetScope()).(*pb.CodeIndexScope),
	})
	return &pb.BeginCodeIndexResponse{
		Scope:          request.GetScope(),
		BuildId:        "build-" + ref.CheckoutID[:8],
		LeaseEpoch:     7,
		LeaseExpiresAt: timestamppb.New(time.Date(2026, time.September, 5, 12, 0, 0, 0, time.UTC)),
	}
}

func (publication *uciContextIntegrationPublication) Stage(authorized uci.AuthorizedContext, frames []*pb.StageCodeIndexFrame) *pb.StageCodeIndexResponse {
	ref := authorized.Ref()
	first := frames[0]
	publication.stageCalls = append(publication.stageCalls, uciContextIntegrationPublicationCall{
		ref:     ref,
		scope:   proto.Clone(first.GetScope()).(*pb.CodeIndexScope),
		payload: append([]byte(nil), first.GetPayload()...),
	})
	return &pb.StageCodeIndexResponse{
		BuildId:           first.GetBuildId(),
		AcceptedSequence:  frames[len(frames)-1].GetSequence(),
		AcceptedPartCount: uint64(len(frames)),
		PartDigest:        first.GetPayloadDigest(),
	}
}

func (publication *uciContextIntegrationPublication) Finalize(authorized uci.AuthorizedContext, request *pb.FinalizeCodeIndexRequest) *pb.FinalizeCodeIndexResponse {
	ref := authorized.Ref()
	publication.finalizeCalls = append(publication.finalizeCalls, uciContextIntegrationPublicationCall{
		ref:   ref,
		scope: proto.Clone(request.GetScope()).(*pb.CodeIndexScope),
	})
	return &pb.FinalizeCodeIndexResponse{
		PublishedContext:           uciContextIntegrationProtoRef(ref),
		BuildId:                    request.GetBuildId(),
		LeaseEpoch:                 request.GetLeaseEpoch(),
		AcceptedFilesystemSequence: request.GetObservedFilesystemSequence(),
	}
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

func uciContextIntegrationFinalizeRequest(scope *pb.CodeIndexScope, begin *pb.BeginCodeIndexResponse, expectedParent *pb.ContextRef) *pb.FinalizeCodeIndexRequest {
	startedAt := time.Date(2026, time.September, 5, 11, 59, 0, 0, time.UTC)
	return &pb.FinalizeCodeIndexRequest{
		Scope:                      scope,
		BuildId:                    begin.GetBuildId(),
		LeaseEpoch:                 begin.GetLeaseEpoch(),
		ExpectedParent:             expectedParent,
		ManifestPartCount:          1,
		PartsDigest:                uciContextIntegrationDigest,
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

func requirePublicationTarget(t *testing.T, calls []uciContextIntegrationPublicationCall, firstRef uci.ContextRef, firstScope *pb.CodeIndexScope, secondRef uci.ContextRef, secondScope *pb.CodeIndexScope) {
	t.Helper()
	require.Len(t, calls, 2)
	require.Equal(t, firstRef, calls[0].ref)
	require.True(t, proto.Equal(firstScope, calls[0].scope), "first scope got=%v want=%v", calls[0].scope, firstScope)
	require.Equal(t, secondRef, calls[1].ref)
	require.True(t, proto.Equal(secondScope, calls[1].scope), "second scope got=%v want=%v", calls[1].scope, secondScope)
}
