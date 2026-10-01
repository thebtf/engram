package grpcserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/uci"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

func TestUCIContextIntegrationNoAuthOpaqueMetadataRoundTrip(t *testing.T) {
	for _, test := range []struct {
		name     string
		instance string
	}{
		{name: "ASCII", instance: "install-a"},
		{name: "Unicode", instance: "界"},
		{name: "numeric-prefix opaque colon", instance: "1:install"},
		{name: "non-scheme opaque colon", instance: "_opaque:install"},
		{name: "Unicode-prefix opaque colon", instance: "界:install"},
		{name: "ASCII rune limit", instance: strings.Repeat("a", 256)},
		{name: "Unicode beyond former byte limit", instance: strings.Repeat("界", 86)},
		{name: "Unicode rune limit", instance: strings.Repeat("界", 256)},
		{name: "four-byte Unicode rune limit", instance: strings.Repeat("😀", 256)},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newUCIContextIntegrationFixture(t)
			workstation, valid := uci.NoAuthCodeWorkstationForInstance(test.instance)
			require.True(t, valid)
			fingerprint := sha256.Sum256([]byte("engram/noauth-code/workstation/v1\x00" + test.instance))
			require.Equal(t, fmt.Sprintf("noauth-code-%x", fingerprint), workstation)
			binding := fixture.bindingA.Clone()
			binding.WorkstationID = workstation
			fixture.runtime.bindings[uciContextIntegrationKey(fixture.refA)] = binding
			fixture.catalog.records[uciContextIntegrationKey(fixture.refA)] = uci.ContextRecord{Ref: fixture.refA, AuthRealm: uci.NoAuthCodeRealm}
			port, err := mcp.NewUCIContextHandlePort(mcp.NewServer(mcp.ServerOptions{Version: "opaque-metadata-test"}), fixture.runtime, fixture.authorizer)
			require.NoError(t, err)
			fixture.server.SetUCITransport(NewContextAwareUCITransport(
				uci.NewContextResolver(fixture.catalog, fixture.authorizer, fixture.runtime),
				uci.NewAliasResolver(fixture.aliases.Lookup), fixture.runtime, port,
			))
			client := startUCIOpaqueMetadataGRPC(t, fixture.server)
			ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs(
				auditcontext.SourceSessionMetadataKey, "noauth-index-session",
				uci.NoAuthCodeClientInstanceMetadataKey, test.instance,
			))

			bound, err := client.BindCodeContext(ctx, &pb.BindCodeContextRequest{
				ClientSessionId: "noauth-index-session", RequestedContext: uciContextIntegrationProtoRef(fixture.refA),
			})
			require.NoError(t, err)
			requireUCIContextIntegrationBinding(t, binding, bound)
			rebound, err := client.BindCodeContext(ctx, &pb.BindCodeContextRequest{
				ClientSessionId: "noauth-index-session", ContextHandle: bound.GetContextHandle(),
			})
			require.NoError(t, err)
			requireUCIContextIntegrationBinding(t, binding, rebound)

			scope := uciContextIntegrationScope(fixture.refA, uciContextIntegrationIncarnationA)
			begin, err := client.BeginCodeIndex(ctx, uciContextIntegrationBeginRequest(scope))
			require.NoError(t, err)
			stream, err := client.StageCodeIndex(ctx)
			require.NoError(t, err)
			require.NoError(t, stream.Send(uciContextIntegrationStageFrame(scope, begin.GetBuildId(), begin.GetLeaseEpoch())))
			staged, err := stream.CloseAndRecv()
			require.NoError(t, err)
			published, err := client.FinalizeCodeIndex(ctx, uciContextIntegrationFinalizeRequest(scope, begin, bound.GetContext(), staged.GetPartDigest()))
			require.NoError(t, err)
			requireUCIContextIntegrationProtoRef(t, fixture.refA, published.GetPublishedContext())
			queried, err := client.QueryCode(ctx, uciContextIntegrationQueryRequest(fixture.refA))
			require.NoError(t, err)
			requireUCIContextIntegrationProtoRef(t, fixture.refA, queried.GetContext())
			explored, err := client.ExploreCode(ctx, &pb.ExploreCodeRequest{
				Context: bound.GetContext(), Operation: "impact", Subject: "RelatedSymbol", MaxDepth: 4,
				MaxVisitedNodes: 100, MaxResultNodes: 10, MaxResultEdges: 20, DeadlineMs: 1000,
			})
			require.NoError(t, err)
			requireUCIContextIntegrationProtoRef(t, fixture.refA, explored.GetContext())

			for _, calls := range [][]uciContextIntegrationPublicationCall{
				fixture.publication.beginCalls, fixture.publication.stageCalls, fixture.publication.finalizeCalls,
			} {
				require.Len(t, calls, 1)
				require.Equal(t, fixture.refA, calls[0].ref)
				require.Equal(t, workstation, calls[0].binding.WorkstationID)
			}
			require.Equal(t, []uciContextIntegrationQueryCall{
				{ref: fixture.refA, query: "SharedSymbol"}, {ref: fixture.refA, query: "RelatedSymbol"},
			}, fixture.query.calls)
		})
	}
}

func TestUCIContextIntegrationNoAuthFreshSessionsEvictOldHandle(t *testing.T) {
	fixture := newUCIContextIntegrationFixture(t)
	const instance = "reconnecting-install"
	workstation, valid := uci.NoAuthCodeWorkstationForInstance(instance)
	require.True(t, valid)
	binding := fixture.bindingA.Clone()
	binding.WorkstationID = workstation
	fixture.runtime.bindings[uciContextIntegrationKey(fixture.refA)] = binding
	fixture.catalog.records[uciContextIntegrationKey(fixture.refA)] = uci.ContextRecord{Ref: fixture.refA, AuthRealm: uci.NoAuthCodeRealm}
	port, err := mcp.NewUCIContextHandlePort(mcp.NewServer(mcp.ServerOptions{Version: "reconnect-test"}), fixture.runtime, fixture.authorizer)
	require.NoError(t, err)
	fixture.server.SetUCITransport(NewContextAwareUCITransport(uci.NewContextResolver(fixture.catalog, fixture.authorizer, fixture.runtime), uci.NewAliasResolver(fixture.aliases.Lookup), fixture.runtime, port))
	client := startUCIOpaqueMetadataGRPC(t, fixture.server)
	caller := func(session string) context.Context {
		return metadata.NewOutgoingContext(context.Background(), metadata.Pairs(auditcontext.SourceSessionMetadataKey, session, uci.NoAuthCodeClientInstanceMetadataKey, instance))
	}
	var first, last *pb.BindCodeContextResponse
	for index := range 1100 {
		session := fmt.Sprintf("fresh-session-%d", index)
		bound, err := client.BindCodeContext(caller(session), &pb.BindCodeContextRequest{ClientSessionId: session, RequestedContext: uciContextIntegrationProtoRef(fixture.refA)})
		require.NoError(t, err)
		if index == 0 {
			first = bound
		}
		last = bound
	}
	_, err = client.BindCodeContext(caller("fresh-session-0"), &pb.BindCodeContextRequest{ClientSessionId: "fresh-session-0", ContextHandle: first.GetContextHandle()})
	requireUCIContextIntegrationClosedStatus(t, err, codes.FailedPrecondition, uci.ContextMismatch)
	rebound, err := client.BindCodeContext(caller("fresh-session-1099"), &pb.BindCodeContextRequest{ClientSessionId: "fresh-session-1099", ContextHandle: last.GetContextHandle()})
	require.NoError(t, err)
	requireUCIContextIntegrationBinding(t, binding, rebound)
}

func TestUCIContextIntegrationNoAuthMalformedMetadataRoundTrip(t *testing.T) {
	for _, test := range []struct {
		name   string
		values []string
		legacy string
	}{
		{name: "missing"},
		{name: "empty", values: []string{""}},
		{name: "duplicate", values: []string{"install-a", "install-a"}},
		{name: "conflicting", values: []string{"install-a", "install-b"}},
		{name: "invalid UTF-8", values: []string{string([]byte{0xff})}},
		{name: "space", values: []string{"install a"}},
		{name: "Unicode whitespace", values: []string{"界\u2003"}},
		{name: "control", values: []string{"install\x00a"}},
		{name: "locator", values: []string{"file:///forged"}},
		{name: "Unix locator", values: []string{"/private/install"}},
		{name: "Windows locator", values: []string{"C:\\private\\install"}},
		{name: "email locator", values: []string{"install@example.invalid"}},
		{name: "scheme", values: []string{"https:private-install"}},
		{name: "ASCII beyond rune limit", values: []string{strings.Repeat("a", 257)}},
		{name: "Unicode beyond rune limit", values: []string{strings.Repeat("界", 257)}},
		{name: "four-byte Unicode beyond rune limit", values: []string{strings.Repeat("😀", 257)}},
		{name: "legacy only", legacy: "install-a"},
		{name: "mixed schemes", values: []string{"install-a"}, legacy: "install-a"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newUCIContextIntegrationFixture(t)
			client := startUCIOpaqueMetadataGRPC(t, fixture.server)
			outgoing := metadata.Pairs(auditcontext.SourceSessionMetadataKey, "noauth-index-session")
			if test.values != nil {
				outgoing.Set(uci.NoAuthCodeClientInstanceMetadataKey, test.values...)
			}
			if test.legacy != "" {
				outgoing.Set("x-engram-uci-client-instance-id", test.legacy)
			}
			ctx := metadata.NewOutgoingContext(context.Background(), outgoing)
			bound, err := client.BindCodeContext(ctx, &pb.BindCodeContextRequest{
				ClientSessionId: "noauth-index-session", RequestedContext: uciContextIntegrationProtoRef(fixture.refA),
			})
			require.Nil(t, bound)
			requireUCIContextIntegrationClosedStatus(t, err, codes.FailedPrecondition, uci.ContextMismatch)
			require.Empty(t, fixture.catalog.calls)
			require.Empty(t, fixture.authorizer.accesses)
			require.Empty(t, fixture.runtime.bindingCalls)
			require.Empty(t, fixture.handles.issues)
		})
	}
}

func TestUCIOpaqueMetadataDoesNotChangeAuthenticatedAuthority(t *testing.T) {
	fixture := newUCIContextIntegrationFixture(t)
	correlation, valid := auditcontext.NewUCIRequestCorrelation(json.RawMessage(`"authenticated-instance"`))
	require.True(t, valid)
	incoming := metadata.Pairs(
		auditcontext.SourceSessionMetadataKey, uciContextIntegrationClientA,
		auditcontext.UCIRequestCorrelationMetadataKey, correlation.MetadataValue(),
		uci.NoAuthCodeClientInstanceMetadataKey, strings.Repeat("界", 257),
		uci.NoAuthCodeClientInstanceMetadataKey, "file:///forged",
		"x-engram-uci-client-instance-id", "legacy-forged",
	)
	identity := auth.ClientWithPrincipal("read-write", "auth-workstation", uciContextIntegrationPrincipalA, auth.PrincipalKindAgent)
	ctx := auth.WithIdentity(metadata.NewIncomingContext(context.Background(), incoming), identity)
	caller, err := contextAwareCallerFrom(ctx)
	require.NoError(t, err)
	require.Equal(t, "auth-workstation", caller.workstationID)
	require.Equal(t, uciContextIntegrationPrincipalA, caller.principal)
	require.Empty(t, caller.clientInstanceID)
	bound, err := fixture.server.BindCodeContext(ctx, &pb.BindCodeContextRequest{
		ClientSessionId: uciContextIntegrationClientA, RequestedContext: uciContextIntegrationProtoRef(fixture.refA),
	})
	require.NoError(t, err)
	requireUCIContextIntegrationBinding(t, fixture.bindingA, bound)
	handler := &uciRequestCorrelationHandler{}
	fixture.server.handler = handler
	_, err = fixture.server.CallTool(ctx, &pb.CallToolRequest{ToolName: "codebase_context", ArgumentsJson: []byte(`{"action":"list"}`)})
	require.NoError(t, err)
	require.Equal(t, 1, handler.calls)
}

func startUCIOpaqueMetadataGRPC(t *testing.T, service *Server) pb.EngramServiceClient {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := grpc.NewServer(grpc.UnaryInterceptor(service.authInterceptor), grpc.StreamInterceptor(service.streamAuthInterceptor))
	pb.RegisterEngramServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	connection, err := grpc.NewClient(
		listener.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close() })
	return pb.NewEngramServiceClient(connection)
}
